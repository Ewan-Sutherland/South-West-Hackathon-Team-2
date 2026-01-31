

import os, json
from collections import defaultdict
from anthropic import Anthropic

bills = ["rent", "tax", "insurance", "utilities", "loan",
          "mortgage", "healthcare", "childcare"]

def chunk_months(txs): #txs = all transactions
    months = []     # Final list of months
    cur = []        # Transactions in month

    for t in txs:   # t = single transaction
        if t.get("amount") == 1:   
            if cur:
                months.append(cur) # save previous month
            cur = []                
        else:
            cur.append(t)           # add transaction to current month

    if cur:
        months.append(cur)           # add final month

    return months

def anthropic_group(notes):
    client = Anthropic(api_key=os.environ["ANTHROPIC_API_KEY"])
    prompt = f"""
You label bill notes into ONE section from: {bills} or 'non-essential'.
Rules: essentials only; if unsure -> non-essential. Output STRICT JSON object mapping note->label.
Notes: {notes}
"""

    msg = client.messages.create(
        model="claude-3-5-sonnet-latest",
        max_tokens=1000,   # limit requested
        temperature=0,
        messages=[{"role":"user","content":prompt}]
    )
    return json.loads(msg.content[0].text)
def main(in_path="transactions.json", out_path="bills_summary.json"):
    txs = json.load(open(in_path, "r", encoding="utf-8"))
    months = chunk_months(txs)
    out = {
        "months": [],            # monthly summaries
        "grand_total_bills": 0.0 # total across all months
    }
    for i, m in enumerate(months, 1):
        notes = sorted({
            t.get("note","").strip()
            for t in m
            if t.get("note")
        })
        labels = anthropic_group(notes) if notes else {}

        # section_sum = accumulates totals per category
        section_sum = defaultdict(float)

        total = 0.0

        for t in m:
            note = (t.get("note","") or "").strip()
            amt = float(t.get("amount", 0))  # transaction amount
            label = labels.get(note, "non-essential")

            # Only keep essential categories
            if label in bills:
                section_sum[label] += amt
                total += amt
        
        out["months"].append({
            "month_index": i,
            "sections": dict(sorted(section_sum.items())),
            "total_bills": total
            })
        out["grand_total_bills"] += total

    with open(out_path, "w", encoding="utf-8") as f:
        json.dump(out, f, indent=2)

    print(f"Wrote {out_path}")


if __name__ == "__main__":
    main()

