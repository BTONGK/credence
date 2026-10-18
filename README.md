# Credence

**Agents propose credit; evidence decides.**

Credence turns AI coding-agent sessions into credit reports. It captures what
Claude Code, Codex and Cursor sessions actually did, collects the claims agents
make about their work ("fixed the race", "all tests pass"), and checks each one
against evidence: the captured events, git, tests re-run in a clean worktree,
and CI. You review the result in a keyboard-first local UI or the CLI, and every
review lands in a signed, hash-chained ledger you can export, verify and share.

It is local-first. Nothing leaves your machine unless you configure a model
judge or push the ledger's git notes yourself.

```
$ credence demo

Sam runs a small bakery and asked Claude Code to update the bakery's website:
holiday hours, a phone-friendly layout, a cake order form, and the hours on Google.
Here is everything the agent said it did, checked against what really happened.

  ✓ Backed by evidence  “I added the holiday hours to index.html.”
                        The session changed index.html.
  ◐ Partly backed       “I made the homepage easier to read on phones.”
                        2 files were changed here (index.html and styles.css). A
                        quick look will match them to this claim.
  ✗ Contradicted        “I also fixed the Instagram link in footer.html.”
                        The session left footer.html unchanged.
  ✓ Backed by evidence  “I added a cake order form to contact.html.”
                        The session changed contact.html.
  ✗ Contradicted        “All 6 tests pass.”
                        The last test run, `npm test` at 7:41, failed: 5 passed,
                        1 failed.
  ✓ Backed by evidence  “The site is live at https://sunrise-bakery.pages.dev.”
                        The link came back from `npx wrangler pages deploy .
                        --project-name sunrise-bakery` at 8:12.
  ? Yours to confirm    “I updated your holiday hours on Google.”
                        The work went through the google business update hours
                        tool.

The agent made 7 claims: 3 backed by evidence, 1 partly backed, 1 yours to confirm, 2 contradicted.
```

The demo then opens the full report in your browser, with the whole
conversation behind every verdict.

## Quick start

On macOS or Linux:

```sh
curl -fsSL https://tokenmanagementgroup.com/credence/install.sh | sh
credence demo         # opens an example report in your browser
```

The installer downloads the build for your machine from the
[latest release](https://github.com/BTONGK/credence/releases/latest), checks
its SHA-256 and puts `credence` in `~/.local/bin`. To build from source
instead, use Go 1.27+:

```sh
go install github.com/BTONGK/credence/cmd/credence@latest
# or, from a checkout:
make build            # writes bin/credence and bin/credenced
```

Node is only needed to work on the UI (the built UI is committed in `ui/dist`
and embedded in the binary).

`credence demo` keeps everything inside its own folder (`~/credence-demo`), so
it is the safe way to look around. When you want to capture your own sessions:

```sh
credence init --dry-run   # shows exactly what would change
credence init             # capture hooks + credence_claim MCP tool + background daemon
credence ui               # open the local UI
credence doctor           # check capture, daemon, keys, ledger and model setup
```

`init` edits your harness settings (marked `# credence-managed`, with a
backup) and installs a user service that runs the daemon. `credence uninstall`
reverses it.

## How it works

1. **Capture.** Harness hooks and transcript watchers append raw events to a
   spool; the daemon normalizes them into a per-session, hash-chained event
   log, redacting secrets before anything is stored.
2. **Claims.** Agents file claims with the `credence_claim` MCP tool. An
   independent auditor reads the session *without* seeing those claims and
   extracts its own. A reconciler clusters claims that describe the same work.
3. **Verification, in tiers.** No later tier can overrule a factual failure.
   - **Tier 0** resolves every evidence target and checks the event chain.
   - **Tier 1** is deterministic. It covers git facts and tests re-run in a
     clean worktree, before and after the commit. It also runs mutation
     checks, GitHub CI status, and your own plugins.
   - **Tier 2** is an optional blind judge. It makes two passes and never
     sees the claimant's confidence.
   - **Tier 3** is you.
4. **Report.** Each session gets five sections: Verified, Unverified,
   Contradicted, Found by auditor, and Reported failures.
5. **Review.** In the UI or `credence review`, `j`/`k` move, `a` accepts, `e`
   edits, `d` disputes (with a reason: overstated, wrong contributor, not
   valuable, factually wrong, duplicate) and `shift+A` accepts every verified
   claim. A contradicted claim cannot be accepted; you can edit it down to
   what the evidence supports.
6. **Ledger.** Every decision is a signed entry chained to the previous one.
   The ledger feeds several things:
   - reputation per agent configuration, harness, model and repo,
   - calibration (the Brier score of agents' stated confidence),
   - 7- and 30-day survival of the code behind accepted claims.

### Anti-gaming rules worth knowing

- A test written in the same session can only *corroborate* a fix. Credence
  then mutates the lines the fix added (`>=`→`>`, `and`→`or`, …). If the
  test catches the mutants, the fix can be `verified`. If not, it stays
  `corroborated` and the report lists the mutants that survived.
- Test commands come from your repo policy, from commands the agent itself
  ran (with no shell metacharacters), or from fixed templates. A claim can
  never introduce a new command, and the test environment is stripped of
  credentials.
- When a session edits `credence.yaml`, that session's own claims are
  checked against the policy from before the session.
- Claim counts are never the metric. Value is per cluster, weighted by claim
  type and capped by policy.

## CLI

| Command | What it does |
|---|---|
| `credence init` / `uninstall` | install or remove hooks, the MCP tool and the service |
| `credence sessions [--needs-review]` | list captured sessions |
| `credence show <session\|claim>` | a session's credit report or one claim card |
| `credence review [session]` | keyboard review in the terminal |
| `credence verify <claim> [--rerun]` | re-run verification |
| `credence ledger [--by agent\|repo\|harness\|model] [--verify [file]]` | reputation, calibration, chain verification |
| `credence export --format json\|csv [--share-person]` | export the signed ledger |
| `credence contributors [--format md] [--release v1.2]` | credits for CONTRIBUTORS files or release notes |
| `credence pr-comment [--trusted-keys ids]` | summarize a PR's reviewed claims (used by the Action) |
| `credence team rollup <exports…> [--notes repo]` | aggregate teammates' ledgers |
| `credence plugin list\|check\|add\|remove` | verifier plugins |
| `credence import agent-trace\|git-ai\|entire\|otlp\|transcript <path>` | import from other tools |
| `credence backfill [--since 30d]` | capture older transcripts |
| `credence config [get\|set key value]` | user configuration |
| `credence ui`, `demo`, `doctor`, `start`, `stop`, `daemon`, `version` | the rest |

Everything in the UI has a CLI equivalent, and most commands take `--json`.

## Configuration

There are two files, and the split is deliberate.

**`~/.credence/config.yaml`** belongs to you. Only this file can choose a
model provider, an endpoint or API key, a budget, or plugins:

```sh
credence config set judge.provider anthropic        # uses ANTHROPIC_API_KEY
credence config set judge.model claude-opus-5-5
credence config set auditor.provider openai-compatible
credence config set auditor.base_url http://127.0.0.1:11434/v1   # e.g. a local Ollama
credence config set budget.monthly_usd 20
```

Model calls are off by default. The rules auditor and tiers 0 and 1 need no
model.

**`credence.yaml`** in a repository is shared policy. It sets which
verifiers each claim type requires, claim weights, test commands, survival
windows, mutation settings and whether to mirror the ledger to git notes.
`credence init --policy` writes an annotated example. A repository can make
verification stricter and can turn model calls *off*. It can never pick a
model, an endpoint or a plugin, because a cloned repo should not be able to
send your transcripts anywhere.

## Integrations

### GitHub Action

When reviewers enable `ledger.git_notes` and push `refs/notes/credence`,
the Action comments on each pull request. The comment lists the reviewed
claims for the PR's commits and checks their signatures.

```yaml
# .github/workflows/credence.yml
on: pull_request
permissions: { contents: read, pull-requests: write }
jobs:
  comment:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
        with: { fetch-depth: 0 }
      - uses: BTONGK/credence/action@main
        with:
          trusted-keys: ${{ vars.CREDENCE_TRUSTED_KEYS }}
```

A valid signature proves an entry is unaltered, not who signed it. Set
`trusted-keys` (from `credence ledger --verify`) in a repository variable,
which a pull request cannot change, so only your reviewers' keys count. See
[`examples/credence-pr.yml`](examples/credence-pr.yml).

### OpenTelemetry

The daemon accepts OTLP/HTTP JSON logs and traces from Claude Code, Codex
and GenAI-instrumented agents:

```sh
export CLAUDE_CODE_ENABLE_TELEMETRY=1
export OTEL_LOGS_EXPORTER=otlp
export OTEL_EXPORTER_OTLP_PROTOCOL=http/json
export OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:7171
export OTEL_EXPORTER_OTLP_HEADERS="Authorization=Bearer $(cat ~/.credence/otlp.token)"
export OTEL_RESOURCE_ATTRIBUTES="credence.repo=$PWD"
```

Sessions already captured from transcripts are not duplicated.

### Imports

- `credence import agent-trace [path]` and `credence import git-ai` add
  line-level provenance for attribution.
- `credence import entire` reads Entire checkpoints.
- `credence import transcript <file> [--harness h]` takes a chat-style JSONL
  transcript in Anthropic or OpenAI message shape (see `adapters/generic`),
  or a Claude Code / Codex / Cursor transcript with `--harness`.

### Plugins

A verifier plugin is any executable that speaks JSON-RPC over stdio (spec
section 11). For example:

```sh
credence plugin list                          # registered plugins and the registry
credence plugin check -- python3 my_plugin.py # protocol conformance checks
credence plugin add todos                     # bundled example: fails commits that add TODO/FIXME
```

Then require it in a repo policy: `claim_types: { feature: { require: [resolve, plugin:todos] } }`.
To list a plugin, add it to [`plugins/registry.json`](plugins/registry.json);
[`plugins/no_new_todos.py`](plugins/no_new_todos.py) is a stdlib-only
template.

### Team rollups

Each teammate exports their ledger and decides whether to be named:

```sh
credence export --format json --out maya.json --share-person   # signed opt-in
credence team rollup ./ledgers/ --notes . --by agent --since 30d
credence team rollup ./ledgers/ --by person --format md
```

Rollups take only the exports and git notes, never transcripts.
- **Verification:** every source is verified, and you can pin trusted keys
  with `--trusted-keys`.
- **Grouping:** by agent configuration by default, or by repo, harness or
  model.
- **People:** a person-level row appears only for people who signed an
  opt-in. Rows are alphabetical, so the view is not a leaderboard.

## Security and privacy

- **Data stays local.** Data lives in `~/.credence` (SQLite plus spool),
  readable only by you. Secrets are redacted before storage.
- **Daemon API.** The daemon listens on 127.0.0.1 only.
  - The API needs a bearer token.
  - Requests are checked for a loopback `Host` (against DNS rebinding) and
    an `Origin` on writes.
  - The UI receives its token through a URL fragment, which is never sent
    to the server.
- **Clean test runs.** Tests run in throwaway git worktrees with
  credentials removed from the environment, never in your checkout.
- **Signed ledger.** Entries are signed with a per-user ed25519 key in
  `~/.credence/keys`, and exports carry the public key. The key is yours;
  back it up if your ledger matters to you.
- **No telemetry.**

## Spec

The claim, verdict, event, ledger, MCP and plugin formats are specified in
[`spec/README.md`](spec/README.md) (v0.9, CC BY 4.0) with JSON Schemas in
[`spec/schemas`](spec/schemas). Two suites let other implementations prove
compatibility:

- [`spec/conformance`](spec/conformance) has format fixtures: canonical
  JSON, claim ids, evidence targets, and a signed ledger with tampered
  copies.
- [`conformance/`](conformance) has synthetic Claude Code, Codex, Cursor and
  generic transcripts, each with the normalized events an adapter must
  produce.

Plugins are checked with `credence plugin check`.

## Development

```sh
make check   # gofmt, go vet, go test, UI typecheck
make ui      # rebuild ui/dist (commit it: the Go build embeds it)
make dev     # Vite dev server with hot reload against a daemon on :7171
make demo    # build and open the demo in ./demo-home
```

The packages:

| Package | Contents |
|---|---|
| `cmd/` | CLI and daemon |
| `capture/` | hooks |
| `adapters/` | harness transcript parsers |
| `daemon/` | ingest, claims, auditor, pipeline, ledger, survival, rollup, server |
| `verifiers/` | the verifier tiers |
| `importers/` | imports from other tools |
| `mcp/` | the `credence_claim` server |
| `ui/` | React + TypeScript |
| `spec/` | specification and format fixtures |
| `conformance/` | adapter fixture sessions |
| `action/` | the GitHub Action |
| `plugins/` | the plugin registry |

## Status

This is v0.9 software. The core is complete and tested end to end against a
synthetic session:

- capture, claims, verification tiers 0 and 1, review, the ledger,
- the UI and CLI,
- the GitHub Action, rollups and plugins.

Known gaps:

- **Importers.** The Agent Trace, git-ai, Entire and OTLP importers follow
  those projects' public formats as of 2026. They are tested only against
  fixtures, not against real exports.
- **Cursor adapter.** It has seen less real-world data than the Claude Code
  and Codex adapters.
- **Mutation operators.** They are line-level and language-agnostic, so a
  mutant that breaks compilation counts as caught.
- **Counterfactual attribution.** Not built. The roadmap lists it as an
  experiment.

## License

Code: [Apache-2.0](LICENSE). Specification and schemas: CC BY 4.0.
