SKILLS        := $(notdir $(wildcard skills/*))
CLAUDE_DIR    ?= $(HOME)/.claude
# Skills go where each agent looks: Claude Code reads ~/.claude/skills; Codex and
# Cursor read ~/.agents/skills (Cursor also reads ~/.claude/skills).
SKILL_DIRS    ?= $(CLAUDE_DIR)/skills $(HOME)/.agents/skills
KI_CONF       ?= $(HOME)/.config/ki
# Where the ki binary goes: user-owned and on most PATHs, unlike Go's ~/go/bin.
BINDIR        ?= $(HOME)/.local/bin
KUSTOMIZE     ?= kustomize
# Same version as CI, so local lint finds exactly what CI finds.
GOLANGCI_VERSION ?= 2.15.0
VERSION       ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

.PHONY: help laptop install-cli skills uninstall-skills doctor diagram \
        test test-go test-kb test-diagram test-packaging lint lint-go lint-py \
        validate-projects manifests check-manifests export-check publish-export licences snapshot try try-restore

help: ## list targets
	@grep -E '^[a-z-]+:.*## ' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-18s %s\n", $$1, $$2}'

# --- laptop ----------------------------------------------------------------

laptop: install-cli skills ## set up this machine: ki binary, skills, attribution off, health check
	@if command -v claude >/dev/null 2>&1; then ki setup claude; fi
	@echo "using Codex too? run: ki setup codex"
	ki doctor

install-cli: ## build and install the ki binary into $(BINDIR), and point it at this checkout
	mkdir -p $(BINDIR)
	GOBIN=$(BINDIR) go install -trimpath -ldflags "-s -w -X main.version=$(VERSION)" ./cmd/ki
	@old="$$(go env GOPATH)/bin/ki"; if [ -x "$$old" ] && [ "$$old" != "$(BINDIR)/ki" ]; then rm -f "$$old" && echo "removed the old $$old"; fi
	mkdir -p $(KI_CONF)/run
	chmod 700 $(KI_CONF)
	ln -sfn $(CURDIR) $(KI_CONF)/harness
	@echo "installed ki $(VERSION) to $(BINDIR) (make sure it is on your PATH)"

skills: ## link the ki-* skills (Claude Code, Codex, Cursor) into place
	@for d in $(SKILL_DIRS); do \
	  mkdir -p "$$d"; \
	  for s in $(SKILLS); do \
	    dest="$$d/$$s"; \
	    if [ -e "$$dest" ] && [ ! -L "$$dest" ]; then echo "skip $$dest: exists and is not a link"; continue; fi; \
	    ln -sfn "$(CURDIR)/skills/$$s" "$$dest"; \
	  done; \
	  echo "linked $(words $(SKILLS)) skills into $$d"; \
	done
	@# ki-standup was renamed ki-recap; drop the links an older make skills left.
	@for d in $(SKILL_DIRS); do [ -L "$$d/ki-standup" ] && rm "$$d/ki-standup" && echo "removed the retired ki-standup link from $$d"; done; true
	@# The ki-resolver subagent was retired for ki delegate; drop the link an older make skills left.
	@[ -L $(CLAUDE_DIR)/agents/ki-resolver.md ] && rm $(CLAUDE_DIR)/agents/ki-resolver.md && echo "removed the retired ki-resolver agent link"; true

uninstall-skills: ## remove the ki-* skill links
	@for d in $(SKILL_DIRS); do for s in $(SKILLS); do [ -L "$$d/$$s" ] && rm "$$d/$$s"; done; echo "removed skills from $$d"; done; true

doctor: ## check this machine: tokens, knowledge base reach, gh accounts, git identity
	ki doctor

diagram: ## install what ki diagram needs: the pinned node packages and headless Chromium (PDFs)
	cd diagram && npm ci --no-audit --no-fund && node_modules/.bin/playwright-core install chromium-headless-shell

# --- development -------------------------------------------------------------

test: test-go test-kb test-diagram check-manifests test-packaging ## everything CI runs

test-go:
	go mod tidy -diff && go vet ./... && go test -race -cover ./...

# The PDF test is skipped when headless Chromium is missing; `make diagram` installs it.
test-diagram:
	cd diagram && npm ci --no-audit --no-fund --silent && npm test

test-packaging: ## render the Homebrew formula from a fixture and check it
	scripts/homebrew-formula_test.sh

test-kb: validate-projects
	cd kb && uv sync && uv run pytest -q

lint: lint-go lint-py ## golangci-lint, ruff and mypy

lint-go:
	@v="$$(golangci-lint version --short 2>/dev/null || true)"; \
	  [ "$$v" = "$(GOLANGCI_VERSION)" ] || printf 'warning: golangci-lint %s found, CI uses %s; results may differ\n  go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v%s\n' "$${v:-none}" "$(GOLANGCI_VERSION)" "$(GOLANGCI_VERSION)" >&2
	golangci-lint run ./...

lint-py:
	cd kb && uv run ruff check . && uv run ruff format --check . && uv run mypy

validate-projects: ## validate deploy/config/projects.yaml with both validators
	go run ./cmd/ki validate deploy/config/projects.yaml
	cd kb && uv run python -m ki_kb.projects ../deploy/config/projects.yaml

manifests: ## render the cluster manifests (the harness's own configuration)
	$(KUSTOMIZE) build deploy

check-manifests: ## verify graphs, sensitivity, images and default-deny in the rendered manifests, base and example overlay
	$(KUSTOMIZE) build deploy | uv run --project kb python deploy/check.py
	$(KUSTOMIZE) build examples/config | uv run --project kb python deploy/check.py

export-check: ## export the public tree to a temp dir, check what it must not contain, build and test it
	scripts/publish/export-check.sh

publish-export: ## export a release as a one-commit snapshot: make publish-export REF=<tag> OUT=<dir> (needs PUBLISH_AUTHOR_NAME and PUBLISH_AUTHOR_EMAIL)
	@[ -n "$(OUT)" ] || { echo "usage: make publish-export [REF=<tag|sha>] OUT=<dir>" >&2; exit 2; }
	scripts/publish/export.sh $(if $(REF),--ref "$(REF)") --out "$(OUT)"

licences: ## list the licence of every dependency (Go, kb/, diagram/) and fail on GPL, AGPL, LGPL, SSPL, BUSL, unknown or custom ones not in .licence-allow.txt
	scripts/licences.sh

snapshot: ## build ki release binaries locally into dist/ (needs goreleaser)
	goreleaser build --snapshot --clean

# --- cluster -------------------------------------------------------------------

try: ## run a pushed image on the cluster without a release: make try COMPONENT=kb|mcp|vault REF=<sha|tag> (KI_IMAGE_REGISTRY defaults to ghcr.io/kislow)
	@[ -n "$(COMPONENT)" ] && [ -n "$(REF)" ] || { echo "usage: make try COMPONENT=kb|mcp|vault REF=<sha|tag>" >&2; exit 2; }
	scripts/try-image.sh "$(COMPONENT)" "$(REF)"

try-restore: ## end a trial: Argo CD reconciles the harness again and puts the released images back
	scripts/try-image.sh --restore
