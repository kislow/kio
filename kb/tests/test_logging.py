import json
import logging

from ki_kb.logging_setup import configure_logging


def test_json_lines_with_extra_fields(capsys):
    configure_logging("debug")
    log = logging.getLogger("ki_kb.test")
    log.info("synced", extra={"project": "notes", "added": 2})
    log.error("failed", exc_info=ValueError("boom"))
    lines = [json.loads(line) for line in capsys.readouterr().err.strip().splitlines()]
    assert (
        lines[0]["msg"] == "synced"
        and lines[0]["project"] == "notes"
        and lines[0]["level"] == "INFO"
    )
    assert "ValueError: boom" in lines[1]["exc"]
