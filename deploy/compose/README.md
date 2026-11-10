# Local stack (Docker Compose)

The knowledge base on one machine: FalkorDB, a Temporal dev server, the ingest worker, kb-query, the embedding service and ki-mcp. The full guide, with security notes and the smoke test, is [docs/local-stack.md](../../docs/local-stack.md).

```bash
deploy/compose/setup.sh                 # writes .env, config/models.env and local/ (never overwrites)
$EDITOR deploy/compose/config/models.env   # KI_LLM_BASE_URL, KI_LLM_MODEL, KI_LLM_API_KEY
# put a read-only GitHub token in deploy/compose/local/git-tokens/me
cd deploy/compose && docker compose up -d --wait
```

| Path | What |
| --- | --- |
| `compose.yaml` | The services, pinned images, the one published port (127.0.0.1:8787) |
| `setup.sh` | Creates the local secrets, the model file and the token store, once |
| `config/` | The configuration directory: projects, identities, brains, ki-mcp's config, the model file example. `ki setup config` links it |
| `smoke-test.sh`, `mcp_check.py` | Brings the stack up, ingests the origin of this checkout, calls `get_context`, tears down |
| `.env`, `config/models.env`, `local/` | Generated on your machine and gitignored |
