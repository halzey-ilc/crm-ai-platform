"""Command line entry point: `crmai-train run <config.toml> [--out DIR]`."""

from __future__ import annotations

import argparse
import sys

from . import config, experiment
from .dataset import DataQualityError


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(prog="crmai-train")
    sub = parser.add_subparsers(dest="cmd", required=True)
    run = sub.add_parser("run", help="run an experiment")
    run.add_argument("config")
    run.add_argument("--out", default="runs")
    args = parser.parse_args(argv)

    try:
        cfg = config.load(args.config)
        run_dir = experiment.run(cfg, args.out)
    except (config.ConfigError, DataQualityError, experiment.InsufficientDataError) as e:
        print(f"crmai-train: {e}", file=sys.stderr)
        return 2
    print(f"run written to {run_dir}")
    print((run_dir / "report.md").read_text(encoding="utf-8"))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
