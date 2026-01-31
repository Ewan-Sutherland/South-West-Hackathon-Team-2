package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"sync"

	"github.com/becomeliminal/nim-go-sdk/server"
	"github.com/becomeliminal/nim-go-sdk/tools"
	"github.com/joho/godotenv"
)

// Normalized format we store in memory after Python parses the demo JSON.
type Transaction struct {
	Timestamp string  `json:"timestamp"`
	Merchant  string  `json:"merchant"`
	Amount    float64 `json:"amount"`   // signed: +income, -spend
	Currency  string  `json:"currency"` // e.g. LIL
	ID        string  `json:"id,omitempty"`
	RawType   string  `json:"type,omitempty"`
	Status    string  `json:"status,omitempty"`
	UsdValue  float64 `json:"usdValue,omitempty"`
}

// In-memory store (state lives here, not in Python subprocesses)
type TxStore struct {
	mu  sync.RWMutex
	txs []Transaction
}

func (s *TxStore) Set(txs []Transaction) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.txs = txs
}
func (s *TxStore) Get() []Transaction {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Transaction, len(s.txs))
	copy(out, s.txs)
	return out
}

func main() {
	_ = godotenv.Load()

	anthropicKey := os.Getenv("ANTHROPIC_API_KEY")
	if anthropicKey == "" {
		log.Fatal("ANTHROPIC_API_KEY is not set (create .env with ANTHROPIC_API_KEY=...)")
	}

	srv, err := server.New(server.Config{AnthropicKey: anthropicKey})
	if err != nil {
		log.Fatal(err)
	}

	store := &TxStore{}

	// Tool 1: Ready for demo -> load JSON file via Python, store normalized txs in memory
	srv.AddTool(tools.New("ready_for_demo").
		Description("Load the demo transaction history from the bundled JSON file. Use when user says 'ready for demo'.").
		Schema(tools.ObjectSchema(map[string]interface{}{
			"path": tools.StringProperty("Optional path to JSON file (default: data/liminal_transactions_6mo.json)"),
		}, "")).
		HandlerFunc(func(ctx context.Context, input json.RawMessage) (interface{}, error) {
			var payload struct {
				Path string `json:"path"`
			}
			_ = json.Unmarshal(input, &payload)

			path := payload.Path
			if path == "" {
				path = "data/liminal_transactions_6mo.json"
			}

			// Call Python loader: outputs {"transactions":[...normalized...], "tx_count":N, "parse_errors":M}
			out, err := runPy(ctx, "python/load_demo_transactions.py", []string{path}, nil)
			if err != nil {
				return nil, err
			}

			var resp struct {
				Transactions []Transaction `json:"transactions"`
				TxCount      int           `json:"tx_count"`
				ParseErrors  int           `json:"parse_errors"`
			}
			if err := json.Unmarshal(out, &resp); err != nil {
				return nil, fmt.Errorf("python loader returned invalid JSON: %v | output: %s", err, string(out))
			}

			store.Set(resp.Transactions)

			return map[string]interface{}{
				"status":       "ready",
				"path":         path,
				"tx_count":     resp.TxCount,
				"parse_errors": resp.ParseErrors,
				"message":      "Demo transaction history loaded and set. You can now ask for a summary or view the history.",
			}, nil
		}).Build())

	// Tool 2: Get currently loaded history (Go returns stored state)
	srv.AddTool(tools.New("get_transaction_history").
		Description("Return the currently loaded transaction history (call ready_for_demo first).").
		Schema(tools.ObjectSchema(map[string]interface{}{}, "")).
		HandlerFunc(func(ctx context.Context, input json.RawMessage) (interface{}, error) {
			txs := store.Get()
			return map[string]interface{}{
				"loaded":       len(txs) > 0,
				"tx_count":     len(txs),
				"transactions": txs,
			}, nil
		}).Build())

	// Tool 3: Summarise loaded history (Python computes summary)
	srv.AddTool(tools.New("summarise_transactions").
		Description("Summarise the loaded transactions (income/spend/net, counts). Requires ready_for_demo first.").
		Schema(tools.ObjectSchema(map[string]interface{}{}, "")).
		HandlerFunc(func(ctx context.Context, input json.RawMessage) (interface{}, error) {
			txs := store.Get()
			if len(txs) == 0 {
				return map[string]interface{}{
					"error": "No transactions loaded. Say 'ready for demo' first.",
				}, nil
			}

			payload, _ := json.Marshal(map[string]interface{}{
				"transactions": txs,
			})

			out, err := runPy(ctx, "python/summarise_transactions.py", nil, payload)
			if err != nil {
				return nil, err
			}

			var result interface{}
			if err := json.Unmarshal(out, &result); err != nil {
				return nil, fmt.Errorf("python summary returned invalid JSON: %v | output: %s", err, string(out))
			}

			return result, nil
		}).Build())

	log.Println("Nim demo server running on :8080")
	srv.Run(":8080")
}

// Runs a Python script. Uses stdin if provided, args if provided.
// Windows-safe: tries "python" first; if you need, swap to "py", "-3".
func runPy(ctx context.Context, script string, args []string, stdin []byte) ([]byte, error) {
	cmdArgs := []string{script}
	cmdArgs = append(cmdArgs, args...)

	cmd := exec.CommandContext(ctx, "python", cmdArgs...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}

	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("python failed (%s): %v | output: %s", script, err, string(out))
	}
	return out, nil
}
