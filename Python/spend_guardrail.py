import json, sys
from datetime import date, datetime

def main():
    payload = json.loads(sys.stdin.read() or "{}")

    txs = payload.get("transactions", [])
    spend_amount = float(payload.get("spend_amount", 0))
    wallet_balance = float(payload.get("wallet_balance", 0))
    savings_balance = float(payload.get("savings_balance", 0))
    lead_days = int(payload.get("lead_days", 3))
    today_str = payload.get("today")

    today = (
        datetime.strptime(today_str, "%Y-%m-%d").date()
        if today_str else date.today()
    )

    # ---------- upcoming bills (reuse your logic assumptions) ----------
    upcoming_bills = payload.get("upcoming_bills", [])
    total_upcoming = float(payload.get("total_upcoming_bills", 0))

    post_spend_balance = wallet_balance - spend_amount

    decision = "allow"
    explanation = "Spending this amount does not affect upcoming bills."
    recommended_withdrawal = 0.0

    if post_spend_balance < total_upcoming:
        shortfall = round(total_upcoming - post_spend_balance, 2)

        if savings_balance >= shortfall:
            decision = "offer_savings_topup"
            recommended_withdrawal = shortfall
            explanation = (
                f"Spending would leave insufficient cash for upcoming bills. "
                f"Savings can cover the £{shortfall:.2f} shortfall."
            )
        else:
            decision = "block"
            explanation = (
                "Spending would jeopardize upcoming bills and savings "
                "cannot cover the shortfall."
            )

    out = {
        "decision": decision,
        "today": today.isoformat(),
        "lead_days": lead_days,

        "inputs": {
            "spend_amount": spend_amount,
            "wallet_balance": wallet_balance,
            "savings_balance": savings_balance,
        },

        "upcoming_bills_total": round(total_upcoming, 2),
        "post_spend_wallet_balance": round(post_spend_balance, 2),
        "recommended_savings_withdrawal": recommended_withdrawal,

        "explanation": explanation,

        "instructions": {
            "allow": "Proceed with the spend.",
            "offer_savings_topup": "Ask user to approve withdrawing the recommended amount from savings before proceeding.",
            "block": "Do not proceed. Ask user to reduce amount or add funds.",
        },
    }

    sys.stdout.write(json.dumps(out))

if __name__ == "__main__":
    main()
