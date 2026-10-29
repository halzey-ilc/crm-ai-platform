"""Model registry: versions, approval, retirement and rollback.

A registry is a directory (outside git) holding a copy of every registered run
and an append-only event log. The state of every model is derived from the log,
so the log is the audit trail: who registered, approved or retired which
version, when and why. Each event carries the hash of the previous one, so an
edited or reordered log is detected by ``verify``.

Statuses: candidate -> approved (for a stage) -> retired. The active model of a
task and stage is the most recently approved one that is not retired; retiring
it rolls back to the previous approval. Nothing here deploys anything: serving
reads the active version explicitly.

Approval rules (``ApprovalError`` otherwise):
* the artifact's sha256 still matches the manifest;
* the model beat the baseline on the test split;
* a person and a reason are named;
* models trained on synthetic data cannot be approved for production, and a
  dataset with undeclared real-data status cannot either.
"""

from __future__ import annotations

import hashlib
import json
import shutil
from dataclasses import dataclass, field
from datetime import UTC, datetime
from pathlib import Path

STAGES = ("staging", "production")
EVENTS = "events.jsonl"
_FILES = ("manifest.json", "metrics.json", "quality.json", "report.md")
_GENESIS = "0" * 64


class RegistryError(RuntimeError):
    """Invalid registry operation."""


class ApprovalError(RegistryError):
    """A version does not meet the approval rules."""


class TamperedError(RegistryError):
    """The event log or an artifact no longer matches what was recorded."""


@dataclass
class Version:
    task: str
    version: str
    run_id: str
    sha256: str
    registered_at: str
    status: str = "candidate"  # candidate | approved | retired
    approvals: list[dict[str, str]] = field(default_factory=list)  # {stage, by, reason, at}
    retired: dict[str, str] | None = None


def _now() -> str:
    return datetime.now(UTC).isoformat(timespec="seconds")


def _sha256(path: Path) -> str:
    return hashlib.sha256(path.read_bytes()).hexdigest()


def _canonical(event: dict) -> bytes:
    return json.dumps(event, sort_keys=True, separators=(",", ":"), ensure_ascii=False).encode()


class Registry:
    def __init__(self, root: str | Path) -> None:
        self.root = Path(root)

    # --- event log ------------------------------------------------------------

    def _events(self) -> list[dict]:
        path = self.root / EVENTS
        if not path.exists():
            return []
        return [json.loads(line) for line in path.read_text(encoding="utf-8").splitlines() if line]

    def _append(self, event: dict) -> dict:
        events = self._events()
        prev = events[-1]["hash"] if events else _GENESIS
        body = {**event, "at": _now(), "prev": prev}
        body["hash"] = hashlib.sha256(_canonical(body)).hexdigest()
        self.root.mkdir(parents=True, exist_ok=True)
        with (self.root / EVENTS).open("a", encoding="utf-8") as f:
            f.write(json.dumps(body, ensure_ascii=False, sort_keys=True) + "\n")
        return body

    def state(self) -> dict[tuple[str, str], Version]:
        """Replay the log into the current state of every version."""
        versions: dict[tuple[str, str], Version] = {}
        for e in self._events():
            key = (e["task"], e["version"])
            if e["action"] == "registered":
                versions[key] = Version(
                    task=e["task"],
                    version=e["version"],
                    run_id=e["run_id"],
                    sha256=e["sha256"],
                    registered_at=e["at"],
                )
            elif e["action"] == "approved":
                v = versions[key]
                v.status = "approved"
                v.approvals.append(
                    {"stage": e["stage"], "by": e["by"], "reason": e["reason"], "at": e["at"]}
                )
            elif e["action"] == "retired":
                v = versions[key]
                v.status = "retired"
                v.retired = {"by": e["by"], "reason": e["reason"], "at": e["at"]}
        return versions

    def versions(self, task: str | None = None) -> list[Version]:
        out = [v for v in self.state().values() if task is None or v.task == task]
        return sorted(out, key=lambda v: (v.task, int(v.version.removeprefix("v"))))

    def get(self, task: str, version: str) -> Version:
        v = self.state().get((task, version))
        if v is None:
            raise RegistryError(f"{task} {version} is not registered")
        return v

    def active(self, task: str, stage: str) -> Version | None:
        """The most recently approved, not retired version for the stage."""
        _check_stage(stage)
        state = self.state()
        chosen: str | None = None
        # Log order, not timestamps: two approvals can share a second.
        for e in self._events():
            if (
                e["action"] == "approved"
                and e["task"] == task
                and e["stage"] == stage
                and state[(task, e["version"])].status != "retired"
            ):
                chosen = e["version"]
        return None if chosen is None else state[(task, chosen)]

    def path(self, task: str, version: str) -> Path:
        return self.root / "models" / task / version

    # --- operations -------------------------------------------------------------

    def register(self, run_dir: str | Path) -> Version:
        """Copy an experiment run into the registry as a new candidate version."""
        run_dir = Path(run_dir)
        manifest = json.loads((run_dir / "manifest.json").read_text(encoding="utf-8"))
        if manifest.get("schema") != "crmai.experiment_run.v1":
            raise RegistryError("not an experiment run (manifest schema)")
        artifact = run_dir / manifest["artifact"]["file"]
        if _sha256(artifact) != manifest["artifact"]["sha256"]:
            raise TamperedError("the artifact does not match its manifest sha256")
        task = manifest["task"]
        for v in self.versions(task):
            if v.run_id == manifest["run_id"]:
                raise RegistryError(
                    f"run {manifest['run_id']} is already registered as {v.version}"
                )
        version = f"v{len(self.versions(task)) + 1}"
        dest = self.path(task, version)
        dest.mkdir(parents=True, exist_ok=False)
        shutil.copy2(artifact, dest / artifact.name)
        for name in _FILES:
            if (run_dir / name).exists():
                shutil.copy2(run_dir / name, dest / name)
        self._append(
            {
                "action": "registered",
                "task": task,
                "version": version,
                "run_id": manifest["run_id"],
                "sha256": manifest["artifact"]["sha256"],
            }
        )
        return self.get(task, version)

    def approve(self, task: str, version: str, *, stage: str, by: str, reason: str) -> Version:
        _check_stage(stage)
        _require_person(by, reason)
        v = self.get(task, version)
        if v.status == "retired":
            raise ApprovalError(f"{task} {version} is retired and cannot be approved again")
        if any(a["stage"] == stage for a in v.approvals):
            raise ApprovalError(f"{task} {version} is already approved for {stage}")
        manifest = self._verified_manifest(v)
        if not manifest.get("result", {}).get("beats_baseline_on_test_roc_auc"):
            raise ApprovalError("the model does not beat the baseline on the test split")
        provenance = manifest["dataset"]["provenance"]
        if stage == "production":
            if provenance.get("kind") == "synthetic":
                raise ApprovalError("a model trained on synthetic data cannot go to production")
            if provenance.get("contains_real_data") is not True:
                raise ApprovalError(
                    "the data owner has not declared the dataset (contains_real_data); "
                    "production approval needs a declared dataset"
                )
        self._append(
            {
                "action": "approved",
                "task": task,
                "version": version,
                "stage": stage,
                "by": by,
                "reason": reason,
            }
        )
        return self.get(task, version)

    def retire(self, task: str, version: str, *, by: str, reason: str) -> Version:
        _require_person(by, reason)
        v = self.get(task, version)
        if v.status == "retired":
            raise RegistryError(f"{task} {version} is already retired")
        self._append(
            {"action": "retired", "task": task, "version": version, "by": by, "reason": reason}
        )
        return self.get(task, version)

    def rollback(self, task: str, stage: str, *, by: str, reason: str) -> Version | None:
        """Retire the active version of the stage; returns the version now active."""
        current = self.active(task, stage)
        if current is None:
            raise RegistryError(f"no active {task} model for {stage}")
        self.retire(task, current.version, by=by, reason=f"rollback: {reason}")
        return self.active(task, stage)

    # --- integrity ------------------------------------------------------------

    def verify(self) -> int:
        """Check the hash chain and every stored artifact. Returns the event count."""
        prev = _GENESIS
        events = self._events()
        for i, e in enumerate(events):
            body = {k: v for k, v in e.items() if k != "hash"}
            if e.get("prev") != prev or hashlib.sha256(_canonical(body)).hexdigest() != e["hash"]:
                raise TamperedError(f"event #{i + 1} of the log was changed or reordered")
            prev = e["hash"]
        for v in self.state().values():
            self._verified_manifest(v)
        return len(events)

    def _verified_manifest(self, v: Version) -> dict:
        folder = self.path(v.task, v.version)
        manifest = json.loads((folder / "manifest.json").read_text(encoding="utf-8"))
        artifact = folder / manifest["artifact"]["file"]
        if not artifact.exists() or _sha256(artifact) != v.sha256:
            raise TamperedError(f"{v.task} {v.version}: the stored artifact was changed")
        return manifest


def _check_stage(stage: str) -> None:
    if stage not in STAGES:
        raise RegistryError(f"stage must be one of {', '.join(STAGES)}")


def _require_person(by: str, reason: str) -> None:
    if not by.strip() or not reason.strip():
        raise ApprovalError("name who decides (--by) and why (--reason)")
