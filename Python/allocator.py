import json, sys

def main():
    payload = json.loads(sys.stdin.read() or "{}")

    risk = payload.get("risk_level", "medium")
    regime = payload.get("regime_hint", "")
    txs = payload.get("transactions", [])

    # placeholder logic for now
    allocation = {
        "directional": 0.30,
        "yield": 0.25,
        "stable": 0.35,
        "cash": 0.10
    }

    out = {
        "inputs_used": {
            "risk_level": risk,
            "regime_hint": regime,
            "tx_count": len(txs),
        },
        "allocation": allocation,
        "rationale": [
            "Placeholder allocator (replace with real CIO logic in Python)."
        ]
    }

    sys.stdout.write(json.dumps(out))

if __name__ == "__main__":
    main()
