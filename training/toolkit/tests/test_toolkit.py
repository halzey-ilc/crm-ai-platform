import json
from dataclasses import replace
from pathlib import Path

import pytest

from crmai_training import config, experiment, synthetic
from crmai_training.dataset import (
    DataQualityError,
    Dataset,
    from_csv_bytes,
    quality_report,
    to_csv_bytes,
    version,
)

CONFIG = Path(__file__).parent.parent / "experiments" / "lead_scoring_synthetic.toml"


@pytest.fixture
def cfg():
    return config.load(CONFIG)


def test_synthetic_dataset_is_reproducible():
    a = to_csv_bytes(synthetic.generate(500, seed=1))
    b = to_csv_bytes(synthetic.generate(500, seed=1))
    c = to_csv_bytes(synthetic.generate(500, seed=2))
    assert version(a) == version(b)
    assert version(a) != version(c)


def test_csv_round_trip_keeps_version():
    data = to_csv_bytes(synthetic.generate(200, seed=3))
    assert to_csv_bytes(from_csv_bytes(data)) == data


def _report(ds, cfg):
    return quality_report(
        ds,
        label=cfg.label,
        time_column=cfg.time_column,
        id_column=cfg.id_column,
        required=[*cfg.categorical, *cfg.numeric],
        forbidden=list(cfg.forbidden_columns),
    )


def test_quality_report_counts(cfg):
    q = _report(synthetic.generate(1000, seed=4), cfg)
    assert q["rows"] == 1000
    assert q["positives"] + q["negatives"] == 1000
    assert q["duplicate_ids"] == 0


def test_leaking_column_is_blocking(cfg):
    ds = synthetic.generate(200, seed=5)
    leaky = Dataset(
        columns=(*ds.columns, "status"), rows=tuple(r | {"status": "converted"} for r in ds.rows)
    )
    with pytest.raises(DataQualityError, match="leak"):
        _report(leaky, cfg)


def test_duplicates_and_naive_timestamps_are_blocking(cfg):
    ds = synthetic.generate(200, seed=6)
    rows = list(ds.rows)
    rows[1] = rows[1] | {"lead_id": rows[0]["lead_id"]}
    rows[2] = rows[2] | {"created_at": "2025-03-01T10:00:00"}
    with pytest.raises(DataQualityError) as e:
        _report(Dataset(columns=ds.columns, rows=tuple(rows)), cfg)
    assert "duplicate" in str(e.value)
    assert "timezone" in str(e.value)


def test_config_rejects_forbidden_feature(tmp_path):
    text = CONFIG.read_text(encoding="utf-8").replace(
        'numeric = ["has_email"', 'numeric = ["status", "has_email"'
    )
    p = tmp_path / "bad.toml"
    p.write_text(text, encoding="utf-8")
    with pytest.raises(config.ConfigError, match="forbidden"):
        config.load(p)


def test_temporal_split_orders_and_separates(cfg):
    parts = experiment.temporal_split(synthetic.generate(1000, seed=8), cfg)
    assert parts["train"][-1]["created_at"] <= parts["valid"][0]["created_at"]
    assert parts["valid"][-1]["created_at"] <= parts["test"][0]["created_at"]
    assert sum(len(p) for p in parts.values()) == 1000


def test_insufficient_data_refuses_to_train(cfg, tmp_path):
    small = replace(cfg, dataset=replace(cfg.dataset, rows=200), min_rows=1000)
    with pytest.raises(experiment.InsufficientDataError):
        experiment.run(small, tmp_path)


def test_end_to_end_run_is_reproducible(cfg, tmp_path):
    small = replace(cfg, dataset=replace(cfg.dataset, rows=2000))
    run1 = experiment.run(small, tmp_path / "a")
    run2 = experiment.run(small, tmp_path / "b")

    m1 = json.loads((run1 / "manifest.json").read_text(encoding="utf-8"))
    m2 = json.loads((run2 / "manifest.json").read_text(encoding="utf-8"))
    assert m1["dataset"]["sha256"] == m2["dataset"]["sha256"]
    assert m1["registry_status"] == "candidate"
    assert m1["approved_for_deployment"] is False
    assert m1["dataset"]["provenance"]["contains_real_data"] is False
    assert sum(m1["split"]["rows"].values()) == 2000

    metrics1 = (run1 / "metrics.json").read_text(encoding="utf-8")
    assert metrics1 == (run2 / "metrics.json").read_text(encoding="utf-8")
    test = json.loads(metrics1)["test"]
    # On data with a known signal the model must beat the prior baseline.
    assert test["logistic_regression"]["roc_auc"] > test["baseline_prior"]["roc_auc"] + 0.1
    assert (run1 / "report.md").read_text(encoding="utf-8").startswith("# Experiment report")
