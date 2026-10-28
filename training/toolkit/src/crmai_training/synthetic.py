"""Synthetic lead data for pipeline tests.

The generator mimics the shape of the CRM's leads (sources match
modules/leads in the CRM) with a known ground-truth model, so the pipeline
can be checked end to end without any real customer data. Metrics on this
data say nothing about real conversion behaviour.
"""

from __future__ import annotations

from datetime import UTC, datetime, timedelta

import numpy as np

from .dataset import Dataset

SOURCES = ("website", "referral", "whatsapp", "telegram", "instagram", "phone", "other")
_SOURCE_EFFECT = {
    "website": 0.0,
    "referral": 1.0,
    "whatsapp": 0.3,
    "telegram": 0.2,
    "instagram": -0.2,
    "phone": 0.5,
    "other": -0.5,
}
COLUMNS = (
    "lead_id",
    "created_at",
    "source",
    "has_email",
    "has_phone",
    "first_response_minutes",
    "activities_7d",
    "meetings_7d",
    "converted",
)


def generate(rows: int, seed: int) -> Dataset:
    rng = np.random.default_rng(seed)
    start = datetime(2025, 1, 1, tzinfo=UTC)
    # Leads arrive over a year, in time order.
    offsets = np.sort(rng.uniform(0, 365 * 24 * 3600, size=rows))
    out = []
    for i in range(rows):
        source = SOURCES[int(rng.integers(len(SOURCES)))]
        has_email = int(rng.random() < 0.7)
        has_phone = int(rng.random() < 0.85)
        response = float(np.round(rng.lognormal(mean=3.0, sigma=1.0), 1))
        activities = int(rng.poisson(2.0))
        meetings = int(rng.poisson(0.3))
        logit = (
            -2.2
            + _SOURCE_EFFECT[source]
            + 0.4 * has_email
            + 0.3 * has_phone
            - 0.01 * min(response, 300)
            + 0.25 * activities
            + 0.9 * meetings
        )
        p = 1 / (1 + np.exp(-logit))
        out.append(
            {
                "lead_id": f"syn-{seed}-{i:06d}",
                "created_at": (start + timedelta(seconds=float(offsets[i]))).isoformat(
                    timespec="seconds"
                ),
                "source": source,
                "has_email": str(has_email),
                "has_phone": str(has_phone),
                "first_response_minutes": f"{response:.1f}",
                "activities_7d": str(activities),
                "meetings_7d": str(meetings),
                "converted": str(int(rng.random() < p)),
            }
        )
    return Dataset(columns=COLUMNS, rows=tuple(out))
