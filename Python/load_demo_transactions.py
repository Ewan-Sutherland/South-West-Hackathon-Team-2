import json, sys

def parse_float(x):
    try:
        return float(x)
    except Exception:
        return None

def main():
    if len(sys.argv) < 2:
        sys.stderr.write("usage: load_demo_transactions.py <path>\n")
        sys.exit(1)

    path = sys.argv[1]
    with open(path, "r", encoding="utf-8") as f:
        data = json.load(f)

    raw_txs = data.get("transactions", [])
    out = []
    parse_errors = 0

    for t in raw_txs:
        # Amounts come in as strings in demo data
        amt = parse_float(t.get("amount"))
        usd = parse_float(t.get("usdValue"))

        if amt is None:
            parse_errors += 1
            continue

        direction = (t.get("direction") or "").lower()
        if direction == "debit":
            signed_amount = -amt
        elif direction == "credit":
            signed_amount = amt
        else:
            # Unknown direction → skip
            parse_errors += 1
            continue

        merchant = (
            t.get("note")
            or t.get("merchant")
            or t.get("type")
            or "unknown"
        )

        out.append({
            "timestamp": t.get("createdAt", ""),
            "merchant": merchant,
            "amount": signed_amount,           # signed: +income, -expense
            "currency": t.get("currency", ""),
            "id": t.get("id", ""),
            "type": t.get("type", ""),
            "status": t.get("status", ""),
            "usdValue": usd if usd is not None else signed_amount,
        })

    sys.stdout.write(json.dumps({
        "tx_count": len(out),
        "parse_errors": parse_errors,
        "transactions": out,
    }))

if __name__ == "__main__":
    main()
