import json, sys
from collections import defaultdict
from statistics import mean, stdev
from datetime import datetime

MIN_OCCURRENCES = 3
MAX_AMOUNT_CV = 0.20
MONTH_TOLERANCE_DAYS = 4

def parse_date(ts):
    try:
        return datetime.fromisoformat(ts.replace("Z", "+00:00"))
    except Exception:
        return None

def parse_amount(t):
    # normalized store: amount is already signed float
    try:
        return float(t.get("amount", 0))
    except Exception:
        return 0.0

def is_income_only(t):
    # HARD GUARD: only positive amounts are income
    return parse_amount(t) > 0

def month_key(dt):
    return f"{dt.year}-{dt.month:02d}"

def cluster_key(t):
    # normalized store uses Merchant field; keep it simple
    return (t.get("merchant") or "unknown").lower().strip()

def amount_cv(amounts):
    if len(amounts) < 2:
        return 0.0
    mu = mean(amounts)
    if mu <= 0:
        return 999.0
    return stdev(amounts) / mu

def day_spread(dates):
    days = [d.day for d in dates]
    return max(days) - min(days) if days else 999

def clamp01(x):
    return 0.0 if x < 0 else (1.0 if x > 1 else x)

def salary_confidence(amounts, dates, months):
    n = len(amounts)
    distinct_months = len(set(months))

    occ_score = clamp01((n - 2) / 4)
    cv_score  = clamp01(1 - (amount_cv(amounts) / MAX_AMOUNT_CV))
    day_score = clamp01(1 - (day_spread(dates) / MONTH_TOLERANCE_DAYS))
    month_score = clamp01(distinct_months / 6)

    return clamp01(
        0.35 * occ_score +
        0.30 * cv_score +
        0.25 * day_score +
        0.10 * month_score
    )

def main():
    data = json.loads(sys.stdin.read() or "{}")
    txs = data["transactions"] if isinstance(data, dict) and "transactions" in data else data

    inflows = []
    for t in txs:
        if not is_income_only(t):
            continue

        amt = parse_amount(t)
        ts = t.get("timestamp") or t.get("createdAt") or ""
        dt = parse_date(ts)
        if not dt:
            continue

        inflows.append({
            "amount": amt,
            "date": dt,
            "month": month_key(dt),
            "source": cluster_key(t),
        })

    clusters = defaultdict(list)
    for it in inflows:
        clusters[it["source"]].append(it)

    salary_streams = []
    monthly_total_income = defaultdict(float)

    for source, items in clusters.items():
        amounts = [x["amount"] for x in items]
        dates   = [x["date"] for x in items]
        months  = [x["month"] for x in items]

        conf = salary_confidence(amounts, dates, months)

        if len(amounts) >= MIN_OCCURRENCES and conf >= 0.60:
            salary_streams.append({
                "source": source,
                "confidence": round(conf, 3),
                "mean_amount": round(mean(amounts), 2),
                "occurrences": len(items),
                "months": sorted(set(months)),
            })

    for x in inflows:
        monthly_total_income[x["month"]] += x["amount"]

    salary_streams.sort(key=lambda s: s["confidence"], reverse=True)

    out = {
        "scope": "income_only",
        "salary_streams": salary_streams,
        "monthly_total_income": dict(sorted(monthly_total_income.items())),
        "notes": "Income-only: only positive cashflows are analysed. Expenses (rent/bills/subscriptions) are excluded by design.",
    }

    sys.stdout.write(json.dumps(out))

if __name__ == "__main__":
    main()
