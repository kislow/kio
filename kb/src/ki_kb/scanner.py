"""Pre-ingest scanner. Anything it flags is never embedded or sent to an LLM.

This is a guardrail, not a DLP product: it catches card numbers (Luhn-valid runs
written the way cards are), Emirates IDs and common secret formats. A flagged
section fails the ingest for that section and is reported by its key only.
"""

from __future__ import annotations

import re
from dataclasses import dataclass

# Contiguous, or grouped as 4-4-4-4(-3) and 4-6-5: other digit runs (phone numbers,
# case numbers, version stamps) pass Luhn one time in ten and are not cards.
_PAN = re.compile(
    r"(?<![\d-])(?:\d{13,19}"
    r"|\d{4}(?P<a>[ -])\d{4}(?P=a)\d{4}(?P=a)\d{4}(?:(?P=a)\d{3})?"
    r"|\d{4}(?P<b>[ -])\d{6}(?P=b)\d{5})(?![\d-])"
)
_NOT_A_CARD = re.compile(r"(?:[+.=]|\bCASE )$", re.IGNORECASE)

_PATTERNS: dict[str, re.Pattern[str]] = {
    "private-key": re.compile(r"-----BEGIN (?:RSA |EC |OPENSSH |DSA |PGP )?PRIVATE KEY-----"),
    "aws-access-key": re.compile(r"\b(?:AKIA|ASIA)[0-9A-Z]{16}\b"),
    "github-token": re.compile(
        r"\b(?:ghp|gho|ghu|ghs|ghr)_[A-Za-z0-9]{36,}\b|\bgithub_pat_[A-Za-z0-9_]{40,}\b"
    ),
    "slack-token": re.compile(r"\bxox[abpors]-[A-Za-z0-9-]{10,}\b"),
    "anthropic-key": re.compile(r"\bsk-ant-[A-Za-z0-9_-]{20,}\b"),
    "openai-key": re.compile(r"\bsk-(?:proj-)?[A-Za-z0-9_-]{32,}\b"),
    "jwt": re.compile(r"\beyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\b"),
    "national-id": re.compile(r"(?<![\d-])784-\d{4}-\d{7}-\d(?![\d-])"),
    # A quoted value holding a placeholder bracket or variable sigil is a template.
    "assigned-secret": re.compile(
        r"(?i)\b(?:password|passwd|secret|api[_-]?key|token)\s*[:=]\s*"
        r"['\"](?![^'\"\s]*[<>\[\]${])[^'\"\s]{8,}['\"]"
    ),
    # Unquoted env-style assignments (FALKORDB_PASSWORD=..., api_key: ...). Placeholders
    # such as <token>, $VAR, ${VAR} and {{ var }} are not values and are skipped, and so
    # is prose after a colon ("token: *Contents*", "api_key: required"): a credential
    # is made of key characters only and contains at least one digit. A key ending in
    # Id, Name, Arn, Ref or Path names a secret rather than holding one, and so does
    # an arn: value.
    "assigned-secret-unquoted": re.compile(
        r"(?i)\b(?!\w*(?:id|name|arn|ref|path)\s*[:=])"
        r"\w*(?:password|passwd|secret|api[_-]?key|token)\w*\s*[:=]\s*"
        r"(?![<$'\"{]|arn:)(?=[A-Za-z0-9_\-./+=]*\d)[A-Za-z0-9_\-./+=]{8,}(?![^\s'\"])"
    ),
}


@dataclass(frozen=True)
class Finding:
    kind: str
    line: int


def luhn_ok(digits: str) -> bool:
    total = 0
    for i, ch in enumerate(reversed(digits)):
        d = int(ch)
        if i % 2 == 1:
            d *= 2
            if d > 9:
                d -= 9
        total += d
    return total % 10 == 0


def scan(text: str) -> list[Finding]:
    findings: list[Finding] = []
    for lineno, line in enumerate(text.splitlines(), start=1):
        for m in _PAN.finditer(line):
            if _NOT_A_CARD.search(line, 0, m.start()):
                continue
            digits = re.sub(r"\D", "", m.group())
            if 13 <= len(digits) <= 19 and luhn_ok(digits) and len(set(digits)) > 1:
                findings.append(Finding("card-number", lineno))
        for kind, pattern in _PATTERNS.items():
            if pattern.search(line):
                findings.append(Finding(kind, lineno))
    return findings
