"""Canonical names, applied before Graphiti sees a document.

Graphiti deduplicates entities itself, but "k8s", "K8s" and "Kubernetes"
still split often enough to matter. Rewriting aliases to one canonical name
up front keeps one node per thing across every brain.
"""

from __future__ import annotations

import re
from pathlib import Path

import yaml


class Aliases:
    def __init__(self, table: dict[str, list[str]]) -> None:
        pairs: list[tuple[str, str]] = []
        for canonical, aliases in table.items():
            for alias in aliases or []:
                pairs.append((alias, canonical))
        # Longest alias first so "AKS cluster" style overlaps resolve predictably.
        pairs.sort(key=lambda p: len(p[0]), reverse=True)
        self._rules = [
            (re.compile(rf"(?<![\w/.-]){re.escape(alias)}(?![\w/-])", re.IGNORECASE), canonical)
            for alias, canonical in pairs
        ]

    @classmethod
    def load(cls, path: Path) -> Aliases:
        return cls(yaml.safe_load(path.read_text(encoding="utf-8")) or {})

    def apply(self, text: str) -> str:
        out_lines = []
        in_code = False
        for line in text.splitlines(keepends=True):
            if line.lstrip().startswith("```"):
                in_code = not in_code
            if not in_code:
                for pattern, canonical in self._rules:
                    line = pattern.sub(canonical, line)
            out_lines.append(line)
        return "".join(out_lines)
