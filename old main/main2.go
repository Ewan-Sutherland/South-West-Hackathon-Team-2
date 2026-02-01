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

			// upcoming bills (auto today/lead)
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

			// Pull recommended withdrawal (robustly)
			recommended := 0.0
			switch v := decision["recommended_savings_withdrawal"].(type) {
			case float64:
				recommended = v
			case json.Number:
				f, _ := v.Float64()
				recommended = f
			}

			// Build options:
			// Always: Cancel
			options := []map[string]interface{}{
				{
					"id":      "deny",
					"label":   "Cancel",
					"tool":    "",
					"summary": "Cancel this payment. No funds moved.",
				},
			}

			// If savings can help, show withdraw option (separate step)
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

			// Only show normal send when guardrail says allow
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

			// Always show override (confirmation-required tool)
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

	/* ---------- Tool 10: demo_withdraw_from_savings (separate step) ---------- */
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

	/* ---------- Tool 11: demo_send_money_apply (normal send; no confirmation) ---------- */
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

	/* ---------- Tool 12: demo_send_money_override (confirmation required) ---------- */
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

	// ---------- Minimal local web dashboard (read-only) ----------
	// Runs on :8090 so it doesn't interfere with the NIM server.
	// Endpoints:
	//   http://localhost:8090/dashboard      (HTML)
	//   http://localhost:8090/dashboard.json (raw JSON)
	//
	// Uses the same source of truth as tools: in-memory TxStore (loaded by ready_for_demo).

	http.HandleFunc("/dashboard.json", func(w http.ResponseWriter, r *http.Request) {
		txs := store.Get()
		if len(txs) == 0 {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"error":"No transactions loaded. Run 'ready for demo' first."}`))
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

		// NEW: wrap analytics with context (bank + savings balances)
		var analytics interface{}
		if err := json.Unmarshal(out, &analytics); err != nil {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(fmt.Sprintf(`{"error":"invalid analytics json: %s"}`, err.Error())))
			return
		}

		wallet, savings, today, leadDays := demo.Get()
		resp := map[string]interface{}{
			"context": map[string]interface{}{
				"bank_balance":    wallet,
				"savings_balance": savings,
				"today":           today,
				"lead_days":       leadDays,
			},
			"analytics": analytics,
		}

		b, _ := json.Marshal(resp)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(b)
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

		// NEW: embed context + analytics for the UI
		var analytics interface{}
		if err := json.Unmarshal(out, &analytics); err != nil {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(`<h2>Error</h2><pre>Invalid analytics JSON: ` + err.Error() + `</pre>`))
			return
		}

		wallet, savings, today, leadDays := demo.Get()
		page := map[string]interface{}{
			"context": map[string]interface{}{
				"bank_balance":    wallet,
				"savings_balance": savings,
				"today":           today,
				"lead_days":       leadDays,
			},
			"analytics": analytics,
		}
		pageJSON, _ := json.Marshal(page)

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
    .card{border:1px solid #e5e7eb; border-radius:16px; padding:16px; box-shadow:0 1px 2px rgba(0,0,0,0.04); min-width:320px; flex:1;}
    h1{font-size:20px; margin:0 0 10px;}
    h2{font-size:14px; margin:0 0 10px; color:#374151;}
    .big{font-size:28px; font-weight:750; margin-top:6px;}
    table{width:100%; border-collapse:collapse; font-size:13px;}
    th, td{padding:8px 6px; border-bottom:1px solid #f1f5f9; text-align:left;}
    .bar{height:10px; background:#111; border-radius:999px;}
    .muted{color:#6b7280; font-size:12px;}
    .pill{display:inline-block; padding:2px 8px; border-radius:999px; font-size:12px; border:1px solid #e5e7eb;}
    .green{background:#ecfdf5;}
    .amber{background:#fffbeb;}
    .red{background:#fef2f2;}
    code{background:#f3f4f6; padding:2px 6px; border-radius:6px;}
    input{outline:none;}
    button{outline:none;}
  </style>
</head>
<body>
  <h1>NIM Demo Spending Dashboard</h1>
  <div class="muted">
    Source: in-memory demo transactions. Refresh after <code>ready for demo</code>.
    JSON: <a href="/dashboard.json">/dashboard.json</a>
  </div>

  <script id="data" type="application/json">` + string(pageJSON) + `</script>
  <script>
  const data = JSON.parse(document.getElementById("data").textContent || "{}");
  const ctx = data.context || {};
  const a = data.analytics || {};

  function get(obj, path, fallback) {
    try {
      // avoid optional chaining; keep deterministic / compatible
      let cur = obj;
      for (let i = 0; i < path.length; i++) {
        if (!cur || cur[path[i]] === undefined) return fallback;
        cur = cur[path[i]];
      }
      return cur === undefined ? fallback : cur;
    } catch (e) {
      return fallback;
    }
  }

  function money(x){
    const cur = (a.summary && a.summary.currency) ? a.summary.currency : "GBP";
    return cur + " " + (Number(x || 0).toFixed(2));
  }

  function renderTable(container, rows, cols){
    const table = document.createElement("table");
    const thead = document.createElement("thead");
    const trh = document.createElement("tr");
    cols.forEach(c=>{
      const th=document.createElement("th"); th.textContent=c.label; trh.appendChild(th);
    });
    thead.appendChild(trh); table.appendChild(thead);

    const tb = document.createElement("tbody");
    (rows || []).forEach(r=>{
      const tr=document.createElement("tr");
      cols.forEach(c=>{
        const td=document.createElement("td");
        const val = (r && r[c.key] !== undefined) ? r[c.key] : "";
        td.textContent = c.f ? c.f(val, r) : String(val);
        tr.appendChild(td);
      });
      tb.appendChild(tr);
    });
    table.appendChild(tb);
    container.appendChild(table);
  }

  function renderBars(container, rows, labelKey, valueKey){
    rows = rows || [];
    let max = 1;
    rows.forEach(r=>{ max = Math.max(max, Number(r && r[valueKey] || 0)); });

    rows.forEach(r=>{
      const wrap=document.createElement("div");
      wrap.style.margin="10px 0";
      const top=document.createElement("div");
      top.style.display="flex";
      top.style.justifyContent="space-between";
      top.style.gap="10px";

      const left = document.createElement("div");
      left.textContent = (r && r[labelKey]) ? String(r[labelKey]) : "";
      const right = document.createElement("div");
      right.className = "muted";
      right.textContent = money(r && r[valueKey]);

      top.appendChild(left);
      top.appendChild(right);

      const bar=document.createElement("div");
      bar.className="bar";
      bar.style.width = (Math.round((Number(r && r[valueKey] || 0)/max)*100)) + "%";
      wrap.appendChild(top);
      wrap.appendChild(bar);
      container.appendChild(wrap);
    });
  }

  // ---------- TOP ROW: Bank balance + Savings balance + Send money ----------
  const topRow = document.createElement("div");
  topRow.className = "row";

  function makeBalanceCard(title, value){
    const card = document.createElement("div");
    card.className = "card";
    const meta = "As of: " + (ctx.today || "—") + " | lead window: " + String(ctx.lead_days ?? "—") + " days";
    card.innerHTML =
      '<h2>' + title + '</h2>' +
      '<div class="big">' + money(value) + '</div>' +
      '<div class="muted" style="margin-top:8px;">' + meta + '</div>';
    return card;
  }

  const bankCard = makeBalanceCard("Bank balance", ctx.bank_balance);
  const savingsCard = makeBalanceCard("Savings balance", ctx.savings_balance);

  const actionsCard = document.createElement("div");
  actionsCard.className = "card";
  actionsCard.innerHTML =
    '<h2>Quick actions</h2>' +
    '<button id="sendBtn" style="padding:10px 12px; border-radius:12px; border:1px solid #e5e7eb; background:#111; color:#fff; cursor:pointer;">Send money</button>' +
    '<div id="sendForm" style="display:none; margin-top:12px;">' +
      '<div class="muted" style="margin-bottom:8px;">This generates a Nim Chat command so guardrails + confirmation still run.</div>' +
      '<div style="display:flex; gap:10px; flex-wrap:wrap;">' +
        '<input id="toInput" placeholder="@alice" style="flex:1; min-width:140px; padding:10px; border:1px solid #e5e7eb; border-radius:12px;" />' +
        '<input id="amtInput" placeholder="Amount" type="number" step="0.01" style="width:140px; padding:10px; border:1px solid #e5e7eb; border-radius:12px;" />' +
      '</div>' +
      '<div style="display:flex; gap:10px; margin-top:10px; flex-wrap:wrap;">' +
        '<button id="genCmdBtn" style="padding:10px 12px; border-radius:12px; border:1px solid #e5e7eb; background:#fff; cursor:pointer;">Generate command</button>' +
        '<button id="copyCmdBtn" style="padding:10px 12px; border-radius:12px; border:1px solid #e5e7eb; background:#fff; cursor:pointer;" disabled>Copy</button>' +
        '<a href="http://localhost:5173" target="_blank" rel="noreferrer" style="padding:10px 12px; border-radius:12px; border:1px solid #e5e7eb; text-decoration:none; color:#111;">Open Nim Chat</a>' +
      '</div>' +
      '<pre id="cmdOut" style="margin-top:10px; background:#f3f4f6; padding:10px; border-radius:12px; white-space:pre-wrap;"></pre>' +
    '</div>';

  topRow.appendChild(bankCard);
  topRow.appendChild(savingsCard);
  topRow.appendChild(actionsCard);
  document.body.appendChild(topRow);

  document.getElementById("sendBtn").addEventListener("click", function(){
    const form = document.getElementById("sendForm");
    form.style.display = (form.style.display === "none") ? "block" : "none";
  });

  document.getElementById("genCmdBtn").addEventListener("click", function(){
    const to = (document.getElementById("toInput").value || "").trim();
    const amt = (document.getElementById("amtInput").value || "").trim();
    const cmdOut = document.getElementById("cmdOut");
    const copyBtn = document.getElementById("copyCmdBtn");

    if (!to || !amt) {
      cmdOut.textContent = "Enter both recipient (e.g. @alice) and amount.";
      copyBtn.disabled = true;
      return;
    }

    const cmd = "demo_send_money to " + to + " amount " + amt;
    cmdOut.textContent = "Paste this into Nim Chat:\n\n" + cmd + "\n\n(Guardrails will run before any funds move.)";
    copyBtn.disabled = false;
    copyBtn.dataset.cmd = cmd;
  });

  document.getElementById("copyCmdBtn").addEventListener("click", async function(e){
    const cmd = e.target.dataset.cmd || "";
    if (!cmd) return;
    const cmdOut = document.getElementById("cmdOut");
    try {
      await navigator.clipboard.writeText(cmd);
      cmdOut.textContent = cmdOut.textContent + "\n\n✅ Copied to clipboard.";
    } catch (err) {
      cmdOut.textContent = cmdOut.textContent + "\n\n(Clipboard blocked — manually copy the line.)";
    }
  });

  // ---------- Existing analytics sections ----------
  const root = document.body;

  const summaryCard = document.createElement("div");
  summaryCard.className="card";
  const income = get(a, ["summary","income_total"], 0);
  const spend = get(a, ["summary","spend_total"], 0);
  const net = get(a, ["summary","net"], 0);
  const excludedCount = get(a, ["exclusions","internal_transfers_excluded_from_rankings","count"], 0);

  summaryCard.innerHTML =
    '<h2>Summary</h2>' +
    '<div><b>Income:</b> ' + money(income) + '</div>' +
    '<div><b>Spend:</b> ' + money(spend) + '</div>' +
    '<div><b>Net:</b> ' + money(net) + '</div>' +
    '<div class="muted" style="margin-top:8px;">' +
      'Internal transfers excluded from rankings: ' + String(excludedCount) +
    '</div>';

  const merchantsCard = document.createElement("div");
  merchantsCard.className="card";
  merchantsCard.innerHTML = '<h2>Top merchants (6 months)</h2>';
  renderBars(merchantsCard, get(a, ["rankings","top_merchants"], []), "merchant", "total");

  const catsCard = document.createElement("div");
  catsCard.className="card";
  catsCard.innerHTML = '<h2>Top categories (6 months)</h2>';
  renderBars(catsCard, get(a, ["rankings","top_categories"], []), "category", "total");

  const recurringCard = document.createElement("div");
  recurringCard.className="card";
  recurringCard.innerHTML = '<h2>Recurring payments (heuristic)</h2>';
  renderTable(recurringCard, get(a, ["recurring_payments"], []), [
    {key:"merchant", label:"Merchant"},
    {key:"months_seen", label:"Months"},
    {key:"total", label:"Total", f:(v)=>money(v)},
    {key:"count", label:"Tx"},
  ]);

  const cutsCard = document.createElement("div");
  cutsCard.className="card";
  cutsCard.innerHTML = '<h2>Potential cuts (demo hints)</h2>';
  renderTable(cutsCard, get(a, ["spending_cuts"], []), [
    {key:"category", label:"Category"},
    {key:"monthly_saving_hint", label:"~Monthly", f:(v)=>money(v)},
    {key:"total_6mo", label:"6mo total", f:(v)=>money(v)},
  ]);

  const ratingsCard = document.createElement("div");
  ratingsCard.className="card";
  ratingsCard.innerHTML = '<h2>Recent rated spend (sample)</h2>';
  const rows = get(a, ["transaction_ratings"], []).slice(0, 30);

  const t = document.createElement("table");
  t.innerHTML = '<thead><tr><th>Date</th><th>Merchant</th><th>Category</th><th>Amount</th><th>Risk</th></tr></thead>';
  const tb = document.createElement("tbody");
  rows.forEach(r=>{
    const tr=document.createElement("tr");
    const rating = (r && r.rating) ? String(r.rating) : "green";
    const pillClass = rating === "amber" ? "amber" : (rating === "red" ? "red" : "green");
    const pill = '<span class="pill ' + pillClass + '">' + rating + '</span>';
    tr.innerHTML =
      '<td>' + (r.timestamp||"") + '</td>' +
      '<td>' + (r.merchant||"") + '</td>' +
      '<td>' + (r.category||"") + '</td>' +
      '<td>' + money(r.amount) + '</td>' +
      '<td>' + pill + '</td>';
    tb.appendChild(tr);
  });
  t.appendChild(tb);
  ratingsCard.appendChild(t);

  const row1=document.createElement("div"); row1.className="row";
  row1.appendChild(summaryCard); row1.appendChild(merchantsCard); row1.appendChild(catsCard);

  const row2=document.createElement("div"); row2.className="row";
  row2.appendChild(recurringCard); row2.appendChild(cutsCard); row2.appendChild(ratingsCard);

  root.appendChild(row1);
  root.appendChild(row2);
</script>
</body>
</html>`))
	})

	go func() {
		log.Println("Spending dashboard: http://localhost:8090/dashboard")
		if err := http.ListenAndServe(":8090", nil); err != nil {
			log.Println("dashboard server stopped:", err)
		}
	}()

	log.Println("Nim demo server running on :8080")
	srv.Run(":8080")
}
