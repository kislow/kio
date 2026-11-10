"""Exceptions for the knowledge base, all deriving from AppError.

The standard-library mixins keep the meaning obvious to callers that only know
the built-in types: a bad config is a ValueError, a refused graph a PermissionError.
"""

from __future__ import annotations


class AppError(Exception):
    """Base for every error ki_kb raises on purpose."""


class ConfigError(AppError, ValueError):
    """Harness configuration (environment, projects file, aliases) is missing or malformed."""


class GraphNotAllowedError(AppError, PermissionError):
    """An instance was asked to touch a graph outside KI_ALLOWED_GRAPHS."""


class GitError(AppError):
    """A git command failed. The message never contains git's own output."""


class GitTimeoutError(GitError):
    """A git command outran its time limit and was killed."""
