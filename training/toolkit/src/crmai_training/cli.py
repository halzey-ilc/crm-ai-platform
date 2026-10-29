"""Command line entry point.

    crmai-train run <config.toml> [--out DIR]
    crmai-train registry [--root DIR] register <run_dir>
    crmai-train registry [--root DIR] list [--task T]
    crmai-train registry [--root DIR] approve <task> <version> --stage S --by NAME --reason TEXT
    crmai-train registry [--root DIR] retire <task> <version> --by NAME --reason TEXT
    crmai-train registry [--root DIR] rollback <task> --stage S --by NAME --reason TEXT
    crmai-train registry [--root DIR] active <task> --stage S
    crmai-train registry [--root DIR] verify

The registry root defaults to $CRMAI_REGISTRY, else ./registry (keep it out of git).
"""

from __future__ import annotations

import argparse
import os
import sys

from . import config, experiment
from .dataset import DataQualityError
from .registry import STAGES, Registry, RegistryError, Version


def _describe(v: Version) -> str:
    stages = ",".join(a["stage"] for a in v.approvals) or "-"
    return f"{v.task} {v.version}  {v.status:<9} approved for: {stages:<19} run {v.run_id}"


def _registry_parser(sub: argparse._SubParsersAction) -> None:
    reg = sub.add_parser("registry", help="model registry")
    reg.add_argument("--root", default=os.environ.get("CRMAI_REGISTRY", "registry"))
    ops = reg.add_subparsers(dest="op", required=True)
    ops.add_parser("register").add_argument("run_dir")
    ops.add_parser("list").add_argument("--task")
    ops.add_parser("verify")
    active = ops.add_parser("active")
    active.add_argument("task")
    active.add_argument("--stage", choices=STAGES, required=True)
    for name in ("approve", "retire", "rollback"):
        p = ops.add_parser(name)
        p.add_argument("task")
        if name != "rollback":
            p.add_argument("version")
        if name != "retire":
            p.add_argument("--stage", choices=STAGES, required=True)
        p.add_argument("--by", required=True, help="who decides")
        p.add_argument("--reason", required=True, help="why")


def _registry(args: argparse.Namespace) -> int:
    r = Registry(args.root)
    if args.op == "register":
        print(f"registered {_describe(r.register(args.run_dir))}")
    elif args.op == "list":
        for v in r.versions(args.task):
            print(_describe(v))
    elif args.op == "approve":
        v = r.approve(args.task, args.version, stage=args.stage, by=args.by, reason=args.reason)
        print(f"approved {_describe(v)}")
    elif args.op == "retire":
        v = r.retire(args.task, args.version, by=args.by, reason=args.reason)
        print(f"retired {_describe(v)}")
    elif args.op == "rollback":
        now = r.rollback(args.task, args.stage, by=args.by, reason=args.reason)
        print(f"active for {args.stage}: {now.version if now else 'none'}")
    elif args.op == "active":
        current = r.active(args.task, args.stage)
        print(current.version if current else "none")
        return 0 if current else 1
    elif args.op == "verify":
        print(f"registry intact: {r.verify()} events, artifacts match")
    return 0


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(prog="crmai-train")
    sub = parser.add_subparsers(dest="cmd", required=True)
    run = sub.add_parser("run", help="run an experiment")
    run.add_argument("config")
    run.add_argument("--out", default="runs")
    _registry_parser(sub)
    args = parser.parse_args(argv)

    try:
        if args.cmd == "registry":
            return _registry(args)
        cfg = config.load(args.config)
        run_dir = experiment.run(cfg, args.out)
    except (
        config.ConfigError,
        DataQualityError,
        experiment.InsufficientDataError,
        RegistryError,
    ) as e:
        print(f"crmai-train: {e}", file=sys.stderr)
        return 2
    print(f"run written to {run_dir}")
    print((run_dir / "report.md").read_text(encoding="utf-8"))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
