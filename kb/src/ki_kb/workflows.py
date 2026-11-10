"""Temporal workflows. Visible in the Temporal Web UI, one run per tick.

SyncProjects is driven by a schedule rather than push webhooks: the cluster is
reachable only over the VPN, so GitHub cannot call in. A project whose
remote head has not moved since its last complete sync costs one ls-remote and
no clone; otherwise only documents whose hash changed are re-extracted.
"""

from __future__ import annotations

import asyncio
from datetime import timedelta

from temporalio import workflow
from temporalio.common import RetryPolicy

with workflow.unsafe.imports_passed_through():
    from .activities import (
        MAX_ATTEMPTS,
        ProjectRef,
        SyncResult,
        load_projects,
        sync_repo,
        sync_timeout,
    )

_RETRY = RetryPolicy(initial_interval=timedelta(seconds=30), maximum_attempts=MAX_ATTEMPTS)
# Marks runs that sync stage by stage. A run started by an older worker has no
# marker and replays as it ran, every project at once, instead of failing
# replay and holding up the schedule (overlap SKIP) behind a stuck run.
_STAGED = "sync-stages"
# Marks SyncOneProject runs that look the timeout up; older runs replay without.
_ONE_TIMEOUT = "sync-one-timeout"


async def _sync(ref: ProjectRef) -> SyncResult:
    return await workflow.execute_activity(
        sync_repo,
        ref,
        start_to_close_timeout=timedelta(seconds=ref.timeout_seconds),
        heartbeat_timeout=timedelta(minutes=5),
        retry_policy=_RETRY,
    )


@workflow.defn
class SyncProjects:
    """Sync every project that has ingest rules. Run by the schedule.

    Stage by stage: the vault, then repo docs, then `bulk: true` projects, so a
    bulk project never delays an edited note. Projects within a stage run side by side.
    """

    @workflow.run
    async def run(self) -> list[SyncResult]:
        refs = await workflow.execute_activity(
            load_projects, start_to_close_timeout=timedelta(seconds=30), retry_policy=_RETRY
        )
        if not workflow.patched(_STAGED):
            return list(await asyncio.gather(*(_sync(r) for r in refs)))
        results: list[SyncResult] = []
        for stage in sorted({r.stage for r in refs}):
            results += await asyncio.gather(*(_sync(r) for r in refs if r.stage == stage))
        return results


@workflow.defn
class SyncOneProject:
    """Sync a single project on demand, e.g. right after merging a decision record.

    Takes only the project name: ingest rules and credentials come from the
    projects file inside the activity. The timeout is looked up the same way, so a
    bulk project on demand gets the bulk limit and any other the default.
    """

    @workflow.run
    async def run(self, name: str) -> SyncResult:
        if not workflow.patched(_ONE_TIMEOUT):
            return await _sync(ProjectRef(name=name))
        seconds = await workflow.execute_activity(
            sync_timeout, name, start_to_close_timeout=timedelta(seconds=30), retry_policy=_RETRY
        )
        return await _sync(ProjectRef(name=name, timeout_seconds=seconds))
