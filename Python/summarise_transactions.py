import json, sys

def main():
    payload = json.loads(sys.stdin.read() or "{}")
    txs = payload.get("transactions", [])
    if not txs:
        sys.stdout.write(json.dumps({"error": "No transactions provided"}))
        return

    income = 0.0
    spend = 0.0
    credits = 0
    debits = 0
    currency = txs[0].get("currency", "")

    for t in txs:
        amt = float(t.get("amount", 0.0))
        if amt >= 0:
            income += amt
            credits += 1
        else:
            spend += -amt
            debits += 1

    sys.stdout.write(json.dumps({
        "tx_count": len(txs),
        "currency": currency,
        "credits": credits,
        "debits": debits,
        "income": round(income, 2),
        "spend": round(spend, 2),
        "net": round(income - spend, 2),
    }))

if __name__ == "__main__":
    main()
