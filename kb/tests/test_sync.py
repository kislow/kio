"""The ingest activity end to end, with git, FalkorDB and Graphiti replaced by fakes."""

import asyncio
import hashlib
import json
import shutil
from dataclasses import dataclass, field
from datetime import UTC, datetime, timedelta
from pathlib import Path
from types import SimpleNamespace
from typing import ClassVar

import httpx
import openai
import pytest
from temporalio.exceptions import ApplicationError

from ki_kb import activities, balance, metrics, plan
from ki_kb.errors import GitError, GitTimeoutError
from ki_kb.settings import Settings

PROJECTS = """
sensitive_brains: [client]
identities:
  personal: {github_user: alice, email: me@example.com, credential: personal, ingest: true}
projects:
  - {name: notes, repo: https://github.com/alice/notes, identity: personal, brains: [voice],
     ingest: [{path: docs/, brain: voice}]}
  - {name: payments, repo: https://github.com/alice/payments, identity: personal, brains: [client],
     ingest: [{path: docs/, brain: client}]}
  - {name: mixed, repo: https://github.com/alice/mixed, identity: personal, brains: [voice],
     ingest: [{path: docs/, brain: voice}, {path: src.md, brain: cortex}]}
"""


class FakeStore:
    data: ClassVar[dict[str, dict[str, plan.Recorded]]] = {}
    markers: ClassVar[dict[str, str]] = {}
    failed: ClassVar[dict[str, dict[str, plan.Failed]]] = {}
    backoffs: ClassVar[dict[str, plan.Backoff]] = {}

    def __init__(self, settings):
        pass

    async def all(self, repo):
        return dict(self.data.get(repo, {}))

    async def put(self, repo, path, rec):
        self.data.setdefault(repo, {})[path] = rec

    async def delete(self, repo, path):
        self.data.get(repo, {}).pop(path, None)

    async def synced(self, repo):
        return self.markers.get(repo)

    async def mark_synced(self, repo, marker):
        self.markers[repo] = marker

    async def failures(self, repo):
        return dict(self.failed.get(repo, {}))

    async def put_failure(self, repo, path, failed):
        self.failed.setdefault(repo, {})[path] = failed

    async def drop_failures(self, repo, paths):
        for path in paths:
            self.failed.get(repo, {}).pop(path, None)

    async def backoff(self, repo):
        return self.backoffs.get(repo)

    async def put_backoff(self, repo, backoff):
        self.backoffs[repo] = backoff

    async def clear_backoff(self, repo):
        self.backoffs.pop(repo, None)

    async def close(self):
        pass


@dataclass
class FakeGraphiti:
    graph: str
    added: list[str] = field(default_factory=list)
    removed: list[str] = field(default_factory=list)
    fail_with: Exception | None = None
    # Only these episode names fail; empty means all of them.
    fail_names: set[str] = field(default_factory=set)
    tried: list[str] = field(default_factory=list)
    fail_bulk: bool = False
    fail_bulk_with: list[Exception] = field(default_factory=list)
    drop_from_bulk: str | None = None
    bulks: list[list[str]] = field(default_factory=list)
    orphans: list[str] = field(default_factory=list)
    started: asyncio.Event = field(default_factory=asyncio.Event)
    wait_for: asyncio.Event | None = None

    async def add_episode(self, name, episode_body, **kw):
        self.started.set()
        self.tried.append(name)
        if self.wait_for:
            await asyncio.wait_for(self.wait_for.wait(), timeout=2)
        if self.fail_with and (not self.fail_names or name in self.fail_names):
            raise self.fail_with
        self.added.append(name)
        return SimpleNamespace(episode=SimpleNamespace(uuid=f"ep-{name}-{len(self.added)}"))

    async def add_episode_bulk(self, episodes, group_id, **kw):
        self.bulks.append([e.name for e in episodes])
        if self.fail_bulk_with:
            raise self.fail_bulk_with.pop(0)
        if self.fail_bulk:
            # Like Graphiti: the episodes are saved before extraction fails.
            self.orphans.extend(f"orphan-{e.name}" for e in episodes)
            raise RuntimeError("bulk failed")
        self.added.extend(e.name for e in episodes)
        kept = [e for e in episodes if e.name != self.drop_from_bulk]
        nodes = [SimpleNamespace(name=e.name, uuid=f"bulk-{e.name}") for e in kept]
        return SimpleNamespace(episodes=nodes)

    async def remove_episode(self, uuid):
        self.removed.append(uuid)
        if uuid in self.orphans:  # gone from the graph, as with Graphiti
            self.orphans.remove(uuid)

    async def close(self):
        pass


@pytest.fixture
def world(tmp_path, monkeypatch):
    """A repo on disk, a projects file, env for the main instance, and fakes for I/O."""
    repo = tmp_path / "origin"
    (repo / "docs").mkdir(parents=True)
    (repo / "docs" / "a.md").write_text("# A\nalpha\n")
    (repo / "docs" / "b.md").write_text("# B\nbeta\n")
    (repo / "src.md").write_text("not under docs/\n")
    projects_file = tmp_path / "projects.yaml"
    projects_file.write_text(PROJECTS)
    tokens = tmp_path / "tokens"
    tokens.mkdir()
    (tokens / "personal").write_text("tok\n")

    env = {
        "FALKORDB_HOST": "falkordb.invalid",
        "KI_ALLOWED_GRAPHS": "cortex,brain-voice",
        "KI_LLM_BASE_URL": "http://llm.invalid/v1",
        "KI_LLM_MODEL": "m",
        "KI_EMBEDDING_BASE_URL": "http://embed.invalid/v1",
        "KI_EMBEDDING_MODEL": "e",
        "KI_EMBEDDING_DIM": "8",
        "KI_TASK_QUEUE": "ki-ingest",
        "KI_PROJECTS_FILE": str(projects_file),
        "KI_GIT_TOKENS_DIR": str(tokens),
        # These tests follow single documents; batching has its own tests.
        "KI_INGEST_BATCH": "1",
    }
    for k, v in env.items():
        monkeypatch.setenv(k, v)

    recent: list[str] = []
    calls: list[str] = []
    remote = SimpleNamespace(readable=True)

    def head():
        """The remote head moves whenever the repo's content does."""
        digest = hashlib.sha256()
        for p in sorted(repo.rglob("*")):
            if p.is_file():
                digest.update(p.relative_to(repo).as_posix().encode() + p.read_bytes())
        return digest.hexdigest()

    async def fake_git(*args, token=None, cwd=None, limit=0):
        calls.append(args[0])
        if args[0] == "ls-remote":
            if not remote.readable:
                raise GitError("git ls-remote failed with exit code 128")
            return f"{head()}\trefs/heads/main"
        if args[0] == "clone":
            shutil.copytree(repo, args[-1])
            return ""
        if args[0] == "log" and "--name-only" in args:
            return "\n".join(recent)
        return {"rev-parse": head(), "log": "2026-10-17T10:00:00+00:00"}[args[0]]

    graphitis: dict[str, FakeGraphiti] = {}

    def fake_build(settings, graph):
        settings.check_graph(graph)
        return graphitis.setdefault(graph, FakeGraphiti(graph))

    FakeStore.data = {}
    FakeStore.markers = {}
    FakeStore.failed = {}
    FakeStore.backoffs = {}
    monkeypatch.setattr(activities, "_git", fake_git)
    monkeypatch.setattr(activities, "StateStore", FakeStore)
    monkeypatch.setattr(activities, "build_graphiti", fake_build)

    async def fake_episodes_named(gi, graph, names):
        return set(gi.orphans)

    monkeypatch.setattr(activities, "_episodes_named", fake_episodes_named)
    monkeypatch.setattr(activities.activity, "heartbeat", lambda *a: None)
    return SimpleNamespace(
        repo=repo,
        graphitis=graphitis,
        store=FakeStore,
        recent=recent,
        calls=calls,
        remote=remote,
        head=head,
    )


async def test_add_replace_remove(world):
    ref = activities.ProjectRef(name="notes")

    first = await activities.sync_repo(ref)
    assert (first.added, first.commit, first.unchanged) == (2, world.head(), 0)
    assert sorted(world.store.data["notes"]) == ["docs/a.md#A", "docs/b.md#B"]

    world.store.markers.clear()
    again = await activities.sync_repo(ref)
    assert (again.added, again.unchanged, again.skipped) == (0, 2, False)

    (world.repo / "docs" / "a.md").write_text("# A\nalpha, revised\n")
    (world.repo / "docs" / "b.md").unlink()
    third = await activities.sync_repo(ref)
    assert (third.replaced, third.removed, third.unchanged) == (1, 1, 0)
    assert len(world.graphitis["brain-voice"].removed) == 2
    assert sorted(world.store.data["notes"]) == ["docs/a.md#A"]


async def test_recently_committed_docs_are_deferred_until_quiet(world):
    ref = activities.ProjectRef(name="notes")
    world.recent.append("docs/a.md")

    first = await activities.sync_repo(ref)
    assert (first.added, first.deferred, first.deferred_paths) == (1, 1, ["docs/a.md"])
    assert first.deferred_reason == "quiet"
    assert sorted(world.store.data["notes"]) == ["docs/b.md#B"]

    world.recent.clear()
    second = await activities.sync_repo(ref)
    assert (second.added, second.deferred, second.unchanged) == (1, 0, 1)

    (world.repo / "docs" / "a.md").write_text("# A\nalpha, revised\n")
    world.recent.append("docs/a.md")
    third = await activities.sync_repo(ref)
    assert (third.replaced, third.deferred, third.unchanged) == (0, 1, 1)
    assert len(world.graphitis["brain-voice"].removed) == 0


async def test_unmoved_remote_head_skips_the_clone(world):
    ref = activities.ProjectRef(name="notes")
    await activities.sync_repo(ref)
    world.calls.clear()

    again = await activities.sync_repo(ref)
    assert (again.skipped, again.commit, again.added, again.unchanged) == (True, world.head(), 0, 0)
    assert world.calls == ["ls-remote"]

    (world.repo / "docs" / "c.md").write_text("# C\ngamma\n")
    moved = await activities.sync_repo(ref)
    assert (moved.skipped, moved.added, moved.unchanged) == (False, 1, 2)


async def test_new_version_resyncs_an_unmoved_head(world, monkeypatch):
    ref = activities.ProjectRef(name="notes")
    await activities.sync_repo(ref)
    monkeypatch.setattr(activities, "version", lambda _: "99.0.0")
    again = await activities.sync_repo(ref)
    assert (again.skipped, again.unchanged) == (False, 2)


async def test_deferred_documents_keep_the_next_run_from_skipping(world):
    ref = activities.ProjectRef(name="notes")
    world.recent.append("docs/a.md")
    await activities.sync_repo(ref)
    assert "notes" not in world.store.markers
    world.recent.clear()
    assert (await activities.sync_repo(ref)).added == 1
    assert "notes" in world.store.markers


async def test_unreadable_remote_head_falls_back_to_a_clone(world):
    ref = activities.ProjectRef(name="notes")
    await activities.sync_repo(ref)
    world.remote.readable = False
    again = await activities.sync_repo(ref)
    assert (again.skipped, again.unchanged) == (False, 2)
    assert "clone" in world.calls


@pytest.fixture
def batch_of_two(world, monkeypatch):
    monkeypatch.setenv("KI_INGEST_BATCH", "2")
    (world.repo / "docs" / "c.md").write_text("# C\ngamma\n")
    return world


async def test_batches_go_through_bulk_and_record_each_episode(batch_of_two):
    world = batch_of_two
    result = await activities.sync_repo(activities.ProjectRef(name="notes"))
    voice = world.graphitis["brain-voice"]
    assert voice.bulks == [["notes/docs/a.md#A", "notes/docs/b.md#B"]]
    assert voice.added == ["notes/docs/a.md#A", "notes/docs/b.md#B", "notes/docs/c.md#C"]
    assert result.added == 3
    assert world.store.data["notes"]["docs/a.md#A"].episode == "bulk-notes/docs/a.md#A"
    assert world.store.data["notes"]["docs/c.md#C"].episode.startswith("ep-")


async def test_failed_bulk_is_retried_one_document_at_a_time(batch_of_two):
    world = batch_of_two
    world.graphitis["brain-voice"] = FakeGraphiti("brain-voice", fail_bulk=True)
    result = await activities.sync_repo(activities.ProjectRef(name="notes"))
    assert result.added == 3
    voice = world.graphitis["brain-voice"]
    assert sorted(voice.removed) == ["orphan-notes/docs/a.md#A", "orphan-notes/docs/b.md#B"]
    assert sorted(voice.added) == [
        "notes/docs/a.md#A",
        "notes/docs/b.md#B",
        "notes/docs/c.md#C",
    ]


async def test_failed_bulk_is_split_and_the_bad_document_isolated(world, monkeypatch):
    monkeypatch.setenv("KI_INGEST_BATCH", "16")
    monkeypatch.setenv("KI_INGEST_QUIET_MINUTES", "0")
    for i in range(14):
        (world.repo / "docs" / f"n{i:02}.md").write_text(f"# n{i}\nbody {i}\n")
    bad = "notes/docs/n07.md#n7"
    voice = world.graphitis["brain-voice"] = FakeGraphiti("brain-voice")
    real_bulk, real_one = voice.add_episode_bulk, voice.add_episode
    paid = 0

    async def bulk(episodes, group_id, **kw):
        nonlocal paid
        paid += len(episodes)
        if bad in [e.name for e in episodes]:
            raise RuntimeError("bad document")
        return await real_bulk(episodes, group_id, **kw)

    async def one(name, episode_body, **kw):
        nonlocal paid
        paid += 1
        if name == bad:
            raise RuntimeError("bad document")
        return await real_one(name, episode_body, **kw)

    voice.add_episode_bulk, voice.add_episode = bulk, one
    result = await activities.sync_repo(activities.ProjectRef(name="notes"))
    assert (result.added, result.failed, result.failed_paths) == (15, 1, ["docs/n07.md"])
    assert paid <= 16 + 4 * 8
    assert paid < 16 + 16 + 16


async def test_outage_mid_batch_makes_no_further_calls_and_is_not_retried(batch_of_two):
    world = batch_of_two
    voice = world.graphitis["brain-voice"] = FakeGraphiti(
        "brain-voice", fail_bulk_with=[ConnectionError("down")]
    )
    with pytest.raises(ApplicationError) as raised:
        await activities.sync_repo(activities.ProjectRef(name="notes"))
    assert raised.value.non_retryable
    assert len(voice.bulks) == 1
    assert voice.tried == []
    assert world.store.backoffs["notes"].failures == 1


async def test_a_failed_document_is_not_a_failed_run(world):
    world.graphitis["brain-voice"] = FakeGraphiti("brain-voice", fail_with=bad_answer())
    await activities.sync_repo(activities.ProjectRef(name="notes"))
    assert world.store.backoffs == {}


async def test_retryable_failure_counts_only_on_the_last_attempt(world, monkeypatch):
    async def boom(*args, **kw):
        # Since #281 a GitError skips the project; a generic error still takes the retry path.
        raise RuntimeError("boom")

    monkeypatch.setattr(activities, "_git", boom)
    attempt = SimpleNamespace(attempt=1)
    monkeypatch.setattr(activities.activity, "info", lambda: attempt)
    ref = activities.ProjectRef(name="notes")
    for attempt.attempt in range(1, activities.MAX_ATTEMPTS + 1):
        with pytest.raises(ApplicationError) as raised:
            await activities.sync_repo(ref)
        assert not raised.value.non_retryable
    assert world.store.backoffs["notes"].failures == 1


async def test_two_failed_runs_defer_the_project_and_a_success_clears_it(world, monkeypatch):
    ref = activities.ProjectRef(name="notes")
    voice = world.graphitis["brain-voice"] = FakeGraphiti(
        "brain-voice", fail_bulk_with=[ConnectionError("down"), ConnectionError("down")]
    )
    monkeypatch.setenv("KI_INGEST_BATCH", "2")
    for _ in range(2):
        with pytest.raises(ApplicationError):
            await activities.sync_repo(ref)
    assert world.store.backoffs["notes"].failures == 2
    calls = len(world.calls)

    deferred = await activities.sync_repo(ref)
    assert (deferred.deferred_reason, deferred.added, deferred.skipped) == ("backoff", 0, False)
    assert len(world.calls) == calls
    assert len(voice.bulks) == 2

    world.store.backoffs["notes"] = plan.Backoff(2, "2000-01-01T00:00:00+00:00")
    result = await activities.sync_repo(ref)
    assert result.added == 2
    assert world.store.backoffs == {}


def test_backoff_doubles_up_to_a_day():
    assert activities.backoff_delay(2) == activities.BACKOFF_BASE
    assert activities.backoff_delay(3) == 2 * activities.BACKOFF_BASE
    assert activities.backoff_delay(40) == activities.BACKOFF_MAX


async def test_a_run_near_its_timeout_defers_the_rest_as_time(batch_of_two, monkeypatch):
    world = batch_of_two
    monkeypatch.setenv("KI_INGEST_BATCH", "1")
    clock = SimpleNamespace(now=0.0)
    monkeypatch.setattr(activities.time, "monotonic", lambda: clock.now)
    monkeypatch.setattr(activities, "_attempt_deadline", lambda: 25.0)
    voice = world.graphitis["brain-voice"] = FakeGraphiti("brain-voice")
    real = FakeGraphiti.add_episode

    async def slow(self, *args, **kwargs):
        clock.now += 10
        return await real(self, *args, **kwargs)

    monkeypatch.setattr(FakeGraphiti, "add_episode", slow)
    result = await activities.sync_repo(activities.ProjectRef(name="notes"))
    # 10s per batch: batches start at 0 and 10, and at 20 only 5s are left.
    assert (result.added, result.deferred, result.deferred_reason) == (2, 1, "time")
    assert result.deferred_paths == ["docs/c.md"]
    assert len(voice.added) == 2
    assert "notes" not in world.store.markers
    assert world.store.backoffs == {}


def timed_batches(world, monkeypatch, durations, deadline, margin=0.0):
    """One document per batch on a fake clock; batch i takes durations[i] seconds."""
    monkeypatch.setenv("KI_INGEST_BATCH", "1")
    for name in "cde":
        (world.repo / "docs" / f"{name}.md").write_text(f"# {name.upper()}\n{name}\n")
    clock = SimpleNamespace(now=0.0)
    monkeypatch.setattr(activities.time, "monotonic", lambda: clock.now)
    monkeypatch.setattr(activities, "_attempt_deadline", lambda: deadline)
    monkeypatch.setattr(activities, "_attempt_margin", lambda: margin)
    seconds = iter(durations)
    real = FakeGraphiti.add_episode

    async def slow(self, *args, **kwargs):
        clock.now += next(seconds)
        return await real(self, *args, **kwargs)

    monkeypatch.setattr(FakeGraphiti, "add_episode", slow)


async def test_the_guard_is_sized_on_the_slowest_batch_so_far(world, monkeypatch):
    timed_batches(world, monkeypatch, [10, 30, 10, 40], deadline=75)
    result = await activities.sync_repo(activities.ProjectRef(name="notes"))
    # After 10s, 30s and 10s the clock is at 50 with 25s left: the last batch took
    # 10s but the slowest took 30s, so the fourth does not start.
    assert (result.added, result.deferred, result.deferred_reason) == (3, 2, "time")


async def test_a_batch_slower_than_any_before_still_ends_the_attempt_cleanly(world, monkeypatch):
    timed_batches(world, monkeypatch, [10, 30, 10, 40], deadline=100)
    result = await activities.sync_repo(activities.ProjectRef(name="notes"))
    # The fourth batch fits (50s left, slowest 30s) and takes 40s; 10s remain
    # against a slowest of 40s, so the fifth does not start.
    assert (result.added, result.deferred, result.deferred_reason) == (4, 1, "time")
    assert result.deferred_paths == ["docs/e.md"]


async def test_the_guard_keeps_a_margin_beyond_the_slowest_batch(world, monkeypatch):
    timed_batches(world, monkeypatch, [10, 10, 10, 10], deadline=34, margin=5)
    result = await activities.sync_repo(activities.ProjectRef(name="notes"))
    # At 20s, 14s are left: enough for another 10s batch, not for 10s plus the 5s margin.
    assert (result.added, result.deferred_reason) == (2, "time")


async def test_a_run_near_its_timeout_defers_the_removals_it_cannot_finish(world, monkeypatch):
    ref = activities.ProjectRef(name="notes")
    (world.repo / "docs" / "c.md").write_text("# C\ncharlie\n")
    await activities.sync_repo(ref)
    for name in "abc":
        (world.repo / "docs" / f"{name}.md").unlink()
    voice = world.graphitis["brain-voice"]
    clock = SimpleNamespace(now=0.0)
    monkeypatch.setattr(activities.time, "monotonic", lambda: clock.now)
    monkeypatch.setattr(activities, "_attempt_deadline", lambda: 15.0)
    real = FakeGraphiti.remove_episode

    async def slow(self, uuid):
        clock.now += 10
        await real(self, uuid)

    monkeypatch.setattr(FakeGraphiti, "remove_episode", slow)
    result = await activities.sync_repo(ref)
    # The first removal starts with 15s left; at 10s only 5s are left for a 10s removal.
    assert (result.removed, result.deferred, result.deferred_reason) == (1, 2, "time")
    assert sorted(result.deferred_paths) == ["docs/b.md", "docs/c.md"]
    assert len(voice.removed) == 1
    assert sorted(world.store.data["notes"]) == ["docs/b.md#B", "docs/c.md#C"]

    monkeypatch.setattr(activities, "_attempt_deadline", lambda: None)
    rest = await activities.sync_repo(ref)
    assert (rest.removed, rest.deferred, rest.deferred_reason) == (2, 0, None)
    assert len(voice.removed) == 3
    assert world.store.data["notes"] == {}


def test_the_margin_is_a_fraction_of_the_timeout(monkeypatch):
    info = SimpleNamespace(start_to_close_timeout=timedelta(minutes=45))
    monkeypatch.setattr(activities.activity, "info", lambda: info)
    assert activities._attempt_margin() == 45 * 60 * activities.TIME_MARGIN_FRACTION


def test_there_is_no_margin_outside_an_activity():
    assert activities._attempt_margin() == 0.0


async def test_a_batch_killed_at_the_timeout_leaves_no_copies_for_the_retry(
    batch_of_two, monkeypatch
):
    """Temporal cancels the attempt mid-batch: Graphiti has saved the episodes but nothing is
    recorded. The retry removes them by name before extracting, so the graph holds one copy."""
    world = batch_of_two
    voice = world.graphitis["brain-voice"] = FakeGraphiti("brain-voice")
    real = voice.add_episode_bulk
    attempt = SimpleNamespace(killed=True)

    async def killable(episodes, group_id, **kw):
        if not attempt.killed:
            return await real(episodes, group_id, **kw)
        voice.orphans.extend(f"saved-{e.name}" for e in episodes)
        await asyncio.sleep(3600)

    monkeypatch.setattr(voice, "add_episode_bulk", killable)
    with pytest.raises(asyncio.TimeoutError):
        await asyncio.wait_for(
            activities.sync_repo(activities.ProjectRef(name="notes")), timeout=0.2
        )
    assert len(voice.orphans) == 2
    assert world.store.data.get("notes", {}) == {}

    attempt.killed = False
    result = await activities.sync_repo(activities.ProjectRef(name="notes"))
    assert result.added == 3
    assert voice.orphans == []
    assert sorted(voice.removed) == ["saved-notes/docs/a.md#A", "saved-notes/docs/b.md#B"]


def rate_limit_error() -> openai.RateLimitError:
    request = httpx.Request("POST", "https://llm.invalid/v1/chat/completions")
    response = httpx.Response(429, request=request)
    return openai.RateLimitError("rate limited", response=response, body=None)


async def test_rate_limited_bulk_retries_then_succeeds_without_singles(
    batch_of_two, monkeypatch, caplog
):
    world = batch_of_two
    voice = world.graphitis["brain-voice"] = FakeGraphiti(
        "brain-voice", fail_bulk_with=[rate_limit_error(), rate_limit_error()]
    )
    sleeps: list[float] = []

    async def instant_sleep(delay):
        sleeps.append(delay)
        await asyncio.sleep(0)

    monkeypatch.setattr(activities, "_sleep_rate_limit", instant_sleep)
    result = await activities.sync_repo(activities.ProjectRef(name="notes"))
    retries = [r for r in caplog.records if r.message == "bulk add rate limited, retrying"]
    assert result.added == 3
    assert voice.bulks[0] == ["notes/docs/a.md#A", "notes/docs/b.md#B"]
    assert len(voice.bulks) == 3
    assert voice.added == ["notes/docs/a.md#A", "notes/docs/b.md#B", "notes/docs/c.md#C"]
    assert len(retries) == 2
    assert [r.attempt for r in retries] == [1, 2]
    assert len(sleeps) == 2
    # Exponential from the 15-second base, plus under a second of jitter.
    assert 15 <= sleeps[0] < 16 and 30 <= sleeps[1] < 31


async def test_exhausted_rate_limit_fails_without_singles(batch_of_two, monkeypatch):
    world = batch_of_two
    voice = world.graphitis["brain-voice"] = FakeGraphiti(
        "brain-voice", fail_bulk_with=[rate_limit_error() for _ in range(4)]
    )
    monkeypatch.setenv("KI_INGEST_RATE_LIMIT_ATTEMPTS", "2")

    async def instant_sleep(_delay):
        await asyncio.sleep(0)

    monkeypatch.setattr(activities, "_sleep_rate_limit", instant_sleep)
    with pytest.raises(ApplicationError, match="RateLimitError"):
        await activities.sync_repo(activities.ProjectRef(name="notes"))
    assert len(voice.bulks) == 3
    assert voice.added == []


async def test_bulk_service_outage_fails_without_singles(batch_of_two):
    world = batch_of_two
    voice = world.graphitis["brain-voice"] = FakeGraphiti(
        "brain-voice", fail_bulk_with=[ConnectionError("provider unavailable")]
    )
    with pytest.raises(ApplicationError, match="ConnectionError"):
        await activities.sync_repo(activities.ProjectRef(name="notes"))
    assert len(voice.bulks) == 1
    assert voice.added == []


async def test_interrupted_singles_keep_each_completed_document_recorded(world, monkeypatch):
    monkeypatch.setenv("KI_INGEST_BATCH", "2")
    monkeypatch.setenv("KI_INGEST_QUIET_MINUTES", "0")
    for name in ("c", "d"):
        (world.repo / "docs" / f"{name}.md").write_text(f"# {name}\n{name}\n")
    voice = world.graphitis["brain-voice"] = FakeGraphiti("brain-voice", fail_bulk=True)
    original_add = voice.add_episode
    calls = 0

    async def interrupt_after_two(name, episode_body, **kw):
        nonlocal calls
        calls += 1
        if calls == 3:
            raise TimeoutError("activity interruption")
        return await original_add(name, episode_body, **kw)

    voice.add_episode = interrupt_after_two
    ref = activities.ProjectRef(name="notes")
    with pytest.raises(ApplicationError, match="TimeoutError"):
        await activities.sync_repo(ref)
    assert sorted(world.store.data["notes"]) == ["docs/a.md#A", "docs/b.md#B"]
    added_before_retry = list(voice.added)
    removed_before_retry = list(voice.removed)

    await activities.sync_repo(ref)
    assert voice.added[len(added_before_retry) :] == ["notes/docs/c.md#c", "notes/docs/d.md#d"]
    assert voice.added.count("notes/docs/a.md#A") == 1
    assert voice.added.count("notes/docs/b.md#B") == 1
    assert not {"orphan-notes/docs/a.md#A", "orphan-notes/docs/b.md#B"} & set(
        voice.removed[len(removed_before_retry) :]
    )


async def test_leftovers_of_a_crashed_attempt_are_removed_before_the_next(batch_of_two):
    """The database died mid-batch: the failed call could not clean up, the retry does."""
    world = batch_of_two
    voice = FakeGraphiti("brain-voice", orphans=["crash-copy-a", "crash-copy-b"])
    world.graphitis["brain-voice"] = voice
    result = await activities.sync_repo(activities.ProjectRef(name="notes"))
    assert result.added == 3
    assert {"crash-copy-a", "crash-copy-b"} <= set(voice.removed)
    assert voice.bulks == [["notes/docs/a.md#A", "notes/docs/b.md#B"]]


async def test_bulk_missing_an_episode_counts_as_failed(batch_of_two):
    world = batch_of_two
    world.graphitis["brain-voice"] = FakeGraphiti("brain-voice", drop_from_bulk="notes/docs/b.md#B")
    result = await activities.sync_repo(activities.ProjectRef(name="notes"))
    assert (result.added, result.failed, result.failed_paths) == (2, 1, ["docs/b.md"])
    assert "docs/b.md#B" not in world.store.data["notes"]
    assert world.store.failed["notes"]["docs/b.md#B"].error == "MissingEpisodeError"


async def test_graphs_are_ingested_side_by_side(world):
    cortex = FakeGraphiti("cortex")
    # The voice lane waits until the cortex lane has started: sequential lanes would time out.
    world.graphitis["brain-voice"] = FakeGraphiti("brain-voice", wait_for=cortex.started)
    world.graphitis["cortex"] = cortex
    result = await activities.sync_repo(activities.ProjectRef(name="mixed"))
    assert result.added == 3
    assert cortex.added == ["mixed/src.md#"]


async def test_an_outage_in_one_lane_cancels_the_others(world):
    world.graphitis["cortex"] = FakeGraphiti("cortex", fail_with=ConnectionError("refused"))
    world.graphitis["brain-voice"] = FakeGraphiti("brain-voice", wait_for=asyncio.Event())
    with pytest.raises(ApplicationError, match=r"ConnectionError at src\.md"):
        await activities.sync_repo(activities.ProjectRef(name="mixed"))
    assert world.graphitis["brain-voice"].added == []
    assert world.store.failed == {}


def bad_answer() -> json.JSONDecodeError:
    return json.JSONDecodeError("Unterminated string", '{"card": "4111 1111 1111 1111', 9)


async def test_failed_document_is_counted_and_the_sync_continues(world):
    world.graphitis["brain-voice"] = FakeGraphiti(
        "brain-voice", fail_with=bad_answer(), fail_names={"notes/docs/a.md#A"}
    )
    result = await activities.sync_repo(activities.ProjectRef(name="notes"))
    assert (result.added, result.failed, result.failed_paths) == (1, 1, ["docs/a.md"])
    assert sorted(world.store.data["notes"]) == ["docs/b.md#B"]
    assert world.store.failed["notes"]["docs/a.md#A"].error == "JSONDecodeError"
    assert "4111" not in repr(result)
    assert "notes" not in world.store.markers


async def test_failed_document_waits_for_new_content_or_version(world, monkeypatch):
    ref = activities.ProjectRef(name="notes")
    voice = world.graphitis["brain-voice"] = FakeGraphiti(
        "brain-voice", fail_with=bad_answer(), fail_names={"notes/docs/a.md#A"}
    )
    await activities.sync_repo(ref)
    voice.tried.clear()

    again = await activities.sync_repo(ref)
    assert (again.skipped, again.failed, again.unchanged, voice.tried) == (False, 1, 1, [])
    assert "notes" not in world.store.markers

    monkeypatch.setattr(activities, "version", lambda _: "99.0.0")
    released = await activities.sync_repo(ref)
    assert (released.failed, voice.tried) == (1, ["notes/docs/a.md#A"])
    assert world.store.failed["notes"]["docs/a.md#A"].version == "99.0.0"

    voice.fail_with = None
    (world.repo / "docs" / "a.md").write_text("# A\nalpha, fixed\n")
    fixed = await activities.sync_repo(ref)
    assert (fixed.added, fixed.failed) == (1, 0)
    assert world.store.failed["notes"] == {}
    assert "notes" in world.store.markers


async def test_failed_replace_keeps_the_old_version(world):
    ref = activities.ProjectRef(name="notes")
    await activities.sync_repo(ref)
    old = world.store.data["notes"]["docs/a.md#A"]
    voice = world.graphitis["brain-voice"]
    voice.fail_with, voice.fail_names = bad_answer(), {"notes/docs/a.md#A"}

    (world.repo / "docs" / "a.md").write_text("# A\nalpha, revised\n")
    result = await activities.sync_repo(ref)
    assert (result.replaced, result.failed, result.unchanged) == (0, 1, 1)
    assert world.store.data["notes"]["docs/a.md#A"] == old
    assert voice.removed == []


async def test_deleting_a_failed_document_drops_its_failure(world):
    ref = activities.ProjectRef(name="notes")
    world.graphitis["brain-voice"] = FakeGraphiti(
        "brain-voice", fail_with=bad_answer(), fail_names={"notes/docs/a.md#A"}
    )
    await activities.sync_repo(ref)
    (world.repo / "docs" / "a.md").unlink()
    result = await activities.sync_repo(ref)
    assert (result.failed, result.unchanged) == (0, 1)
    assert world.store.failed["notes"] == {}
    assert "notes" in world.store.markers


async def test_sensitive_failures_are_counted_without_paths(world, monkeypatch):
    monkeypatch.setenv("KI_ALLOWED_GRAPHS", "brain-client")
    (world.repo / "docs" / "b.md").unlink()
    world.graphitis["brain-client"] = FakeGraphiti("brain-client", fail_with=bad_answer())
    result = await activities.sync_repo(activities.ProjectRef(name="payments"))
    assert (result.failed, result.failed_paths) == (1, [])
    again = await activities.sync_repo(activities.ProjectRef(name="payments"))
    assert (again.failed, again.failed_paths) == (1, [])


def test_outages_are_told_apart_from_document_failures():
    wrapped = RuntimeError("rate limited")
    wrapped.__cause__ = TimeoutError()
    assert activities.is_outage(wrapped)
    assert activities.is_outage(ConnectionRefusedError())
    assert not activities.is_outage(bad_answer())
    assert not activities.is_outage(ValueError("schema echo"))


async def test_recently_committed_reads_git_log(tmp_path, monkeypatch):
    repo = tmp_path / "r"
    repo.mkdir()
    (repo / "docs").mkdir()
    (repo / "docs" / "new.md").write_text("fresh\n")
    env = {
        "GIT_AUTHOR_NAME": "t",
        "GIT_AUTHOR_EMAIL": "t@x",
        "GIT_COMMITTER_NAME": "t",
        "GIT_COMMITTER_EMAIL": "t@x",
    }
    for k, v in env.items():
        monkeypatch.setenv(k, v)
    for args in (("init", "-q"), ("add", "."), ("commit", "-q", "-m", "x")):
        await activities._git(*args, cwd=repo)

    assert await activities._recently_committed(repo, 30) == frozenset({"docs/new.md"})
    assert await activities._recently_committed(repo, 0) == frozenset()


async def test_scanner_rejects_and_reports_path_for_non_sensitive(world):
    (world.repo / "docs" / "b.md").write_text("FALKORDB_PASSWORD=hunter2hunter2\n")
    result = await activities.sync_repo(activities.ProjectRef(name="notes"))
    assert (result.added, result.rejected, result.rejected_paths) == (1, 1, ["docs/b.md"])


FAKE_KEY = "AKIAIOSFODNN7EXAMPLE"


def _write_rejected_doc(world) -> None:
    # Three sections, so the document is rejected three times but reported once.
    (world.repo / "docs" / "b.md").write_text(f"# B\nbeta\n## Keys\nid {FAKE_KEY}\n## More\nmore\n")


def _rejections(project: str, rule: str) -> float:
    return metrics.INGEST_REJECTIONS.labels(project, rule)._value.get()


async def test_rejection_reports_rules_and_lines_without_the_match(world, caplog):
    _write_rejected_doc(world)
    before = _rejections("notes", "aws-access-key")
    caplog.set_level("INFO", logger="ki_kb.activities")
    result = await activities.sync_repo(activities.ProjectRef(name="notes"))

    assert result.rejected == 3
    assert result.rejected_rules == {"docs/b.md": ["aws-access-key"]}
    assert result.rejected_rule_counts == {"aws-access-key": 1}
    assert _rejections("notes", "aws-access-key") == before + 1

    [record] = [r for r in caplog.records if r.message == "document rejected by scanner"]
    assert (record.project, record.path, record.rules) == (
        "notes",
        "docs/b.md",
        {"aws-access-key": [4]},
    )
    seen = [FAKE_KEY in str(r.__dict__) for r in caplog.records]
    assert not any(seen)
    assert FAKE_KEY not in json.dumps(result.__dict__)
    assert FAKE_KEY not in str(metrics.INGEST_REJECTIONS.collect())


async def test_sensitive_rejection_reports_rule_counts_without_the_path(world, monkeypatch, caplog):
    monkeypatch.setenv("KI_ALLOWED_GRAPHS", "brain-client")
    world.graphitis["brain-client"] = FakeGraphiti("brain-client")
    _write_rejected_doc(world)
    caplog.set_level("INFO", logger="ki_kb.activities")
    result = await activities.sync_repo(activities.ProjectRef(name="payments"))

    assert (result.rejected_paths, result.rejected_rules) == ([], {})
    assert result.rejected_rule_counts == {"aws-access-key": 1}
    [record] = [r for r in caplog.records if r.message == "document rejected by scanner"]
    assert not hasattr(record, "path")
    assert record.rules == {"aws-access-key": [4]}
    assert "docs/b.md" not in str(record.__dict__) and FAKE_KEY not in str(record.__dict__)


async def test_graph_outside_allowed_graphs_is_refused(world):
    # payments feeds brain-client, which this worker's KI_ALLOWED_GRAPHS does not list.
    with pytest.raises(ApplicationError) as exc:
        await activities.sync_repo(activities.ProjectRef(name="payments"))
    assert "GraphNotAllowedError" in str(exc.value) and exc.value.__cause__ is None
    # payments is sensitive: no path in the error.
    assert " at " not in str(exc.value)


async def test_model_outage_leaves_no_text_in_the_error(world):
    world.graphitis["brain-voice"] = FakeGraphiti(
        "brain-voice", fail_with=TimeoutError("request body: card 4111 1111 1111 1111")
    )
    with pytest.raises(ApplicationError) as exc:
        await activities.sync_repo(activities.ProjectRef(name="notes"))
    assert "4111" not in str(exc.value) and exc.value.__cause__ is None
    assert "TimeoutError at docs/a.md" in str(exc.value)


async def test_git_failure_skips_the_project_instead_of_failing_the_run(world, monkeypatch):
    async def failing_git(*args, token=None, cwd=None, limit=0):
        raise GitError("git clone failed with exit code 128")

    monkeypatch.setattr(activities, "_git", failing_git)
    skipped = metrics.SYNC_SKIPPED.labels("notes", "git")
    before = skipped._value.get()
    result = await activities.sync_repo(activities.ProjectRef(name="notes"))
    assert (result.skipped_reason, result.added, result.skipped) == ("git", 0, False)
    assert skipped._value.get() == before + 1


async def test_hanging_ls_remote_skips_the_project_and_others_complete(world, monkeypatch):
    real = activities._git

    async def hanging_ls_remote(*args, token=None, cwd=None, limit=0):
        if args[0] == "ls-remote":
            raise GitTimeoutError("git ls-remote timed out after 60s")
        return await real(*args, token=token, cwd=cwd)

    monkeypatch.setattr(activities, "_git", hanging_ls_remote)
    stuck = await activities.sync_repo(activities.ProjectRef(name="notes"))
    assert stuck.skipped_reason == "git" and "clone" not in world.calls

    monkeypatch.setattr(activities, "_git", real)
    other = await activities.sync_repo(activities.ProjectRef(name="notes"))
    assert other.skipped_reason is None and other.added == 2


async def test_unreadable_clone_is_recloned_once(world, monkeypatch):
    real = activities._git
    failed = []

    async def broken_first_log(*args, token=None, cwd=None, limit=0):
        if args[0] == "log" and "-1" in args and not failed:
            failed.append(1)
            raise GitError("git log failed with exit code 128")
        return await real(*args, token=token, cwd=cwd)

    monkeypatch.setattr(activities, "_git", broken_first_log)
    result = await activities.sync_repo(activities.ProjectRef(name="notes"))
    assert result.skipped_reason is None and result.added == 2
    assert world.calls.count("clone") == 2


async def test_clone_that_stays_unreadable_is_skipped(world, monkeypatch):
    real = activities._git

    async def always_broken(*args, token=None, cwd=None, limit=0):
        if args[0] == "log" and "-1" in args:
            raise GitError("git log failed with exit code 128")
        return await real(*args, token=token, cwd=cwd)

    monkeypatch.setattr(activities, "_git", always_broken)
    result = await activities.sync_repo(activities.ProjectRef(name="notes"))
    assert result.skipped_reason == "git"


async def test_load_projects(world):
    refs = await activities.load_projects()
    assert {r.name for r in refs} == {"notes", "payments", "mixed"}


async def test_load_projects_refuses_stranded_graphs(world):
    world.store.data["payments"] = {
        "docs/x.md#X": plan.Recorded(hash="h", episode="e", graph="brain-client")
    }
    with pytest.raises(ApplicationError) as exc:
        await activities.load_projects()
    assert exc.value.type == "StrandedGraph" and "brain-client" in str(exc.value)


async def test_real_git_kills_a_command_that_outruns_its_timeout(tmp_path):
    with pytest.raises(GitTimeoutError, match=r"timed out after 0\.01s"):
        await activities._git("-c", "alias.slow=!sleep 5", "slow", cwd=tmp_path, limit=0.01)


async def test_real_git_log_exits_128_when_a_commit_object_is_gone(tmp_path):
    repo = tmp_path / "r"
    await activities._git("init", "-q", str(repo))
    await activities._git(
        "-c", "user.name=t", "-c", "user.email=t@t", "commit", "--allow-empty", "-qm", "x", cwd=repo
    )
    head = await activities._git("rev-parse", "HEAD", cwd=repo)
    (repo / ".git" / "objects" / head[:2] / head[2:]).chmod(0o644)
    (repo / ".git" / "objects" / head[:2] / head[2:]).unlink()
    with pytest.raises(GitError, match="exit code 128"):
        await activities._git("log", "-1", "--format=%cI", cwd=repo)


async def test_real_git_error_carries_no_stderr(tmp_path):
    with pytest.raises(GitError) as exc:
        await activities._git("log", cwd=tmp_path)
    assert "exit code" in str(exc.value) and "fatal" not in str(exc.value)


async def test_real_git_reads_a_repo(tmp_path):
    await activities._git("init", "-q", str(tmp_path / "r"))
    assert (
        await activities._git("rev-parse", "--is-inside-work-tree", cwd=tmp_path / "r")
    ) == "true"


def test_settings_from_env_matches_fixture(world):
    s = Settings.from_env()
    assert s.task_queue == "ki-ingest" and "brain-voice" in s.allowed_graphs
    assert json.loads(json.dumps(sorted(s.allowed_graphs))) == ["brain-voice", "cortex"]
    assert Path(s.projects_file or "").exists()


async def test_episodes_named_queries_by_group_and_name():
    calls = []

    async def execute_query(query, **params):
        calls.append(params)
        return [{"uuid": "u1"}, {"uuid": "u2"}], ["uuid"], None

    gi = SimpleNamespace(driver=SimpleNamespace(execute_query=execute_query))
    assert await activities._episodes_named(gi, "brain-voice", ["n/a.md"]) == {"u1", "u2"}
    assert calls == [{"group": "brain-voice", "names": ["n/a.md"]}]


# Every minute of every day, so these tests do not depend on the clock.
ALWAYS_PEAK = "Mon-Sun 00:00-12:00,12:00-00:00"

BULK_PROJECTS = """
sensitive_brains: []
identities:
  personal: {github_user: alice, email: me@example.com, credential: personal, ingest: true}
projects:
  - {name: archive, repo: https://github.com/alice/archive, identity: personal, brains: [voice],
     bulk: true, ingest: [{path: docs/, brain: voice}]}
  - {name: vault, repo: https://github.com/alice/vault, identity: personal, brains: [voice],
     ingest: [{path: docs/, brain: voice}]}
"""


@pytest.fixture
def bulk_world(world, tmp_path, monkeypatch):
    """The world with a `bulk: true` archive and a vault, both over the same two documents."""
    path = tmp_path / "projects-bulk.yaml"
    path.write_text(BULK_PROJECTS)
    monkeypatch.setenv("KI_PROJECTS_FILE", str(path))
    return world


@pytest.fixture
def balance_reads(monkeypatch):
    """Replaces the balance endpoint: set .usd to a number, or to an exception to raise."""
    state = SimpleNamespace(usd=100.0, calls=0)

    async def fake_read(settings, transport=None):
        state.calls += 1
        if isinstance(state.usd, Exception):
            raise state.usd
        return state.usd

    monkeypatch.setattr(balance, "read_balance", fake_read)
    return state


async def test_a_small_change_goes_through_at_peak(world, monkeypatch):
    monkeypatch.setenv("KI_LLM_PEAK_UTC", ALWAYS_PEAK)
    res = await activities.sync_repo(activities.ProjectRef(name="notes"))
    assert (res.added, res.deferred, res.deferred_reason) == (2, 0, None)


async def test_more_than_the_threshold_waits_for_off_peak(world, monkeypatch):
    monkeypatch.setenv("KI_LLM_PEAK_UTC", ALWAYS_PEAK)
    monkeypatch.setenv("KI_INGEST_BULK_THRESHOLD", "1")
    ref = activities.ProjectRef(name="notes")

    held = await activities.sync_repo(ref)
    assert (held.added, held.deferred, held.deferred_reason) == (0, 2, "peak")
    assert held.deferred_paths == ["docs/a.md", "docs/b.md"]
    assert "notes" not in world.store.markers  # the next run must not skip it
    assert world.graphitis == {}  # no model call at all

    monkeypatch.setenv("KI_LLM_PEAK_UTC", "")
    later = await activities.sync_repo(ref)
    assert (later.added, later.deferred, later.deferred_reason) == (2, 0, None)
    assert "notes" in world.store.markers


async def test_bulk_flag_waits_for_off_peak_at_any_size(bulk_world, monkeypatch):
    monkeypatch.setenv("KI_LLM_PEAK_UTC", ALWAYS_PEAK)
    ref = activities.ProjectRef(name="archive")

    held = await activities.sync_repo(ref)
    assert (held.added, held.deferred, held.deferred_reason) == (0, 2, "peak")

    vault = await activities.sync_repo(activities.ProjectRef(name="vault"))
    assert (vault.added, vault.deferred_reason) == (2, None)

    monkeypatch.setenv("KI_LLM_PEAK_UTC", "")
    later = await activities.sync_repo(ref)
    assert (later.added, later.deferred, later.deferred_reason) == (2, 0, None)


async def test_removals_go_through_while_bulk_waits(bulk_world, monkeypatch):
    ref = activities.ProjectRef(name="archive")
    await activities.sync_repo(ref)
    monkeypatch.setenv("KI_LLM_PEAK_UTC", ALWAYS_PEAK)
    (bulk_world.repo / "docs" / "a.md").write_text("# A\nalpha, revised\n")
    (bulk_world.repo / "docs" / "b.md").unlink()

    res = await activities.sync_repo(ref)
    assert (res.replaced, res.removed, res.deferred, res.deferred_reason) == (0, 1, 1, "peak")


async def test_low_balance_read_by_the_run_holds_bulk_only(bulk_world, balance_reads):
    archive = await activities.sync_repo(activities.ProjectRef(name="archive", low_balance=True))
    assert (archive.added, archive.deferred, archive.deferred_reason) == (0, 2, "balance")

    vault = await activities.sync_repo(activities.ProjectRef(name="vault", low_balance=True))
    assert (vault.added, vault.deferred_reason) == (2, None)
    assert balance_reads.calls == 0  # the run's reading is used, not a new one


@pytest.mark.parametrize(
    ("usd", "want"),
    [
        (1.0, "balance"),  # below the floor of 5
        (httpx.ConnectError("unreachable"), "balance"),
        (balance.BalanceError("no USD entry"), "balance"),
        (5.0, None),  # at the floor is enough
    ],
    ids=["below", "unreachable", "unreadable", "above"],
)
async def test_bulk_reads_the_balance_when_the_run_did_not(
    bulk_world, balance_reads, monkeypatch, usd, want
):
    monkeypatch.setenv("KI_LLM_BALANCE_URL", "https://balance.invalid/user/balance")
    monkeypatch.setenv("KI_LLM_MIN_BALANCE", "5")
    balance_reads.usd = usd

    res = await activities.sync_repo(activities.ProjectRef(name="archive"))
    assert res.deferred_reason == want
    assert (res.added, res.deferred) == ((0, 2) if want else (2, 0))
    assert balance_reads.calls == 1


@pytest.mark.parametrize(
    ("mid_run_balance", "expected_batches"),
    [(1.0, 2), (balance.BalanceError("unreadable"), 2)],
    ids=["below-floor-after-second-batch", "unreadable-after-second-batch"],
)
async def test_bulk_rechecks_balance_between_batches_and_defers_the_rest(
    bulk_world, monkeypatch, mid_run_balance, expected_batches
):
    for name in ("c", "d"):
        (bulk_world.repo / "docs" / f"{name}.md").write_text(f"# {name}\n{name}\n")
    monkeypatch.setenv("KI_LLM_BALANCE_URL", "https://balance.invalid/user/balance")
    monkeypatch.setenv("KI_LLM_BALANCE_CHECK_INTERVAL_MINUTES", "10")

    reads = iter([100.0, 100.0, mid_run_balance])
    calls = 0

    async def fake_read(_settings, transport=None):
        nonlocal calls
        calls += 1
        value = next(reads)
        if isinstance(value, Exception):
            raise value
        return value

    monkeypatch.setattr(balance, "read_balance", fake_read)
    clock = SimpleNamespace(now=0.0)
    monkeypatch.setattr(activities.time, "monotonic", lambda: clock.now)
    add_episode = FakeGraphiti.add_episode

    async def add_episode_and_advance(self, *args, **kwargs):
        result = await add_episode(self, *args, **kwargs)
        clock.now += 10 * 60
        return result

    monkeypatch.setattr(FakeGraphiti, "add_episode", add_episode_and_advance)

    result = await activities.sync_repo(activities.ProjectRef(name="archive"))

    assert (result.added, result.deferred, result.deferred_reason) == (2, 2, "balance")
    assert result.deferred_paths == ["docs/c.md", "docs/d.md"]
    assert len(bulk_world.graphitis["brain-voice"].added) == expected_batches
    assert calls == 3
    assert "archive" not in bulk_world.store.markers

    async def topped_up(_settings, transport=None):
        return 100.0

    monkeypatch.setattr(balance, "read_balance", topped_up)
    resumed = await activities.sync_repo(activities.ProjectRef(name="archive"))
    assert (resumed.added, resumed.deferred, resumed.deferred_reason) == (2, 0, None)
    assert "archive" in bulk_world.store.markers


async def test_peak_is_reported_before_the_balance_is_read(bulk_world, balance_reads, monkeypatch):
    monkeypatch.setenv("KI_LLM_PEAK_UTC", ALWAYS_PEAK)
    monkeypatch.setenv("KI_LLM_BALANCE_URL", "https://balance.invalid/user/balance")
    res = await activities.sync_repo(activities.ProjectRef(name="archive"))
    assert res.deferred_reason == "peak"
    assert balance_reads.calls == 0


async def test_a_weekday_morning_archive_waits_until_ten(world, monkeypatch):
    monkeypatch.setenv("KI_LLM_PEAK_UTC", "Mon-Fri 01:00-04:00,06:00-10:00")
    settings = Settings.from_env()
    monday = datetime(2026, 10, 26, tzinfo=UTC)
    assert await activities.bulk_hold(settings, None, monday.replace(hour=9, minute=59)) == "peak"
    assert await activities.bulk_hold(settings, None, monday.replace(hour=10)) is None
    assert await activities.bulk_hold(settings, False, monday.replace(hour=5)) is None
    assert await activities.bulk_hold(settings, True, monday.replace(hour=5)) == "balance"
    saturday = datetime(2026, 10, 31, 7, tzinfo=UTC)
    assert await activities.bulk_hold(settings, None, saturday) is None


async def test_load_projects_orders_stages_and_reads_the_balance_once(
    bulk_world, balance_reads, tmp_path, monkeypatch
):
    path = tmp_path / "projects-order.yaml"
    path.write_text(
        BULK_PROJECTS
        + """  - {name: docs, repo: https://github.com/alice/docs, identity: personal,
     brains: [voice], ingest: [{path: docs/, brain: voice}]}
  - {name: no-ingest, repo: https://github.com/alice/other, identity: personal, brains: [voice]}
"""
    )
    monkeypatch.setenv("KI_PROJECTS_FILE", str(path))
    monkeypatch.setenv("KI_LLM_BALANCE_URL", "https://balance.invalid/user/balance")
    balance_reads.usd = 1.0

    refs = await activities.load_projects()
    assert [(r.name, r.stage) for r in refs] == [
        ("vault", activities.STAGE_VAULT),
        ("docs", activities.STAGE_DOCS),
        ("archive", activities.STAGE_BULK),
    ]
    assert all(r.low_balance is True for r in refs)
    assert balance_reads.calls == 1


async def test_load_projects_without_a_balance_url_reads_nothing(world, balance_reads):
    refs = await activities.load_projects()
    assert all(r.low_balance is False for r in refs)
    assert balance_reads.calls == 0


async def test_load_projects_gives_bulk_projects_the_bulk_timeout(
    bulk_world, balance_reads, tmp_path, monkeypatch
):
    path = tmp_path / "projects-timeouts.yaml"
    path.write_text(
        BULK_PROJECTS
        + """  - {name: docs, repo: https://github.com/alice/docs, identity: personal,
     brains: [voice], ingest: [{path: docs/, brain: voice}]}
"""
    )
    monkeypatch.setenv("KI_PROJECTS_FILE", str(path))
    by_name = {r.name: r.timeout_seconds for r in await activities.load_projects()}
    assert by_name == {"vault": 45 * 60, "docs": 45 * 60, "archive": 240 * 60}

    monkeypatch.setenv("KI_SYNC_TIMEOUT_MINUTES", "30")
    monkeypatch.setenv("KI_SYNC_BULK_TIMEOUT_MINUTES", "90")
    by_name = {r.name: r.timeout_seconds for r in await activities.load_projects()}
    assert by_name["docs"] == 30 * 60 and by_name["archive"] == 90 * 60


async def test_load_projects_clamps_to_the_ceiling(bulk_world, balance_reads, monkeypatch):
    monkeypatch.setenv("KI_SYNC_TIMEOUT_MINUTES", "60")
    monkeypatch.setenv("KI_SYNC_BULK_TIMEOUT_MINUTES", "30")
    monkeypatch.setenv("KI_SYNC_TIMEOUT_CEILING_MINUTES", "30")
    assert {r.timeout_seconds for r in await activities.load_projects()} == {30 * 60}


async def test_sync_timeout_for_one_project_is_bulk_only_when_flagged(bulk_world):
    assert await activities.sync_timeout("archive") == 240 * 60
    assert await activities.sync_timeout("nobody") == 45 * 60


SECTIONED = "# Guide\nintro\n\n## Setup\ninstall it\n\n## Usage\nrun it\n"


async def test_editing_one_section_extracts_only_that_section(world):
    ref = activities.ProjectRef(name="notes")
    (world.repo / "docs" / "g.md").write_text(SECTIONED)
    first = await activities.sync_repo(ref)
    assert (first.added, first.replaced, first.removed, first.unchanged) == (5, 0, 0, 0)

    voice = world.graphitis["brain-voice"]
    voice.added.clear()
    voice.removed.clear()
    (world.repo / "docs" / "g.md").write_text(SECTIONED.replace("run it", "run it twice"))
    second = await activities.sync_repo(ref)
    assert (second.added, second.replaced, second.removed, second.unchanged) == (0, 1, 0, 4)
    assert voice.added == ["notes/docs/g.md#Guide > Usage"]
    assert len(voice.removed) == 1


async def test_sections_added_and_removed_are_reported(world):
    ref = activities.ProjectRef(name="notes")
    (world.repo / "docs" / "g.md").write_text(SECTIONED)
    await activities.sync_repo(ref)
    edited = "# Guide\nintro\n\n## Extra\nnew\n\n## Usage\nrun it\n"
    (world.repo / "docs" / "g.md").write_text(edited)
    result = await activities.sync_repo(ref)
    assert (result.added, result.replaced, result.removed, result.unchanged) == (1, 0, 1, 4)
    assert "docs/g.md#Guide > Setup" not in world.store.data["notes"]


async def test_removal_finishes_when_the_episode_was_already_removed(world, monkeypatch):
    from graphiti_core import Graphiti
    from graphiti_core.errors import NodeNotFoundError
    from graphiti_core.nodes import EpisodicNode

    from ki_kb.graph import MeteredGraphiti

    ref = activities.ProjectRef(name="notes")
    await activities.sync_repo(ref)
    modelled: list[str] = []

    async def gone(driver, uuid):
        raise NodeNotFoundError(uuid)

    async def remove_episode(self, uuid):
        modelled.append(uuid)

    async def close(self):
        pass

    monkeypatch.setattr(EpisodicNode, "get_by_uuid", gone)
    monkeypatch.setattr(Graphiti, "remove_episode", remove_episode)
    monkeypatch.setattr(Graphiti, "close", close)
    metered = MeteredGraphiti.__new__(MeteredGraphiti)
    metered._usage_fields = {"graph": "brain-voice", "model": "m"}
    metered.driver = SimpleNamespace()
    world.graphitis["brain-voice"] = metered

    (world.repo / "docs" / "b.md").unlink()
    res = await activities.sync_repo(ref)
    assert (res.removed, res.failed) == (1, 0)
    assert sorted(world.store.data["notes"]) == ["docs/a.md#A"]
    assert modelled == []


async def test_rewrap_and_whitespace_changes_extract_nothing(world):
    ref = activities.ProjectRef(name="notes")
    (world.repo / "docs" / "g.md").write_text(SECTIONED)
    await activities.sync_repo(ref)
    voice = world.graphitis["brain-voice"]
    voice.added.clear()
    voice.removed.clear()
    (world.repo / "docs" / "g.md").write_text(
        "# Guide\n\nintro\n\n\n## Setup\ninstall\nit   \n\n## Usage\n\nrun it\n\n"
    )
    result = await activities.sync_repo(ref)
    assert (result.added, result.replaced, result.removed, result.unchanged) == (0, 0, 0, 5)
    assert (voice.added, voice.removed) == ([], [])


async def test_whole_document_episodes_stay_until_the_document_changes(world):
    ref = activities.ProjectRef(name="notes")
    (world.repo / "docs" / "g.md").write_text(SECTIONED)
    legacy = plan.Recorded(
        hash=plan.content_hash(SECTIONED), episode="whole-g", graph="brain-voice"
    )
    world.store.data["notes"] = {"docs/g.md": legacy}

    same = await activities.sync_repo(ref)
    assert (same.unchanged, same.added, same.removed) == (1, 2, 0)  # a.md, b.md are new
    voice = world.graphitis["brain-voice"]
    assert "whole-g" not in voice.removed
    assert world.store.data["notes"]["docs/g.md"] == legacy

    (world.repo / "docs" / "g.md").write_text(SECTIONED.replace("run it", "run it twice"))
    changed = await activities.sync_repo(ref)
    assert (changed.added, changed.removed) == (3, 1)
    assert "whole-g" in voice.removed
    assert "docs/g.md" not in world.store.data["notes"]
    assert sorted(k for k in world.store.data["notes"] if k.startswith("docs/g.md#")) == [
        "docs/g.md#Guide",
        "docs/g.md#Guide > Setup",
        "docs/g.md#Guide > Usage",
    ]


async def test_whole_document_episode_is_kept_while_its_sections_are_incomplete(world):
    ref = activities.ProjectRef(name="notes")
    (world.repo / "docs" / "g.md").write_text(SECTIONED.replace("run it", "run it twice"))
    world.store.data["notes"] = {
        "docs/g.md": plan.Recorded(
            hash=plan.content_hash(SECTIONED), episode="whole-g", graph="brain-voice"
        )
    }
    world.graphitis["brain-voice"] = FakeGraphiti(
        "brain-voice", fail_with=bad_answer(), fail_names={"notes/docs/g.md#Guide > Usage"}
    )
    result = await activities.sync_repo(ref)
    assert result.failed == 1
    assert "whole-g" not in world.graphitis["brain-voice"].removed
    assert "docs/g.md" in world.store.data["notes"]
