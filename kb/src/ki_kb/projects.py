"""The central projects file: which repos exist, what they feed, who owns them.

Replaces per-repo files so that no project repo carries any trace of the
harness. Everything here is validated up front; a bad file fails loudly
instead of ingesting into the wrong brain.

The file's shape is checked by pydantic (unknown keys, wrong types and missing
fields are errors), then the cross-references by hand. pkg/projects in Go
applies the same rules; both are tested against testdata/projects/.
"""

from __future__ import annotations

import logging
import re
import sys
from collections.abc import Sequence
from dataclasses import dataclass
from pathlib import Path, PurePosixPath
from typing import Annotated

import yaml
from pydantic import (
    BaseModel,
    ConfigDict,
    Field,
    StringConstraints,
    ValidationError,
    field_validator,
)

from .brains import CORTEX, graph_for, matches_any, valid_entry, valid_name
from .errors import ConfigError

logger = logging.getLogger(__name__)

# Same pattern as NormalizeRepo in Go, SSH host aliases (`git@github.com-work:`) included.
_GITHUB = re.compile(
    r"^(?:https://github\.com/|git@github\.com(?:-[\w.-]+)?:|ssh://git@github\.com(?:-[\w.-]+)?/)"
    r"([\w.-]+)/([\w.-]+?)(?:\.git)?/?$"
)

Text = Annotated[str, StringConstraints(min_length=1)]


def normalize_repo(url: str) -> str:
    """Canonical https URL for any GitHub remote form."""
    m = _GITHUB.match(url.strip())
    if not m:
        raise ConfigError(f"not a GitHub repo URL: {url!r}")
    return f"https://github.com/{m.group(1)}/{m.group(2)}".lower()


# --- the file as written -------------------------------------------------------


class _Strict(BaseModel):
    model_config = ConfigDict(extra="forbid", strict=True, frozen=True)


class _RawIdentity(_Strict):
    github_user: Text
    email: Text
    credential: Text
    vault: bool = True
    ingest: bool  # no default: every identity says whether its repos may be ingested


class _RawRule(_Strict):
    path: Text
    brain: Text


class _RawProject(_Strict):
    name: Text
    repo: Text
    identity: Text
    branch: Text = "main"
    brains: list[Text] = Field(min_length=1)
    ingest: list[_RawRule] = Field(default_factory=list)
    board: str | None = None
    bulk: bool = False
    vault_folders: list[Text] = Field(default_factory=list)

    @field_validator("ingest", mode="before")
    @classmethod
    def _none_is_empty(cls, value: object) -> object:
        return [] if value is None else value


class _RawWorkspace(_Strict):
    name: Text
    title: str | None = None
    brains: list[Text] = Field(min_length=1)
    projects: list[Text] = Field(default_factory=list)
    board: str | None = None
    vault: list[Text] = Field(min_length=1)

    @field_validator("projects", mode="before")
    @classmethod
    def _none_is_empty(cls, value: object) -> object:
        return [] if value is None else value


class _RawFile(_Strict):
    sensitive_brains: list[str] = Field(default_factory=list)
    identities: dict[str, _RawIdentity] = Field(default_factory=dict)
    projects: list[_RawProject] = Field(default_factory=list)
    workspaces: list[_RawWorkspace] = Field(default_factory=list)

    @field_validator("sensitive_brains", "identities", "projects", "workspaces", mode="before")
    @classmethod
    def _none_is_empty(cls, value: object, info: object) -> object:
        if value is None:
            return {} if getattr(info, "field_name", "") == "identities" else []
        return value


# --- the validated config ------------------------------------------------------


@dataclass(frozen=True)
class Identity:
    name: str
    github_user: str
    email: str
    credential: str
    # False for employers or clients whose content must never be copied into the vault.
    vault: bool = True
    # False for employers or clients whose repos must never be ingested. Always stated.
    ingest: bool = False


@dataclass(frozen=True)
class IngestRule:
    path: str
    brain: str


@dataclass(frozen=True)
class Project:
    name: str
    repo: str
    branch: str
    identity: Identity
    brains: tuple[str, ...]
    ingest: tuple[IngestRule, ...]
    # True when any ingest brain is sensitive: its paths stay out of Temporal history.
    sensitive: bool
    board: str | None = None
    bulk: bool = False
    vault_folders: tuple[str, ...] = ()

    def brain_for(self, rel_path: str) -> str | None:
        """Brain a repo-relative file belongs to; the longest matching rule wins."""
        best: IngestRule | None = None
        for rule in self.ingest:
            prefix = rule.path.rstrip("/")
            matches = rel_path == prefix or rel_path.startswith(prefix + "/")
            if matches and (best is None or len(rule.path) > len(best.path)):
                best = rule
        return best.brain if best else None


@dataclass(frozen=True)
class Workspace:
    name: str
    title: str | None
    brains: tuple[str, ...]
    projects: tuple[str, ...]
    board: str | None
    vault: tuple[str, ...]


@dataclass(frozen=True)
class Config:
    sensitive_brains: frozenset[str]
    identities: dict[str, Identity]
    projects: tuple[Project, ...]
    workspaces: tuple[Workspace, ...]

    def is_sensitive(self, brain: str) -> bool:
        return matches_any(self.sensitive_brains, brain)

    def project(self, name: str) -> Project:
        for p in self.projects:
            if p.name == name:
                return p
        raise ConfigError(f"unknown project {name!r}")


def _shape(data: object) -> _RawFile:
    try:
        return _RawFile.model_validate(data or {})
    except ValidationError as err:
        first = err.errors()[0]
        where = ".".join(str(p) for p in first["loc"]) or "file"
        raise ConfigError(f"{where}: {first['msg']}") from None


def _identities(raw: _RawFile) -> dict[str, Identity]:
    identities: dict[str, Identity] = {}
    for name, ri in raw.identities.items():
        where = f"identity {name!r}"
        if not valid_name(name):
            raise ConfigError(f"{where}: invalid name")
        if not valid_name(ri.credential):
            raise ConfigError(f"{where}: invalid credential name {ri.credential!r}")
        identities[name] = Identity(
            name=name,
            github_user=ri.github_user,
            email=ri.email,
            credential=ri.credential,
            vault=ri.vault,
            ingest=ri.ingest,
        )
    return identities


def _rules(rp: _RawProject, identity: Identity, where: str) -> tuple[IngestRule, ...]:
    if rp.ingest and not identity.ingest:
        raise ConfigError(
            f"{where}: identity {identity.name!r} has ingest: false, "
            "so the project may not have ingest rules"
        )
    rules = []
    for rule in rp.ingest:
        graph_for(rule.brain)
        if rule.brain not in rp.brains and rule.brain != CORTEX:
            raise ConfigError(f"{where}: ingest brain {rule.brain!r} is not in 'brains'")
        p = PurePosixPath(rule.path)
        if p.is_absolute() or ".." in p.parts:
            raise ConfigError(f"{where}: ingest path {rule.path!r} must stay inside the repo")
        rules.append(IngestRule(path=rule.path, brain=rule.brain))
    return tuple(rules)


def _workspaces(
    raw: _RawFile, projects: list[Project], sensitive: frozenset[str]
) -> tuple[Workspace, ...]:
    project_by_name = {project.name: project for project in projects}
    workspaces: list[Workspace] = []
    seen_names: set[str] = set()
    vault_folders: list[tuple[str, str]] = []
    for rw in raw.workspaces:
        where = f"workspace {rw.name!r}"
        if not valid_name(rw.name) or rw.name in seen_names:
            raise ConfigError(f"{where}: invalid or duplicate name")
        seen_names.add(rw.name)

        listed_projects: set[str] = set()
        for name in rw.projects:
            if name not in project_by_name:
                raise ConfigError(f"{where}: unknown project {name!r}")
            if name in listed_projects:
                raise ConfigError(f"{where}: duplicate project {name!r}")
            listed_projects.add(name)

        seen_brains: set[str] = set()
        has_sensitive = False
        has_nonsensitive = False
        for brain in rw.brains:
            if not valid_name(brain):
                raise ConfigError(f"{where}: invalid brain name {brain!r}")
            if brain in {"voice", "security"}:
                raise ConfigError(f"{where}: {brain!r} is not allowed as a workspace brain")
            if brain in seen_brains:
                raise ConfigError(f"{where}: duplicate brain {brain!r}")
            seen_brains.add(brain)
            if brain != CORTEX and not any(
                brain in project_by_name[name].brains for name in listed_projects
            ):
                raise ConfigError(
                    f"{where}: brain {brain!r} is not listed by any workspace project"
                )
            is_sensitive = matches_any(sensitive, brain)
            has_sensitive |= is_sensitive
            has_nonsensitive |= not is_sensitive
        if has_sensitive and has_nonsensitive:
            raise ConfigError(f"{where}: brains may not mix sensitive and non-sensitive")

        for folder in rw.vault:
            path = PurePosixPath(folder)
            valid_root = folder.startswith(("projects/", "diagrams/projects/"))
            if (
                path.is_absolute()
                or ".." in path.parts
                or not folder.endswith("/")
                or not valid_root
                or folder.rstrip("/") in {"projects", "diagrams/projects"}
                or str(path) != folder.rstrip("/")
            ):
                raise ConfigError(
                    f"{where}: vault path {folder!r} must be a relative folder "
                    "under projects/ or diagrams/projects/, ending in /"
                )
            for used, owner in vault_folders:
                if used == folder:
                    raise ConfigError(
                        f"{where}: vault folder {folder!r} is already used by workspace {owner!r}"
                    )
                # Folders end in "/", so a prefix match is a match on whole path segments.
                if owner != rw.name and (used.startswith(folder) or folder.startswith(used)):
                    raise ConfigError(
                        f"{where}: vault folder {folder!r} is nested with folder {used!r} "
                        f"of workspace {owner!r}"
                    )
            vault_folders.append((folder, rw.name))
        workspaces.append(
            Workspace(
                name=rw.name,
                title=rw.title,
                brains=tuple(rw.brains),
                projects=tuple(rw.projects),
                board=rw.board,
                vault=tuple(rw.vault),
            )
        )
    return tuple(workspaces)


def parse(text: str) -> Config:
    try:
        data = yaml.safe_load(text)
    except yaml.YAMLError as err:
        raise ConfigError(f"not valid YAML: {err}") from None
    if data is not None and not isinstance(data, dict):
        raise ConfigError("projects file must be a mapping")
    raw = _shape(data)

    for b in raw.sensitive_brains:
        if not valid_entry(b):
            raise ConfigError(f"invalid brain name {b!r}")
    sensitive = frozenset(raw.sensitive_brains)
    if matches_any(sensitive, CORTEX):
        raise ConfigError("cortex cannot be sensitive")

    identities = _identities(raw)
    projects: list[Project] = []
    seen_names: set[str] = set()
    repo_identity: dict[str, str] = {}
    for rp in raw.projects:
        where = f"project {rp.name!r}"
        if not valid_name(rp.name) or rp.name in seen_names:
            raise ConfigError(f"{where}: invalid or duplicate name")
        seen_names.add(rp.name)

        identity = identities.get(rp.identity)
        if identity is None:
            raise ConfigError(f"{where}: unknown identity {rp.identity!r}")
        repo = normalize_repo(rp.repo)
        if repo_identity.setdefault(repo, identity.name) != identity.name:
            raise ConfigError(f"{where}: {repo} is listed under two identities")
        for b in rp.brains:
            graph_for(b)

        rules = _rules(rp, identity, where)
        if len(set(rp.vault_folders)) != len(rp.vault_folders) or not all(
            valid_name(f) for f in rp.vault_folders
        ):
            raise ConfigError(f"{where}: vault_folders entries must be unique valid names")

        projects.append(
            Project(
                name=rp.name,
                repo=repo,
                branch=rp.branch,
                identity=identity,
                brains=tuple(rp.brains),
                ingest=rules,
                sensitive=any(matches_any(sensitive, r.brain) for r in rules),
                board=rp.board,
                bulk=rp.bulk,
                vault_folders=tuple(rp.vault_folders),
            )
        )
    workspaces = _workspaces(raw, projects, sensitive)
    return Config(
        sensitive_brains=sensitive,
        identities=identities,
        projects=tuple(projects),
        workspaces=workspaces,
    )


def load(path: Path) -> Config:
    try:
        text = path.read_text(encoding="utf-8")
    except OSError as err:
        raise ConfigError(f"reading {path}: {err.strerror}") from None
    return parse(text)


def main(argv: Sequence[str] | None = None) -> int:
    """Validate a projects file: python -m ki_kb.projects <path>."""
    args = sys.argv[1:] if argv is None else list(argv)
    if len(args) != 1:
        sys.stderr.write("usage: python -m ki_kb.projects <projects.yaml>\n")
        return 2
    try:
        cfg = load(Path(args[0]))
    except ConfigError as err:
        sys.stderr.write(f"invalid: {err}\n")
        return 1
    for p in cfg.projects:
        ingest = ", ".join(f"{r.path}->{r.brain}" for r in p.ingest) or "none"
        sys.stdout.write(f"{p.name}: {p.repo} [{p.identity.name}] ingest={ingest}\n")
    sys.stdout.write(f"ok: {len(cfg.projects)} project(s), {len(cfg.identities)} identit(ies)\n")
    return 0


if __name__ == "__main__":
    sys.exit(main())
