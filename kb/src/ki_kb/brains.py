"""Brain names and the FalkorDB graph that stores each one.

A brain is a logical scope (cortex, voice, acme, client). Graphiti only
accepts [A-Za-z0-9_-] in graph names, so brains map to "cortex" and
"brain-<name>".

A family pattern, "client-*", covers every brain named "client-<suffix>"
(never "client" itself). The same rules are in pkg/scope.
"""

from __future__ import annotations

import re
from collections.abc import Iterable

from .errors import ConfigError

__all__ = [
    "CORTEX",
    "ConfigError",
    "graph_for",
    "is_family",
    "matches",
    "matches_any",
    "valid_entry",
    "valid_name",
]

CORTEX = "cortex"
NAME = re.compile(r"^[a-z0-9][a-z0-9-]{0,62}$")


def valid_name(name: str) -> bool:
    return bool(NAME.match(name))


FAMILY_SUFFIX = "-*"


def is_family(entry: str) -> bool:
    return entry.endswith(FAMILY_SUFFIX)


def valid_entry(entry: str) -> bool:
    """A brain name, or a family pattern: a valid name followed by "-*"."""
    if is_family(entry):
        return valid_name(entry[: -len(FAMILY_SUFFIX)])
    return valid_name(entry)


def matches(entry: str, name: str) -> bool:
    if not is_family(entry):
        return entry == name
    prefix = entry[:-1]
    return name.startswith(prefix) and len(name) > len(prefix) and valid_name(name)


def matches_any(entries: Iterable[str], name: str) -> bool:
    return any(matches(e, name) for e in entries)


def graph_for(brain: str) -> str:
    if not valid_name(brain):
        raise ConfigError(f"invalid brain name {brain!r}")
    return CORTEX if brain == CORTEX else f"brain-{brain}"
