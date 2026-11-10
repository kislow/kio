"""The `ki-sync-projects` schedule: how often SyncProjects starts on its own.

KI_SYNC_EVERY_MINUTES sets the interval, and 0 makes syncing manual: the
schedule has no interval and is kept paused, and a sync runs only when someone
triggers it (the schedule's Trigger in the Temporal UI, or
`temporal schedule trigger --schedule-id ki-sync-projects`), which works while
it is paused. The worker applies the setting to an existing schedule on every
start, so the interval lives in config rather than in hand edits. It never
unpauses: a schedule paused by hand stays paused.
"""

from __future__ import annotations

import logging
from datetime import timedelta

from temporalio.client import (
    Client,
    Schedule,
    ScheduleActionStartWorkflow,
    ScheduleAlreadyRunningError,
    ScheduleIntervalSpec,
    ScheduleOverlapPolicy,
    SchedulePolicy,
    ScheduleSpec,
    ScheduleState,
    ScheduleUpdate,
    ScheduleUpdateInput,
)

from .workflows import SyncProjects

logger = logging.getLogger(__name__)

SCHEDULE_ID = "ki-sync-projects"
MANUAL_NOTE = "manual (KI_SYNC_EVERY_MINUTES=0): trigger the schedule to sync"


def spec_for(every_minutes: int) -> ScheduleSpec:
    """An interval every `every_minutes`, or no interval at all for manual syncing."""
    if every_minutes == 0:
        return ScheduleSpec()
    return ScheduleSpec(intervals=[ScheduleIntervalSpec(every=timedelta(minutes=every_minutes))])


async def ensure_schedule(client: Client, queue: str, every_minutes: int) -> None:
    """Creates the schedule, or brings an existing one in line with the setting."""
    manual = every_minutes == 0
    spec = spec_for(every_minutes)
    fields = {"schedule": SCHEDULE_ID, "every_minutes": every_minutes, "manual": manual}
    try:
        await client.create_schedule(
            SCHEDULE_ID,
            Schedule(
                action=ScheduleActionStartWorkflow(
                    SyncProjects.run, id="ki-sync-projects-run", task_queue=queue
                ),
                spec=spec,
                policy=SchedulePolicy(overlap=ScheduleOverlapPolicy.SKIP),
                state=ScheduleState(paused=manual, note=MANUAL_NOTE if manual else None),
            ),
        )
    except ScheduleAlreadyRunningError:
        pass
    else:
        logger.info("created schedule", extra=fields)
        return

    def update(current: ScheduleUpdateInput) -> ScheduleUpdate:
        schedule = current.description.schedule
        schedule.spec = spec
        if manual and not schedule.state.paused:
            schedule.state.paused = True
            schedule.state.note = MANUAL_NOTE
        return ScheduleUpdate(schedule=schedule)

    await client.get_schedule_handle(SCHEDULE_ID).update(update)
    logger.info("updated schedule", extra=fields)
