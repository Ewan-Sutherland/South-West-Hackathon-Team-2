#!/usr/bin/env python3
import json
import sys
from collections import defaultdict
from datetime import datetime
from typing import Any, Dict, List, Optional, Tuple

INTERNAL_TYPES = {"deposit_savings_demo", "withdraw_savings_demo"}

# ---------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------
def safe_float(x: Any, default: float = 0.0) -> float:
    try:
        return float(x)
    except Exception:
        return default


def parse_dt(ts: str) -> Optional[datetime]:
    if not ts:
        return None
    try:
        # Accept "YYYY-MM-DD" or ISO like "YYYY-MM-DDTHH:MM:SSZ"
        if "T" in ts:
            return datetime.fromisoformat(ts.replace("Z", "+00:00"))
        return datetime.fromisoformat(ts)
    except Exception:
        return None


def month_key(dt: datetime) -> str:
    return f"{dt.year:04d}-{dt.month:02d}"


def normalize_merchant(t: Dict[str, Any]) -> str:
    # Keep close to your normalized schema but tolerate variations
    m = t.get("merchant") or t.get("Merchant") or t.get("counterparty") or ""
    return str(m).strip() or "Unknown"


def normalize_type(t: Dict[str, Any]) -> str:
    return str(t.get("type") or t.get("raw_type") or t.get("RawType") or "").strip()


def normalize_currency(t: Dict[str, Any]) -> str:
    return str(t.get("currency") or t.get("Currency") or "").strip() or "GBP"


def normalize_timestamp(t: Dict[str, Any]) -> str:
    return str(t.get("timestamp") or t.get("date") or t.get("Date") or "").strip()


def categorize(merchant: str, raw_type: str) -> str:
    m = (merchant or "").lower()

    if raw_type in INTERNAL_TYPES:
        return "internal_transfer"

    # Deterministic rules (demo-friendly, no ML)
    if "rent" in m or "landlord" in m:
        return "housing_rent"
    if any(k in m for k in ["council", "electric", "gas", "water", "utility", "internet", "broadband", "phone"]):
        return "utilities"
    if any(k in m for k in ["netflix", "spotify", "prime", "disney", "subscription", "icloud", "apple"]):
        return "subscriptions"
    if any(k in m for k in ["tesco", "sainsbury", "asda", "aldi", "lidl", "waitrose", "m&s", "marks", "co-op", "coop", "grocery", "supermarket"]):
        return "groceries"
    if any(k in m for k in ["uber", "bolt", "train", "rail", "tfl", "bus", "fuel", "petrol", "shell", "bp", "esso", "parking"]):
        return "transport"
    if any(k in m for k in ["deliveroo", "ubereats", "just eat", "restaurant", "cafe", "coffee", "bar", "pub"]):
        return "eating_out"
    if any(k in m for k in ["amazon", "shop", "store", "retail"]):
        return "shopping"
    if any(k in m for k in ["gym", "cinema", "entertainment", "ticket", "concert"]):
        return "leisure"

    return "other"


def is_questionable(category: str, merchant: str) -> bool:
    # Deterministic "questionable" heuristic (you can tweak labels later)
    if category in {"gambling", "adult", "crypto", "other"}:
        return True
    m = (merchant or "").lower()
    if any(k in m for k in ["bet", "casino", "poker", "bookmaker", "gamble"]):
        return True
    return False


# ---------------------------------------------------------------------
# Core
# ---------------------------------------------------------------------
def main() -> None:
    payload = json.loads(sys.stdin.read() or "{}")
    txs: List[Dict[str, Any]] = payload.get("transactions") or []
    top_n = int(payload.get("top_n") or 8)

    if not txs:
        sys.stdout.write(json.dumps({"error": "No transactions provided"}))
        return

    currency = normalize_currency(txs[0])

    # Totals (all txs)
    income_total = 0.0
    spend_total = 0.0
    credits = 0
    debits = 0

    # Rankings (exclude internal transfers)
    spend_by_merchant = defaultdict(lambda: {"total": 0.0, "count": 0})
    spend_by_category = defaultdict(lambda: {"total": 0.0, "count": 0})

    # Recurring detection: merchant appears in N distinct months (debit only, non-internal)
    merchant_months = defaultdict(set)

    # Questionable
    questionable = defaultdict(lambda: {"total": 0.0, "count": 0})

    # Per-tx ratings (optional, capped)
    rated_txs = []

    excluded_internal = {"count": 0, "total": 0.0}

    for t in txs:
        amt = safe_float(t.get("amount"), 0.0)
        merchant = normalize_merchant(t)
        raw_type = normalize_type(t)
        ts = normalize_timestamp(t)

        # Credits
        if amt >= 0:
            credits += 1
            income_total += amt
            continue

        # Debits
        debits += 1
        spend_total += -amt

        # Exclude internal transfers from "expense ranking"
        if raw_type in INTERNAL_TYPES:
            excluded_internal["count"] += 1
            excluded_internal["total"] += -amt
            continue

        cat = categorize(merchant, raw_type)

        spend_by_merchant[merchant]["total"] += -amt
        spend_by_merchant[merchant]["count"] += 1

        spend_by_category[cat]["total"] += -amt
        spend_by_category[cat]["count"] += 1

        dt = parse_dt(ts)
        if dt:
            merchant_months[merchant].add(month_key(dt))

        # Flag questionable spend
        if is_questionable(cat, merchant):
            questionable[merchant]["total"] += -amt
            questionable[merchant]["count"] += 1

        # Lightweight deterministic "rating" for explainability (optional)
        rating = "green"
        if cat in {"subscriptions", "eating_out", "shopping", "leisure", "other"} and (-amt) >= 40:
            rating = "amber"
        if is_questionable(cat, merchant) and (-amt) >= 20:
            rating = "red"

        rated_txs.append(
            {
                "timestamp": ts,
                "merchant": merchant,
                "category": cat,
                "amount": round(-amt, 2),
                "currency": currency,
                "rating": rating,
            }
        )

    # Build rankings
    def rank_map_as_list(m: Dict[str, Dict[str, Any]], key_name: str) -> List[Dict[str, Any]]:
        rows = []
        for k, v in m.items():
            total = float(v["total"])
            count = int(v["count"])
            rows.append(
                {
                    key_name: k,
                    "total": round(total, 2),
                    "count": count,
                    "avg": round(total / max(count, 1), 2),
                }
            )
        rows.sort(key=lambda r: (-r["total"], -r["count"], str(r[key_name]).lower()))
        return rows

    top_merchants = rank_map_as_list(spend_by_merchant, "merchant")[: max(top_n, 1)]
    top_categories = rank_map_as_list(spend_by_category, "category")[: max(top_n, 1)]

    # Recurring: appears in >= 3 distinct months (debit only, non-internal)
    recurring = []
    for merch, months in merchant_months.items():
        if len(months) >= 3 and merch in spend_by_merchant:
            recurring.append(
                {
                    "merchant": merch,
                    "months_seen": len(months),
                    "total": round(spend_by_merchant[merch]["total"], 2),
                    "count": spend_by_merchant[merch]["count"],
                }
            )
    recurring.sort(key=lambda x: (-x["months_seen"], -x["total"], x["merchant"].lower()))
    recurring = recurring[: max(top_n, 1)]

    # Questionable list
    questionable_list = rank_map_as_list(questionable, "merchant")[: max(top_n, 1)]

    # "Cuts": simple deterministic suggestions from non-essential categories
    non_essential_categories = {"subscriptions", "eating_out", "shopping", "leisure", "other"}
    cuts = []
    for cat in non_essential_categories:
        if cat in spend_by_category and spend_by_category[cat]["total"] > 0:
            cuts.append(
                {
                    "category": cat,
                    "monthly_saving_hint": round(spend_by_category[cat]["total"] / 6.0, 2),  # 6mo dataset
                    "total_6mo": round(spend_by_category[cat]["total"], 2),
                }
            )
    cuts.sort(key=lambda x: (-x["monthly_saving_hint"], -x["total_6mo"], x["category"]))
    cuts = cuts[: max(top_n, 1)]

    # Cap rated txs for readability
    rated_txs.sort(key=lambda x: (x["timestamp"], x["merchant"].lower()))
    rated_txs = rated_txs[:200]

    out = {
        "summary": {
            "currency": currency,
            "transaction_count": len(txs),
            "credits": credits,
            "debits": debits,
            "income_total": round(income_total, 2),
            "spend_total": round(spend_total, 2),
            "net": round(income_total - spend_total, 2),
        },
        "exclusions": {
            "internal_transfer_types": sorted(list(INTERNAL_TYPES)),
            "internal_transfers_excluded_from_rankings": excluded_internal,
        },
        "rankings": {
            "top_merchants": top_merchants,
            "top_categories": top_categories,
        },
        "recurring_payments": recurring,
        "top_questionable_spending": questionable_list,
        "spending_cuts": cuts,
        "transaction_ratings": rated_txs,
    }

    sys.stdout.write(json.dumps(out, ensure_ascii=False))


if __name__ == "__main__":
    main()
