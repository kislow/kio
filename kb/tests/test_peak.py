"""Tests for peak window parsing and containment."""

from datetime import UTC, datetime, timedelta, timezone

import pytest

from ki_kb.peak import PeakSchedule, parse_peak_windows, parse_time_range


def test_empty_windows():
    sched = parse_peak_windows("")
    assert sched == PeakSchedule(())
    assert not sched.is_peak(datetime(2026, 10, 26, 2, 0, tzinfo=UTC))

    sched_spaces = parse_peak_windows("   ")
    assert sched_spaces == PeakSchedule(())
    assert not sched_spaces.is_peak()

    sched_commas = parse_peak_windows(",,,")
    assert sched_commas == PeakSchedule(())


def test_is_peak_now():
    sched = parse_peak_windows("Mon-Fri 01:00-04:00")
    # Calling is_peak() with no args uses datetime.now(UTC)
    assert isinstance(sched.is_peak(), bool)


def test_standard_deepseek_windows():
    spec = "Mon-Fri 01:00-04:00,06:00-10:00"
    sched = parse_peak_windows(spec)
    assert len(sched.windows) == 2

    # Monday 01:00 UTC is peak
    assert sched.is_peak(datetime(2026, 10, 26, 1, 0, tzinfo=UTC))
    # Monday 03:59 UTC is peak
    assert sched.is_peak(datetime(2026, 10, 26, 3, 59, tzinfo=UTC))
    # Monday 04:00 UTC is off-peak
    assert not sched.is_peak(datetime(2026, 10, 26, 4, 0, tzinfo=UTC))
    # Monday 05:00 UTC is off-peak
    assert not sched.is_peak(datetime(2026, 10, 26, 5, 0, tzinfo=UTC))
    # Monday 06:00 UTC is peak
    assert sched.is_peak(datetime(2026, 10, 26, 6, 0, tzinfo=UTC))
    # Monday 10:00 UTC is off-peak
    assert not sched.is_peak(datetime(2026, 10, 26, 10, 0, tzinfo=UTC))

    # Saturday 02:00 UTC is off-peak (weekend)
    assert not sched.is_peak(datetime(2026, 10, 31, 2, 0, tzinfo=UTC))
    # Sunday 07:00 UTC is off-peak (weekend)
    assert not sched.is_peak(datetime(2026, 11, 1, 7, 0, tzinfo=UTC))


def test_day_range_wrap_around():
    # Fri-Mon wraps around Friday, Saturday, Sunday, Monday
    spec = "Fri-Mon 02:00-05:00"
    sched = parse_peak_windows(spec)

    # Friday (weekday 4)
    assert sched.is_peak(datetime(2026, 10, 30, 3, 0, tzinfo=UTC))
    # Saturday (weekday 5)
    assert sched.is_peak(datetime(2026, 10, 31, 3, 0, tzinfo=UTC))
    # Sunday (weekday 6)
    assert sched.is_peak(datetime(2026, 11, 1, 3, 0, tzinfo=UTC))
    # Monday (weekday 0)
    assert sched.is_peak(datetime(2026, 11, 2, 3, 0, tzinfo=UTC))
    # Tuesday (weekday 1) - off-peak
    assert not sched.is_peak(datetime(2026, 11, 3, 3, 0, tzinfo=UTC))


def test_time_wrap_around():
    # Mon 22:00-04:00: starts Monday 22:00, ends Tuesday 04:00
    spec = "Mon 22:00-04:00"
    sched = parse_peak_windows(spec)

    # Monday 21:59 UTC - off-peak
    assert not sched.is_peak(datetime(2026, 10, 26, 21, 59, tzinfo=UTC))
    # Monday 22:00 UTC - peak
    assert sched.is_peak(datetime(2026, 10, 26, 22, 0, tzinfo=UTC))
    # Monday 23:30 UTC - peak
    assert sched.is_peak(datetime(2026, 10, 26, 23, 30, tzinfo=UTC))
    # Tuesday 02:00 UTC - peak
    assert sched.is_peak(datetime(2026, 10, 27, 2, 0, tzinfo=UTC))
    # Tuesday 03:59 UTC - peak
    assert sched.is_peak(datetime(2026, 10, 27, 3, 59, tzinfo=UTC))
    # Tuesday 04:00 UTC - off-peak
    assert not sched.is_peak(datetime(2026, 10, 27, 4, 0, tzinfo=UTC))
    # Monday 02:00 UTC - off-peak (belongs to Sunday night which is not in schedule)
    assert not sched.is_peak(datetime(2026, 10, 26, 2, 0, tzinfo=UTC))


def test_naive_datetime_treated_as_utc():
    sched = parse_peak_windows("Mon 01:00-04:00")
    # Naive datetime
    assert sched.is_peak(datetime(2026, 10, 26, 2, 0))


@pytest.mark.parametrize(
    "invalid",
    [
        "invalid",
        "Mon-Fri",
        "01:00-04:00",
        "Mon-Fri 25:00-04:00",
        "Mon-Fri 01:60-04:00",
        "Mon-Fri 01:00-01:00",
        "Foo-Fri 01:00-04:00",
        "Mon-Bar 01:00-04:00",
        "Mon-Fri-Sat 01:00-04:00",
        "Mon-Fri 01:00",
        "Mon-Fri 01:00-04:00-06:00",
        "Mon-Fri 01:00",
    ],
)
def test_malformed_spec_raises(invalid):
    with pytest.raises(ValueError):
        parse_peak_windows(invalid)


def test_parse_time_range_invalid():
    with pytest.raises(ValueError):
        parse_time_range("single")


def test_weekday_window_past_midnight_runs_into_saturday():
    sched = parse_peak_windows("Mon-Fri 22:00-02:00")
    assert sched.is_peak(datetime(2026, 10, 31, 1, 0, tzinfo=UTC))  # Saturday, from Friday
    assert not sched.is_peak(datetime(2026, 10, 31, 22, 0, tzinfo=UTC))  # Saturday evening
    assert not sched.is_peak(datetime(2026, 10, 26, 1, 0, tzinfo=UTC))  # Monday, from Sunday


def test_days_carry_over_and_change_between_clauses():
    sched = parse_peak_windows("mon 01:00-02:00, 03:00-04:00; Sat 05:00-06:00")
    assert [sorted(w.days) for w in sched.windows] == [[0], [0], [5]]
    assert sched.is_peak(datetime(2026, 10, 26, 3, 30, tzinfo=UTC))
    assert sched.is_peak(datetime(2026, 10, 31, 5, 30, tzinfo=UTC))
    assert not sched.is_peak(datetime(2026, 10, 31, 1, 30, tzinfo=UTC))


def test_other_timezones_are_converted_to_utc():
    sched = parse_peak_windows("Mon-Fri 06:00-10:00")
    dubai = timezone(timedelta(hours=4))
    assert sched.is_peak(datetime(2026, 10, 26, 13, 59, tzinfo=dubai))  # 09:59 UTC
    assert not sched.is_peak(datetime(2026, 10, 26, 14, 0, tzinfo=dubai))  # 10:00 UTC
