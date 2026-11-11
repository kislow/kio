# Build from the repo root: docker build -f kb/Dockerfile .
FROM python:3.13.17-slim-trixie
RUN apt-get update && apt-get install -y --no-install-recommends git ca-certificates \
    && rm -rf /var/lib/apt/lists/*
RUN pip install --no-cache-dir uv==0.13.5
WORKDIR /app
COPY kb/pyproject.toml kb/uv.lock ./
RUN uv sync --frozen --no-dev --no-install-project
COPY kb/src ./src
RUN uv sync --frozen --no-dev
# Fail the build, not the pod, when ki-kb declares a dependency the lock did
# not install (0.25.0 shipped without prometheus-client and crash-looped).
RUN uv pip check
ARG VERSION=dev
LABEL org.opencontainers.image.source="https://github.com/kislow/kio" \
      org.opencontainers.image.licenses="Apache-2.0" \
      org.opencontainers.image.version="${VERSION}"
COPY LICENSE NOTICE /usr/share/doc/kio/
RUN useradd --uid 10001 --create-home ki
USER 10001
ENV PATH="/app/.venv/bin:$PATH"
# Same image, two roles: `ki-kb-worker` (ingest) or `ki-kb-query` (search API).
CMD ["ki-kb-worker"]
