"""Family patterns must behave exactly like pkg/scope and deploy/check.py.

All three run the cases in testdata/brains/matches.json.
"""

import json
from pathlib import Path

import pytest

from ki_kb.brains import matches, valid_entry

CASES = json.loads(
    (Path(__file__).resolve().parents[2] / "testdata" / "brains" / "matches.json").read_text()
)


@pytest.mark.parametrize("case", CASES["matches"], ids=lambda c: f"{c['entry']}~{c['name']}")
def test_matches(case):
    assert matches(case["entry"], case["name"]) is case["want"]


@pytest.mark.parametrize("entry", CASES["valid_entries"])
def test_valid_entry(entry):
    assert valid_entry(entry)


@pytest.mark.parametrize("entry", CASES["invalid_entries"])
def test_invalid_entry(entry):
    assert not valid_entry(entry)
