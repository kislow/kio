"""Split a Markdown document into sections, one episode each.

A section runs from a heading to the next heading of any level, and is keyed
`path#heading-path`. Its hash covers normalized text, so a reflow or a
whitespace fix is not a change and costs no extraction.
"""

from __future__ import annotations

import hashlib
import re
from collections.abc import Iterator
from dataclasses import dataclass

_HEADING = re.compile(r"^ {0,3}(#{1,6})[ \t]+(.+?)[ \t]*#*[ \t]*$")
_FENCE = re.compile(r"^ {0,3}(`{3,}|~{3,})")
_FRONT_MATTER_TITLE = re.compile(r"^title:\s*[\"']?(.+?)[\"']?\s*$", re.MULTILINE)
_SPACE = re.compile(r"\s+")


@dataclass(frozen=True)
class Section:
    key: str
    heading_path: str
    text: str
    hash: str


def normalize(text: str) -> str:
    """Whitespace and line wrapping collapsed to single spaces."""
    return _SPACE.sub(" ", text).strip()


def normalized_hash(text: str) -> str:
    return hashlib.sha256(normalize(text).encode("utf-8")).hexdigest()


def _front_matter_lines(lines: list[str]) -> int:
    """Number of leading lines taken by a `---` front matter block, 0 if none."""
    if not lines or lines[0].strip() != "---":
        return 0
    for i in range(1, len(lines)):
        if lines[i].strip() in ("---", "..."):
            return i + 1
    return 0


def _headings(lines: list[str]) -> Iterator[tuple[int, int, str]]:
    """(line index, level, text) of each heading outside fenced code."""
    fence = ""
    for i, line in enumerate(lines):
        if m := _FENCE.match(line):
            marker = m.group(1)
            if not fence:
                fence = marker
            elif marker[0] == fence[0] and len(marker) >= len(fence):
                fence = ""
        elif not fence and (h := _HEADING.match(line)):
            yield i, len(h.group(1)), h.group(2)


def title_of(path: str, text: str) -> str:
    """The front matter title, else the first level-1 heading, else the file name."""
    lines = text.splitlines()
    skip = _front_matter_lines(lines)
    if skip and (m := _FRONT_MATTER_TITLE.search("\n".join(lines[:skip]))):
        return m.group(1)
    for _, level, heading in _headings(lines[skip:]):
        if level == 1:
            return heading
    return path.rsplit("/", 1)[-1]


def split(path: str, text: str) -> list[Section]:
    """The document's sections in order.

    Text before the first heading, front matter included, is a section of its
    own keyed `path#`. A heading with no text of its own, such as a title right
    above a subheading, is not a section: the sections under it carry it in
    their heading path. A repeated heading path gets a numeric suffix so keys
    stay unique.
    """
    lines = text.splitlines()
    start = _front_matter_lines(lines)
    bounds = [(i + start, level, h) for i, level, h in _headings(lines[start:])]
    first = bounds[0][0] if bounds else len(lines)
    chunks: list[tuple[str, str]] = []
    if "\n".join(lines[:first]).strip():
        chunks.append(("", "\n".join(lines[:first])))
    stack: list[tuple[int, str]] = []
    for n, (i, level, heading) in enumerate(bounds):
        while stack and stack[-1][0] >= level:
            stack.pop()
        stack.append((level, heading))
        end = bounds[n + 1][0] if n + 1 < len(bounds) else len(lines)
        if not "\n".join(lines[i + 1 : end]).strip():
            continue
        chunks.append((" > ".join(h for _, h in stack), "\n".join(lines[i:end])))

    sections: list[Section] = []
    seen: dict[str, int] = {}
    for heading_path, body in chunks:
        key = f"{path}#{heading_path}"
        seen[key] = seen.get(key, 0) + 1
        if seen[key] > 1:
            key = f"{key}~{seen[key]}"
        sections.append(Section(key, heading_path, body, normalized_hash(body)))
    return sections
