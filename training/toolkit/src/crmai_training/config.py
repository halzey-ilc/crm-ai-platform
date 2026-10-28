"""Experiment configuration (TOML), validated before anything runs."""

from __future__ import annotations

import tomllib
from dataclasses import dataclass, field
from pathlib import Path


class ConfigError(ValueError):
    """The experiment configuration is invalid."""


@dataclass(frozen=True)
class DatasetConfig:
    kind: str  # "synthetic" (generated) or "file" (an approved CSV)
    path: str | None = None
    rows: int = 0
    seed: int = 0
    tenant: str = "synthetic"


@dataclass(frozen=True)
class SplitConfig:
    train: float = 0.7
    valid: float = 0.15
    test: float = 0.15


@dataclass(frozen=True)
class ExperimentConfig:
    name: str
    task: str
    dataset: DatasetConfig
    split: SplitConfig
    seed: int
    label: str
    time_column: str
    id_column: str
    categorical: tuple[str, ...]
    numeric: tuple[str, ...]
    forbidden_columns: tuple[str, ...]
    min_rows: int
    min_positives: int
    model_params: dict[str, float | int | str] = field(default_factory=dict)


TASKS = {"lead_scoring"}


def load(path: str | Path) -> ExperimentConfig:
    raw = tomllib.loads(Path(path).read_text(encoding="utf-8"))
    try:
        exp, ds, sp, ft = raw["experiment"], raw["dataset"], raw.get("split", {}), raw["features"]
    except KeyError as e:
        raise ConfigError(f"missing section [{e.args[0]}]") from None

    cfg = ExperimentConfig(
        name=str(exp["name"]),
        task=str(exp["task"]),
        seed=int(exp.get("seed", 0)),
        dataset=DatasetConfig(
            kind=str(ds["kind"]),
            path=ds.get("path"),
            rows=int(ds.get("rows", 0)),
            seed=int(ds.get("seed", 0)),
            tenant=str(ds.get("tenant", "synthetic")),
        ),
        split=SplitConfig(**{k: float(v) for k, v in sp.items()}),
        label=str(ft["label"]),
        time_column=str(ft["time_column"]),
        id_column=str(ft["id_column"]),
        categorical=tuple(ft.get("categorical", ())),
        numeric=tuple(ft.get("numeric", ())),
        forbidden_columns=tuple(ft.get("forbidden_columns", ())),
        min_rows=int(raw.get("sufficiency", {}).get("min_rows", 0)),
        min_positives=int(raw.get("sufficiency", {}).get("min_positives", 0)),
        model_params=dict(raw.get("model", {})),
    )
    _validate(cfg)
    return cfg


def _validate(cfg: ExperimentConfig) -> None:
    if cfg.task not in TASKS:
        raise ConfigError(f"unknown task {cfg.task!r}; supported: {sorted(TASKS)}")
    if cfg.dataset.kind == "synthetic":
        if cfg.dataset.rows < 100:
            raise ConfigError("synthetic dataset needs rows >= 100")
    elif cfg.dataset.kind == "file":
        if not cfg.dataset.path:
            raise ConfigError("file dataset needs a path")
    else:
        raise ConfigError("dataset.kind must be 'synthetic' or 'file'")
    s = cfg.split
    if min(s.train, s.valid, s.test) <= 0 or abs(s.train + s.valid + s.test - 1) > 1e-9:
        raise ConfigError("split fractions must be positive and sum to 1")
    features = set(cfg.categorical) | set(cfg.numeric)
    if not features:
        raise ConfigError("no features configured")
    leaked = features & ({cfg.label, cfg.id_column, cfg.time_column} | set(cfg.forbidden_columns))
    if leaked:
        raise ConfigError(f"features include label/id/time/forbidden columns: {sorted(leaked)}")
