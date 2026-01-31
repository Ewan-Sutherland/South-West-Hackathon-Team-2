import json, sys

def parse_float(s):
    try:
        return float(s)
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
        amt = parse_float(t.get("amount", "0"))
        usd = parse_float(t.get("usdValue", "0"))
        if amt is None or usd is None:
            parse_errors += 1
            continue

        direction = t.get("direction", "")
        signed = -amt if direction == "debit" else amt

        merchant = t.get("note") or t.get("type") or "unknown"

        out.append({
            "timestamp": t.get("createdAt", ""),
            "merchant": merchant,
            "amount": signed,
            "currency": t.get("currency", ""),
            "id": t.get("id", ""),
            "type": t.get("type", ""),
            "status": t.get("status", ""),
            "usdValue": usd,
        })

    sys.stdout.write(json.dumps({
        "tx_count": len(out),
        "parse_errors": parse_errors,
        "transactions": out,
    }))

if __name__ == "__main__":
    main()
