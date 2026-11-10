"""Custom Graphiti entity types, identical for every brain.

Keeping one schema everywhere is what lets one set of MCP tools work across
brains. Descriptions matter: Graphiti passes them to the extraction model.
"""

from __future__ import annotations

from pydantic import BaseModel, Field


class Project(BaseModel):
    """A product or codebase, such as Coalesse."""

    stack: str | None = Field(None, description="Primary languages and frameworks")


class Service(BaseModel):
    """A deployable component: an API, worker, job or database."""

    language: str | None = Field(None, description="Primary language, e.g. Go")
    owner: str | None = Field(None, description="Team or person responsible")


class Tech(BaseModel):
    """A technology, tool, library or cloud service, e.g. Kubernetes or Vault."""

    category: str | None = Field(None, description="e.g. database, CI, runtime, cloud service")


class Concept(BaseModel):
    """An engineering concept or practice, e.g. error budgets or idempotency."""

    domain: str | None = Field(None, description="One of: sre, swe, ai, devsecops")


class Decision(BaseModel):
    """A choice that was made, with its reason. Usually from a decision record or ADR."""

    status: str | None = Field(None, description="active or superseded")
    rationale: str | None = Field(None, description="Why it was decided")


class Incident(BaseModel):
    """Something that went wrong in production."""

    severity: str | None = Field(None, description="e.g. sev1, sev2")


class Runbook(BaseModel):
    """A documented procedure for operating or recovering a service."""

    trigger: str | None = Field(None, description="When to use it")


ENTITY_TYPES: dict[str, type[BaseModel]] = {
    "Project": Project,
    "Service": Service,
    "Tech": Tech,
    "Concept": Concept,
    "Decision": Decision,
    "Incident": Incident,
    "Runbook": Runbook,
}

EXTRACTION_INSTRUCTIONS = (
    "This is internal engineering documentation. Prefer the custom entity types. "
    "Record decisions with their rationale and the components they apply to. "
    "When a document says something replaces or supersedes an earlier choice, "
    "state that explicitly."
)
