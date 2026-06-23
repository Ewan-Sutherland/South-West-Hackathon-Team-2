import json, sys
from collections import defaultdict
from statistics import mean, stdev
from datetime import datetime

MIN_OCCURRENCES = 3
MAX_AMOUNT_CV = 0.20
MONTH_TOLERANCE_DAYS = 5

RULES = {
    "rent": ["rent", "landlord", "letting", "estate"],
    "council_tax": ["council tax"],
    "utilities": ["energy", "electric", "electricity", "gas", "water", "utility", "utilities", "edf", "eon", "octopus", "british gas"],
    "insurance": ["insurance", "insure", "aviva", "direct line", "admiral", "axa"],
    "loan_mortgage": ["mortgage", "loan", "lending", "finance"],
    "phone_internet": ["vodafone", "o2", "ee", "three", "giffgaff", "sky", "bt", "virgin", "broadband", "internet", "phone"],
    "transport": ["tfl", "train", "rail", "national express", "uber", "bus", "tram"],
    "subscriptions": ["netflix", "spotify", "prime", "amazon prime", "disney", "apple", "icloud", "google", "microsoft", "gym", "membership"],
}

def parse_date(ts):
    try:
        return datetime.fromisoformat(ts.replace("Z", "+00:00"))
    except Exception:
        return None

def month_key(ts):
    return (ts or "")[:7]

def is_expense(t):
    try:
        return float(t.get("amount", 0)) < 0
    except Exception:
        return False

def merchant_text(t):
    return (t.get("merchant") or "").lower().strip()

def categorize(merchant: str) -> str:
    m = merchant.lower()
    for cat, kws in RULES.items():
        for kw in kws:
            if kw in m:
                return cat
    return "non_bill_spend"  # IMPORTANT: don't call unknowns "bills"

def clamp01(x):
    return 0.0 if x < 0 else (1.0 if x > 1 else x)

def amount_cv(xs):
    if len(xs) < 2:
        return 0.0
    mu = mean(xs)
    if mu <= 0:
        return 999.0
    return stdev(xs) / mu

def day_spread(dates):
    days = [d.day for d in dates]
    return max(days) - min(days) if days else 999

def recurring_confidence(amounts, dates, months):
    n = len(amounts)
    distinct_months = len(set(months))

    occ_score = clamp01((n - 2) / 4)
    cv_score = clamp01(1 - (amount_cv(amounts) / MAX_AMOUNT_CV))
    day_score = clamp01(1 - (day_spread(dates) / MONTH_TOLERANCE_DAYS))
    month_score = clamp01(distinct_months / 6)

    return clamp01(0.35*occ_score + 0.30*cv_score + 0.25*day_score + 0.10*month_score)

def main():
    data = json.loads(sys.stdin.read() or "{}")
    txs = data["transactions"] if isinstance(data, dict) and "transactions" in data else data

    expenses = []
    for t in txs:
        if not is_expense(t):
            continue
        amt = float(t.get("amount", 0.0))
        ts = t.get("timestamp") or ""
        dt = parse_date(ts)
        if not dt:
            continue

        merch = merchant_text(t) or "unknown"
        cat = categorize(merch)

        expenses.append({
            "month": month_key(ts),
            "date": dt,
            "merchant": merch,
            "category": cat,
            "amount_abs": round(-amt, 2),
        })

    # Cluster by category+merchant for recurrence detection
    streams = defaultdict(list)
    for e in expenses:
        key = f"{e['category']}|{e['merchant']}"
        streams[key].append(e)

    recurring_streams = []
    recurring_keys = set()

    for key, items in streams.items():
        cat, merch = key.split("|", 1)

        # Only consider categories that are actually "bill-like"
        if cat == "non_bill_spend":
            continue

        if len(items) < MIN_OCCURRENCES:
            continue

        amounts = [x["amount_abs"] for x in items]
        dates = [x["date"] for x in items]
        months = [x["month"] for x in items]
        conf = recurring_confidence(amounts, dates, months)

        if conf >= 0.60:
            recurring_keys.add(key)
            recurring_streams.append({
                "category": cat,
                "merchant": merch,
                "confidence": round(conf, 3),
                "mean_amount": round(mean(amounts), 2),
                "occurrences": len(items),
                "months": sorted(set(months)),
            })

    recurring_streams.sort(key=lambda x: x["confidence"], reverse=True)

    # Monthly totals from RECURRING BILL STREAMS ONLY
    monthly_recurring_bills = defaultdict(lambda: defaultdict(float))
    monthly_recurring_total = defaultdict(float)

    for e in expenses:
        key = f"{e['category']}|{e['merchant']}"
        if key not in recurring_keys:
            continue
        monthly_recurring_bills[e["month"]][e["category"]] += e["amount_abs"]
        monthly_recurring_total[e["month"]] += e["amount_abs"]

    out = {
        "scope": "expenses_only_recurring_bills",
        "recurring_bill_streams": recurring_streams,
        "monthly_recurring_bills_by_category": {
            m: {k: round(v, 2) for k, v in sorted(monthly_recurring_bills[m].items())}
            for m in sorted(monthly_recurring_bills)
        },
        "monthly_recurring_bills_total": {m: round(monthly_recurring_total[m], 2) for m in sorted(monthly_recurring_total)},
        "notes": "This tool reports recurring bills only (rent/utilities/subscriptions/etc.). Other discretionary spending is excluded from 'bills totals'.",
    }

    sys.stdout.write(json.dumps(out))

if __name__ == "__main__":
    main()
