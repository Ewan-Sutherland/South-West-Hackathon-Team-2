import json, sys
from collections import defaultdict, Counter
from datetime import datetime, date, timedelta

MIN_OCCURRENCES = 3
DAY_TOLERANCE = 5
CONF_THRESHOLD = 0.60

def parse_dt(ts: str):
    try:
        return datetime.fromisoformat(ts.replace("Z", "+00:00"))
    except Exception:
        return None

def month_key(dt: datetime) -> str:
    return f"{dt.year}-{dt.month:02d}"

def safe_day_in_month(y: int, m: int, dom: int) -> date:
    nxt = date(y + 1, 1, 1) if m == 12 else date(y, m + 1, 1)
    last = nxt - timedelta(days=1)
    dom = max(1, min(dom, last.day))
    return date(y, m, dom)

def next_due(today: date, typical_day: int) -> date:
    cand = safe_day_in_month(today.year, today.month, typical_day)
    if cand >= today:
        return cand
    y, m = today.year, today.month
    if m == 12:
        return safe_day_in_month(y + 1, 1, typical_day)
    return safe_day_in_month(y, m + 1, typical_day)

def clamp01(x):
    return 0.0 if x < 0 else (1.0 if x > 1 else x)

def amount_cv(xs):
    if len(xs) < 2:
        return 0.0
    mu = sum(xs) / len(xs)
    if mu <= 0:
        return 999.0
    var = sum((x - mu) ** 2 for x in xs) / (len(xs) - 1)
    return (var ** 0.5) / mu

def confidence_score(amounts, dates, months):
    n = len(amounts)
    distinct_months = len(set(months))

    occ_score = clamp01((n - 2) / 4)
    cv_score = clamp01(1 - (amount_cv(amounts) / 0.20))
    days = [d.day for d in dates]
    spread = (max(days) - min(days)) if days else 999
    day_score = clamp01(1 - (spread / DAY_TOLERANCE))
    month_score = clamp01(distinct_months / 6)

    return clamp01(0.35*occ_score + 0.30*cv_score + 0.25*day_score + 0.10*month_score)

def typical_day_of_month(dates):
    c = Counter([d.day for d in dates])
    return c.most_common(1)[0][0]

def main():
    payload = json.loads(sys.stdin.read() or "{}")
    txs = payload.get("transactions", [])
    lead_days = int(payload.get("lead_days", 3))
    today_str = payload.get("today")

    today = datetime.strptime(today_str, "%Y-%m-%d").date() if today_str else date.today()

    # Expenses only from normalized txs: amount < 0
    items = []
    for t in txs:
        try:
            amt = float(t.get("amount", 0.0))
        except Exception:
            continue
        if amt >= 0:
            continue
        dt = parse_dt(t.get("timestamp", ""))
        if not dt:
            continue
        merch = (t.get("merchant") or "unknown").lower().strip()
        items.append({
            "dt": dt,
            "month": month_key(dt),
            "merchant": merch,
            "spend": -amt,  # positive
        })

    # Cluster by merchant and detect recurrence
    clusters = defaultdict(list)
    for it in items:
        clusters[it["merchant"]].append(it)

    recurring = []
    for merch, its in clusters.items():
        if len(its) < MIN_OCCURRENCES:
            continue
        amounts = [x["spend"] for x in its]
        dates = [x["dt"] for x in its]
        months = [x["month"] for x in its]
        conf = confidence_score(amounts, dates, months)
        if conf < CONF_THRESHOLD:
            continue

        td = typical_day_of_month(dates)
        due = next_due(today, td)
        recurring.append({
            "merchant": merch,
            "confidence": round(conf, 3),
            "typical_day": td,
            "mean_amount": round(sum(amounts) / len(amounts), 2),
            "due_date": due.isoformat(),
            "days_until": (due - today).days,
        })

    upcoming = [r for r in recurring if 0 <= r["days_until"] <= lead_days]
    upcoming.sort(key=lambda r: r["days_until"])

    total_upcoming = round(sum(r["mean_amount"] for r in upcoming), 2)

    out = {
        "scope": "upcoming_bills_warning",
        "today": today.isoformat(),
        "lead_days": lead_days,
        "upcoming_bills": upcoming,
        "total_upcoming_bills": total_upcoming,
        "message": (
            f"Upcoming recurring bills in next {lead_days} days: {len(upcoming)}. Total ~{total_upcoming}."
        ) if upcoming else f"No recurring bills due in the next {lead_days} days.",
        "notes": "Recurring bills inferred from negative transactions by merchant recurrence, amount stability, and day-of-month stability.",
    }

    sys.stdout.write(json.dumps(out))

if __name__ == "__main__":
    main()
