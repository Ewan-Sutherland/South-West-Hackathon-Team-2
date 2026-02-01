package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
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

// Normalized format we store in memory after Python parses the demo JSON.
type Transaction struct {
	Timestamp string  `json:"timestamp"`
	Merchant  string  `json:"merchant"`
	Amount    float64 `json:"amount"`   // signed: +income, -spend
	Currency  string  `json:"currency"` // e.g. GBP
	ID        string  `json:"id,omitempty"`
	RawType   string  `json:"type,omitempty"`
	Status    string  `json:"status,omitempty"`
	UsdValue  float64 `json:"usdValue,omitempty"`
}

// In-memory transaction store
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

// Demo state store (balances + date + lead window)
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

func (d *DemoState) Apply(walletDelta, savingsDelta float64) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.WalletBalance += walletDelta
	d.SavingsBalance += savingsDelta
}

/* =======================
   Helpers
   ======================= */

func round2(x float64) float64 {
	return math.Round(x*100) / 100
}

// Compute wallet + demo savings from the loaded normalized tx list.
// Savings is inferred ONLY from demo transfer types:
// - deposit_savings_demo: wallet outflow, savings += abs(amount)
// - withdraw_savings_demo: wallet inflow, savings -= amount
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
	return round2(wallet), round2(savings)
}

// Runs a Python script. Uses stdin if provided, args if provided.
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

// Small helper for CORS so localhost:5173 can fetch the JSON without proxy.
func setCORS(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "http://localhost:5173")
	w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
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

	/* ---------- Tool 1: ready_for_demo ---------- */
	srv.AddTool(tools.New("ready_for_demo").
		Description(`
Load the demo transaction history and initialise demo state automatically.

What it does:
- Loads + normalises transactions into memory
- Computes demo wallet balance from full history
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
				"message": "Demo loaded. Try demo_send_money to get options. If recommended, withdraw (demo_withdraw_from_savings), then retry demo_send_money.",
			}, nil
		}).Build())

	/* ---------- Tool 2: demo_state ---------- */
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

	/* ---------- Tool 3: get_transaction_history ---------- */
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

	/* ---------- Tool 4: summarise_transactions ---------- */
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

	/* ---------- Tool 4b: demo_spending_dashboard ---------- */
	srv.AddTool(tools.New("demo_spending_dashboard").
		Description("Rank and summarise spending over the loaded demo transaction history. Requires ready_for_demo first.").
		Schema(tools.ObjectSchema(map[string]interface{}{
			"top_n": tools.NumberProperty("Number of ranked items to return (default: 8)"),
		}, "")).
		HandlerFunc(func(ctx context.Context, args json.RawMessage) (interface{}, error) {
			txs := store.Get()
			if len(txs) == 0 {
				return map[string]interface{}{"error": "No transactions loaded. Say 'ready for demo' first."}, nil
			}

			topN := 8
			if len(args) > 0 {
				var a map[string]interface{}
				if err := json.Unmarshal(args, &a); err == nil {
					if v, ok := a["top_n"]; ok {
						if n, ok := v.(float64); ok {
							if int(n) > 0 {
								topN = int(n)
							}
						}
					}
				}
			}

			payload, _ := json.Marshal(map[string]interface{}{
				"transactions": txs,
				"top_n":        topN,
			})
			out, err := runPy(ctx, "python/expense_ranking.py", nil, payload)
			if err != nil {
				return nil, err
			}

			var result interface{}
			if err := json.Unmarshal(out, &result); err != nil {
				return nil, fmt.Errorf("python expense ranking returned invalid JSON: %v | output: %s", err, string(out))
			}
			return map[string]interface{}{
				"message":            "📊 Open the full spending dashboard: http://localhost:8090/dashboard\nRaw JSON: http://localhost:8090/dashboard.json",
				"dashboard_url":      "http://localhost:8090/dashboard",
				"dashboard_json_url": "http://localhost:8090/dashboard.json",
				"analytics":          result,
			}, nil
		}).Build())

	/* ---------- Tool 5: income_tracker ---------- */
	srv.AddTool(tools.New("income_tracker").
		Description(`
INCOME ONLY.
Analyse ONLY positive inflows from the loaded transaction history.

Rules:
- Ignore all expenses (rent, bills, subscriptions).
- In your response, ONLY report tool output fields.
- Do NOT infer expenses or categories outside the tool output.
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

	/* ---------- Tool 6: bills_tracker ---------- */
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

	/* ---------- Tool 7: upcoming_bills (debug) ---------- */
	srv.AddTool(tools.New("upcoming_bills").
		Description(`
Return upcoming recurring bills within the current demo lead window (default 3 days).
Uses demo today/lead_days automatically. Does not move funds.
`).
		Schema(tools.ObjectSchema(map[string]interface{}{}, "")).
		HandlerFunc(func(ctx context.Context, _ json.RawMessage) (interface{}, error) {
			txs := store.Get()
			if len(txs) == 0 {
				return map[string]interface{}{"error": "No transactions loaded. Say 'ready for demo' first."}, nil
			}
			_, _, today, lead := demo.Get()

			payload, _ := json.Marshal(map[string]interface{}{
				"transactions": txs,
				"today":        today,
				"lead_days":    lead,
			})
			out, err := runPy(ctx, "python/upcoming_bills.py", nil, payload)
			if err != nil {
				return nil, err
			}

			var result interface{}
			if err := json.Unmarshal(out, &result); err != nil {
				return nil, fmt.Errorf("upcoming_bills.py returned invalid JSON: %v | output: %s", err, string(out))
			}
			return result, nil
		}).Build())

	/* ---------- Tool 8: spend_guardrail (manual inputs) ---------- */
	srv.AddTool(tools.New("spend_guardrail").
		Description(`
DECISION ONLY (manual balances).
Determine whether a proposed spend would endanger upcoming bills.

This tool does NOT move money.
It protects upcoming bills only (no living buffer).
If wallet cash would be insufficient, it checks savings and may recommend a savings top-up amount.
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

			upcomingPayload, _ := json.Marshal(map[string]interface{}{
				"transactions": txs,
				"today":        req.Today,
				"lead_days":    req.LeadDays,
			})
			billsOut, err := runPy(ctx, "python/upcoming_bills.py", nil, upcomingPayload)
			if err != nil {
				return nil, err
			}

			var bills map[string]interface{}
			if err := json.Unmarshal(billsOut, &bills); err != nil {
				return nil, fmt.Errorf("upcoming_bills.py returned invalid JSON: %v | output: %s", err, string(billsOut))
			}

			decisionPayload, _ := json.Marshal(map[string]interface{}{
				"spend_amount":         req.SpendAmount,
				"wallet_balance":       req.WalletBalance,
				"savings_balance":      req.SavingsBalance,
				"today":                req.Today,
				"lead_days":            req.LeadDays,
				"upcoming_bills":       bills["upcoming_bills"],
				"total_upcoming_bills": bills["total_upcoming_bills"],
			})
			out, err := runPy(ctx, "python/spend_guardrail.py", nil, decisionPayload)
			if err != nil {
				return nil, err
			}

			var result interface{}
			if err := json.Unmarshal(out, &result); err != nil {
				return nil, fmt.Errorf("spend_guardrail.py returned invalid JSON: %v | output: %s", err, string(out))
			}
			return result, nil
		}).Build())

	/* ---------- Tool 9: demo_send_money (OPTIONS ONLY, no funds moved) ---------- */
	srv.AddTool(tools.New("demo_send_money").
		Description(`
DEMO INTERCEPTOR (OPTIONS ONLY).

This tool does NOT move money. It evaluates a proposed payment using demo state.
It returns explicit options the UI/agent must present.

Important UI rule:
- "Send now" is only offered when decision == "allow".
- If decision != "allow", only show:
  - Cancel
  - Withdraw from savings (if recommended_withdrawal > 0)
  - Override and send anyway (requires confirmation)
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

			upcomingPayload, _ := json.Marshal(map[string]interface{}{
				"transactions": txs,
				"today":        today,
				"lead_days":    lead,
			})
			billsOut, err := runPy(ctx, "python/upcoming_bills.py", nil, upcomingPayload)
			if err != nil {
				return nil, err
			}
			var bills map[string]interface{}
			if err := json.Unmarshal(billsOut, &bills); err != nil {
				return nil, fmt.Errorf("upcoming_bills.py returned invalid JSON: %v | output: %s", err, string(billsOut))
			}

			decisionPayload, _ := json.Marshal(map[string]interface{}{
				"spend_amount":         req.Amount,
				"wallet_balance":       wallet,
				"savings_balance":      savings,
				"today":                today,
				"lead_days":            lead,
				"upcoming_bills":       bills["upcoming_bills"],
				"total_upcoming_bills": bills["total_upcoming_bills"],
			})
			decisionOut, err := runPy(ctx, "python/spend_guardrail.py", nil, decisionPayload)
			if err != nil {
				return nil, err
			}

			var decision map[string]interface{}
			if err := json.Unmarshal(decisionOut, &decision); err != nil {
				return nil, fmt.Errorf("spend_guardrail.py returned invalid JSON: %v | output: %s", err, string(decisionOut))
			}

			recommended := 0.0
			switch v := decision["recommended_savings_withdrawal"].(type) {
			case float64:
				recommended = v
			case json.Number:
				f, _ := v.Float64()
				recommended = f
			}

			options := []map[string]interface{}{
				{
					"id":      "deny",
					"label":   "Cancel",
					"tool":    "",
					"summary": "Cancel this payment. No funds moved.",
				},
			}

			if recommended > 0 {
				options = append(options, map[string]interface{}{
					"id":    "withdraw_from_savings",
					"label": fmt.Sprintf("Withdraw £%.2f from savings", recommended),
					"tool":  "demo_withdraw_from_savings",
					"params": map[string]interface{}{
						"amount": recommended,
					},
					"summary": "Withdraw from demo savings into wallet (separate step), then retry demo_send_money.",
				})
			}

			if decision["decision"] == "allow" {
				options = append(options, map[string]interface{}{
					"id":    "proceed_send",
					"label": "Send now",
					"tool":  "demo_send_money_apply",
					"params": map[string]interface{}{
						"to":     req.To,
						"amount": req.Amount,
					},
					"summary": "Send the payment. Upcoming bills remain covered.",
				})
			}

			options = append(options, map[string]interface{}{
				"id":    "override_send",
				"label": "Override and send anyway",
				"tool":  "demo_send_money_override",
				"params": map[string]interface{}{
					"to":     req.To,
					"amount": req.Amount,
				},
				"summary": "Override the safeguard and send anyway (requires confirmation).",
			})

			return map[string]interface{}{
				"to":                             req.To,
				"amount":                         req.Amount,
				"decision":                       decision["decision"],
				"message":                        decision["message"],
				"explanation":                    decision["explanation"],
				"upcoming_bills":                 decision["upcoming_bills"],
				"recommended_savings_withdrawal": decision["recommended_savings_withdrawal"],
				"balances": map[string]interface{}{
					"wallet_balance":  wallet,
					"savings_balance": savings,
				},
				"demo_context": map[string]interface{}{
					"today":     today,
					"lead_days": lead,
				},
				"options": options,
			}, nil
		}).Build())

	/* ---------- Tool 10: demo_withdraw_from_savings ---------- */
	srv.AddTool(tools.New("demo_withdraw_from_savings").
		Description(`
DEMO SAVINGS WITHDRAWAL (separate step).

Simulate withdrawing money from demo savings into the demo wallet.
- updates demo state
- appends a withdrawal transaction

Rules:
- amount must be > 0
- savings must be sufficient
`).
		Schema(tools.ObjectSchema(map[string]interface{}{
			"amount": tools.NumberProperty("Amount to withdraw from demo savings into demo wallet"),
		}, "amount")).
		HandlerFunc(func(ctx context.Context, input json.RawMessage) (interface{}, error) {
			var req struct {
				Amount float64 `json:"amount"`
			}
			_ = json.Unmarshal(input, &req)
			req.Amount = round2(req.Amount)

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

			demo.Apply(req.Amount, -req.Amount)

			store.Append(Transaction{
				Timestamp: time.Now().UTC().Format(time.RFC3339),
				Merchant:  "Savings withdrawal (demo)",
				Amount:    req.Amount,
				Currency:  "GBP",
				ID:        fmt.Sprintf("demo_%d", time.Now().UnixNano()),
				RawType:   "withdraw_savings_demo",
				Status:    "completed",
				UsdValue:  req.Amount,
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
				"message": "Withdrawal applied. Retry demo_send_money now.",
			}, nil
		}).Build())

	/* ---------- Tool 11: demo_send_money_apply ---------- */
	srv.AddTool(tools.New("demo_send_money_apply").
		Description(`
DEMO SEND (normal).

Applies the send immediately (no confirmation).
Use this after demo_send_money if you choose to proceed normally.

Note: This tool does not re-run guardrails. The guardrail is done by demo_send_money.
`).
		Schema(tools.ObjectSchema(map[string]interface{}{
			"to":     tools.StringProperty("Recipient"),
			"amount": tools.NumberProperty("Amount"),
		}, "to", "amount")).
		HandlerFunc(func(ctx context.Context, input json.RawMessage) (interface{}, error) {
			var req struct {
				To     string  `json:"to"`
				Amount float64 `json:"amount"`
			}
			_ = json.Unmarshal(input, &req)
			req.Amount = round2(req.Amount)

			if req.Amount <= 0 {
				return map[string]interface{}{"error": "amount must be > 0"}, nil
			}

			wallet, _, today, lead := demo.Get()
			if wallet-req.Amount < -1e-9 {
				return map[string]interface{}{
					"error":          "Insufficient demo wallet balance",
					"wallet_balance": wallet,
					"requested":      req.Amount,
				}, nil
			}

			demo.Apply(-req.Amount, 0)

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
			return map[string]interface{}{
				"status": "sent",
				"to":     req.To,
				"amount": req.Amount,
				"balances": map[string]interface{}{
					"wallet_balance":  newWallet,
					"savings_balance": newSavings,
				},
				"demo_context": map[string]interface{}{
					"today":     today,
					"lead_days": lead,
				},
			}, nil
		}).Build())

	/* ---------- Tool 12: demo_send_money_override ---------- */
	srv.AddTool(tools.New("demo_send_money_override").
		Description(`
DEMO OVERRIDE SEND (requires confirmation).

Overrides guardrail and applies send anyway.
Use this only if user explicitly chooses the override option.
`).
		RequiresConfirmation().
		SummaryTemplate("Override and send £{{.amount}} to {{.to}} (demo)").
		Schema(tools.ObjectSchema(map[string]interface{}{
			"to":     tools.StringProperty("Recipient"),
			"amount": tools.NumberProperty("Amount"),
		}, "to", "amount")).
		HandlerFunc(func(ctx context.Context, input json.RawMessage) (interface{}, error) {
			var req struct {
				To     string  `json:"to"`
				Amount float64 `json:"amount"`
			}
			_ = json.Unmarshal(input, &req)
			req.Amount = round2(req.Amount)

			if req.Amount <= 0 {
				return map[string]interface{}{"error": "amount must be > 0"}, nil
			}

			wallet, _, today, lead := demo.Get()
			if wallet-req.Amount < -1e-9 {
				return map[string]interface{}{
					"error":          "Insufficient demo wallet balance",
					"wallet_balance": wallet,
					"requested":      req.Amount,
				}, nil
			}

			demo.Apply(-req.Amount, 0)

			store.Append(Transaction{
				Timestamp: time.Now().UTC().Format(time.RFC3339),
				Merchant:  fmt.Sprintf("Send money to %s (demo) [OVERRIDE]", req.To),
				Amount:    -req.Amount,
				Currency:  "GBP",
				ID:        fmt.Sprintf("demo_%d", time.Now().UnixNano()),
				RawType:   "send_money_demo_override",
				Status:    "completed",
				UsdValue:  -req.Amount,
			})

			newWallet, newSavings, _, _ := demo.Get()
			return map[string]interface{}{
				"status": "sent_override",
				"to":     req.To,
				"amount": req.Amount,
				"balances": map[string]interface{}{
					"wallet_balance":  newWallet,
					"savings_balance": newSavings,
				},
				"demo_context": map[string]interface{}{
					"today":     today,
					"lead_days": lead,
				},
			}, nil
		}).Build())

	// ---------- Minimal local web dashboard + frontend JSON endpoints ----------
	// Runs on :8090 so it doesn't interfere with the NIM server.
	// Endpoints:
	//   http://localhost:8090/dashboard       (HTML)
	//   http://localhost:8090/dashboard.json  (raw analytics JSON)
	//   http://localhost:8090/state.json      (wallet+savings for the 5173 page)
	//   http://localhost:8090/income.json     (auto-run income_tracker for side panel)
	//   http://localhost:8090/bills.json      (auto-run bills_tracker + upcoming_bills for side panel)

	http.HandleFunc("/state.json", func(w http.ResponseWriter, r *http.Request) {
		setCORS(w)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		wallet, savings, today, lead := demo.Get()
		resp := map[string]interface{}{
			"wallet_balance":  wallet,
			"savings_balance": savings,
			"today":           today,
			"lead_days":       lead,
			"loaded":          len(store.Get()) > 0,
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})

	// NEW: income.json (for the left panel)
	http.HandleFunc("/income.json", func(w http.ResponseWriter, r *http.Request) {
		setCORS(w)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		txs := store.Get()
		if len(txs) == 0 {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"error":"No transactions loaded. Run 'ready_for_demo' first."}`))
			return
		}

		stdin, _ := json.Marshal(map[string]interface{}{
			"transactions": txs,
		})
		out, err := runPy(r.Context(), "python/income_tracker.py", nil, stdin)
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(fmt.Sprintf(`{"error":"%s"}`, err.Error())))
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(out)
	})

	// NEW: bills.json (for the right panel) – merges recurring + upcoming
	http.HandleFunc("/bills.json", func(w http.ResponseWriter, r *http.Request) {
		setCORS(w)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		txs := store.Get()
		if len(txs) == 0 {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"error":"No transactions loaded. Run 'ready_for_demo' first."}`))
			return
		}

		_, _, today, lead := demo.Get()

		// 1) recurring bills
		stdinBills, _ := json.Marshal(map[string]interface{}{
			"transactions": txs,
		})
		billsOut, err := runPy(r.Context(), "python/bills_tracker.py", nil, stdinBills)
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(fmt.Sprintf(`{"error":"%s"}`, err.Error())))
			return
		}

		// 2) upcoming bills (lead window)
		stdinUpcoming, _ := json.Marshal(map[string]interface{}{
			"transactions": txs,
			"today":        today,
			"lead_days":    lead,
		})
		upOut, err := runPy(r.Context(), "python/upcoming_bills.py", nil, stdinUpcoming)
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(fmt.Sprintf(`{"error":"%s"}`, err.Error())))
			return
		}

		// Merge JSON into one object for frontend simplicity
		var billsObj map[string]interface{}
		if err := json.Unmarshal(billsOut, &billsObj); err != nil {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(fmt.Sprintf(`{"error":"bills_tracker invalid JSON: %s"}`, err.Error())))
			return
		}

		var upObj map[string]interface{}
		if err := json.Unmarshal(upOut, &upObj); err != nil {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(fmt.Sprintf(`{"error":"upcoming_bills invalid JSON: %s"}`, err.Error())))
			return
		}

		// Normalize for frontend:
		// billsObj might have recurring lists under different keys depending on your script;
		// we add these universally:
		billsObj["today"] = today
		billsObj["lead_days"] = lead
		billsObj["upcoming_bills"] = upObj["upcoming_bills"]
		billsObj["total_upcoming_bills"] = upObj["total_upcoming_bills"]

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(billsObj)
	})

	http.HandleFunc("/dashboard.json", func(w http.ResponseWriter, r *http.Request) {
		setCORS(w)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		txs := store.Get()
		if len(txs) == 0 {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"error":"No transactions loaded. Run 'ready_for_demo' first."}`))
			return
		}

		stdin, _ := json.Marshal(map[string]interface{}{
			"transactions": txs,
			"top_n":        8,
		})
		out, err := runPy(r.Context(), "python/expense_ranking.py", nil, stdin)
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(fmt.Sprintf(`{"error":"%s"}`, err.Error())))
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(out)
	})

	http.HandleFunc("/dashboard", func(w http.ResponseWriter, r *http.Request) {
		txs := store.Get()
		if len(txs) == 0 {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(`<h2>No demo loaded</h2><p>Run <code>ready for demo</code> first, then refresh.</p>`))
			return
		}

		stdin, _ := json.Marshal(map[string]interface{}{
			"transactions": txs,
			"top_n":        8,
		})
		out, err := runPy(r.Context(), "python/expense_ranking.py", nil, stdin)
		if err != nil {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(`<h2>Error</h2><pre>` + err.Error() + `</pre>`))
			return
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<!doctype html>
<html>
<head>
  <meta charset="utf-8" />
  <meta name="viewport" content="width=device-width, initial-scale=1" />
  <title>NIM Demo Spending Dashboard</title>
  <style>
    body{font-family:system-ui, -apple-system, Segoe UI, Roboto, sans-serif; margin:24px; color:#111;}
    .row{display:flex; gap:18px; flex-wrap:wrap;}
    .card{border:1px solid #e5e7eb; border-radius:14px; padding:16px; box-shadow:0 1px 2px rgba(0,0,0,0.04); min-width:320px; flex:1;}
    h1{font-size:20px; margin:0 0 10px;}
    h2{font-size:14px; margin:0 0 10px; color:#374151;}
    table{width:100%; border-collapse:collapse; font-size:13px;}
    th, td{padding:8px 6px; border-bottom:1px solid #f1f5f9; text-align:left;}
    .bar{height:10px; background:#111; border-radius:999px;}
    .muted{color:#6b7280; font-size:12px;}
    .pill{display:inline-block; padding:2px 8px; border-radius:999px; font-size:12px; border:1px solid #e5e7eb;}
    .green{background:#ecfdf5;}
    .amber{background:#fffbeb;}
    .red{background:#fef2f2;}
    code{background:#f3f4f6; padding:2px 6px; border-radius:6px;}
  </style>
</head>
<body>
  <h1>NIM Demo Spending Dashboard</h1>
  <div class="muted">
    Source: in-memory demo transactions. Refresh after <code>ready for demo</code>.
    JSON: <a href="/dashboard.json">/dashboard.json</a>
    | State: <a href="/state.json">/state.json</a>
    | Income: <a href="/income.json">/income.json</a>
    | Bills: <a href="/bills.json">/bills.json</a>
  </div>

  <script id="data" type="application/json">` + string(out) + `</script>
  <script>
    // unchanged dashboard script...
  </script>
</body>
</html>`))
	})

	go func() {
		log.Println("Spending dashboard: http://localhost:8090/dashboard")
		log.Println("State JSON for frontend: http://localhost:8090/state.json")
		log.Println("Income JSON for frontend: http://localhost:8090/income.json")
		log.Println("Bills JSON for frontend: http://localhost:8090/bills.json")
		if err := http.ListenAndServe(":8090", nil); err != nil {
			log.Println("dashboard server stopped:", err)
		}
	}()

	log.Println("Nim demo server running on :8080")
	_ = srv.Run(":8080")
}
