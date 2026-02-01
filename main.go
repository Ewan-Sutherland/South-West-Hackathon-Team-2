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
	"time"

	"github.com/becomeliminal/nim-go-sdk/server"
	"github.com/becomeliminal/nim-go-sdk/tools"
	"github.com/joho/godotenv"
)

/* =======================
   Data Models & Stores
   ======================= */

type Transaction struct {
	Timestamp string  `json:"timestamp"`
	Merchant  string  `json:"merchant"`
	Amount    float64 `json:"amount"`   // signed: +income, -expense
	Currency  string  `json:"currency"` // e.g. GBP
	ID        string  `json:"id,omitempty"`
	RawType   string  `json:"type,omitempty"`
	Status    string  `json:"status,omitempty"`
	UsdValue  float64 `json:"usdValue,omitempty"`
}

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

func (s *TxStore) Append(t Transaction) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.txs = append(s.txs, t)
}

type DemoState struct {
	mu             sync.RWMutex
	WalletBalance  float64
	SavingsBalance float64
	Today          string // YYYY-MM-DD
	LeadDays       int
}

func (d *DemoState) Set(wallet, savings float64, today string, leadDays int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.WalletBalance = wallet
	d.SavingsBalance = savings
	d.Today = today
	d.LeadDays = leadDays
}

func (d *DemoState) Get() (wallet, savings float64, today string, leadDays int) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.WalletBalance, d.SavingsBalance, d.Today, d.LeadDays
}

func (d *DemoState) ApplyWalletDelta(delta float64) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.WalletBalance += delta
}

func (d *DemoState) ApplySavingsDelta(delta float64) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.SavingsBalance += delta
}

/* =======================
   Helpers
   ======================= */

func computeBalances(txs []Transaction) (wallet float64, savings float64) {
	for _, t := range txs {
		wallet += t.Amount

		switch t.RawType {
		case "deposit_savings_demo":
			if t.Amount < 0 {
				savings += -t.Amount
			}
		case "withdraw_savings_demo":
			if t.Amount > 0 {
				savings -= t.Amount
			}
		}
	}
	if wallet < 0 && wallet > -1e-6 {
		wallet = 0
	}
	if savings < 0 && savings > -1e-6 {
		savings = 0
	}
	return wallet, savings
}

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

/* =======================
   Main
   ======================= */

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
	demo := &DemoState{}

	// Tool 1: ready_for_demo
	srv.AddTool(tools.New("ready_for_demo").
		Description(`
Load the demo transaction history and initialise demo state automatically.

What it does:
- Loads and normalises transactions into memory
- Computes demo wallet balance from the full history
- Computes demo savings balance from deposit_savings_demo / withdraw_savings_demo transactions
- Sets:
  - today = real today's date
  - lead_days = 3
`).
		Schema(tools.ObjectSchema(map[string]interface{}{
			"path": tools.StringProperty("Optional path (default: data/demo_transactions_6mo_with_savings.json)"),
		}, "")).
		HandlerFunc(func(ctx context.Context, input json.RawMessage) (interface{}, error) {
			var payload struct {
				Path string `json:"path"`
			}
			_ = json.Unmarshal(input, &payload)

			path := payload.Path
			if path == "" {
				path = "data/demo_transactions_6mo_with_savings.json"
			}

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

			wallet, savings := computeBalances(resp.Transactions)
			today := time.Now().Format("2006-01-02")
			demo.Set(wallet, savings, today, 3)

			return map[string]interface{}{
				"status":       "ready",
				"path":         path,
				"tx_count":     resp.TxCount,
				"parse_errors": resp.ParseErrors,
				"demo_state": map[string]interface{}{
					"wallet_balance":  wallet,
					"savings_balance": savings,
					"today":           today,
					"lead_days":       3,
				},
				"message": "Demo loaded. Balances initialised automatically. Try demo_send_money then demo_withdraw_from_savings if needed.",
			}, nil
		}).Build())

	// Tool 2: demo_state
	srv.AddTool(tools.New("demo_state").
		Description("Return current demo wallet balance, demo savings balance, today's date, and lead_days.").
		Schema(tools.ObjectSchema(map[string]interface{}{}, "")).
		HandlerFunc(func(ctx context.Context, _ json.RawMessage) (interface{}, error) {
			w, s, today, lead := demo.Get()
			return map[string]interface{}{
				"wallet_balance":  w,
				"savings_balance": s,
				"today":           today,
				"lead_days":       lead,
			}, nil
		}).Build())

	// Tool 3: get_transaction_history
	srv.AddTool(tools.New("get_transaction_history").
		Description("Return the currently loaded transaction history (call ready_for_demo first).").
		Schema(tools.ObjectSchema(map[string]interface{}{}, "")).
		HandlerFunc(func(ctx context.Context, _ json.RawMessage) (interface{}, error) {
			txs := store.Get()
			return map[string]interface{}{
				"loaded":       len(txs) > 0,
				"tx_count":     len(txs),
				"transactions": txs,
			}, nil
		}).Build())

	// Tool 4: summarise_transactions
	srv.AddTool(tools.New("summarise_transactions").
		Description("Summarise the loaded transactions (income/spend/net, counts). Requires ready_for_demo first.").
		Schema(tools.ObjectSchema(map[string]interface{}{}, "")).
		HandlerFunc(func(ctx context.Context, _ json.RawMessage) (interface{}, error) {
			txs := store.Get()
			if len(txs) == 0 {
				return map[string]interface{}{"error": "No transactions loaded. Say 'ready for demo' first."}, nil
			}

			payload, _ := json.Marshal(map[string]interface{}{"transactions": txs})
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

	// Tool 5: income_tracker
	srv.AddTool(tools.New("income_tracker").
		Description(`
INCOME ONLY.
Analyse ONLY positive inflows from the loaded transaction history.

Rules:
- Ignore all expenses (rent, bills, subscriptions).
- In your response, ONLY report tool output fields.
- Do NOT infer expenses or other categories outside the tool output.
`).
		Schema(tools.ObjectSchema(map[string]interface{}{}, "")).
		HandlerFunc(func(ctx context.Context, _ json.RawMessage) (interface{}, error) {
			txs := store.Get()
			if len(txs) == 0 {
				return map[string]interface{}{"error": "No transactions loaded. Say 'ready for demo' first."}, nil
			}

			payload, _ := json.Marshal(map[string]interface{}{"transactions": txs})
			out, err := runPy(ctx, "python/income_tracker.py", nil, payload)
			if err != nil {
				return nil, err
			}

			var result interface{}
			if err := json.Unmarshal(out, &result); err != nil {
				return nil, fmt.Errorf("python income_tracker returned invalid JSON: %v | output: %s", err, string(out))
			}
			return result, nil
		}).Build())

	// Tool 6: bills_tracker
	srv.AddTool(tools.New("bills_tracker").
		Description(`
BILLS ONLY (recurring expenses).
Analyse ONLY recurring bills/regular expenses (rent, council tax, utilities, insurance, subscriptions, phone/internet).

Rules:
- Do NOT treat discretionary spending (groceries, shopping, eating out) as bills.
- In your response: report recurring bills outputs only.
- If asked about discretionary spending, say it is out of scope for bills_tracker.
`).
		Schema(tools.ObjectSchema(map[string]interface{}{}, "")).
		HandlerFunc(func(ctx context.Context, _ json.RawMessage) (interface{}, error) {
			txs := store.Get()
			if len(txs) == 0 {
				return map[string]interface{}{"error": "No transactions loaded. Say 'ready for demo' first."}, nil
			}

			payload, _ := json.Marshal(map[string]interface{}{"transactions": txs})
			out, err := runPy(ctx, "python/bills_tracker.py", nil, payload)
			if err != nil {
				return nil, err
			}

			var result interface{}
			if err := json.Unmarshal(out, &result); err != nil {
				return nil, fmt.Errorf("python bills_tracker returned invalid JSON: %v | output: %s", err, string(out))
			}
			return result, nil
		}).Build())

	// Tool 7: spend_guardrail (manual inputs)
	srv.AddTool(tools.New("spend_guardrail").
		Description(`
DECISION ONLY.
Determine whether a proposed spend would endanger upcoming bills.

This tool does NOT move money.
It protects upcoming bills only (no living buffer).
If wallet cash would be insufficient, it checks savings and may recommend a savings top-up amount.

Follow the returned 'decision' exactly; do NOT guess or invent outcomes.
`).
		Schema(tools.ObjectSchema(map[string]interface{}{
			"spend_amount":    tools.NumberProperty("Amount the user wants to spend/send"),
			"wallet_balance":  tools.NumberProperty("Current wallet balance"),
			"savings_balance": tools.NumberProperty("Current savings balance"),
			"lead_days":       tools.NumberProperty("Days ahead to protect bills (default 3)"),
			"today":           tools.StringProperty("Optional YYYY-MM-DD for deterministic demo"),
		}, "spend_amount", "wallet_balance", "savings_balance")).
		HandlerFunc(func(ctx context.Context, input json.RawMessage) (interface{}, error) {
			txs := store.Get()
			if len(txs) == 0 {
				return map[string]interface{}{"error": "No transactions loaded. Say 'ready for demo' first."}, nil
			}

			var req struct {
				SpendAmount    float64 `json:"spend_amount"`
				WalletBalance  float64 `json:"wallet_balance"`
				SavingsBalance float64 `json:"savings_balance"`
				LeadDays       int     `json:"lead_days"`
				Today          string  `json:"today"`
			}
			_ = json.Unmarshal(input, &req)
			if req.LeadDays == 0 {
				req.LeadDays = 3
			}

			// Step 1: upcoming bills
			upcomingPayload, _ := json.Marshal(map[string]interface{}{
				"transactions": txs,
				"lead_days":    req.LeadDays,
				"today":        req.Today,
			})

			billsOut, err := runPy(ctx, "python/upcoming_bills.py", nil, upcomingPayload)
			if err != nil {
				return nil, err
			}

			var bills struct {
				TotalUpcomingBills float64       `json:"total_upcoming_bills"`
				UpcomingBills      []interface{} `json:"upcoming_bills"`
			}
			if err := json.Unmarshal(billsOut, &bills); err != nil {
				return nil, fmt.Errorf("upcoming_bills.py returned invalid JSON: %v | output: %s", err, string(billsOut))
			}

			// Step 2: decision
			decisionPayload, _ := json.Marshal(map[string]interface{}{
				"spend_amount":         req.SpendAmount,
				"wallet_balance":       req.WalletBalance,
				"savings_balance":      req.SavingsBalance,
				"lead_days":            req.LeadDays,
				"today":                req.Today,
				"upcoming_bills":       bills.UpcomingBills,
				"total_upcoming_bills": bills.TotalUpcomingBills,
			})

			out, err := runPy(ctx, "python/spend_guardrail.py", nil, decisionPayload)
			if err != nil {
				return nil, err
			}

			var result interface{}
			if err := json.Unmarshal(out, &result); err != nil {
				return nil, fmt.Errorf("spend_guardrail returned invalid JSON: %v | output: %s", err, string(out))
			}
			return result, nil
		}).Build())

	// Tool 8: demo_send_money (automatic balances/dates)
	srv.AddTool(tools.New("demo_send_money").
		Description(`
DEMO INTERCEPTOR.

Simulate sending money from the demo wallet with automatic guardrails:
- Uses demo wallet balance and demo savings balance (initialised by ready_for_demo)
- Uses today's date (initialised by ready_for_demo)
- Uses lead_days = 3

It will:
- Compute upcoming recurring bills within the lead window
- Run spend_guardrail decision logic
- If decision == allow, it applies the spend to demo wallet and appends a demo transaction
- If decision != allow, it does not move funds
`).
		Schema(tools.ObjectSchema(map[string]interface{}{
			"to":     tools.StringProperty("Recipient handle or name (demo)"),
			"amount": tools.NumberProperty("Amount to send"),
		}, "to", "amount")).
		HandlerFunc(func(ctx context.Context, input json.RawMessage) (interface{}, error) {
			txs := store.Get()
			if len(txs) == 0 {
				return map[string]interface{}{"error": "No transactions loaded. Say 'ready for demo' first."}, nil
			}

			var req struct {
				To     string  `json:"to"`
				Amount float64 `json:"amount"`
			}
			_ = json.Unmarshal(input, &req)
			if req.Amount <= 0 {
				return map[string]interface{}{"error": "Amount must be > 0."}, nil
			}

			wallet, savings, today, lead := demo.Get()

			// 1) upcoming bills
			upcomingPayload, _ := json.Marshal(map[string]interface{}{
				"transactions": txs,
				"lead_days":    lead,
				"today":        today,
			})

			billsOut, err := runPy(ctx, "python/upcoming_bills.py", nil, upcomingPayload)
			if err != nil {
				return nil, err
			}

			var bills struct {
				TotalUpcomingBills float64       `json:"total_upcoming_bills"`
				UpcomingBills      []interface{} `json:"upcoming_bills"`
			}
			if err := json.Unmarshal(billsOut, &bills); err != nil {
				return nil, fmt.Errorf("upcoming_bills.py returned invalid JSON: %v | output: %s", err, string(billsOut))
			}

			// 2) decision
			decisionPayload, _ := json.Marshal(map[string]interface{}{
				"spend_amount":         req.Amount,
				"wallet_balance":       wallet,
				"savings_balance":      savings,
				"lead_days":            lead,
				"today":                today,
				"upcoming_bills":       bills.UpcomingBills,
				"total_upcoming_bills": bills.TotalUpcomingBills,
			})

			decisionOut, err := runPy(ctx, "python/spend_guardrail.py", nil, decisionPayload)
			if err != nil {
				return nil, err
			}

			// use map so we don't break if python adds fields later
			var decision map[string]interface{}
			if err := json.Unmarshal(decisionOut, &decision); err != nil {
				return nil, fmt.Errorf("spend_guardrail.py returned invalid JSON: %v | output: %s", err, string(decisionOut))
			}

			if decision["decision"] == "allow" {
				if wallet-req.Amount < -1e-6 {
					return map[string]interface{}{
						"decision": "block",
						"error":    "Blocked: would make wallet negative.",
					}, nil
				}

				demo.ApplyWalletDelta(-req.Amount)

				store.Append(Transaction{
					Timestamp: time.Now().UTC().Format(time.RFC3339),
					Merchant:  fmt.Sprintf("Send money to %s (demo)", req.To),
					Amount:    -req.Amount,
					Currency:  "GBP",
					ID:        fmt.Sprintf("demo_%d", time.Now().UnixNano()),
					RawType:   "send_money_demo",
					Status:    "completed",
					UsdValue:  -req.Amount,
				})

				newWallet, newSavings, _, _ := demo.Get()
				decision["balances"] = map[string]interface{}{
					"wallet_balance":  newWallet,
					"savings_balance": newSavings,
				}
			} else {
				decision["balances"] = map[string]interface{}{
					"wallet_balance":  wallet,
					"savings_balance": savings,
				}
			}

			decision["to"] = req.To
			decision["amount"] = req.Amount

			return decision, nil
		}).Build())

	// Tool 9: demo_withdraw_from_savings (NEW)
	srv.AddTool(tools.New("demo_withdraw_from_savings").
		Description(`
DEMO SAVINGS WITHDRAWAL.

Simulate withdrawing money from the demo savings account into the demo wallet.
This tool updates demo state (wallet + savings) and appends a withdrawal transaction to history.

Rules:
- Amount must be > 0
- Savings must be sufficient (savings_balance >= amount)
- No money moves outside demo state
`).
		Schema(tools.ObjectSchema(map[string]interface{}{
			"amount": tools.NumberProperty("Amount to withdraw from demo savings into demo wallet"),
		}, "amount")).
		HandlerFunc(func(ctx context.Context, input json.RawMessage) (interface{}, error) {
			var req struct {
				Amount float64 `json:"amount"`
			}
			_ = json.Unmarshal(input, &req)
			if req.Amount <= 0 {
				return map[string]interface{}{"error": "amount must be > 0"}, nil
			}

			wallet, savings, today, lead := demo.Get()
			if savings+1e-9 < req.Amount {
				return map[string]interface{}{
					"error":           "Insufficient demo savings for withdrawal",
					"wallet_balance":  wallet,
					"savings_balance": savings,
					"requested":       req.Amount,
				}, nil
			}

			// Call Python helper to build the transaction + deltas
			payload, _ := json.Marshal(map[string]interface{}{
				"amount": req.Amount,
			})
			out, err := runPy(ctx, "python/demo_withdraw_from_savings.py", nil, payload)
			if err != nil {
				return nil, err
			}

			var py struct {
				WalletDelta  float64 `json:"wallet_delta"`
				SavingsDelta float64 `json:"savings_delta"`
				Transaction  struct {
					Timestamp string  `json:"timestamp"`
					Merchant  string  `json:"merchant"`
					Amount    float64 `json:"amount"`
					Currency  string  `json:"currency"`
					Type      string  `json:"type"`
					Status    string  `json:"status"`
					UsdValue  float64 `json:"usdValue"`
				} `json:"transaction"`
				Error string `json:"error"`
			}
			if err := json.Unmarshal(out, &py); err != nil {
				return nil, fmt.Errorf("python demo_withdraw_from_savings returned invalid JSON: %v | output: %s", err, string(out))
			}
			if py.Error != "" {
				return map[string]interface{}{"error": py.Error}, nil
			}

			// Validate deltas (belt-and-braces)
			if py.WalletDelta <= 0 || py.SavingsDelta >= 0 {
				return map[string]interface{}{"error": "invalid deltas from python helper"}, nil
			}
			if savings+py.SavingsDelta < -1e-6 {
				return map[string]interface{}{"error": "withdrawal would make savings negative"}, nil
			}

			// Apply deltas
			demo.ApplyWalletDelta(py.WalletDelta)
			demo.ApplySavingsDelta(py.SavingsDelta)

			// Append transaction
			store.Append(Transaction{
				Timestamp: py.Transaction.Timestamp,
				Merchant:  py.Transaction.Merchant,
				Amount:    py.Transaction.Amount,
				Currency:  py.Transaction.Currency,
				ID:        fmt.Sprintf("demo_%d", time.Now().UnixNano()),
				RawType:   "withdraw_savings_demo",
				Status:    py.Transaction.Status,
				UsdValue:  py.Transaction.UsdValue,
			})

			newWallet, newSavings, _, _ := demo.Get()

			return map[string]interface{}{
				"status": "withdraw_completed",
				"amount": req.Amount,
				"balances": map[string]interface{}{
					"wallet_balance":  newWallet,
					"savings_balance": newSavings,
				},
				"demo_context": map[string]interface{}{
					"today":     today,
					"lead_days": lead,
				},
				"message": "Demo savings withdrawal applied (savings -> wallet). You can retry demo_send_money now.",
			}, nil
		}).Build())

	log.Println("Nim demo server running on :8080")
	srv.Run(":8080")
}
