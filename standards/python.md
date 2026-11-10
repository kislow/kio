# Python standards

Read with `common.md`. Target: Python 3.13+ for projects (newer versions are fine for scratch work). One virtual environment per project.

## Project layout (applications and libraries)

```
myproject/
├── pyproject.toml        # deps, ruff, mypy, pytest all configured here
├── uv.lock               # committed
├── README.md
├── .python-version
├── src/myproject/
│   ├── __init__.py
│   ├── __main__.py       # python -m myproject -> main()
│   ├── cli.py            # argparse/typer, thin
│   ├── config.py         # pydantic-settings, reads env
│   ├── logging_setup.py
│   ├── errors.py         # AppError and subclasses
│   └── <domain>/         # pure logic, no I/O side effects at import
└── tests/
    ├── conftest.py
    └── test_*.py
```

Single-file scripts may skip `src/`, but still use type hints, `main()`, and the logger.

## Tooling

- **MUST** manage dependencies with `uv` and `pyproject.toml` (`uv add`, `uv lock`, `uv sync`, `uv run pytest`). No new `requirements.txt` or `setup.py`.
- **MUST** pass `ruff check` and `ruff format` (black-compatible, includes import sorting), and `mypy --strict` on new code. `ruff` replaces black, isort and flake8; keep `black` only if a project already uses it.
- **MUST** pass `pytest --cov` with a failing threshold of 80% on critical paths.

## Code

- **MUST** type-annotate every function signature. Use `X | None` and built-in generics (`list[str]`).
- **MUST** validate data at boundaries (HTTP bodies, files, env) with pydantic. Never assume a data shape.
- **MUST NOT** use a bare `except:`; catch the narrowest exception. If you catch `Exception`, log with `logger.exception` and re-raise or return an explicit failure.
- **MUST NOT** have side effects at import time. Entry points live in `main()` under `if __name__ == "__main__":`.
- **MUST** raise custom exceptions deriving from one `AppError`, and chain with `raise X from err`.
- **MUST** use `logging` through the `logging_setup.py` template: JSON output, one logger per module via `logging.getLogger(__name__)`. No `print`.
- **MUST** pass `timeout=` to `requests`/`httpx` calls and `subprocess.run`. Use `shell=False`.
- **SHOULD** use context managers for files, locks and connections, `pathlib` over `os.path`, f-strings, dataclasses for internal data, `async`/`await` for I/O-bound work.
- **SHOULD** replace long `if/elif` dispatch chains with a dict of handlers, and pass configuration as one object instead of eight arguments.

## Examples

```python
# no
try:
    data = json.loads(path.read_text())
except:
    pass

# yes
try:
    data = json.loads(path.read_text(encoding="utf-8"))
except (OSError, json.JSONDecodeError) as err:
    raise ConfigError(f"reading {path}") from err
```

```python
# no
print(f"fetched {user_id}")

# yes
logger.info("fetched user", extra={"user_id": user_id, "duration_ms": elapsed_ms})
```

## Templates

| File | Use |
| --- | --- |
| `standards/templates/python/pyproject.toml` | uv project with ruff, mypy `--strict`, pytest and an 80% coverage gate |
| `standards/templates/python/logging_setup.py` | JSON logging on the standard library; call `configure_logging()` once from `main()` |

## Checks

| Step | Command |
| --- | --- |
| Format | `uv run ruff format --check .` |
| Lint | `uv run ruff check .` (rule set in the `pyproject.toml` template) |
| Types | `uv run mypy --strict src` |
| Tests | `uv run pytest --cov` |
