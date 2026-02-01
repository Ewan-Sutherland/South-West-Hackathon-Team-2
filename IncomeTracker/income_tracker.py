import os, json
from collections import defaultdict
from anthropic import Anthropic

# Everything will be classified into these two labels
LABELS = ["salary", "additional_income"]

def month_key(ts):
    """Changes timestamp from format like "2023-01-31T08:15:00Z" to "2023-01" to group by month"""
    # Expects ISO like "2023-01-31T08:15:00Z"
    return (ts or "")[:7]  # "YYYY-MM"


def inflows(txs):
    """
    Filters the data so it only contains inflows with positive amounts and cleans it to remove unneecssary columns. Output columns are 
    ["id", "createdAt", "month", "amount", "counterparty", "note"] 
    """

    # Keep only positive inflows; uses usdValue if present else amount
    out = []
    for t in txs:
        direction = (t.get("direction","") or "").lower()
        ttype = (t.get("type","") or "").lower()
        inbound = (direction == "in") or (ttype == "receive")
        amt_raw = t.get("usdValue", t.get("amount", 0))
        try: amt = float(amt_raw)
        except: amt = 0.0
        if inbound and amt > 0:
            tx_id = str(t.get("id") or t.get("txHash") or f"{t.get('createdAt','')}|{amt}|{t.get('note','')}")
            out.append({
                "id": tx_id,
                "createdAt": t.get("createdAt",""),
                "month": month_key(t.get("createdAt","")),
                "amount": amt,
                "counterparty": t.get("counterparty"),
                "note": (t.get("note") or "").strip(),
            })
    return sorted(out, key=lambda x: x["createdAt"])


def anthropic_classify(inflows_):
    """Sends cleaned inflows to Anthropic to classify into salary vs additional_income"""

    client = Anthropic(api_key=os.environ["ANTHROPIC_API_KEY"])
    prompt = f"""
Classify EACH inflow transaction as EXACTLY ONE of: {LABELS}.

Definitions:
- "salary": income that is very likely a salary/wages/payroll payment from an employer.
  Evidence can include BOTH (a) labels/keywords and (b) periodic patterns.
- "additional_income": everything else.

How to decide "salary" (use judgement, but follow this guidance):
1) Keywords: If note/counterparty strongly indicates salary/wages/payroll/employer, you may classify as "salary"
   EVEN if periodicity is not perfectly visible (e.g., missing months in the data). Still sanity-check amount/source.
2) Periodicity: If keywords are weak/absent, classify as "salary" ONLY when there is a clear repeating pattern:
   - same or similar counterparty and/or similar note
   - amounts are consistent within reasonable variation (e.g., tax/bonus/partial month)
   - timing is roughly regular (monthly/biweekly/weekly). Allow natural drift (paydays move for weekends/holidays)
   - if uncertain, prefer "additional_income"

Output STRICT JSON ONLY:
{{"classifications": {{"<id>": "salary"|"additional_income", ...}}}}
Rules:
- Include every id exactly once.
- Only the two allowed labels.
- No extra keys, no commentary.

INFLOWS:
{json.dumps(inflows_, ensure_ascii=False)}
""".strip()

    msg = client.messages.create(
        model="claude-3-5-sonnet-latest",
        max_tokens=2000,
        temperature=0,
        messages=[{"role": "user", "content": prompt}],
    )
    return json.loads(msg.content[0].text)["classifications"]

def main(in_path="transactions.json", out_path="income_summary.json"):
    """
    Loads transaction data from a JSON file and runs functions:
    inflows -> filter data to inflows only
    anthropic_classify -> classify inflows as salary or additional income
    then aggregates classified inflows into monthly summaries and grand totals
    and writes the results to an output JSON file.
    """

    txs = json.load(open(in_path, "r", encoding="utf-8"))
    if isinstance(txs, dict) and "transactions" in txs: txs = txs["transactions"]

    ins = inflows(txs)
    print("SENT TO AI:",
          json.dumps(ins[:20],indent=2))
    classes = anthropic_classify(ins) if ins else {}

    monthly = defaultdict(lambda: {"salary": 0.0, "additional_income": 0.0, "total_inflows": 0.0})
    grand = {"salary": 0.0, "additional_income": 0.0, "total_inflows": 0.0}

    tx_map = {}
    for t in ins:
        lab = classes.get(t["id"], "additional_income")
        amt = float(t["amount"])
        m = t["month"]
        tx_map[t["id"]] = {"label": lab, "amount": amt, "month": m, "createdAt": t["createdAt"], "note": t["note"]}
        monthly[m][lab] += amt
        monthly[m]["total_inflows"] += amt
        grand[lab] += amt
        grand["total_inflows"] += amt

    out = {
        "months": [{"month": m, **monthly[m]} for m in sorted(monthly)],
        "grand_totals": grand,
        "transaction_classifications": tx_map,
    }

    with open(out_path, "w", encoding="utf-8") as f:
        json.dump(out, f, indent=2)

    print(f"Wrote {out_path}")

if __name__ == "__main__":
    main()