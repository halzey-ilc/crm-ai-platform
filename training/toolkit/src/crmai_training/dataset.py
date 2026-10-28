"""Tabular datasets: loading, content-addressed versioning and quality checks."""

from __future__ import annotations

import csv
import hashlib
import io
from collections import Counter
from dataclasses import dataclass
from datetime import datetime
from pathlib import Path

SCHEMA_VERSION = "lead_scoring.dataset.v1"


@dataclass(frozen=True)
class Dataset:
    columns: tuple[str, ...]
    rows: tuple[dict[str, str], ...]

    def column(self, name: str) -> list[str]:
        return [r[name] for r in self.rows]


def to_csv_bytes(ds: Dataset) -> bytes:
    """Canonical serialisation: fixed column order, LF line endings, UTF-8."""
    buf = io.StringIO()
    w = csv.DictWriter(buf, fieldnames=list(ds.columns), lineterminator="\n")
    w.writeheader()
    w.writerows(ds.rows)
    return buf.getvalue().encode("utf-8")


def from_csv_bytes(data: bytes) -> Dataset:
    reader = csv.DictReader(io.StringIO(data.decode("utf-8")))
    if not reader.fieldnames:
        raise ValueError("dataset has no header")
    return Dataset(columns=tuple(reader.fieldnames), rows=tuple(reader))


def load_csv(path: str | Path) -> tuple[Dataset, bytes]:
    data = Path(path).read_bytes()
    return from_csv_bytes(data), data


def version(data: bytes) -> dict[str, str]:
    """Content address of a dataset: identical bytes give an identical version."""
    digest = hashlib.sha256(data).hexdigest()
    return {"sha256": digest, "version": digest[:16]}


class DataQualityError(ValueError):
    """The dataset fails a blocking quality rule."""


def quality_report(
    ds: Dataset,
    *,
    label: str,
    time_column: str,
    id_column: str,
    required: list[str],
    forbidden: list[str],
) -> dict:
    """Measure completeness, duplicates, timestamps, labels and leakage risks.

    Raises DataQualityError for blocking problems; returns the metrics
    otherwise. The report contains counts only, never row values.
    """
    problems: list[str] = []
    missing_cols = [c for c in [*required, label, time_column, id_column] if c not in ds.columns]
    if missing_cols:
        raise DataQualityError(f"missing columns: {missing_cols}")
    present_forbidden = sorted(set(forbidden) & set(ds.columns))
    if present_forbidden:
        problems.append(f"columns that leak the outcome are present: {present_forbidden}")

    n = len(ds.rows)
    missing = {c: sum(1 for r in ds.rows if r[c] in ("", None)) for c in ds.columns}
    ids = Counter(ds.column(id_column))
    duplicates = sum(c - 1 for c in ids.values() if c > 1)
    if duplicates:
        problems.append(f"{duplicates} duplicate {id_column} values")

    bad_ts = 0
    stamps = []
    for v in ds.column(time_column):
        try:
            ts = datetime.fromisoformat(v)
            if ts.tzinfo is None:
                bad_ts += 1
            stamps.append(ts)
        except ValueError:
            bad_ts += 1
    if bad_ts:
        problems.append(f"{bad_ts} timestamps are unparseable or lack a timezone")

    labels = Counter(ds.column(label))
    if set(labels) - {"0", "1"}:
        problems.append("label must be 0/1")
    positives = labels.get("1", 0)

    if problems:
        raise DataQualityError("; ".join(problems))
    return {
        "rows": n,
        "columns": list(ds.columns),
        "missing_per_column": missing,
        "duplicate_ids": duplicates,
        "time_range": [min(stamps).isoformat(), max(stamps).isoformat()] if stamps else None,
        "positives": positives,
        "negatives": labels.get("0", 0),
        "positive_rate": positives / n if n else 0.0,
    }
