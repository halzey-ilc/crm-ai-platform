"""Evaluate call.summary.v1 through the running gateway (whatever provider it routes to).

Usage (from the repository root, with the stack up):
    python3 -I evaluation/run_call_summary_eval.py [dataset.jsonl] [--out report.json]

For each synthetic call it measures:
  * succeeded   - the job finished and passed the output contract + grounding checks;
  * fact_recall - share of expected fact stems found in summary/key points (case-insensitive);
  * next_step   - whether a next step was returned exactly when one was agreed;
  * latency_s   - wall time from job creation to result.
These are rough, automatic checks on a tiny synthetic set: they catch regressions
and gross failures, they do not prove quality. Nothing here is real customer data.
"""

from __future__ import annotations

import json
import os
import statistics
import subprocess
import sys
import time
import urllib.error
import urllib.request
import uuid
from pathlib import Path

BASE = os.environ.get("AI_GATEWAY_URL", "http://127.0.0.1:8090")
ROOT = Path(__file__).resolve().parents[1]


def _token() -> str:
    env = dict(os.environ)
    for line in (ROOT / ".env").read_text(encoding="utf-8").splitlines():
        if "=" in line and not line.startswith("#"):
            key, value = line.split("=", 1)
            env.setdefault(key, value)
    return subprocess.run(  # noqa: S603 - fixed local tool
        # The gateway refuses tokens living longer than 10 minutes: mint one per case.
        ["go", "run", "./tools/devtoken", "-tenant", "eval", "-ttl", "10m"],  # noqa: S607
        cwd=ROOT, env=env, capture_output=True, text=True, check=True,
    ).stdout.strip()


def _call(method: str, path: str, token: str, body: dict | None = None, key: str | None = None) -> dict:
    req = urllib.request.Request(BASE + path, method=method)  # noqa: S310 - local gateway
    req.add_header("Authorization", f"Bearer {token}")
    data = None
    if body is not None:
        data = json.dumps(body).encode("utf-8")
        req.add_header("Content-Type", "application/json")
    if key:
        req.add_header("Idempotency-Key", key)
    with urllib.request.urlopen(req, data=data, timeout=30) as resp:  # noqa: S310
        return json.loads(resp.read())


def _run_case(case: dict, token: str) -> dict:
    started = time.monotonic()
    job = _call(
        "POST",
        "/v1/jobs",
        token,
        {"type": "call.summary.v1", "input": {"transcript": case["transcript"], "language": case["language"]}},
        key=f"eval-{case['id']}-{uuid.uuid4().hex[:8]}",
    )
    while True:
        job = _call("GET", f"/v1/jobs/{job['id']}", token)
        if job["status"] in ("succeeded", "failed", "cancelled"):
            break
        if time.monotonic() - started > 900:
            break
        time.sleep(2)
    result = {"id": case["id"], "language": case["language"], "status": job["status"],
              "attempts": job["attempts"], "latency_s": round(time.monotonic() - started, 1)}
    if job["status"] != "succeeded":
        result["error"] = (job.get("error") or {}).get("code")
        return result
    out = job["result"]["output"]
    text = " ".join([out["summary"], *(kp["text"] for kp in out["key_points"])]).lower()
    found = [f for f in case["expect_facts"] if f.lower() in text]
    result.update(
        provider=job["result"]["provider"],
        model=job["result"]["model"],
        fact_recall=round(len(found) / len(case["expect_facts"]), 2),
        missed_facts=[f for f in case["expect_facts"] if f not in found],
        next_step_ok=(out["next_step"] is not None) == case["expect_next_step"],
        key_points=len(out["key_points"]),
        output=out,
    )
    return result


def main() -> int:
    args = [a for a in sys.argv[1:] if not a.startswith("--out")]
    out_path = next((a.split("=", 1)[1] for a in sys.argv[1:] if a.startswith("--out=")), None)
    dataset = Path(args[0]) if args else ROOT / "evaluation/datasets/call_summary.synthetic.v1.jsonl"
    cases = [json.loads(line) for line in dataset.read_text(encoding="utf-8").splitlines() if line.strip()]
    results = []
    for case in cases:
        try:
            r = _run_case(case, _token())
        except urllib.error.HTTPError as exc:
            r = {"id": case["id"], "language": case["language"], "status": "error",
                 "error": f"HTTP {exc.code}"}
        except urllib.error.URLError as exc:
            r = {"id": case["id"], "language": case["language"], "status": "error", "error": str(exc)}
        results.append(r)
        print(f"{r['id']:6} {r['status']:9} recall={r.get('fact_recall', '-')!s:4} "
              f"next_step_ok={r.get('next_step_ok', '-')!s:5} {r.get('latency_s', '-')}s", flush=True)

    ok = [r for r in results if r["status"] == "succeeded"]
    by_lang: dict[str, list[dict]] = {}
    for r in ok:
        by_lang.setdefault(r["language"], []).append(r)
    summary = {
        "dataset": dataset.name,
        "cases": len(results),
        "succeeded": len(ok),
        "fact_recall_mean": round(statistics.mean(r["fact_recall"] for r in ok), 2) if ok else None,
        "next_step_accuracy": round(sum(r["next_step_ok"] for r in ok) / len(ok), 2) if ok else None,
        "latency_s_median": statistics.median(r["latency_s"] for r in ok) if ok else None,
        "by_language": {
            lang: {
                "succeeded": len(rs),
                "fact_recall_mean": round(statistics.mean(r["fact_recall"] for r in rs), 2),
                "next_step_accuracy": round(sum(r["next_step_ok"] for r in rs) / len(rs), 2),
            }
            for lang, rs in sorted(by_lang.items())
        },
    }
    print(json.dumps(summary, ensure_ascii=False, indent=2))
    if out_path:
        Path(out_path).parent.mkdir(parents=True, exist_ok=True)
        Path(out_path).write_text(
            json.dumps({"summary": summary, "results": results}, ensure_ascii=False, indent=2) + "\n",
            encoding="utf-8",
        )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
