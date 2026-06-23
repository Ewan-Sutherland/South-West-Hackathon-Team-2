**Predictive Financial Advice - South West Hackathon (Team 2)**

Our goal for the South West Hackathon was to build a financial assistant that doesn't just track your money but looks ahead and steps in before a payment gets you into trouble. It runs as an AI chat agent on the Nim platform - you talk to it in plain language ("send £300 to @alice") and it intercepts the request, checks what bills are coming up, and warns you if the payment would leave you short.

We focused on predictive financial advice rather than after-the-fact reporting: predicting incoming payments and upcoming obligations, warning about risky spending before it happens, and flagging overspending that could be cut.

**The core idea - the spend guardrail**

The headline feature is an interceptor that sits in front of every payment. The example we built the demo around: you go to send £300 to a friend, but £800 of rent is due in 2 days and no salary is scheduled to land before then - sending the £300 would leave you unable to cover the rent.

Instead of letting the payment through, the assistant stops and gives you options:
1. Cancel the payment.
2. Rebalance - move money from savings to cover it (a separate, explicit step).
3. Send anyway - an override that requires confirmation.

"Send now" is only offered when the payment is actually safe; if it isn't, that option doesn't appear at all. The decision stays with the user - the agent just makes the consequences visible first.

**The spending dashboard**

Alongside the chat there's an analytics dashboard (a local web page) for seeing where the money goes. It ranks transactions by how necessary each one was given its size, rates recent spend green / amber / red by risk, surfaces recurring payments, and highlights categories that could be cut for savings.

**How it's built**

- **Nim agent (Go)** - `main.go` registers the assistant's tools on the Nim Go SDK and runs the agent server. It keeps the demo state (wallet + savings balances, today's date, and a "lead window" for upcoming bills) in memory, orchestrates the tools, and serves the read-only dashboard on a separate port.
- **Analysis (Python)** - the number-crunching lives in `python/`: loading and normalising transactions, summarising income vs spend, ranking expenses, tracking income, detecting recurring bills, finding upcoming bills inside the lead window, and the spend-guardrail decision itself. The Go server shells out to these and passes JSON back and forth.
- **Chat UI (React / TypeScript)** - `main.tsx` and `styles.css` are the front-end the user talks to.
- **Demo data** - `data/` holds a 6-month sample transaction history (including a savings account) so the whole thing runs without a real bank connection.

**The files**

main.go - the Nim agent: tool definitions, demo state, and the local spending dashboard.

python/ - the analysis scripts (load / summarise / rank / income / bills / upcoming bills / guardrail).

main.tsx, styles.css - the chat front-end.

data/ - the 6-month demo transaction history.

go.mod, go.sum - Go dependencies.

**Running it**

Needs Go and Python 3, and an `ANTHROPIC_API_KEY` in a `.env` file (the agent is Anthropic-powered). Start the Go server with `go run main.go` - the agent runs on :8080 and the dashboard on :8090 - then open the Nim chat front-end.

The demo flow in the chat:
- `ready for demo` - loads the sample transactions and sets up the balances (run this first).
- `summarise the loaded transactions` - income / spend / net overview.
- `run bills_tracker` - the recurring bills.
- `demo_send_money to @alice amount 300` - triggers the guardrail and the options above.

The spending dashboard is at `http://localhost:8090/dashboard`.

**Notes**

This is a hackathon build: it runs on a fixed demo dataset rather than a live bank feed, and the balances and payments are simulated in memory, so nothing touches real money. The bill detection and the "necessity" rating are heuristics tuned for the demo data rather than a general model.
