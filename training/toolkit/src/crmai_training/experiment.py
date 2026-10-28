"""Run one experiment: data checks, temporal split, baseline vs model, report."""

from __future__ import annotations

import hashlib
import itertools
import json
import pickle
import platform
from dataclasses import asdict
from datetime import UTC, datetime
from pathlib import Path

import numpy as np
import sklearn
from sklearn.dummy import DummyClassifier
from sklearn.linear_model import LogisticRegression
from sklearn.metrics import (
    average_precision_score,
    brier_score_loss,
    f1_score,
    log_loss,
    precision_score,
    recall_score,
    roc_auc_score,
)
from sklearn.pipeline import make_pipeline
from sklearn.preprocessing import StandardScaler

from . import __version__, synthetic
from .config import ExperimentConfig
from .dataset import (
    SCHEMA_VERSION,
    DataQualityError,
    Dataset,
    load_csv,
    quality_report,
    to_csv_bytes,
    version,
)


class InsufficientDataError(RuntimeError):
    """The dataset is too small or too unbalanced to train responsibly."""


def temporal_split(ds: Dataset, cfg: ExperimentConfig) -> dict[str, list[dict[str, str]]]:
    """Split by creation time so the model is always tested on later leads."""
    rows = sorted(ds.rows, key=lambda r: (r[cfg.time_column], r[cfg.id_column]))
    n = len(rows)
    a = int(n * cfg.split.train)
    b = a + int(n * cfg.split.valid)
    parts = {"train": rows[:a], "valid": rows[a:b], "test": rows[b:]}
    for earlier, later in (("train", "valid"), ("valid", "test")):
        if (
            parts[earlier]
            and parts[later]
            and parts[earlier][-1][cfg.time_column] > parts[later][0][cfg.time_column]
        ):
            raise AssertionError("temporal split is not ordered")
    ids = [{r[cfg.id_column] for r in p} for p in parts.values()]
    if ids[0] & ids[1] or ids[0] & ids[2] or ids[1] & ids[2]:
        raise AssertionError("an id appears in more than one split")
    return parts


def featurize(
    rows: list[dict[str, str]], cfg: ExperimentConfig, categories: dict[str, list[str]]
) -> np.ndarray:
    cols = []
    for c in cfg.numeric:
        cols.append(np.array([float(r[c]) for r in rows]))
    for c in cfg.categorical:
        values = [r[c] for r in rows]
        for cat in categories[c]:
            cols.append(np.array([1.0 if v == cat else 0.0 for v in values]))
    return np.column_stack(cols)


def _metrics(y: np.ndarray, p: np.ndarray) -> dict[str, float]:
    pred = (p >= 0.5).astype(int)
    out = {
        "positive_rate": float(y.mean()),
        "brier": float(brier_score_loss(y, p)),
        "log_loss": float(log_loss(y, p, labels=[0, 1])),
        "precision@0.5": float(precision_score(y, pred, zero_division=0)),
        "recall@0.5": float(recall_score(y, pred, zero_division=0)),
        "f1@0.5": float(f1_score(y, pred, zero_division=0)),
    }
    if len(set(y.tolist())) == 2:
        out["roc_auc"] = float(roc_auc_score(y, p))
        out["pr_auc"] = float(average_precision_score(y, p))
    return out


def _calibration(y: np.ndarray, p: np.ndarray, bins: int = 10) -> list[dict[str, float]]:
    edges = np.linspace(0, 1, bins + 1)
    out = []
    for lo, hi in itertools.pairwise(edges):
        mask = (p >= lo) & ((p < hi) if hi < 1 else (p <= hi))
        if mask.any():
            out.append(
                {
                    "bin": f"{lo:.1f}-{hi:.1f}",
                    "count": int(mask.sum()),
                    "mean_predicted": float(p[mask].mean()),
                    "observed_rate": float(y[mask].mean()),
                }
            )
    return out


def _by_group(rows, y, p, column) -> dict[str, dict[str, float]]:
    groups: dict[str, list[int]] = {}
    for i, r in enumerate(rows):
        groups.setdefault(r[column], []).append(i)
    out = {}
    for g, idx in sorted(groups.items()):
        yi, pi = y[idx], p[idx]
        entry = {
            "count": len(idx),
            "positive_rate": float(yi.mean()),
            "recall@0.5": float(recall_score(yi, (pi >= 0.5).astype(int), zero_division=0)),
        }
        if len(set(yi.tolist())) == 2:
            entry["roc_auc"] = float(roc_auc_score(yi, pi))
        out[g] = entry
    return out


def run(cfg: ExperimentConfig, out_dir: str | Path) -> Path:
    """Execute the experiment and write its artifacts. Returns the run directory."""
    if cfg.dataset.kind == "synthetic":
        ds = synthetic.generate(cfg.dataset.rows, cfg.dataset.seed)
        data = to_csv_bytes(ds)
        provenance = {
            "kind": "synthetic",
            "generator": "crmai_training.synthetic",
            "rows": cfg.dataset.rows,
            "seed": cfg.dataset.seed,
            "contains_real_data": False,
        }
    else:
        ds, data = load_csv(cfg.dataset.path)  # type: ignore[arg-type]
        provenance = {
            "kind": "file",
            "path_name": Path(cfg.dataset.path).name,  # type: ignore[arg-type]
            "contains_real_data": "unknown - must be declared by the data owner",
        }
    ds_version = version(data)

    quality = quality_report(
        ds,
        label=cfg.label,
        time_column=cfg.time_column,
        id_column=cfg.id_column,
        required=[*cfg.categorical, *cfg.numeric],
        forbidden=list(cfg.forbidden_columns),
    )
    if quality["rows"] < cfg.min_rows or quality["positives"] < cfg.min_positives:
        raise InsufficientDataError(
            f"need >= {cfg.min_rows} rows and >= {cfg.min_positives} positives; "
            f"have {quality['rows']} rows and {quality['positives']} positives"
        )

    parts = temporal_split(ds, cfg)
    categories = {c: sorted({r[c] for r in parts["train"]}) for c in cfg.categorical}
    x = {k: featurize(v, cfg, categories) for k, v in parts.items()}
    y = {k: np.array([int(r[cfg.label]) for r in v]) for k, v in parts.items()}

    baseline = DummyClassifier(strategy="prior").fit(x["train"], y["train"])
    model = make_pipeline(
        StandardScaler(),
        LogisticRegression(
            C=float(cfg.model_params.get("C", 1.0)),
            max_iter=int(cfg.model_params.get("max_iter", 1000)),
            random_state=cfg.seed,
        ),
    )
    model.fit(x["train"], y["train"])

    evaluation = {}
    for split in ("valid", "test"):
        evaluation[split] = {
            "baseline_prior": _metrics(y[split], baseline.predict_proba(x[split])[:, 1]),
            "logistic_regression": _metrics(y[split], model.predict_proba(x[split])[:, 1]),
        }
    p_test = model.predict_proba(x["test"])[:, 1]
    evaluation["test_calibration"] = _calibration(y["test"], p_test)
    evaluation["test_by_source"] = _by_group(parts["test"], y["test"], p_test, "source")

    model_bytes = pickle.dumps(model)
    run_id = f"{cfg.name}-{ds_version['version'][:8]}-s{cfg.seed}"
    run_dir = Path(out_dir) / run_id
    run_dir.mkdir(parents=True, exist_ok=True)

    beats = evaluation["test"]["logistic_regression"].get("roc_auc", 0) > evaluation["test"][
        "baseline_prior"
    ].get("roc_auc", 0.5)
    manifest = {
        "schema": "crmai.experiment_run.v1",
        "run_id": run_id,
        "experiment": cfg.name,
        "task": cfg.task,
        "created_at": datetime.now(UTC).isoformat(timespec="seconds"),
        "dataset": {
            "schema_version": SCHEMA_VERSION,
            **ds_version,
            "rows": quality["rows"],
            "tenant": cfg.dataset.tenant,
            "provenance": provenance,
        },
        "split": {
            "strategy": "temporal",
            "fractions": asdict(cfg.split),
            "rows": {k: len(v) for k, v in parts.items()},
        },
        "features": {
            "categorical": list(cfg.categorical),
            "numeric": list(cfg.numeric),
            "forbidden_excluded": list(cfg.forbidden_columns),
        },
        "training": {
            "algorithm": "logistic_regression",
            "params": cfg.model_params,
            "seed": cfg.seed,
        },
        "artifact": {
            "file": "model.pkl",
            "format": "sklearn-pickle",
            "sha256": hashlib.sha256(model_bytes).hexdigest(),
            "note": "pickle is not a serving format; never load artifacts from untrusted sources",
        },
        "runtime": {
            "python": platform.python_version(),
            "numpy": np.__version__,
            "scikit_learn": sklearn.__version__,
            "toolkit": __version__,
        },
        "result": {"beats_baseline_on_test_roc_auc": bool(beats)},
        # Training success never implies approval: see docs/MODEL_EVALUATION.md.
        "registry_status": "candidate",
        "approved_for_deployment": False,
    }
    (run_dir / "model.pkl").write_bytes(model_bytes)
    _write_json(run_dir / "manifest.json", manifest)
    _write_json(run_dir / "quality.json", quality)
    _write_json(run_dir / "metrics.json", evaluation)
    (run_dir / "report.md").write_text(_report(manifest, quality, evaluation), encoding="utf-8")
    return run_dir


def _write_json(path: Path, obj) -> None:
    path.write_text(
        json.dumps(obj, ensure_ascii=False, indent=2, sort_keys=True) + "\n", encoding="utf-8"
    )


def _report(manifest: dict, quality: dict, ev: dict) -> str:
    t = ev["test"]
    ds, split = manifest["dataset"], manifest["split"]["rows"]
    lines = [
        f"# Experiment report: {manifest['run_id']}",
        "",
        f"- Task: `{manifest['task']}`",
        f"- Dataset version: `{ds['version']}` (sha256 `{ds['sha256']}`)",
        f"- Provenance: {ds['provenance']['kind']}, "
        f"real data: {ds['provenance']['contains_real_data']}",
        f"- Rows: {quality['rows']}, positive rate {quality['positive_rate']:.3f}",
        f"- Split (temporal): train {split['train']}, valid {split['valid']}, test {split['test']}",
        f"- Registry status: **{manifest['registry_status']}**, approved for deployment: "
        f"**{manifest['approved_for_deployment']}**",
        "",
        "## Test metrics",
        "",
        "| metric | baseline (prior) | logistic regression |",
        "|---|---|---|",
    ]
    for k in sorted(t["logistic_regression"]):
        b, m = t["baseline_prior"].get(k), t["logistic_regression"][k]
        lines.append(f"| {k} | {b:.4f} | {m:.4f} |" if b is not None else f"| {k} | - | {m:.4f} |")
    lines += ["", "## Limitations", ""]
    if manifest["dataset"]["provenance"]["kind"] == "synthetic":
        lines.append(
            "- Synthetic data with a known generating model: these numbers validate the "
            "pipeline only and say nothing about real lead conversion."
        )
    lines.append(
        "- Not evaluated for fairness, drift, or production latency; not approved for use."
    )
    return "\n".join(lines) + "\n"


__all__ = ["DataQualityError", "InsufficientDataError", "run", "temporal_split"]
