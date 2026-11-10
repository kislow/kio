import pytest

from ki_kb.errors import ConfigError, GraphNotAllowedError
from ki_kb.settings import Settings

REQUIRED = {
    "KI_ALLOWED_GRAPHS": "brain-client",
    "KI_LLM_BASE_URL": "http://llm.invalid/v1",
    "KI_LLM_MODEL": "m",
    "KI_EMBEDDING_BASE_URL": "http://embed.invalid/v1",
    "KI_EMBEDDING_MODEL": "e",
    "KI_EMBEDDING_DIM": "1024",
    "FALKORDB_HOST": "falkordb.invalid",
}


@pytest.fixture
def env(monkeypatch):
    for k, v in REQUIRED.items():
        monkeypatch.setenv(k, v)
    return monkeypatch


@pytest.mark.parametrize(
    "missing",
    [
        "KI_ALLOWED_GRAPHS",
        "KI_LLM_BASE_URL",
        "KI_LLM_MODEL",
        "KI_EMBEDDING_BASE_URL",
        "FALKORDB_HOST",
    ],
)
def test_no_silent_provider_defaults(env, missing):
    env.delenv(missing)
    with pytest.raises(ConfigError, match=missing):
        Settings.from_env()


def test_rejects_unknown_structured_mode(env):
    env.setenv("KI_LLM_STRUCTURED_MODE", "xml")
    with pytest.raises(ConfigError):
        Settings.from_env()


def test_git_token_per_credential(env, tmp_path):
    (tmp_path / "org-a").write_text("tok-a\n")
    (tmp_path / "empty").write_text("")
    env.setenv("KI_GIT_TOKENS_DIR", str(tmp_path))
    s = Settings.from_env()
    assert s.git_token("org-a") == "tok-a"
    with pytest.raises(ConfigError, match="empty"):
        s.git_token("empty")
    with pytest.raises(ConfigError, match="no git token"):
        s.git_token("org-b")


def test_check_graph_fails_closed(env):
    s = Settings.from_env()
    s.check_graph("brain-client")
    with pytest.raises(GraphNotAllowedError):
        s.check_graph("brain-acme")


def test_forget_graph_selects_only_that_graph():
    import json

    from ki_kb.admin import entries_to_forget

    state = {
        "docs/a.md": json.dumps({"hash": "1", "episode": "e1", "graph": "brain-acme"}),
        "docs/b.md": json.dumps({"hash": "2", "episode": "e2", "graph": "cortex"}),
        "docs/c.md": json.dumps({"hash": "3", "episode": "e3", "graph": "brain-acme"}),
    }
    assert entries_to_forget(state, "brain-acme") == ["docs/a.md", "docs/c.md"]
    assert entries_to_forget(state, "brain-voice") == []


def test_check_graph_family(env):
    env.setenv("KI_ALLOWED_GRAPHS", "brain-client,brain-client-*")
    s = Settings.from_env()
    s.check_graph("brain-client")
    s.check_graph("brain-client-acme")
    for graph in ("brain-clientx", "brain-client-", "cortex", "brain-acme"):
        with pytest.raises(GraphNotAllowedError):
            s.check_graph(graph)


@pytest.mark.parametrize("bad", ["*", "brain-*x", "brain-client*", "Brain-X"])
def test_rejects_malformed_allowed_graphs(env, bad):
    env.setenv("KI_ALLOWED_GRAPHS", f"cortex,{bad}")
    with pytest.raises(ConfigError, match="invalid entries"):
        Settings.from_env()


def test_errors_name_variables_but_not_values(env):
    env.setenv("KI_EMBEDDING_DIM", "not-a-number-secretvalue")
    with pytest.raises(ConfigError) as exc:
        Settings.from_env()
    assert "KI_EMBEDDING_DIM" in str(exc.value)
    assert "secretvalue" not in str(exc.value)


def test_unknown_env_is_ignored_and_defaults_hold(env):
    s = Settings.from_env()
    assert s.task_queue == "" and s.small_model == "m"
    assert s.llm_timeout_seconds > 0


def test_thinking_is_on_unless_switched_off(env) -> None:
    assert Settings.from_env().llm_thinking is True
    env.setenv("KI_LLM_THINKING", "false")
    assert Settings.from_env().llm_thinking is False


def test_prompt_structured_mode_is_accepted(env) -> None:
    env.setenv("KI_LLM_STRUCTURED_MODE", "prompt")
    assert Settings.from_env().llm_structured_mode == "prompt"


def test_falkordb_pool_size_defaults_and_bounds(env) -> None:
    # Three lanes times Graphiti's nested gather (10 * 10). A missing line must
    # not fall back to one pool of 64 per graph.
    assert Settings.from_env().falkordb_max_connections == 300
    env.setenv("KI_FALKORDB_MAX_CONNECTIONS", "2")
    assert Settings.from_env().falkordb_max_connections == 2
    env.setenv("KI_FALKORDB_MAX_CONNECTIONS", "0")
    with pytest.raises(ConfigError, match="KI_FALKORDB_MAX_CONNECTIONS"):
        Settings.from_env()


def test_ingest_batch_defaults_to_sixteen(env) -> None:
    # A config that loses the line must not fall back to one document a call.
    assert Settings.from_env().ingest_batch == 16
    env.setenv("KI_INGEST_BATCH", "4")
    assert Settings.from_env().ingest_batch == 4


def test_edge_candidate_bounds(env) -> None:
    s = Settings.from_env()
    assert (s.edge_dedupe_candidates, s.edge_invalidation_candidates) == (5, 3)
    env.setenv("KI_EDGE_DEDUPE_CANDIDATES", "8")
    env.setenv("KI_EDGE_INVALIDATION_CANDIDATES", "0")
    s = Settings.from_env()
    assert (s.edge_dedupe_candidates, s.edge_invalidation_candidates) == (8, 0)
    env.setenv("KI_EDGE_DEDUPE_CANDIDATES", "0")
    with pytest.raises(ConfigError, match="KI_EDGE_DEDUPE_CANDIDATES"):
        Settings.from_env()


def test_ingest_rate_limit_backoff_settings(env) -> None:
    s = Settings.from_env()
    assert s.ingest_rate_limit_attempts == 5
    assert s.ingest_rate_limit_max_wait_seconds == 600

    env.setenv("KI_INGEST_RATE_LIMIT_ATTEMPTS", "3")
    env.setenv("KI_INGEST_RATE_LIMIT_MAX_WAIT_SECONDS", "90")
    s = Settings.from_env()
    assert s.ingest_rate_limit_attempts == 3
    assert s.ingest_rate_limit_max_wait_seconds == 90


def test_peak_and_balance_settings(env) -> None:
    s = Settings.from_env()
    assert s.llm_peak_utc == "" and s.peak_schedule.windows == ()
    assert s.ingest_bulk_threshold == 25
    assert s.llm_balance_url is None
    assert s.llm_min_balance == 5.0
    assert s.llm_balance_check_interval_minutes == 10

    env.setenv("KI_LLM_PEAK_UTC", "Mon-Fri 01:00-04:00,06:00-10:00")
    env.setenv("KI_INGEST_BULK_THRESHOLD", "50")
    env.setenv("KI_LLM_BALANCE_URL", "https://api.deepseek.com/user/balance")
    env.setenv("KI_LLM_MIN_BALANCE", "5.5")
    env.setenv("KI_LLM_BALANCE_CHECK_INTERVAL_MINUTES", "15")
    s = Settings.from_env()
    assert len(s.peak_schedule.windows) == 2
    assert s.ingest_bulk_threshold == 50
    assert s.llm_balance_url == "https://api.deepseek.com/user/balance"
    assert s.llm_min_balance == 5.5
    assert s.llm_balance_check_interval_minutes == 15


@pytest.mark.parametrize(
    ("var", "value"),
    [
        ("KI_LLM_PEAK_UTC", "Mon-Fri invalid"),
        ("KI_LLM_PEAK_UTC", "Mon-Fri 01:00-25:00"),
        ("KI_LLM_PEAK_UTC", "01:00-04:00"),
        ("KI_INGEST_BULK_THRESHOLD", "0"),
        ("KI_LLM_BALANCE_URL", "api.deepseek.com/user/balance"),
        ("KI_LLM_BALANCE_URL", "ftp://api.deepseek.com/user/balance"),
        ("KI_LLM_MIN_BALANCE", "-1"),
        ("KI_LLM_BALANCE_CHECK_INTERVAL_MINUTES", "0"),
    ],
)
def test_rejects_malformed_off_peak_settings_at_start_up(env, var, value) -> None:
    env.setenv(var, value)
    with pytest.raises(ConfigError, match=var):
        Settings.from_env()


def test_sync_timeouts_default_and_are_read_from_env(env) -> None:
    s = Settings.from_env()
    assert s.sync_timeout_seconds(bulk=False) == 45 * 60
    assert s.sync_timeout_seconds(bulk=True) == 240 * 60
    assert s.sync_timeout_ceiling_minutes == 360
    env.setenv("KI_SYNC_TIMEOUT_MINUTES", "10")
    env.setenv("KI_SYNC_BULK_TIMEOUT_MINUTES", "300")
    s = Settings.from_env()
    assert s.sync_timeout_seconds(bulk=False) == 10 * 60
    assert s.sync_timeout_seconds(bulk=True) == 300 * 60


def test_sync_timeout_is_clamped_to_the_ceiling(env) -> None:
    env.setenv("KI_SYNC_TIMEOUT_MINUTES", "60")
    env.setenv("KI_SYNC_TIMEOUT_CEILING_MINUTES", "50")
    env.setenv("KI_SYNC_BULK_TIMEOUT_MINUTES", "50")
    s = Settings.from_env()
    assert s.sync_timeout_seconds(bulk=False) == 50 * 60


@pytest.mark.parametrize(
    ("var", "value"),
    [
        ("KI_SYNC_TIMEOUT_MINUTES", "0"),
        ("KI_SYNC_BULK_TIMEOUT_MINUTES", "0"),
        ("KI_SYNC_TIMEOUT_CEILING_MINUTES", "-1"),
        ("KI_SYNC_BULK_TIMEOUT_MINUTES", "361"),
    ],
)
def test_sync_timeouts_out_of_range_are_a_startup_error(env, var, value) -> None:
    env.setenv(var, value)
    with pytest.raises(ConfigError, match=var):
        Settings.from_env()
