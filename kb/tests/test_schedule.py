from datetime import timedelta
from types import SimpleNamespace

import pytest
from temporalio.client import (
    Schedule,
    ScheduleActionStartWorkflow,
    ScheduleAlreadyRunningError,
    ScheduleIntervalSpec,
    ScheduleSpec,
    ScheduleState,
)

from ki_kb import schedule
from ki_kb.errors import ConfigError
from ki_kb.settings import Settings
from ki_kb.workflows import SyncProjects


def existing(every_minutes: int = 5, paused: bool = False, note: str | None = None) -> Schedule:
    return Schedule(
        action=ScheduleActionStartWorkflow(SyncProjects.run, id="run", task_queue="q"),
        spec=schedule.spec_for(every_minutes),
        state=ScheduleState(paused=paused, note=note),
    )


class FakeClient:
    """create_schedule fails when `current` is set; the handle's update edits it in place."""

    def __init__(self, current: Schedule | None = None) -> None:
        self.current = current
        self.created: Schedule | None = None

    async def create_schedule(self, schedule_id: str, sched: Schedule) -> None:
        assert schedule_id == schedule.SCHEDULE_ID
        if self.current is not None:
            raise ScheduleAlreadyRunningError()
        self.created = self.current = sched

    def get_schedule_handle(self, schedule_id: str) -> SimpleNamespace:
        assert schedule_id == schedule.SCHEDULE_ID

        async def update(updater):  # type: ignore[no-untyped-def]
            got = updater(SimpleNamespace(description=SimpleNamespace(schedule=self.current)))
            self.current = got.schedule

        return SimpleNamespace(update=update)


def test_spec_is_an_interval_or_nothing():
    assert schedule.spec_for(5) == ScheduleSpec(
        intervals=[ScheduleIntervalSpec(every=timedelta(minutes=5))]
    )
    assert schedule.spec_for(10080).intervals[0].every == timedelta(days=7)
    assert schedule.spec_for(0) == ScheduleSpec()


async def test_creates_a_running_schedule_on_the_interval():
    client = FakeClient()
    await schedule.ensure_schedule(client, "q", 5)  # type: ignore[arg-type]
    assert client.created is not None
    assert client.created.spec == schedule.spec_for(5)
    assert not client.created.state.paused


async def test_creates_a_paused_schedule_without_an_interval_for_manual():
    client = FakeClient()
    await schedule.ensure_schedule(client, "q", 0)  # type: ignore[arg-type]
    assert client.created is not None
    assert client.created.spec.intervals == []
    assert client.created.state.paused
    assert client.created.state.note == schedule.MANUAL_NOTE


async def test_an_existing_schedule_takes_the_new_interval():
    client = FakeClient(existing(5))
    await schedule.ensure_schedule(client, "q", 60)  # type: ignore[arg-type]
    assert client.current is not None
    assert client.current.spec == schedule.spec_for(60)
    assert not client.current.state.paused


async def test_manual_pauses_a_running_schedule_and_drops_its_interval():
    client = FakeClient(existing(5))
    await schedule.ensure_schedule(client, "q", 0)  # type: ignore[arg-type]
    assert client.current is not None
    assert client.current.spec.intervals == []
    assert client.current.state.paused
    assert client.current.state.note == schedule.MANUAL_NOTE


@pytest.mark.parametrize("every_minutes", [0, 5])
async def test_a_schedule_paused_by_hand_stays_paused_with_its_note(every_minutes):
    client = FakeClient(existing(5, paused=True, note="cost: paused by hand"))
    await schedule.ensure_schedule(client, "q", every_minutes)  # type: ignore[arg-type]
    assert client.current is not None
    assert client.current.state.paused
    assert client.current.state.note == "cost: paused by hand"


def test_sync_every_defaults_to_five_minutes_and_rejects_negatives(monkeypatch):
    env = {
        "KI_ALLOWED_GRAPHS": "brain-client",
        "KI_LLM_BASE_URL": "http://llm.invalid/v1",
        "KI_LLM_MODEL": "m",
        "KI_EMBEDDING_BASE_URL": "http://embed.invalid/v1",
        "KI_EMBEDDING_MODEL": "e",
        "KI_EMBEDDING_DIM": "1024",
        "FALKORDB_HOST": "falkordb.invalid",
    }
    for k, v in env.items():
        monkeypatch.setenv(k, v)
    assert Settings.from_env().sync_every_minutes == 5
    monkeypatch.setenv("KI_SYNC_EVERY_MINUTES", "0")
    assert Settings.from_env().sync_every_minutes == 0
    monkeypatch.setenv("KI_SYNC_EVERY_MINUTES", "-1")
    with pytest.raises(ConfigError):
        Settings.from_env()
