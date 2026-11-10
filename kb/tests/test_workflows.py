"""Workflows run every project with ingest rules, and one project on demand.

Needs the Temporal dev server, which the SDK downloads on first use. Skipped
when that download is not possible (e.g. offline sandboxes); runs in CI.
"""

import asyncio
import uuid

import pytest
from temporalio import activity
from temporalio.testing import WorkflowEnvironment
from temporalio.worker import Worker

from ki_kb.activities import (
    DEFAULT_SYNC_TIMEOUT_SECONDS,
    STAGE_BULK,
    STAGE_DOCS,
    STAGE_VAULT,
    ProjectRef,
    SyncResult,
)
from ki_kb.workflows import SyncOneProject, SyncProjects

QUEUE = "ki-ingest"
REFS = [ProjectRef(name="acme"), ProjectRef(name="payments")]


@activity.defn(name="load_projects")
async def fake_load_projects() -> list[ProjectRef]:
    return REFS


@activity.defn(name="sync_timeout")
async def fake_sync_timeout(name: str) -> int:
    return 240 * 60 if name == "archive" else 45 * 60


@activity.defn(name="sync_repo")
async def fake_sync(ref: ProjectRef) -> SyncResult:
    return SyncResult(project=ref.name, added=1)


async def _env() -> WorkflowEnvironment:
    try:
        return await WorkflowEnvironment.start_local()
    except RuntimeError as e:
        pytest.skip(f"Temporal dev server unavailable: {e}")


@pytest.mark.asyncio
async def test_sync_projects_runs_every_project():
    async with await _env() as env:
        worker = Worker(
            env.client,
            task_queue=QUEUE,
            workflows=[SyncProjects],
            activities=[fake_load_projects, fake_sync],
        )
        async with worker:
            results = await env.client.execute_workflow(
                SyncProjects.run, id=f"test-{uuid.uuid4()}", task_queue=QUEUE
            )
    assert sorted(r.project for r in results) == ["acme", "payments"]


@pytest.mark.asyncio
async def test_sync_one_project_by_name():
    async with await _env() as env:
        worker = Worker(
            env.client,
            task_queue=QUEUE,
            workflows=[SyncOneProject],
            activities=[fake_sync, fake_sync_timeout],
        )
        async with worker:
            result = await env.client.execute_workflow(
                SyncOneProject.run, "payments", id=f"test-{uuid.uuid4()}", task_queue=QUEUE
            )
    assert result.project == "payments" and result.added == 1


@pytest.mark.asyncio
async def test_sync_projects_runs_stage_by_stage():
    events: list[str] = []
    refs = [
        ProjectRef(name="vault", stage=STAGE_VAULT),
        ProjectRef(name="docs-a", stage=STAGE_DOCS),
        ProjectRef(name="docs-b", stage=STAGE_DOCS),
        ProjectRef(name="archive", stage=STAGE_BULK),
    ]
    both_docs_started = asyncio.Event()

    @activity.defn(name="load_projects")
    async def load_staged() -> list[ProjectRef]:
        return refs

    @activity.defn(name="sync_repo")
    async def sync_staged(ref: ProjectRef) -> SyncResult:
        events.append(f"start {ref.name}")
        if ref.stage == STAGE_DOCS:
            # Both repo-docs projects must be running at once to get past here.
            if sum(e.startswith("start docs") for e in events) == 2:
                both_docs_started.set()
            await asyncio.wait_for(both_docs_started.wait(), timeout=10)
        events.append(f"end {ref.name}")
        return SyncResult(project=ref.name, added=1)

    async with await _env() as env:
        worker = Worker(
            env.client,
            task_queue=QUEUE,
            workflows=[SyncProjects],
            activities=[load_staged, sync_staged],
        )
        async with worker:
            results = await env.client.execute_workflow(
                SyncProjects.run, id=f"test-{uuid.uuid4()}", task_queue=QUEUE
            )
    assert [r.project for r in results] == ["vault", "docs-a", "docs-b", "archive"]
    assert events[:2] == ["start vault", "end vault"]
    assert sorted(events[2:6]) == ["end docs-a", "end docs-b", "start docs-a", "start docs-b"]
    assert events[6:] == ["start archive", "end archive"]


async def _scheduled_timeouts(env: WorkflowEnvironment, workflow_id: str) -> dict[str, float]:
    """StartToClose seconds of each sync_repo activity the run scheduled, by project."""
    out: dict[str, float] = {}
    history = await env.client.get_workflow_handle(workflow_id).fetch_history()
    for event in history.events:
        attrs = event.activity_task_scheduled_event_attributes
        if attrs.activity_type.name != "sync_repo":
            continue
        ref = await env.client.data_converter.decode(attrs.input.payloads, [ProjectRef])
        out[ref[0].name] = attrs.start_to_close_timeout.ToTimedelta().total_seconds()
        assert attrs.heartbeat_timeout.ToTimedelta().total_seconds() == 5 * 60
    return out


@pytest.mark.asyncio
async def test_each_ref_gets_its_own_start_to_close():
    refs = [
        ProjectRef(name="notes"),
        ProjectRef(name="archive", stage=STAGE_BULK, timeout_seconds=240 * 60),
    ]

    @activity.defn(name="load_projects")
    async def load_two() -> list[ProjectRef]:
        return refs

    wid = f"test-{uuid.uuid4()}"
    async with await _env() as env:
        worker = Worker(
            env.client,
            task_queue=QUEUE,
            workflows=[SyncProjects],
            activities=[load_two, fake_sync],
        )
        async with worker:
            await env.client.execute_workflow(SyncProjects.run, id=wid, task_queue=QUEUE)
        scheduled = await _scheduled_timeouts(env, wid)
    # The test environment has no time skipping for a 45-minute activity that
    # heartbeats, so the scheduled StartToClose is what is asserted.
    assert scheduled == {"notes": 45 * 60, "archive": 240 * 60}


def test_a_ref_without_a_timeout_keeps_forty_five_minutes():
    assert ProjectRef(name="old").timeout_seconds == 45 * 60 == DEFAULT_SYNC_TIMEOUT_SECONDS


@pytest.mark.asyncio
async def test_sync_one_project_uses_the_looked_up_timeout():
    wid = f"test-{uuid.uuid4()}"
    async with await _env() as env:
        worker = Worker(
            env.client,
            task_queue=QUEUE,
            workflows=[SyncOneProject],
            activities=[fake_sync, fake_sync_timeout],
        )
        async with worker:
            await env.client.execute_workflow(
                SyncOneProject.run, "archive", id=wid, task_queue=QUEUE
            )
        scheduled = await _scheduled_timeouts(env, wid)
    assert scheduled == {"archive": 240 * 60}
