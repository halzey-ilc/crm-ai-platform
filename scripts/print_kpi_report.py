"""Print a CRM KPI explanation (JSON on stdin) and re-check its numbers.

Used by e2e_kpi.sh. Exits non-zero unless the report succeeded and every
number in its texts is one of the CRM's input numbers.
"""

from __future__ import annotations

import json
import re
import sys

NUMBER = re.compile(r"\d{1,3}(?:[   ]\d{3})+(?:[.,]\d+)?|\d+(?:[.,]\d+)?")


def allowed_numbers(payload: dict) -> set[float]:
    out: set[float] = set()
    for m in payload["metrics"]:
        for key in ("current", "previous", "change", "change_pct"):
            v = m[key]
            if v is not None:
                for x in (v, abs(v)):
                    out.update({float(x), round(x, 2), round(x, 1), float(round(x))})
    for period in (payload["period"], payload["previous_period"]):
        for d in (period["start"], period["end"]):
            out.update(float(p) for p in d.split("-"))
    return out


def numbers(text: str) -> list[float]:
    found = []
    for raw in NUMBER.findall(text):
        cleaned = raw.replace(" ", "").replace(" ", "").replace(" ", "").replace(",", ".")
        found.append(float(cleaned))
    return found


def main() -> int:
    sys.stdout.reconfigure(encoding="utf-8")
    d = json.load(sys.stdin)
    print(f"report: {d['status']} | provider: {d['provider']} | model: {d['model']} | is_mock: {d['is_mock']}")
    print("--- CRM numbers (input):")
    for m in d["input"]["metrics"]:
        if m["current"] or m["previous"]:
            print(f"  {m['code']:22} current={m['current']} previous={m['previous']} "
                  f"change={m['change']} change_pct={m['change_pct']}")
    if d["status"] != "succeeded":
        print("error_code:", d["error_code"])
        return 1
    r = d["result"]
    print("--- AI explanation:")
    print(r["headline"])
    texts = [r["headline"]]
    for o in r["observations"]:
        print(" -", o["text"], "->", ",".join(o["metric_codes"]))
        texts.append(o["text"])
    for rec in r["recommendations"]:
        print(" *", rec["text"])
        texts.append(rec["text"])
    allowed = allowed_numbers(d["input"])
    bad = [x for t in texts for x in numbers(t) if not any(abs(x - a) < 1e-6 for a in allowed)]
    print("numbers not from the CRM:", bad or "none")
    return 1 if bad else 0


if __name__ == "__main__":
    raise SystemExit(main())
