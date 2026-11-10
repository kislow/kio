# Install ki: instructions for the agent

You are setting up ki on this machine for the person who asked you to. Read this whole file first. Then ask the three questions in the quick start, install, and only then offer the optional sections. Do not install anything before the questions are answered and the person has said yes to your summary.

## What ki is

- ki starts coding agents (Claude Code, Codex, Cursor, agy) in one of the person's repos, with that repo's GitHub identity and instructions.
- `ki delegate` hands one issue-sized task to one agent, headless, in its own git worktree, and reports one status line.
- A knowledge base is optional. It holds conventions and decisions for the person's repos. Nothing in the quick start needs one.
- Config can be shared through git. Credentials are created on each machine and never copied between machines.

## Rules for you

- Never ask for a token, password, API key or SSH private key in chat. Never print one, never write one into a file you wrote, never put one in a command line.
- The person creates each secret in their own terminal or secret manager. You only check that the file exists, with its permissions, and its hash (`shasum -a 256`), never its contents.
- Logins that open a browser or need a prompt (`gh auth login`, `ssh-keygen`) are the person's to run. Tell them the exact command, then verify the result with the command shown below.
- Write configuration only through `ki` commands or into the configuration directory you create. Do not edit files in a kio checkout. Shell profiles and git's global ignore file are the person's to edit: give them the line, do not write it yourself.
- Do not commit or push anything to the person's repos. The configuration directory is the person's own git repo; they commit it when they are ready.
- Do not set `KI_TOKEN` or `GH_TOKEN` anywhere. `ki` exports them per session.

## Quick start

About five minutes, no knowledge base. Each step says what it produces.

### 1. Three questions

Ask them in one short batch. Record each answer in your reply so the person can correct it.

1. **Agents.** Which agents are installed? Check first: `command -v claude codex agy cursor-agent`. Then confirm with the person, and ask which one a bare `ki` should start first, second and so on. That order is the planner role (step 6). Only agents on PATH can be named there.
2. **GitHub logins.** For each GitHub account this machine uses: a label (`me`, `work`, `client`), the GitHub login, the commit email, and the folder its repos live in: one folder per account, for example `~/dev/personal/` or `~/dev/work/`. One account is the common case: label `me`.
3. **Repos.** For each repo ki should start sessions in: its GitHub URL and which label owns it.

Summarise the answers in a short table and get a yes before you continue:

| Label | GitHub login | Commit email | Folder | Repos |
| --- | --- | --- | --- | --- |
| me | alice | alice@example.com | `~/dev/personal/` | `https://github.com/alice/notes` |

The knowledge base, delegation and the notes vault are not part of these questions. Offer them only when the person asks (see [Optional sections](#optional-sections)).

### 2. Check the machine

```bash
command -v git gh          # both required
gh auth status             # one entry per label from question 2
```

Install what is missing with the person's usual package manager, along with one agent CLI (Claude Code, Codex, Cursor or agy) and its own login. For each GitHub login without an entry, the person runs `gh auth login` and signs in as that login. Do not try to log in for them.

For each folder from question 2, check the commit email git uses inside a repo there (or after they clone one):

```bash
git config --get user.email
```

If it differs from the email in question 2, the person fixes their git `includeIf` setting, see [laptop setup, step 2](docs/laptop-setup.md#2-one-folder-per-github-identity). `ki` refuses sessions where they differ.

### 3. Install the binary

Pick one route. Each puts `ki` on the PATH; `~/.local/bin` is the usual place for the release route. `ki version` must print.

**Homebrew** (macOS or Linux, if `brew` is installed). It builds `ki` from the release's source, so it pulls in Go as a build dependency:

```bash
brew install kislow/tap/kio
```

Then run `ki setup skills` (step 4).

**Release archive** (no Go needed). Use the release number from [the releases page](https://github.com/kislow/kio/releases), without the `v`:

```bash
version=<version>
os=$(uname -s | tr '[:upper:]' '[:lower:]')                # darwin or linux
case "$(uname -m)" in arm64|aarch64) arch=arm64 ;; x86_64|amd64) arch=amd64 ;; esac
archive="kio_${version}_${os}_${arch}.tar.gz"
base="https://github.com/kislow/kio/releases/download/v${version}"
mkdir -p ~/.local/bin /tmp/kio-download && cd /tmp/kio-download
curl -fsSLO "$base/$archive"
curl -fsSLO "$base/checksums.txt"
grep " $archive\$" checksums.txt | shasum -a 256 -c -      # expects: OK
tar -xzf "$archive" -C ~/.local/bin ki
```

**Go** (needs Go 1.27 or later). The binary lands in `$(go env GOPATH)/bin`:

```bash
go install github.com/kislow/kio/cmd/ki@latest
```

Check with `command -v ki` and `ki version`. If `ki` is not found, the person adds the folder to PATH in their shell profile.

### 4. The skills

The `/ki-*` skills and the agent instructions are inside the binary, so there is nothing to clone or build. Link the skills for Claude Code (`~/.claude/skills`) and the other agents (`~/.agents/skills`):

```bash
ki setup skills
```

It writes the binary's own copy under `~/.config/ki/skills/<version>/` and links it, and it leaves any existing skill folder that is not a link alone. To upgrade, install the new binary with the route from step 3, then run `ki update`: it pulls a linked configuration that is a git checkout and runs `ki setup skills` again.

### 5. The configuration directory

The person's configuration is a directory of their own. Start it from the minimal example, taken from the release that matches the binary (`ki version` prints the number; use `main` for a build from source):

```bash
ref="v$(ki version | sed 's/^v//')"
mkdir -p ~/code/my-config && cd ~/code/my-config && git init
for f in projects.yaml config.yaml models.env aliases.yaml mcp-config.json; do
  curl -fsSLO "https://raw.githubusercontent.com/kislow/kio/$ref/examples/config-minimal/$f"
done
```

Edit `projects.yaml` from the answers, one entry per GitHub login under `identities:` and one per repo under `projects:`. For the two-account example from question 2:

```yaml
identities:
  me:
    github_user: alice               # the GitHub login
    email: alice@example.com         # the commit email for that folder
    credential: me                   # the label is fine
    ingest: false                    # no knowledge base, so nothing is ingested
  work:
    github_user: alice-work
    email: alice@work.example.com
    credential: work
    ingest: false

projects:
  - name: notes
    repo: https://github.com/alice/notes
    identity: me
    brains: [cortex]
  - name: client-site
    repo: https://github.com/alice-work/client-site
    identity: work
    brains: [cortex]
```

Leave the other four files of the example as they are. Then validate and link:

```bash
ki validate ~/code/my-config/projects.yaml     # expects: ok: N project(s), N identit(ies)
ki setup config ~/code/my-config
```

Fix any error `ki validate` reports in `projects.yaml` and run it again. Do not weaken the check.

### 6. This machine: identities and the planner

```bash
ki setup machine me work         # the labels from question 2
```

This writes `~/.config/ki/machine.yaml`. Add a `roles` block to that file, keeping what `ki setup machine` wrote:

```yaml
roles:
  planner:
    providers: [claude, codex]   # installed agents, in the order a bare ki starts them
```

Provider names are the commands on PATH: `claude`, `codex`, `agy` or `cursor-agent`. List only the ones installed, and a single agent is a one-item list. A bare `ki` starts the first provider that is available; one over its usage limit is skipped, and `ki doctor` says which.

Do not put `KI_AGENT` in the shell profile: it outranks this role and turns off failover. It is a per-shell override only: `export KI_AGENT=codex` in one terminal, for the session that needs it.

### 7. Agents

```bash
ki setup claude      # only if Claude Code is installed; run once per machine
ki setup codex       # only if Codex is installed
```

`ki setup codex` writes the agent instructions into `~/.codex/AGENTS.md`.

Ask the person to add these two lines to their global git ignore (`~/.config/git/ignore`, or the file `git config --global core.excludesFile` names). `ki cursor` writes both files into a repo, and `ki doctor` checks for them even when Cursor is not installed:

```
.cursor/mcp.json
.cursor/rules/ki.mdc
```

Cursor is set up per repo with `ki cursor`, when the person asks. Its attribution switches live in Cursor's settings (Settings > Agents > Attribution), which `ki` cannot check.

### 8. Check, then start

```bash
ki doctor            # expects: all good
```

Fix each failing line as it says. A `gh login` line means the person runs `gh auth login` for that login. If a line still fails after a fix, say which line and the exact message.

Then show the person one of their repos, and run `ki status` there:

```bash
cd ~/dev/personal/notes    # a repo from question 3
ki status            # the repo, its identity, its brains
```

Ask the person to start a session in that repo with `ki`. It is interactive, so you cannot start it for them.

## Optional sections

Only when the person asks for one of these. Each stands alone.

### Delegation: roles and vendor rotation

Ask which class of work to delegate (`execution` is the usual one), which vendors, and for several vendors the weight of each and a daily job cap if wanted. Add the role under the existing `roles:` key in `~/.config/ki/machine.yaml`:

```yaml
roles:
  planner:
    providers: [claude, codex]
  execution:
    strategy: ordered            # or round-robin, with weights
    providers:
      - {agent: claude, model: sonnet, weight: 2, daily_jobs: 20}
      - codex:gpt-5-mini
```

One vendor is one `ordered` provider. Name only agents on PATH. The full policy, failover and the queue are in [ki delegate](docs/ki-delegate.md#roles-letting-the-machine-pick-the-vendor). `ki doctor` prints a line for each role.

Inside a `ki` session, agent work goes to `ki delegate` and reviews to `ki skill run ki-review`; the instructions say so to every agent, and in Claude Code a hook also denies built-in subagents other than the ones in `sessions.subagents.allow` (default `[Explore]`, read-only search). To allow more types, list them under `sessions:` in `machine.yaml`; `["*"]` turns the guard off. [Details](docs/laptop-setup.md#subagents).

### Knowledge base

Ask which of these the person has:

- **An existing server.** They have its URL (`https://...`, not a secret) and a device token from whoever runs it.
- **Their own, on one machine.** The Docker Compose stack, which needs a clone of the repository (`git clone --depth 1 https://github.com/kislow/kio ~/.local/share/kio`), see [local stack](docs/local-stack.md). Its setup script writes a token file to `~/.local/share/kio/deploy/compose/local/ki-token`.
- **Their own, in a cluster.** See [self-hosting](docs/self-hosting.md). The operator of that cluster does most of it.

Once there is a URL and a token:

1. The token is a random value in one file, `~/.config/ki/token`. The person creates or copies it in their own terminal, never through chat. For a new token made on this machine:

   ```bash
   mkdir -p ~/.config/ki && chmod 700 ~/.config/ki
   umask 077; openssl rand -hex 32 | tr -d '\n' > ~/.config/ki/token
   shasum -a 256 < ~/.config/ki/token      # the hash the server operator registers
   ```

   For the Compose stack, the person copies `ki-token` from the path above to `~/.config/ki/token`. For an existing server, the person copies the token from their secret manager.

2. Make the file private to the person and check it without reading it: `chmod 600 ~/.config/ki/token`, then `ls -l ~/.config/ki/token`, which must show `-rw-------`.

3. Check that the server answers, without a token: `curl -s https://<knowledge-base host>/healthz` expects `ok`. If the server is private, the person connects the network it needs first. A 403 from outside that network is expected.

4. Set `knowledge_base.url` in `config.yaml` of the configuration directory. `KI_MCP_URL` in the person's shell overrides it on this machine only.

5. Run `ki setup codex` again if Codex is installed, then `ki doctor`.

`ki-mcp` is the knowledge-base server. It is in the release archive too, but extract it only when the person runs a server from that binary: the Compose stack and a cluster run their own images.

### Notes vault

Only if the person keeps their notes in a git repo. The person exports the clone in their shell profile:

```bash
export KI_VAULT="$HOME/notes"
```

Add the vault repo to `projects.yaml` under the identity that owns it. Then `ki doctor` gains a Vault section that says whether `KI_VAULT` is a usable clone. Sync, a second machine and pushing from a server are covered in [vault and diagrams](docs/vault.md).

## Final message

Finish with a short summary for the person:

- The answers you recorded, one line each.
- What you installed and where: the `ki` path, the skill links, the configuration directory, `~/.config/ki/machine.yaml`, and any token file (path and mode only).
- What still needs the person: each `gh auth login`, the two global ignore lines, the PATH line if one was needed, the first commit of the configuration repo, and any token hash for a knowledge base.
- The output of the last `ki doctor` and `ki status`.
- Anything skipped, and why. Without a knowledge base, say that `ki ask` and `ki kb` wait for one.

Then ask the person to start a session in one of their repos with `ki`. Suggest `/ki-decision` for any non-obvious choice made in the interview, so it is recorded.
