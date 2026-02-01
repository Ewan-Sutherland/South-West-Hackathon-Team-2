from __future__ import annotations

import argparse, json, os, re
from datetime import date, datetime, timedelta
from typing import Any, Dict, List, Tuple
from dateutil.relativedelta import relativedelta
import anthropic

MODEL = os.getenv("ANTHROPIC_MODEL", "claude-3-5-sonnet-latest")

def parse_date(s: str) -> date:
    return datetime.strptime(s, "%Y-%m-%d").date()

def safe_day_in_month(y: int, m: int, dom: int) -> date:
    first = date(y, m, 1)
    last = first + relativedelta(months=+1) - timedelta(days=1)
    dom = max(1, min(dom, last.day))
    return date(y, m, dom)

def next_due(today: date, typical_day: int) -> date:
    """
    Compute next due date based on monthly recurrence.
    If this month's date has passed, move to next month.
    """
    cand = safe_day_in_month(today.year, today.month, typical_day)
    if cand >= today:
        return cand

    nxt = today + relativedelta(months=+1)
    return safe_day_in_month(nxt.year, nxt.month, typical_day)

def fmt_money(x: float) -> str:
    return f"${x:.2f}"

def load_txns(path: str) -> List[Dict[str, Any]]:
    data = json.loads(open(path, "r", encoding="utf-8").read())

    out: List[Dict[str, Any]] = []
    for t in data:
        out.append({
            "date": parse_date(t["date"]),
            "description": re.sub(r"\s+", " ", str(t.get("description", "")).strip()),
            "amount": float(t.get("amount", 0.0))
        })

    out.sort(key=lambda x: x["date"])
    return out

def extract_monthly_recurring(txns: List[Dict[str, Any]]) -> List[Dict[str, Any]]:
    # Only payments (negative values)
    payments = [t for t in txns if t["amount"] < 0]

    if len(payments) < 2:
        return []

    # Compact ledger format for the LLM
    lines = []
    for i, t in enumerate(payments):
        lines.append(
            f"{i} | {t['date'].isoformat()} | {t['amount']:.4f} | {t['description']}"
        )
    ledger = "\n".join(lines)

    system = "Extract recurring MONTHLY payments. Return JSON only."

    user = f"""
Find recurring monthly payments.

Rules:
- Must appear >= 2 times.
- Merge merchant name variants.
- Output amount as POSITIVE.
- typical_day = most common day-of-month.
- reason = short label.

Return EXACT JSON:
{{
  "recurring":[
    {{
      "name":"",
      "reason":"",
      "amount":0.0,
      "typical_day":1,
      "tolerance":2,
      "confidence":0.0,
      "evidence_indices":[0,1]
    }}
  ]
}}

Transactions:
{ledger}
""".strip()

    client = anthropic.Anthropic(api_key=os.environ["ANTHROPIC_API_KEY"])

    msg = client.messages.create(
        model=MODEL,
        max_tokens=800,
        temperature=0,
        system=system,
        messages=[{"role": "user", "content": user}],
    )

    # Extract text blocks from Claude response
    text = "".join(
        b.text for b in msg.content if getattr(b, "type", None) == "text"
    ).strip()

    # Remove accidental markdown fences
    text = re.sub(r"^```json\s*", "", text, flags=re.IGNORECASE)
    text = re.sub(r"^```\s*", "", text)
    text = re.sub(r"\s*```$", "", text)

    data = json.loads(text)
    return data.get("recurring", [])

def build_notification(
    recurring: List[Dict[str, Any]],
    balance: float,
    lead_days: int,
    today: date,
) -> Dict[str, Any]:

    if not recurring:
        return {"should_notify": False}

    # Compute next due date for each recurring payment
    due_list: List[Tuple[Dict[str, Any], date]] = []
    for r in recurring:
        due = next_due(today, int(r["typical_day"]))
        due_list.append((r, due))

    # Pick soonest due payment
    due_list.sort(key=lambda x: x[1])
    r, due = due_list[0]

    days_until = (due - today).days

    # Only notify within lead window
    if days_until > lead_days:
        return {"should_notify": False}

    amount = float(r["amount"])
    left = balance - amount

    message = (
        f"Hey, {r['name']} is due soon for "
        f"{fmt_money(amount)}. You have {fmt_money(left)} left."
    )

    return {
        "should_notify": True,
        "notify_on": today.isoformat(),
        "due_date": due.isoformat(),
        "title": "Payment due soon",
        "message": message,
        "merchant": r["name"],
        "reason": r.get("reason", ""),
        "amount": amount,
        "balance_left": left,
    }

def main() -> int:
    """
    Script entry:
    1. Load transactions
    2. Detect recurring payments
    3. Build notification
    4. Write JSON output
    """

    ap = argparse.ArgumentParser()
    ap.add_argument("json_path")
    ap.add_argument("--balance", type=float, required=True)
    ap.add_argument("--lead-days", type=int, default=3)
    ap.add_argument("--out", default="notification.json")
    ap.add_argument("--min-confidence", type=float, default=0.55)
    args = ap.parse_args()

    if "ANTHROPIC_API_KEY" not in os.environ:
        raise SystemExit("Set ANTHROPIC_API_KEY.")

    txns = load_txns(args.json_path)

    recurring = extract_monthly_recurring(txns)
    recurring = [
        r for r in recurring
        if float(r.get("confidence", 0.0)) >= args.min_confidence
    ]

    payload = build_notification(
        recurring=recurring,
        balance=args.balance,
        lead_days=args.lead_days,
        today=date.today(),
    )

    # Write message for app consumption
    with open(args.out, "w", encoding="utf-8") as f:
        json.dump(payload, f, indent=2)

    # Also print for logs
    print(json.dumps(payload))

    return 0


if __name__ == "__main__":
    raise SystemExit(main())
