"""Peak windows: when the extraction provider bills more, from KI_LLM_PEAK_UTC.

Provider-neutral: the harness parses windows and knows no provider. The format
is comma-separated clauses ``<days> HH:MM-HH:MM``; a clause without days reuses
the previous clause's days. Days are one day or a range (``Mon``, ``Mon-Fri``,
``Fri-Mon`` wraps over the weekend). A window whose end is before its start
runs past midnight into the next day (``Fri 22:00-02:00`` covers Saturday
01:00). Times are UTC, the start inclusive and the end exclusive. Empty means
no windows. Anything else is rejected with a ValueError naming the bad part,
which Settings turns into a start-up error.

DeepSeek (since 2026-08): ``Mon-Fri 01:00-04:00,06:00-10:00``.
"""

from __future__ import annotations

import re
from dataclasses import dataclass
from datetime import UTC, datetime, time

_DAYS = {
    "mon": 0,
    "monday": 0,
    "tue": 1,
    "tuesday": 1,
    "wed": 2,
    "wednesday": 2,
    "thu": 3,
    "thursday": 3,
    "fri": 4,
    "friday": 4,
    "sat": 5,
    "saturday": 5,
    "sun": 6,
    "sunday": 6,
}


def parse_day(name: str) -> int:
    """Map a day name (case-insensitive) to 0 (Monday) .. 6 (Sunday)."""
    key = name.strip().lower()
    if key in _DAYS:
        return _DAYS[key]
    raise ValueError(f"invalid day name {name!r}")


def parse_days(spec: str) -> frozenset[int]:
    """Parse a single day (e.g. 'Mon') or a day range (e.g. 'Mon-Fri' or wrap-around 'Fri-Mon')."""
    parts = spec.strip().split("-")
    if len(parts) == 1:
        return frozenset({parse_day(parts[0])})
    if len(parts) == 2:
        start = parse_day(parts[0])
        end = parse_day(parts[1])
        if start <= end:
            return frozenset(range(start, end + 1))
        # Wrap-around day range: e.g. Fri(4) - Mon(0) -> {4, 5, 6, 0}
        return frozenset(range(start, 7)) | frozenset(range(0, end + 1))
    raise ValueError(f"invalid day range {spec!r}")


_TIME_RE = re.compile(r"^(\d{1,2}):(\d{2})$")


def parse_time(spec: str) -> time:
    """Parse HH:MM into a time object."""
    m = _TIME_RE.match(spec.strip())
    if not m:
        raise ValueError(f"invalid time format {spec!r}, expected HH:MM")
    hour = int(m.group(1))
    minute = int(m.group(2))
    if not (0 <= hour <= 23 and 0 <= minute <= 59):
        raise ValueError(f"time out of range {spec!r}")
    return time(hour, minute)


def parse_time_range(spec: str) -> tuple[time, time]:
    """Parse HH:MM-HH:MM into (start_time, end_time)."""
    parts = spec.strip().split("-")
    if len(parts) != 2:
        raise ValueError(f"invalid time range {spec!r}, expected HH:MM-HH:MM")
    start = parse_time(parts[0])
    end = parse_time(parts[1])
    if start == end:
        raise ValueError(f"time range start and end cannot be equal: {spec!r}")
    return start, end


@dataclass(frozen=True)
class PeakWindow:
    """A recurring UTC peak window across specific days of the week."""

    days: frozenset[int]
    start_time: time
    end_time: time

    def contains(self, dt: datetime) -> bool:
        """True if dt (in UTC) falls within this window [start, end)."""
        dt_utc = dt.astimezone(UTC) if dt.tzinfo is not None else dt.replace(tzinfo=UTC)
        w = dt_utc.weekday()
        t = dt_utc.time()
        if self.start_time < self.end_time:
            return w in self.days and self.start_time <= t < self.end_time
        # Time wrap-around (e.g. 22:00-04:00)
        return (w in self.days and t >= self.start_time) or (
            (w - 1) % 7 in self.days and t < self.end_time
        )


@dataclass(frozen=True)
class PeakSchedule:
    """Collection of peak windows."""

    windows: tuple[PeakWindow, ...] = ()

    def is_peak(self, dt: datetime | None = None) -> bool:
        """True if dt (or current UTC time) falls in any peak window."""
        if not self.windows:
            return False
        if dt is None:
            dt = datetime.now(UTC)
        return any(w.contains(dt) for w in self.windows)


def parse_peak_windows(spec: str) -> PeakSchedule:
    """Parse KI_LLM_PEAK_UTC format into a PeakSchedule.

    Example format: 'Mon-Fri 01:00-04:00,06:00-10:00' or 'Mon-Fri 01:00-04:00, Sat 02:00-04:00'.
    Empty string means no windows.
    """
    raw = spec.strip()
    if not raw:
        return PeakSchedule(())

    # Split by comma or semicolon
    clauses = [c.strip() for c in re.split(r"[,;]", raw) if c.strip()]
    if not clauses:
        return PeakSchedule(())

    windows: list[PeakWindow] = []
    current_days: frozenset[int] | None = None

    for clause in clauses:
        parts = clause.split(None, 1)
        if len(parts) == 2:
            current_days = parse_days(parts[0])
            time_spec = parts[1]
        elif len(parts) == 1:
            if current_days is None:
                raise ValueError(
                    f"clause {clause!r} specifies time range without preceding day specification"
                )
            time_spec = parts[0]
        else:
            raise ValueError(f"malformed peak window clause: {clause!r}")

        start_t, end_t = parse_time_range(time_spec)
        windows.append(PeakWindow(days=current_days, start_time=start_t, end_time=end_t))

    return PeakSchedule(tuple(windows))
