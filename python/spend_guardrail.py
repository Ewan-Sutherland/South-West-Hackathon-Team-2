import json, sys
from datetime import date, datetime

def main():
    payload = json.loads(sys.stdin.read() or "{}")

    spend_amount = float(payload.get("spend_amount", 0))
    wallet_balance = float(payload.get("wallet_balance", 0))
    savings_balance = float(payload.get("savings_balance", 0))
    lead_days = int(payload.get("lead_days", 3))
    today_str = payload.get("today")

    today = datetime.strptime(today_str, "%Y-%m-%d").date() if today_str else date.today()

    upcoming_bills = payload.get("upcoming_bills", [])
    total_upcoming = float(payload.get("total_upcoming_bills", 0))

    post_spend = round(wallet_balance - spend_amount, 2)

    decision = "allow"
    shortfall = 0.0
    recommended_withdrawal = 0.0

    if post_spend < total_upcoming:
        shortfall = round(total_upcoming - post_spend, 2)
        if savings_balance >= shortfall:
            decision = "offer_savings_topup"
            recommended_withdrawal = shortfall
        else:
            decision = "block"

    # Build explicit options for the UI (or for the agent to follow exactly)
    options = []

    # Always allow "deny"
    options.append({
        "id": "deny",
        "label": "Cancel",
        "type": "deny",
        "summary": "Cancel this payment. No funds moved."
    })

    if decision == "allow":
        options.append({
            "id": "proceed",
            "label": "Proceed",
            "type": "proceed",
            "summary": "Proceed with the payment. Upcoming bills remain covered."
        })
    else:
        # Offer savings top-up if possible
        if savings_balance >= shortfall and shortfall > 0:
            options.append({
                "id": "withdraw_then_send",
                "label": f"Withdraw £{recommended_withdrawal:.2f} from savings, then send",
                "type": "withdraw_then_send",
                "withdraw_amount": recommended_withdrawal,
                "summary": "Recommended: top up wallet from savings so upcoming bills remain covered."
            })

        # Always allow override (requires confirmation in Go)
        options.append({
            "id": "override_send",
            "label": "Override and send anyway",
            "type": "override_send",
            "summary": "Proceed even if this risks upcoming bills (requires confirmation)."
        })

    explanation = {
        "wallet_balance": round(wallet_balance, 2),
        "post_spend_wallet_balance": post_spend,
        "upcoming_bills_total": round(total_upcoming, 2),
        "shortfall": shortfall,
        "savings_balance": round(savings_balance, 2),
    }

    if decision == "allow":
        msg = "Payment is safe: upcoming bills remain covered."
    elif decision == "offer_savings_topup":
        msg = "Payment would risk upcoming bills. Savings can cover the shortfall."
    else:
        msg = "Payment would risk upcoming bills and savings cannot cover the shortfall."

    out = {
        "decision": decision,
        "today": today.isoformat(),
        "lead_days": lead_days,
        "upcoming_bills": upcoming_bills,
        "recommended_savings_withdrawal": recommended_withdrawal,
        "explanation": explanation,
        "message": msg,
        "options": options,
    }

    sys.stdout.write(json.dumps(out))

if __name__ == "__main__":
    main()
