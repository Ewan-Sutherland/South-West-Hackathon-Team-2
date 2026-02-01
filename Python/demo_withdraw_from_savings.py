import json, sys
from datetime import datetime, timezone

def main():
    payload = json.loads(sys.stdin.read() or "{}")

    amount = payload.get("amount", 0)
    try:
        amount = float(amount)
    except Exception:
        sys.stdout.write(json.dumps({"error": "amount must be a number"}))
        return

    if amount <= 0:
        sys.stdout.write(json.dumps({"error": "amount must be > 0"}))
        return

    # Python can't persist balances; Go will validate and apply deltas.
    now = datetime.now(timezone.utc).isoformat().replace("+00:00", "Z")

    out = {
        "wallet_delta": round(amount, 2),
        "savings_delta": round(-amount, 2),
        "transaction": {
            "timestamp": now,
            "merchant": "Savings withdrawal (demo)",
            "amount": round(amount, 2),  # positive inflow to wallet
            "currency": "GBP",
            "type": "withdraw_savings_demo",
            "status": "completed",
            "usdValue": round(amount, 2),
        }
    }

    sys.stdout.write(json.dumps(out))

if __name__ == "__main__":
    main()
