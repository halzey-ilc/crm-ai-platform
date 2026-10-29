"""Model registry: approval rules, rollback, audit log and tamper detection."""

from __future__ import annotations

import json
from pathlib import Path

import pytest

from crmai_training import cli, config, experiment
from crmai_training.registry import ApprovalError, Registry, RegistryError, TamperedError

CONFIG = Path(__file__).resolve().parents[1] / "experiments" / "lead_scoring_synthetic.toml"


@pytest.fixture(scope="module")
def runs(tmp_path_factory: pytest.TempPathFactory) -> list[Path]:
    """One synthetic run, trained once for the module; tests copy it as needed."""
    return [experiment.run(config.load(CONFIG), tmp_path_factory.mktemp("runs"))]


def _copy(run: Path, dest: Path, run_id: str) -> Path:
    """A second run of the same model under another run id (cheap stand-in for retraining)."""
    dest.mkdir(parents=True)
    for f in run.iterdir():
        (dest / f.name).write_bytes(f.read_bytes())
    manifest = json.loads((dest / "manifest.json").read_text(encoding="utf-8"))
    manifest["run_id"] = run_id
    (dest / "manifest.json").write_text(json.dumps(manifest), encoding="utf-8")
    return dest


def _real(run: Path, dest: Path, contains_real_data: object) -> Path:
    path = _copy(run, dest, f"real-{dest.name}")
    manifest = json.loads((path / "manifest.json").read_text(encoding="utf-8"))
    manifest["dataset"]["provenance"] = {"kind": "file", "contains_real_data": contains_real_data}
    (path / "manifest.json").write_text(json.dumps(manifest), encoding="utf-8")
    return path


def test_register_approve_rollback(runs: list[Path], tmp_path: Path) -> None:
    r = Registry(tmp_path / "reg")
    v1 = r.register(runs[0])
    assert (v1.version, v1.status) == ("v1", "candidate")
    assert r.active("lead_scoring", "staging") is None
    with pytest.raises(RegistryError):
        r.register(runs[0])  # the same run twice

    r.approve("lead_scoring", "v1", stage="staging", by="Ann", reason="pipeline check")
    v2 = r.register(_copy(runs[0], tmp_path / "run2", "second-run"))
    r.approve("lead_scoring", v2.version, stage="staging", by="Ann", reason="newer")
    assert r.active("lead_scoring", "staging").version == "v2"  # type: ignore[union-attr]

    now = r.rollback("lead_scoring", "staging", by="Ann", reason="worse on live data")
    assert now is not None
    assert now.version == "v1"
    assert r.get("lead_scoring", "v2").status == "retired"
    with pytest.raises(ApprovalError):
        r.approve("lead_scoring", "v2", stage="staging", by="Ann", reason="again")
    assert r.verify() == 5


def test_approval_rules(runs: list[Path], tmp_path: Path) -> None:
    r = Registry(tmp_path / "reg")
    r.register(runs[0])
    with pytest.raises(ApprovalError, match="who decides"):
        r.approve("lead_scoring", "v1", stage="staging", by=" ", reason="x")
    with pytest.raises(ApprovalError, match="synthetic"):
        r.approve("lead_scoring", "v1", stage="production", by="Ann", reason="x")
    undeclared = r.register(_real(runs[0], tmp_path / "undeclared", "unknown"))
    with pytest.raises(ApprovalError, match="declared"):
        r.approve("lead_scoring", undeclared.version, stage="production", by="Ann", reason="x")
    declared = r.register(_real(runs[0], tmp_path / "declared", True))
    r.approve("lead_scoring", declared.version, stage="production", by="Ann", reason="signed off")
    assert r.active("lead_scoring", "production").version == declared.version  # type: ignore[union-attr]

    weak = _copy(runs[0], tmp_path / "weak", "weak-run")
    manifest = json.loads((weak / "manifest.json").read_text(encoding="utf-8"))
    manifest["result"]["beats_baseline_on_test_roc_auc"] = False
    (weak / "manifest.json").write_text(json.dumps(manifest), encoding="utf-8")
    v = r.register(weak)
    with pytest.raises(ApprovalError, match="baseline"):
        r.approve("lead_scoring", v.version, stage="staging", by="Ann", reason="x")


def test_tampering_is_detected(runs: list[Path], tmp_path: Path) -> None:
    r = Registry(tmp_path / "reg")
    r.register(runs[0])
    r.approve("lead_scoring", "v1", stage="staging", by="Ann", reason="ok")

    log = tmp_path / "reg" / "events.jsonl"
    original = log.read_text(encoding="utf-8")
    log.write_text(original.replace('"by": "Ann"', '"by": "Bob"'), encoding="utf-8")
    with pytest.raises(TamperedError, match="log"):
        r.verify()
    log.write_text(original, encoding="utf-8")

    artifact = r.path("lead_scoring", "v1") / "model.pkl"
    artifact.write_bytes(artifact.read_bytes() + b"x")
    with pytest.raises(TamperedError, match="artifact"):
        r.verify()

    changed = _copy(runs[0], tmp_path / "changed", "changed-run")
    (changed / "model.pkl").write_bytes(b"not the trained model")
    with pytest.raises(TamperedError):
        r.register(changed)


def test_cli(runs: list[Path], tmp_path: Path, capsys: pytest.CaptureFixture[str]) -> None:
    root = str(tmp_path / "reg")
    assert cli.main(["registry", "--root", root, "register", str(runs[0])]) == 0
    assert (
        cli.main(["registry", "--root", root, "active", "lead_scoring", "--stage", "staging"]) == 1
    )
    assert (
        cli.main(
            [
                "registry",
                "--root",
                root,
                "approve",
                "lead_scoring",
                "v1",
                "--stage",
                "production",
                "--by",
                "Ann",
                "--reason",
                "x",
            ]
        )
        == 2
    )
    assert "synthetic" in capsys.readouterr().err
    assert (
        cli.main(
            [
                "registry",
                "--root",
                root,
                "approve",
                "lead_scoring",
                "v1",
                "--stage",
                "staging",
                "--by",
                "Ann",
                "--reason",
                "pipeline check",
            ]
        )
        == 0
    )
    assert cli.main(["registry", "--root", root, "verify"]) == 0
    assert "intact" in capsys.readouterr().out
