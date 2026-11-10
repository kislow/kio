"""Structured (JSON) logging setup using only the standard library.

Call ``configure_logging()`` once from ``main()``. In modules use:

    import logging
    logger = logging.getLogger(__name__)
    logger.info("fetched user", extra={"user_id": user_id, "duration_ms": 12})
"""

from __future__ import annotations

import json
import logging
import os
import sys
from datetime import UTC, datetime
from typing import Any

# Attributes every LogRecord has; anything else came from ``extra=`` and is emitted as a field.
_RESERVED = set(vars(logging.makeLogRecord({}))) | {"message", "asctime", "taskName"}


class JsonFormatter(logging.Formatter):
    """Format records as one JSON object per line."""

    def format(self, record: logging.LogRecord) -> str:
        payload: dict[str, Any] = {
            "ts": datetime.fromtimestamp(record.created, tz=UTC).isoformat(),
            "level": record.levelname,
            "logger": record.name,
            "msg": record.getMessage(),
        }
        payload.update({k: v for k, v in record.__dict__.items() if k not in _RESERVED})
        if record.exc_info:
            payload["exc"] = self.formatException(record.exc_info)
        return json.dumps(payload, default=str)


def configure_logging(level: str | None = None) -> None:
    """Send JSON logs to stderr. Level comes from the argument or the LOG_LEVEL env var."""
    handler = logging.StreamHandler(sys.stderr)
    handler.setFormatter(JsonFormatter())
    root = logging.getLogger()
    root.handlers[:] = [handler]
    root.setLevel((level or os.environ.get("LOG_LEVEL", "INFO")).upper())
