"""Hash-based diffing: decide which documents to add, replace or remove.

Pure functions, so the expensive part (LLM extraction) only ever runs for
documents whose content actually changed.
"""

from __future__ import annotations

import hashlib
from dataclasses import dataclass, field


def content_hash(text: str) -> str:
    return hashlib.sha256(text.encode("utf-8")).hexdigest()


@dataclass(frozen=True)
class Recorded:
    """What the state store remembers about an ingested document."""

    hash: str
    episode: str
    graph: str


@dataclass(frozen=True)
class Failed:
    """A document whose extraction failed, and what it was tried with."""

    hash: str
    graph: str
    version: str
    error: str


@dataclass(frozen=True)
class Backoff:
    """A project whose last runs failed: how many in a row, and when it may run again."""

    failures: int
    until: str


@dataclass(frozen=True)
class Current:
    """A document as it exists in the repo now."""

    path: str
    hash: str
    graph: str


@dataclass
class Plan:
    add: list[Current] = field(default_factory=list)
    replace: list[tuple[Current, Recorded]] = field(default_factory=list)
    remove: list[tuple[str, Recorded]] = field(default_factory=list)
    unchanged: int = 0

    @property
    def empty(self) -> bool:
        return not (self.add or self.replace or self.remove)


def diff(current: list[Current], recorded: dict[str, Recorded]) -> Plan:
    plan = Plan()
    seen = set()
    for doc in sorted(current, key=lambda d: d.path):
        seen.add(doc.path)
        old = recorded.get(doc.path)
        if old is None:
            plan.add.append(doc)
        elif old.hash != doc.hash or old.graph != doc.graph:
            plan.replace.append((doc, old))
        else:
            plan.unchanged += 1
    for path in sorted(recorded):
        if path not in seen:
            plan.remove.append((path, recorded[path]))
    return plan


def failed_before(doc: Current, failed: Failed | None, worker_version: str) -> bool:
    """True if this exact content already failed in this graph with this worker version.

    Retrying it would pay for the same extraction and fail the same way; a new
    content hash, a new graph or a release is what makes another attempt worthwhile.
    """
    return failed is not None and (failed.hash, failed.graph, failed.version) == (
        doc.hash,
        doc.graph,
        worker_version,
    )
