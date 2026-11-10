"""Which section of a context pack a document's facts belong to.

The kind is decided from the document path at ingest time and stored on the
episode (source_description = "<kind>:<repo>/<path>"), so kb-query can label
every fact without guessing from its text.
"""

from __future__ import annotations

DECISION = "decision"
CONVENTION = "convention"
STATE = "state"
POINTER = "pointer"

KINDS = (DECISION, CONVENTION, STATE, POINTER)

_RULES: list[tuple[tuple[str, ...], str]] = [
    (("decisions/", "adr/", "adrs/", "docs/adr/", "docs/decisions/"), DECISION),
    (
        ("conventions/", "docs/conventions/", "standards/", "CONVENTIONS.md", "CONTRIBUTING.md"),
        CONVENTION,
    ),
    (("architecture/", "docs/architecture/", "services/", "runbooks/", "docs/runbooks/"), STATE),
]


def kind_for(rel_path: str) -> str:
    lower = rel_path.lower()
    for prefixes, kind in _RULES:
        for prefix in prefixes:
            p = prefix.lower()
            if lower == p or lower.startswith(p) or f"/{p}" in f"/{lower}":
                return kind
    return POINTER


def source_description(kind: str, repo: str, rel_path: str, context: str = "") -> str:
    """`context` (document title and heading path of a section) trails the source."""
    base = f"{kind}:{repo}/{rel_path}"
    return f"{base} | {context}" if context else base


def parse_source_description(value: str) -> tuple[str, str]:
    """(kind, `repo/path`), without the section context, which is only for extraction."""
    kind, _, rest = value.partition(":")
    source = rest.split(" | ", 1)[0]
    return (kind if kind in KINDS else POINTER), source
