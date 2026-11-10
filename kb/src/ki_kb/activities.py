"""Temporal activities. All document content stays inside these functions.

Workflow inputs and results carry only project names and counts (plus file
paths for non-sensitive projects). Document text never enters Temporal
history, because the Temporal server and its database sit outside the harness
namespace and keep every run's inputs and results. Ingest rules and
credentials are read from the projects file inside the activity, not passed
through workflows.

Errors are part of Temporal history too. Anything raised from here is an
ApplicationError with a fixed message (the project name, the error type, and a
path only for non-sensitive projects); the original exception, which may carry
model output, request bodies or git output, is logged in this pod and never
attached as a cause.

A document whose extraction fails is recorded in the ingest state and skipped
until its content, its graph or the worker version changes; the sync goes on
with the rest. Outages (model, embedder or FalkorDB unreachable, rate-limited
or refusing credentials) fail the activity without marking any document
failed, and without a Temporal retry: the batch in flight is paid for and
discarded, so the next scheduled run resumes from the saved progress instead.
A project whose runs keep failing is deferred with a growing delay.
"""

from __future__ import annotations

import asyncio
import base64
import contextlib
import hashlib
import json
import logging
import os
import random
import shutil
import tempfile
import time
from collections.abc import AsyncIterator, Awaitable, Callable, Sequence
from dataclasses import dataclass, field
from datetime import UTC, datetime, timedelta
from importlib.metadata import version
from pathlib import Path

import openai
import redis.exceptions
from graphiti_core import Graphiti
from graphiti_core.llm_client.errors import RateLimitError as GraphitiRateLimitError
from graphiti_core.nodes import EpisodeType
from graphiti_core.utils.bulk_utils import RawEpisode
from temporalio import activity
from temporalio.exceptions import ApplicationError

from . import balance, brains, kinds, metrics, plan, projects, scanner, sections
from .aliases import Aliases
from .entities import ENTITY_TYPES, EXTRACTION_INSTRUCTIONS
from .errors import ConfigError, GitError, GitTimeoutError
from .graph import StateStore, build_graphiti
from .settings import Settings

logger = logging.getLogger(__name__)

MAX_DOC_BYTES = 200_000
GIT_TIMEOUT_SECONDS = 60
# A blobless clone of a large history legitimately takes longer than a ref lookup.
GIT_CLONE_TIMEOUT_SECONDS = 300
SKIP_DIRS = {".git", "node_modules", "vendor", ".venv", "dist", "build"}
SKIP_FILES = {"TEMPLATE.md"}
DOC_SUFFIXES = {".md", ".mdx"}


HEARTBEAT_SECONDS = 30
# Attempts Temporal gives a failed sync; the workflow's retry policy uses it too.
MAX_ATTEMPTS = 3
# Share of the attempt's timeout kept free on top of the slowest batch so far: a
# batch can run slower than any before it.
TIME_MARGIN_FRACTION = 0.05
# A project is deferred after this many failed runs in a row, for BACKOFF_BASE
# doubled per further failure, at most BACKOFF_MAX.
BACKOFF_AFTER = 2
BACKOFF_BASE = timedelta(minutes=30)
BACKOFF_MAX = timedelta(hours=24)


class MissingEpisodeError(Exception):
    """Graphiti returned no episode for a document it was given."""


_OUTAGES: tuple[type[BaseException], ...] = (
    openai.APIConnectionError,
    openai.AuthenticationError,
    openai.PermissionDeniedError,
    openai.NotFoundError,
    openai.RateLimitError,
    openai.InternalServerError,
    GraphitiRateLimitError,
    redis.exceptions.ConnectionError,
    redis.exceptions.TimeoutError,
    ConnectionError,
    TimeoutError,
)

_RATE_LIMITS: tuple[type[BaseException], ...] = (
    openai.RateLimitError,
    GraphitiRateLimitError,
)
# A provider's rate limit lasts minutes at peak hours: 15, 30, 60, 120, 120 seconds
# with the default five attempts, about six minutes before the attempt fails.
RATE_LIMIT_BASE_SECONDS = 15
MAX_RATE_LIMIT_SLEEP_SECONDS = 120


def is_outage(err: BaseException) -> bool:
    """True if a service failed rather than one document's extraction.

    Graphiti re-raises some provider errors wrapped (`raise X from err`), so
    the explicit cause chain counts too.
    """
    cause: BaseException | None = err
    while cause is not None:
        if isinstance(cause, _OUTAGES):
            return True
        cause = cause.__cause__
    return False


def is_rate_limited(err: BaseException) -> bool:
    """True when an error or its explicit cause is a provider rate limit."""
    cause: BaseException | None = err
    while cause is not None:
        if isinstance(cause, _RATE_LIMITS):
            return True
        cause = cause.__cause__
    return False


async def _sleep_rate_limit(delay: float) -> None:
    await asyncio.sleep(delay)


async def _episodes_named(gi: Graphiti, graph: str, names: list[str]) -> set[str]:
    """Uuids of the episodes in this graph that carry any of these names."""
    result = await gi.driver.execute_query(
        "MATCH (e:Episodic) WHERE e.group_id = $group AND e.name IN $names RETURN e.uuid AS uuid",
        group=graph,
        names=names,
    )
    return {str(r["uuid"]) for r in result[0]} if result else set()


@contextlib.asynccontextmanager
async def _heartbeat() -> AsyncIterator[None]:
    """Heartbeats while a long bulk call runs, so Temporal does not time the activity out."""

    async def beat() -> None:
        while True:
            activity.heartbeat("ingest")
            await asyncio.sleep(HEARTBEAT_SECONDS)

    task = asyncio.create_task(beat())
    try:
        yield
    finally:
        task.cancel()
        with contextlib.suppress(asyncio.CancelledError):
            await task


# Stages of a SyncProjects run, in order: the vault, repo docs, bulk projects.
STAGE_VAULT = 0
STAGE_DOCS = 1
STAGE_BULK = 2
VAULT_PROJECT = "vault"
# StartToClose of a sync attempt for a ref that carries none (older history).
DEFAULT_SYNC_TIMEOUT_SECONDS = 45 * 60


@dataclass
class ProjectRef:
    """What a workflow knows about a project: its name and when to run it, no content."""

    name: str
    # Its stage within a SyncProjects run (STAGE_*).
    stage: int = STAGE_DOCS
    # The balance guard's reading for this run: True holds bulk work back. None
    # when nobody read it (SyncOneProject); the activity then reads it if needed.
    low_balance: bool | None = None
    # StartToClose for its sync attempt, set by load_projects from Settings
    # because workflow code may not read the environment. The default keeps refs
    # from older history decodable and on the old 45 minutes.
    timeout_seconds: int = DEFAULT_SYNC_TIMEOUT_SECONDS


@dataclass
class SyncResult:
    project: str
    commit: str = ""
    added: int = 0
    replaced: int = 0
    removed: int = 0
    unchanged: int = 0
    rejected: int = 0
    # Committed within the quiet period; a later run takes them.
    deferred: int = 0
    # Extraction failed, now or before at the same content and worker version.
    failed: int = 0
    # The remote head matched the last complete sync, so nothing was cloned.
    skipped: bool = False
    # Set when the project was left out of this run for a reason other than an
    # unmoved head: "git" when a git command failed or timed out.
    skipped_reason: str | None = None
    # Why documents were deferred: "peak" or "balance" when a bulk project waits
    # (see bulk_hold), "time" when the attempt's timeout left no room for another
    # batch, "backoff" when the project's last runs failed (nothing was cloned, so
    # `deferred` stays 0), otherwise "quiet" for the quiet period; None if none were.
    deferred_reason: str | None = None
    # Paths are reported only for non-sensitive projects.
    rejected_paths: list[str] = field(default_factory=list)
    # Why documents were rejected, never the matched text: rule kinds per path
    # (non-sensitive projects only) and, for every project, documents per rule kind.
    rejected_rules: dict[str, list[str]] = field(default_factory=dict)
    rejected_rule_counts: dict[str, int] = field(default_factory=dict)
    deferred_paths: list[str] = field(default_factory=list)
    failed_paths: list[str] = field(default_factory=list)


def _load(settings: Settings) -> projects.Config:
    if settings.projects_file is None:
        raise ConfigError("KI_PROJECTS_FILE must be set")
    return projects.load(settings.projects_file)


def stage_of(project: projects.Project) -> int:
    """The vault first, so an edited note never waits behind a bulk project; `bulk: true` last."""
    if project.name == VAULT_PROJECT:
        return STAGE_VAULT
    return STAGE_BULK if project.bulk else STAGE_DOCS


def order_projects(projs: Sequence[projects.Project]) -> list[projects.Project]:
    """Projects in stage order, keeping the projects file's order within a stage."""
    return sorted(projs, key=stage_of)


@activity.defn
async def load_projects() -> list[ProjectRef]:
    settings = Settings.from_env()
    cfg = _load(settings)
    await _refuse_stranded(settings, cfg)
    # Once per run: exports ki_llm_balance_usd even when nothing is bulk.
    low = await balance.balance_low(settings)
    return [
        ProjectRef(
            name=p.name,
            stage=stage_of(p),
            low_balance=low,
            timeout_seconds=settings.sync_timeout_seconds(bulk=p.bulk),
        )
        for p in order_projects([p for p in cfg.projects if p.ingest])
    ]


@activity.defn
async def sync_timeout(name: str) -> int:
    """StartToClose seconds for one project synced on demand; the bulk limit if it is bulk."""
    settings = Settings.from_env()
    cfg = _load(settings)
    bulk = any(p.bulk for p in cfg.projects if p.name == name)
    return settings.sync_timeout_seconds(bulk=bulk)


async def bulk_hold(
    settings: Settings, low_balance: bool | None, now: datetime | None = None
) -> str | None:
    """Why bulk work must wait now: "peak", "balance", or None when it may run.

    The balance is read here only when the run did not read it (low_balance None)
    and the clock alone does not already hold the work back.
    """
    if settings.peak_schedule.is_peak(now):
        return "peak"
    if low_balance is None:
        low_balance = await balance.balance_low(settings)
    return "balance" if low_balance else None


def stranded_graphs(
    recorded: dict[str, dict[str, plan.Recorded]], allowed: frozenset[str]
) -> set[str]:
    """Graphs this instance still holds documents for but may no longer touch.

    That happens when a brain changes sensitivity: its old documents stay in the
    instance it used to live on. Pure, so it can be tested without FalkorDB.
    """
    return {
        rec.graph
        for docs in recorded.values()
        for rec in docs.values()
        if not brains.matches_any(allowed, rec.graph)
    }


async def _refuse_stranded(settings: Settings, cfg: projects.Config) -> None:
    store = StateStore(settings)
    try:
        recorded = {p.name: await store.all(p.name) for p in cfg.projects}
    finally:
        await store.close()
    stranded = stranded_graphs(recorded, settings.allowed_graphs)
    if stranded:
        raise ApplicationError(
            f"this instance still holds documents for {', '.join(sorted(stranded))}, "
            "which it may no longer touch; run `python -m ki_kb.admin forget-stranded` here "
            "(see operations.md)",
            type="StrandedGraph",
            non_retryable=True,
        )


async def _git(
    *args: str,
    token: str | None = None,
    cwd: Path | None = None,
    limit: float = GIT_TIMEOUT_SECONDS,
) -> str:
    env = dict(os.environ, GIT_TERMINAL_PROMPT="0")
    if token:
        # Passed through the environment so the token never appears in argv or logs.
        basic = base64.b64encode(f"x-access-token:{token}".encode()).decode()
        env.update(
            GIT_CONFIG_COUNT="1",
            GIT_CONFIG_KEY_0="http.https://github.com/.extraheader",
            GIT_CONFIG_VALUE_0=f"AUTHORIZATION: basic {basic}",
        )
    proc = await asyncio.create_subprocess_exec(
        "git",
        *args,
        cwd=cwd,
        env=env,
        stdout=asyncio.subprocess.PIPE,
        stderr=asyncio.subprocess.PIPE,
    )
    try:
        out, err = await asyncio.wait_for(proc.communicate(), timeout=limit)
    except TimeoutError:
        proc.kill()
        await proc.wait()
        raise GitTimeoutError(f"git {args[0]} timed out after {limit:g}s") from None
    if proc.returncode != 0:
        # stderr stays in this pod's log; the error itself carries no git output.
        logger.warning(
            "git failed", extra={"command": args[0], "stderr": err.decode(errors="replace")[-500:]}
        )
        raise GitError(f"git {args[0]} failed with exit code {proc.returncode}")
    return out.decode().strip()


async def _clone(project: projects.Project, root: Path, token: str) -> tuple[str, datetime]:
    # Blobless: the full commit history for the quiet-period check, but
    # only the checked-out blobs are downloaded.
    await _git(
        "clone",
        "--filter=blob:none",
        "--branch",
        project.branch,
        project.repo,
        str(root),
        token=token,
        limit=GIT_CLONE_TIMEOUT_SECONDS,
    )
    commit = await _git("rev-parse", "HEAD", cwd=root)
    return commit, datetime.fromisoformat(await _git("log", "-1", "--format=%cI", cwd=root))


async def _checkout(project: projects.Project, root: Path, token: str) -> tuple[str, datetime]:
    """Clone and read the head commit, cloning once more if the first copy is unreadable.

    A clone can come back with objects git cannot read (git exits 128 on the
    first log); a fresh clone has them. A timeout is not retried: the remote is the problem.
    """
    try:
        return await _clone(project, root, token)
    except GitTimeoutError:
        raise
    except GitError:
        logger.warning("clone unreadable, cloning again", extra={"project": project.name})
        shutil.rmtree(root, ignore_errors=True)
        return await _clone(project, root, token)


def collect_docs(root: Path, project: projects.Project) -> dict[str, tuple[str, str]]:
    """Map repo-relative path -> (brain, text) for every document to ingest.

    Symlinks are never followed: a repo could point docs/x.md at
    /proc/self/environ or any other file in the worker, and its content would go
    to the model and into the graph.
    """
    docs: dict[str, tuple[str, str]] = {}
    real_root = root.resolve()
    for path in sorted(root.rglob("*")):
        rel = path.relative_to(root)
        if any(part in SKIP_DIRS for part in rel.parts) or path.is_symlink() or not path.is_file():
            continue
        if not path.resolve().is_relative_to(real_root):
            continue
        if path.name in SKIP_FILES or path.suffix.lower() not in DOC_SUFFIXES:
            continue
        if path.stat().st_size > MAX_DOC_BYTES:
            continue
        brain = project.brain_for(rel.as_posix())
        if brain is None:
            continue
        docs[rel.as_posix()] = (brain, path.read_text(encoding="utf-8", errors="replace"))
    return docs


async def _recently_committed(root: Path, quiet_minutes: int) -> frozenset[str]:
    """Repo-relative paths touched by a commit younger than the quiet period."""
    if quiet_minutes <= 0:
        return frozenset()
    cutoff = datetime.now(UTC) - timedelta(minutes=quiet_minutes)
    # --no-renames: rename detection compares file contents, which the blobless
    # clone does not have, so git fetches them from the remote without the token
    # and fails ("could not fetch ... from promisor remote") whenever a recent
    # commit renamed a file. Only the names are needed here.
    out = await _git(
        "log", f"--since={cutoff.isoformat()}", "--name-only", "--no-renames", "--format=", cwd=root
    )
    return frozenset(line.strip() for line in out.splitlines() if line.strip())


def backoff_delay(failures: int) -> timedelta:
    delay = BACKOFF_BASE * float(2 ** min(max(failures - BACKOFF_AFTER, 0), 16))
    return min(delay, BACKOFF_MAX)


@dataclass(frozen=True)
class _Unit:
    """What one episode holds: a section of a document, or a whole legacy document."""

    path: str
    text: str
    context: str = ""


def _units(
    docs: dict[str, tuple[str, str]], recorded: dict[str, plan.Recorded], settings: Settings
) -> tuple[dict[str, _Unit], list[plan.Current]]:
    """Episode key -> unit, and the keyed hashes the diff compares.

    A document still recorded as one whole-document episode with its content
    unchanged stays that way; once it changes it is replaced by its sections.
    """
    units: dict[str, _Unit] = {}
    current: list[plan.Current] = []
    for path, (brain, text) in docs.items():
        graph = brains.graph_for(brain)
        settings.check_graph(graph)
        legacy = recorded.get(path)
        whole = plan.content_hash(text)
        if legacy and (legacy.hash, legacy.graph) == (whole, graph):
            units[path] = _Unit(path, text)
            current.append(plan.Current(path=path, hash=whole, graph=graph))
            continue
        title = sections.title_of(path, text)
        for sec in sections.split(path, text):
            context = f"{title} > {sec.heading_path}" if sec.heading_path else title
            units[sec.key] = _Unit(path, sec.text, context)
            current.append(plan.Current(path=sec.key, hash=sec.hash, graph=graph))
    return units, current


def _migrated(
    path: str,
    units: dict[str, _Unit],
    current: list[plan.Current],
    recorded: dict[str, plan.Recorded],
) -> bool:
    """True once every section of a document is recorded at its current hash.

    A whole-document episode of a document that still exists is removed only then,
    so a section that failed, was deferred or was rejected never leaves a gap.
    """
    return all(
        (rec := recorded.get(c.path)) is not None and rec.hash == c.hash
        for c in current
        if units[c.path].path == path
    )


def _attempt_deadline() -> float | None:
    """The monotonic time at which Temporal kills this attempt; None outside an activity."""
    try:
        info = activity.info()
    except RuntimeError:
        return None
    if info.start_to_close_timeout is None:
        return None
    elapsed = (datetime.now(UTC) - info.started_time).total_seconds()
    return time.monotonic() + info.start_to_close_timeout.total_seconds() - elapsed


def _attempt_margin() -> float:
    """Seconds to keep free beyond the slowest batch; 0 outside an activity."""
    try:
        timeout = activity.info().start_to_close_timeout
    except RuntimeError:
        return 0.0
    return timeout.total_seconds() * TIME_MARGIN_FRACTION if timeout else 0.0


def _is_last_attempt() -> bool:
    try:
        return activity.info().attempt >= MAX_ATTEMPTS
    except RuntimeError:
        return True


async def _note_failed_run(
    settings: Settings, project: projects.Project, err: ApplicationError
) -> None:
    """Counts a failed run toward the project's backoff; an attempt Temporal will retry does not."""
    if not (err.non_retryable or _is_last_attempt()):
        return
    store = StateStore(settings)
    try:
        failures = (current.failures if (current := await store.backoff(project.name)) else 0) + 1
        until = datetime.now(UTC) + backoff_delay(failures)
        await store.put_backoff(project.name, plan.Backoff(failures, until.isoformat()))
    except Exception as store_err:  # noqa: BLE001 -- the sync's own error is the one to report
        _log_failure("backoff not recorded", project, store_err)
    finally:
        await store.close()


@activity.defn
async def sync_repo(ref: ProjectRef) -> SyncResult:
    settings = Settings.from_env()
    project = _load(settings).project(ref.name)
    try:
        return await _sync(settings, project, ref.low_balance)
    except ApplicationError as err:
        await _note_failed_run(settings, project, err)
        raise
    except GitError as err:
        # One unreachable or broken repo must not fail the run for the projects after it.
        _log_failure("git failed, project skipped", project, err)
        metrics.SYNC_SKIPPED.labels(project.name, "git").inc()
        return SyncResult(project=project.name, skipped_reason="git")
    except Exception as err:  # noqa: BLE001 -- replaced by a fixed-message error below
        safe = _safe_error(project, err)
        await _note_failed_run(settings, project, safe)
        raise safe from None


def _log_failure(
    message: str, project: projects.Project, err: BaseException, path: str | None = None
) -> None:
    """Logs the error type always, the path and traceback only for non-sensitive projects."""
    kind = type(err).__name__
    if project.sensitive:
        logger.error(message, extra={"project": project.name, "error": kind})
    else:
        logger.error(
            message, extra={"project": project.name, "path": path, "error": kind}, exc_info=err
        )


def count_outcomes(result: SyncResult) -> None:
    """Add one sync result's per-outcome document counts to the metrics."""
    for outcome in metrics.INGEST_OUTCOMES:
        if n := getattr(result, outcome, 0):
            metrics.INGEST_DOCUMENTS.labels(result.project, outcome).inc(n)


def report_rejection(
    result: SyncResult,
    project: projects.Project,
    path: str,
    findings: Sequence[scanner.Finding],
) -> None:
    """Records why the scanner rejected one document: rule kinds and lines, never the match."""
    lines: dict[str, list[int]] = {}
    for f in findings:
        lines.setdefault(f.kind, []).append(f.line)
    for kind in lines:
        metrics.INGEST_REJECTIONS.labels(project.name, kind).inc()
        result.rejected_rule_counts[kind] = result.rejected_rule_counts.get(kind, 0) + 1
    extra: dict[str, object] = {"project": project.name, "rules": lines}
    if not project.sensitive:
        extra["path"] = path
        result.rejected_rules[path] = sorted(lines)
    logger.info("document rejected by scanner", extra=extra)


def _safe_error(
    project: projects.Project, err: BaseException, path: str | None = None
) -> ApplicationError:
    """An error fit for Temporal history: no exception text, no paths for sensitive projects."""
    kind = type(err).__name__
    where = f" at {path}" if path and not project.sensitive else ""
    _log_failure("sync failed", project, err, path)
    message = str(err) if isinstance(err, GitError) else f"{kind}{where}"
    return ApplicationError(
        f"sync of {project.name} failed: {message}", type=kind, non_retryable=is_outage(err)
    )


def sync_marker(commit: str, project: projects.Project, settings: Settings) -> str:
    """Everything a complete sync depended on; skipping is safe only while all of it is the same.

    The package version is in it so a release that changes collection or
    scanning re-diffs every project once.
    """
    basis = [
        commit,
        project.branch,
        sorted((r.path, r.brain) for r in project.ingest),
        sorted(settings.allowed_graphs),
        version("ki-kb"),
    ]
    return hashlib.sha256(json.dumps(basis).encode()).hexdigest()


async def _remote_head(project: projects.Project, token: str) -> str | None:
    """The branch's commit on the remote, or None if unreadable, in which case sync clones."""
    try:
        out = await _git("ls-remote", project.repo, f"refs/heads/{project.branch}", token=token)
    except GitTimeoutError:
        raise
    except GitError:
        return None
    return out.split()[0] if out else None


async def _sync(
    settings: Settings, project: projects.Project, low_balance: bool | None = None
) -> SyncResult:
    token = settings.git_token(project.identity.credential)
    store = StateStore(settings)
    try:
        previous = await store.backoff(project.name)
        if (
            previous
            and previous.failures >= BACKOFF_AFTER
            and datetime.fromisoformat(previous.until) > datetime.now(UTC)
        ):
            logger.info(
                "sync deferred, recent runs failed",
                extra={"project": project.name, "failures": previous.failures},
            )
            return SyncResult(project=project.name, deferred_reason="backoff")
        result = await _run(settings, project, store, token, low_balance)
        if previous:
            await store.clear_backoff(project.name)
        return result
    finally:
        await store.close()


async def _run(
    settings: Settings,
    project: projects.Project,
    store: StateStore,
    token: str,
    low_balance: bool | None,
) -> SyncResult:
    head = await _remote_head(project, token)
    if head and await store.synced(project.name) == sync_marker(head, project, settings):
        return SyncResult(project=project.name, commit=head, skipped=True)
    result = await _clone_and_ingest(settings, project, store, token, low_balance)
    # Deferred and failed documents still need a later run, so that run must
    # not be skipped; failed ones are only retried once content or version moves.
    if result.deferred == 0 and result.failed == 0:
        await store.mark_synced(project.name, sync_marker(result.commit, project, settings))
    return result


async def _clone_and_ingest(
    settings: Settings,
    project: projects.Project,
    store: StateStore,
    token: str,
    low_balance: bool | None = None,
) -> SyncResult:
    aliases = Aliases.load(settings.aliases_file) if settings.aliases_file else Aliases({})
    result = SyncResult(project=project.name)
    graphitis: dict[str, Graphiti] = {}

    try:
        with tempfile.TemporaryDirectory(prefix="ki-sync-") as tmp:
            root = Path(tmp) / "repo"
            result.commit, committed = await _checkout(project, root, token)
            recent = await _recently_committed(root, settings.ingest_quiet_minutes)

            docs = collect_docs(root, project)
            recorded = await store.all(project.name)
            units, current = _units(docs, recorded, settings)
            todo = plan.diff(current, recorded)
            result.unchanged = todo.unchanged
            worker_version = version("ki-kb")
            failures = await store.failures(project.name)
            changed = todo.add + [d for d, _ in todo.replace]
            known = {
                d.path
                for d in changed
                if plan.failed_before(d, failures.get(d.path), worker_version)
            }
            # Records for documents that changed since, or were deleted.
            await store.drop_failures(project.name, sorted(set(failures) - known))

            # Bulk: flagged `bulk: true`, or more documents waiting for extraction
            # than the threshold. Bulk work waits for off-peak and enough balance;
            # anything smaller, such as an edited note, goes through at any hour.
            def note(paths: list[str], key: str) -> None:
                """Reports a document once, however many of its sections are affected."""
                if (path := units[key].path) not in paths:
                    paths.append(path)

            def is_recent(key: str) -> bool:
                return units[key].path in recent

            waiting = {d.path for d in changed if not is_recent(d.path) and d.path not in known}
            held: set[str] = set()
            reason = None
            is_bulk = bool(waiting) and (
                project.bulk or len(waiting) > settings.ingest_bulk_threshold
            )
            if is_bulk:
                reason = await bulk_hold(settings, low_balance)
                if reason:
                    held = waiting
                    logger.info(
                        "bulk ingest waits",
                        extra={"project": project.name, "reason": reason, "documents": len(held)},
                    )

            for doc in changed:
                if is_recent(doc.path) or doc.path in held:
                    result.deferred += 1
                    if not project.sensitive:
                        note(result.deferred_paths, doc.path)
                elif doc.path in known:
                    result.failed += 1
                    if not project.sensitive:
                        note(result.failed_paths, doc.path)
            if result.deferred:
                result.deferred_reason = reason or "quiet"

            def skipped(key: str) -> bool:
                return is_recent(key) or key in known or key in held

            todo.add = [d for d in todo.add if not skipped(d.path)]
            todo.replace = [(d, old) for d, old in todo.replace if not skipped(d.path)]
            if todo.empty:
                return result

            def g(graph: str) -> Graphiti:
                if graph not in graphitis:
                    graphitis[graph] = build_graphiti(settings, graph)
                return graphitis[graph]

            deadline = _attempt_deadline()
            margin = _attempt_margin()
            slowest_batch = 0.0
            balance_check_lock = asyncio.Lock()
            last_balance_check = time.monotonic()
            balance_depleted = False

            async def balance_holds_next_batch() -> bool:
                """Recheck an active bulk sync at most once per configured interval."""
                nonlocal last_balance_check, balance_depleted
                if not is_bulk or not settings.llm_balance_url:
                    return False
                async with balance_check_lock:
                    if balance_depleted:
                        return True
                    now = time.monotonic()
                    interval = settings.llm_balance_check_interval_minutes * 60
                    if now - last_balance_check < interval:
                        return False
                    last_balance_check = now
                    balance_depleted = await balance.balance_low(settings)
                    return balance_depleted

            def episode(doc: plan.Current) -> RawEpisode:
                unit = units[doc.path]
                kind = kinds.kind_for(unit.path)
                return RawEpisode(
                    name=f"{project.name}/{doc.path}",
                    content=aliases.apply(unit.text),
                    source_description=kinds.source_description(
                        kind, project.name, doc.path, unit.context
                    ),
                    source=EpisodeType.text,
                    reference_time=committed,
                )

            async def fail(doc: plan.Current, err: BaseException) -> None:
                _log_failure("document failed", project, err, doc.path)
                await store.put_failure(
                    project.name,
                    doc.path,
                    plan.Failed(
                        hash=doc.hash,
                        graph=doc.graph,
                        version=worker_version,
                        error=type(err).__name__,
                    ),
                )
                result.failed += 1
                if not project.sensitive:
                    note(result.failed_paths, doc.path)

            async def add_one(doc: plan.Current) -> str | None:
                """The new episode's uuid, or None once a failed extraction is recorded."""
                ep = episode(doc)
                started = time.monotonic()
                try:
                    res = await g(doc.graph).add_episode(
                        name=ep.name,
                        episode_body=ep.content,
                        source_description=ep.source_description,
                        reference_time=ep.reference_time,
                        source=ep.source,
                        group_id=doc.graph,
                        entity_types=ENTITY_TYPES,
                        custom_extraction_instructions=EXTRACTION_INSTRUCTIONS,
                    )
                except Exception as err:  # noqa: BLE001 -- outages fail the sync, others the doc
                    if is_outage(err):
                        raise _safe_error(project, err, doc.path) from None
                    await fail(doc, err)
                    return None
                metrics.INGEST_SECONDS.labels(project.name).observe(time.monotonic() - started)
                return str(res.episode.uuid)

            async def clear_leftovers(graph: str, names: list[str]) -> None:
                """Removes episodes with these names that the state does not record.

                Bulk saves the episodes before extracting. A failed bulk call cleans up
                after itself, but when the database itself went down (a crash, an outage)
                that clean-up fails too, and every retry would add another copy. So the
                same clean-up also runs before each attempt.
                """
                keep = {rec.episode for rec in recorded.values()}
                for uuid in await _episodes_named(g(graph), graph, names) - keep:
                    await g(graph).remove_episode(uuid)

            async def add_batch(
                graph: str,
                batch: list[plan.Current],
                record_added: Callable[[plan.Current, str], Awaitable[None]],
            ) -> dict[str, str | None]:
                """Path -> episode uuid, None for a failed document; a failed bulk call is
                split in halves, so a bad document costs about log2(batch) extra calls."""
                by_name = {f"{project.name}/{d.path}": d.path for d in batch}
                await clear_leftovers(graph, list(by_name))
                if len(batch) == 1:
                    doc = batch[0]
                    uuid = await add_one(doc)
                    if uuid is not None:
                        await record_added(doc, uuid)
                    return {doc.path: uuid}
                started = time.monotonic()
                waited = 0.0
                retry = 0
                while True:
                    try:
                        res = await g(graph).add_episode_bulk(
                            [episode(d) for d in batch],
                            group_id=graph,
                            entity_types=ENTITY_TYPES,
                            custom_extraction_instructions=EXTRACTION_INSTRUCTIONS,
                        )
                        break
                    except Exception as err:  # noqa: BLE001 -- classify outage vs content failure
                        if is_rate_limited(err):
                            if (
                                retry >= settings.ingest_rate_limit_attempts
                                or waited >= settings.ingest_rate_limit_max_wait_seconds
                            ):
                                raise _safe_error(project, err) from None
                            delay = min(
                                RATE_LIMIT_BASE_SECONDS * 2**retry + random.SystemRandom().random(),
                                MAX_RATE_LIMIT_SLEEP_SECONDS,
                                settings.ingest_rate_limit_max_wait_seconds - waited,
                            )
                            if delay <= 0:
                                raise _safe_error(project, err) from None
                            retry += 1
                            logger.warning(
                                "bulk add rate limited, retrying",
                                extra={
                                    "attempt": retry,
                                    "delay_seconds": delay,
                                    "project": project.name,
                                    "graph": graph,
                                },
                            )
                            # A failed bulk call may have saved episodes before extraction.
                            await clear_leftovers(graph, list(by_name))
                            await _sleep_rate_limit(delay)
                            waited += delay
                            continue
                        if is_outage(err):
                            raise _safe_error(project, err) from None
                        logger.warning(
                            "bulk add failed, splitting the batch",
                            extra={
                                "project": project.name,
                                "graph": graph,
                                "documents": len(batch),
                                "error": type(err).__name__,
                            },
                        )
                        # Bulk saves episodes before extracting; the halves clean them first.
                        mid = len(batch) // 2
                        halves = await add_batch(graph, batch[:mid], record_added)
                        halves.update(await add_batch(graph, batch[mid:], record_added))
                        return halves
                added = {by_name[e.name]: str(e.uuid) for e in res.episodes if e.name in by_name}
                share = (time.monotonic() - started) / len(batch)
                for d in batch:
                    uuid = added.get(d.path)
                    if uuid is None:
                        await fail(d, MissingEpisodeError())
                    else:
                        metrics.INGEST_SECONDS.labels(project.name).observe(share)
                        await record_added(d, uuid)
                return {d.path: added.get(d.path) for d in batch}

            Pending = list[tuple[plan.Current, plan.Recorded | None]]

            async def lane(graph: str, items: Pending) -> None:
                """One graph's documents in order, batch after batch: Graphiti resolves each
                batch against everything already in that graph."""
                nonlocal slowest_batch
                size = settings.ingest_batch

                def defer(remaining: Pending, reason: str) -> None:
                    result.deferred += len(remaining)
                    if not project.sensitive:
                        for doc, _ in remaining:
                            note(result.deferred_paths, doc.path)
                    result.deferred_reason = reason

                for i in range(0, len(items), size):
                    if await balance_holds_next_batch():
                        defer(items[i:], "balance")
                        return
                    # Temporal kills the attempt at its timeout and the batch in flight
                    # is paid for and lost, so stop unless the slowest batch so far, plus a
                    # margin for one slower still, fits in what is left.
                    if deadline is not None and (
                        deadline - time.monotonic() < slowest_batch + margin
                    ):
                        defer(items[i:], "time")
                        return
                    chunk = items[i : i + size]
                    old_by_path = {doc.path: old for doc, old in chunk}

                    async def record_added(
                        doc: plan.Current,
                        uuid: str,
                        old_by_path: dict[str, plan.Recorded | None] = old_by_path,
                    ) -> None:
                        nonlocal recorded
                        await store.put(
                            project.name,
                            doc.path,
                            plan.Recorded(hash=doc.hash, episode=uuid, graph=doc.graph),
                        )
                        recorded[doc.path] = plan.Recorded(
                            hash=doc.hash, episode=uuid, graph=doc.graph
                        )
                        old = old_by_path[doc.path]
                        if old is None:
                            result.added += 1
                        else:
                            # Record the new version before removing the old episode.
                            await g(old.graph).remove_episode(old.episode)
                            result.replaced += 1

                    batch_started = time.monotonic()
                    await add_batch(graph, [d for d, _ in chunk], record_added)
                    slowest_batch = max(slowest_batch, time.monotonic() - batch_started)

            lanes: dict[str, Pending] = {}
            pending = [(d, None) for d in todo.add] + list(todo.replace)
            scanned: dict[str, list[scanner.Finding]] = {}
            for doc, old in pending:
                path = units[doc.path].path
                if path not in scanned:
                    scanned[path] = scanner.scan(docs[path][1])
                    if scanned[path]:
                        report_rejection(result, project, path, scanned[path])
                if scanned[path]:
                    result.rejected += 1
                    if not project.sensitive:
                        note(result.rejected_paths, doc.path)
                    continue
                lanes.setdefault(doc.graph, []).append((doc, old))

            # Graphs share no entities, so their lanes run side by side. A lane that
            # hits an outage cancels the others before the Graphiti clients are closed.
            async with _heartbeat():
                try:
                    async with asyncio.TaskGroup() as tg:
                        for graph, items in lanes.items():
                            tg.create_task(lane(graph, items))
                except ExceptionGroup as group:
                    raise group.exceptions[0] from None
            result.failed_paths.sort()

            slowest_removal = 0.0
            for i, (path, old) in enumerate(todo.remove):
                if path in docs and not _migrated(path, units, current, recorded):
                    continue
                # Same guard as the batches: a removal cut off by the timeout is
                # retried next run, but only the records left in place let it be found.
                if deadline is not None and (
                    deadline - time.monotonic() < slowest_removal + margin
                ):
                    left = [
                        p
                        for p, _ in todo.remove[i:]
                        if p not in docs or _migrated(p, units, current, recorded)
                    ]
                    result.deferred += len(left)
                    if not project.sensitive:
                        # A removed section is no longer in units; its key starts with the path.
                        for file in dict.fromkeys(p.partition("#")[0] for p in left):
                            if file not in result.deferred_paths:
                                result.deferred_paths.append(file)
                    result.deferred_reason = "time"
                    break
                activity.heartbeat("remove")
                removal_started = time.monotonic()
                await g(old.graph).remove_episode(old.episode)
                await store.delete(project.name, path)
                result.removed += 1
                slowest_removal = max(slowest_removal, time.monotonic() - removal_started)

            count_outcomes(result)
            return result
    finally:
        for gi in graphitis.values():
            await gi.close()  # type: ignore[no-untyped-call]
