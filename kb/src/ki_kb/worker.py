"""Worker entrypoint: runs the workflows and sync activities on KI_TASK_QUEUE
and owns the schedule that starts SyncProjects (schedule.py).
"""

from __future__ import annotations

import asyncio
import logging

from temporalio.client import Client
from temporalio.worker import Worker

from . import metrics
from .activities import load_projects, sync_repo, sync_timeout
from .errors import ConfigError
from .logging_setup import configure_logging
from .schedule import ensure_schedule
from .settings import Settings
from .workflows import SyncOneProject, SyncProjects

logger = logging.getLogger(__name__)


async def run() -> None:
    settings = Settings.from_env()
    if not settings.task_queue:
        raise ConfigError("KI_TASK_QUEUE must be set for a worker")
    metrics.serve(settings.metrics_port)
    client = await Client.connect(settings.temporal_address, namespace=settings.temporal_namespace)
    await ensure_schedule(client, settings.task_queue, settings.sync_every_minutes)
    worker = Worker(
        client,
        task_queue=settings.task_queue,
        workflows=[SyncProjects, SyncOneProject],
        activities=[load_projects, sync_repo, sync_timeout],
    )
    logger.info(
        "worker started",
        extra={
            "queue": settings.task_queue,
            "graphs": sorted(settings.allowed_graphs),
            "metrics_port": settings.metrics_port,
        },
    )
    await worker.run()


def main() -> None:
    configure_logging()
    asyncio.run(run())


if __name__ == "__main__":
    main()
