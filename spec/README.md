# Credence record formats, v0.9 (draft)

This document specifies the records Credence produces and consumes: events,
claims, verdicts, attributions and ledger entries, plus the `credence_claim`
MCP tool, the verifier plugin protocol and the `credence.yaml` policy file.
It is written so that another tool can produce claims Credence will verify,
or verify a Credence ledger without running Credence.

The key words MUST, SHOULD and MAY are used as in RFC 2119.

The specification text and the JSON Schemas in [`schemas/`](schemas) are
licensed under [CC BY 4.0](https://creativecommons.org/licenses/by/4.0/).
The reference implementation in this repository is licensed under Apache 2.0.

**Thesis:** agents propose credit; evidence decides. A claim is a falsifiable
statement of contribution. Verifiers try to falsify it, cheapest first. A
human makes the final call, and every call is signed into a hash-chained
ledger.

---

## 1. Canonical JSON and identifiers

Records are hashed over their **canonical JSON** encoding:

- object keys sorted by the byte values of their UTF-8 encoding,
- no insignificant whitespace,
- no HTML escaping (`<`, `>`, `&` are written literally),
- numbers in their shortest round-trip form (`0.85`, `50`, `1e-7`), as
  ECMAScript `Number.prototype.toString` and Go's `encoding/json` write them,
- timestamps as RFC 3339 strings with up to nanosecond precision and
  trailing zeros of the fraction removed (`2026-10-17T20:01:02.1234Z`).

`hash(x)` is `"sha256:" + lowercase hex(SHA-256(canonical(x)))`.

Required fields (those listed as required in the schemas) are always
present, even when empty (`""`, `[]`). Optional fields are omitted when empty,
zero or null. Implementations MUST follow this rule exactly, because it
changes the bytes that are hashed.

`short_id(h, n)` is the first `n` characters of the Crockford base32
encoding (alphabet `0123456789ABCDEFGHJKMNPQRSTVWXYZ`, no padding) of the
32 raw bytes of hash `h`.

| Record | Id | Content address |
|---|---|---|
| Claim | `clm_` + `short_id(hash(claim), 10)` with `id` set to `""` and `signature` removed | yes |
| Verdict | `vrd_` + `short_id(hash(verdict), 10)` with `id` set to `""` | yes |
| Cluster | `cls_` + 10 chars | derived from member ids |
| Session | `ses_` + first 10 hex chars of SHA-256(`<harness>:<harness session id>`) | no |
| Artifact | `art_` + `short_id("sha256:" + hex(SHA-256(locator)), 10)` | by locator |

Because ids are content addresses, any edit to a claim produces a different
id. Human edits are therefore recorded as decisions in the ledger, never by
rewriting the claim.

## 2. Contributors

Contributor ids are strings with a scheme prefix:

| Form | Meaning |
|---|---|
| `agent:<harness>/<model>@cfg_<hex4>` | an agent configuration: harness, model and a hash of what a builder tunes (instructions, skills, MCP servers) |
| `agent:<tool>/<model>` | an agent known only from imported provenance |
| `subagent:<parent session>/<type>` | a subagent run spawned by a session |
| `human:<handle>` | a person; handles are lowercase slugs |
| `auditor:<name>@<version>` | the auditor that extracted a claim (never receives credit) |
| `tool:<harness>`, `system:<harness>` | event actors for tool output and harness records |

## 3. Events

An event is one normalized step of a session.

```json
{"session_id":"ses_3b2028956d","seq":31,"ts":"2026-10-18T05:34:50.015449Z",
 "kind":"file_edit","actor":"agent:claude-code/claude-opus-5-5@cfg_82c2",
 "payload":{"path":"scheduler/queue.py","added":8,"removed":5,"added_line_hashes":["a957b0a2cbd3aca3"]},
 "payload_ref":"sha256:…","prev_hash":"sha256:…","hash":"sha256:…"}
```

`kind` is one of `session_start`, `prompt`, `message`, `reasoning`,
`tool_call`, `tool_result`, `file_read`, `file_edit`, `search`, `command`,
`subagent_start`, `subagent_stop`, `commit`, `claim_filed`, `compaction`,
`session_end`, `hook`.

Payloads are redacted before storage. `payload_ref` is the hash of the
redacted payload. Events are **hash-chained** in arrival order:

```
hash = hash({"session_id","seq","ts","kind","actor","payload_ref","prev_hash"})
```

where `ts` is RFC 3339 with nanoseconds in UTC and `prev_hash` is the
previous event's `hash` (empty for the first event). A verifier MUST treat a
claim whose cited events fail chain verification as contradicted (tier 0
`integrity` failure).

`added_line_hashes` on `file_edit` payloads are the first 16 hex chars of
SHA-256 of each added line with surrounding whitespace trimmed; lines shorter
than 4 characters after trimming are not hashed. They are used for line
provenance (section 7).

## 4. Claims

```json
{
  "spec": "credence.claim/0.9",
  "id": "clm_126MK49090",
  "session_id": "ses_3b2028956d",
  "claimant": "agent:claude-code/claude-opus-5-5@cfg_82c2",
  "origin": "self",
  "type": "fix",
  "title": "Fixed race condition in job scheduler",
  "statement": "test_concurrency fails at the parent of f7539d5 and passes at f7539d5 in 50 of 50 runs",
  "assertions": [
    {"check": "test_fails_before_passes_after", "test": "tests/test_scheduler.py::SchedulerTest::test_concurrency", "commit": "f7539d5", "runs": 50},
    {"check": "commit_touches", "commit": "f7539d5", "paths": ["scheduler/queue.py"]}
  ],
  "evidence": [
    {"target": "git:commit/f7539d5edba1d12677c72372c3d7388d15ca2cfe", "relation": "produced"},
    {"target": "event:ses_3b2028956d#36-38", "relation": "produced", "note": "scheduler/queue.py"}
  ],
  "co_contributors": [{"id": "human:maya", "basis": "identified the failing test in the first prompt"}],
  "self_confidence": 0.85,
  "created_at": "2026-10-18T05:37:51.015449Z"
}
```

### 4.1 Fields

| Field | Required | Notes |
|---|---|---|
| `spec` | yes | `credence.claim/0.9` |
| `id` | yes | content address (section 1) |
| `session_id` | yes | the session the work happened in |
| `claimant` | yes | contributor id of whoever filed the claim |
| `origin` | yes | `self` (filed by the agent), `auditor` (found by the independent auditor), `human` (added by a reviewer) |
| `type` | yes | see 4.2 |
| `title` | yes | one line, at most 100 characters |
| `statement` | yes | a falsifiable statement of what is now true |
| `rationale` | no | why it matters |
| `assertions` | yes (may be empty) | machine-checkable predicates (4.4) |
| `evidence` | yes (may be empty) | typed pointers (4.3) |
| `evidence_hints` | no | unresolved hints from the claimant (paths, shas, tool call ids) |
| `co_contributors` | no | `{id, basis}`; humans named here may receive a direction share |
| `self_confidence` | no | the claimant's probability, in [0, 1], that the claim will be verified |
| `source_event` | no | seq of the `claim_filed` event |
| `created_at` | yes | RFC 3339 |
| `signature` | no | reserved for claimant signatures; excluded from the content address |

### 4.2 Types

`fix`, `feature`, `diagnosis`, `test`, `refactor`, `docs`, `review`,
`research`, `ops`, `decision`, `negative_result`.

`negative_result` records an approach that was tried and did not work. It is
shown under *Reported failures* and earns a small weight: knowing what does
not work is credit too.

### 4.3 Evidence targets

| Target | Points at |
|---|---|
| `event:<session>#<from>-<to>` | transcript events, inclusive (`#<n>` for one event) |
| `git:commit/<sha>` | a commit in the session's repository |
| `file:<path>@<commit>#L<a>-L<b>` | a file range at a commit (range optional) |
| `test_run:<id>` | a test run Credence executed |
| `ci:github/<owner>/<repo>/<sha>` | CI status for a commit |
| `pr:github/<owner>/<repo>#<n>` | a pull request |
| `issue:github/<owner>/<repo>#<n>` | an issue |
| `unresolved:<hint>` | a hint that could not be resolved |

`relation` is `produced` (the target is the work), `demonstrates` (the target
shows the work is correct, e.g. a test run) or `informed` (context the
contributor used). Attribution ignores `informed` evidence when it looks for
commits.

### 4.4 Assertions

Each assertion is an object with a `check` and check-specific parameters.
Unknown checks are passed to plugins that declare them, and are otherwise
inconclusive.

| Check | Parameters | Tier | Passes when |
|---|---|---|---|
| `commit_in_session` | `commit` | 1 | the commit was made during the session |
| `commit_exists` | `commit` | 1 | the commit exists in the repository |
| `commit_reachable` | `commit` | 1 | the commit is reachable from a branch |
| `commit_touches` | `commit`, `paths[]` | 1 | the commit changes every listed path |
| `file_exists` | `path`, `commit?` | 1 | the file exists at the commit |
| `test_passes` | `test` or `command`, `commit`, `runs?` | 1 | the test passes in every run in a clean worktree of `commit` |
| `test_fails_before_passes_after` | `test` or `command`, `commit`, `runs?`, `overlay?[]` | 1 | it fails in at least one run at the parent and passes in every run at `commit` |
| `command_passes` | `command`, `commit`, `runs?` | 1 | the command exits 0 in every run |
| `command_fails_before_passes_after` | `command`, `commit`, `runs?` | 1 | like the test transition, for a command such as a repro script |
| `mutation_score` | `test` or `command`, `commit`, `min?`, `max_mutants?` | 1 | the test fails against at least `min` (default 0.6) of the mutants made from the source lines `commit` added |
| `ci_green` | `commit?` | 1 | every check run and status on the commit succeeded |
| `pr_merged` | `pr` | 1 | the pull request is merged |
| `issue_closed` | `issue` | 1 | the issue is closed |

A mutant replaces one operator on one added line of a non-test source file
(`==`↔`!=`, `<=`→`<`, `>=`→`>`, ` < `→` <= `, ` > `→` >= `, `&&`↔`||`,
`and`↔`or`, removing `not `, `true`↔`false`, `+=`↔`-=`, ` + 1`↔` - 1`),
outside string literals and comments. Mutants are chosen round-robin across
lines, one per line first. A mutant is caught when the test fails or hangs
past the budget.

Tests run in clean git worktrees, never in the developer's checkout.
Commands come from the repository policy, from commands the agent itself ran
in the session (when they contain no shell metacharacters), or from fixed
templates inferred from the test id. Assertions never introduce new shell
commands on their own. The test environment strips credentials.

## 5. Verdicts and tiers

```json
{"spec":"credence.verdict/0.9","id":"vrd_…","claim_id":"clm_126MK49090",
 "verifier":"tests","tier":1,"check":"test_fails_before_passes_after",
 "result":"pass","outcome":"transition",
 "details":"failed 50/50 at parent f215c4a and passed 50/50 at f7539d5",
 "artifacts":[{"kind":"test_run","locator":"test_run:run_8e4fd01e23"}],
 "duration_ms":133,"created_at":"…"}
```

`result` is `pass`, `fail`, `inconclusive` or `blocked`. `outcome` is a short
machine-readable reason (`transition`, `flaky_after`, `no_failure_before`,
`untouched_paths`, `integrity`, …).

| Tier | Verifiers | Cost |
|---|---|---|
| 0 | `resolve`: evidence targets exist; cited events pass chain verification | free |
| 1 | `git`, `tests`, `mutation`, `github`, plugins | deterministic, local or CI |
| 2 | `judge`: a blind two-pass model judgment of the cited excerpts | model tokens |
| 3 | a human reviewer | attention |

Rules:

1. Tiers run in order. A tier-0 `fail` or `blocked` stops verification.
2. **No later tier overrules an earlier factual failure.** A tier-0 or
   tier-1 `fail` makes the claim `contradicted`; the judge is not consulted,
   and a reviewer cannot accept it (they may edit it to what the evidence
   supports, or dispute it).
3. A code change (`fix`, `feature`, `refactor`, `ops`) whose only passing
   tests were written in the same session is at most `corroborated`, unless
   a `mutation` verdict passed for it. When `tests.mutation.enabled` is on
   (the default), the `mutation` verifier runs on its own in that case; a
   score below the minimum is `inconclusive` there (a weak test is not
   evidence against the fix) and adds the flag `weak_test`. An explicit
   `mutation_score` assertion that scores below its minimum `fail`s.
4. The judge never sees the claimant's confidence or the auditor's opinion.
   Pass 1 extracts what the excerpts show; pass 2 rules on the claim against
   that extraction.

### 5.1 Status

| Status | When |
|---|---|
| `verified` | every required verifier passed (section 9) |
| `corroborated` | only weaker evidence passed, e.g. tests that were born in the session for a code change, or the judge alone |
| `contradicted` | a tier-0 or tier-1 verifier failed, or the judge ruled `contradicted` |
| `overstated` | the judge ruled the claim says more than the evidence shows |
| `unsupported` | the judge found no support |
| `unverified` | plausible, but nothing machine-checkable backs it yet |
| `blocked` | the evidence could not be resolved |
| `proposed`, `resolved` | intermediate states while verification runs |

### 5.2 Report sections

A session's credit report groups claims into five sections, in this order of
precedence:

1. **Contradicted**: status `contradicted` or `overstated`.
2. **Reported failures**: type `negative_result`.
3. **Found by auditor**: claims only the auditor found (no self-claim in
   their cluster).
4. **Verified**: status `verified` or `corroborated`.
5. **Unverified**: everything else.

Self-claims and auditor claims describing the same work are reconciled into
a cluster; the primary claim carries the review and the credit.

## 6. The independent auditor

The auditor extracts claims from the session transcript **without seeing
the agent's self-claims**. The deterministic rules auditor
(`auditor:rules@0.9`) works from facts (tests run, files edited, commits,
repro scripts that fail then pass). A model auditor MAY be configured by the
user; it receives a rendered trace with `claim_filed` events removed.

Work the auditor finds that the agent did not claim appears under *Found by
auditor*. Claims the agent made that the auditor did not find still go
through verification; the auditor's silence is not evidence against them.

## 7. Attribution

```json
{"claim_id":"clm_126MK49090","method":"line_provenance+direction",
 "shares":[
   {"contributor":"agent:claude-code/claude-opus-5-5@cfg_82c2","share":0.7,"basis":"wrote lines in f7539d5"},
   {"contributor":"human:maya","share":0.3,"basis":"direction: identified the failing test in the first prompt"}]}
```

Shares sum to exactly 1 and are rounded to 0.01.

- **Line provenance** (code claims): each added line of the claim's commits
  is hashed (section 3) and matched against the `added_line_hashes` of
  captured edits, including subagents' edits. Lines the session did not
  write are attributed to the human unless the file was written by an agent
  shell command. Provenance imported from Agent Trace or git-ai fills in
  lines the session did not capture.
- **Event authorship** (other claims): weights by the kind of cited events
  (edits 3, commands 1.5, analysis 1, investigation 0.5).
- **Direction**: humans named as co-contributors (other than "did the work")
  receive a configurable share (default 30%) split between them.
- A reviewer MAY override the split (`method` becomes
  `human_override:<handle>`).

## 8. Ledger

Every human decision and every survival check is a ledger entry.

```json
{
  "spec": "credence.ledger/0.9",
  "seq": 1,
  "kind": "decision",
  "claim_id": "clm_126MK49090",
  "claim_hash": "sha256:…",
  "claim": { "…": "full claim snapshot" },
  "status": "verified",
  "verdict_hashes": ["sha256:…"],
  "attribution": { "…": "…" },
  "human_decision": {"decision": "accepted", "by": "human:maya", "at": "…"},
  "repo": "/path/to/repo",
  "commits": ["f7539d5edba1d12677c72372c3d7388d15ca2cfe"],
  "agent_config": "agent:claude-code/claude-opus-5-5@cfg_82c2",
  "prev_entry_hash": "sha256:…",
  "entry_hash": "sha256:…",
  "signature": "ed25519:7cf4f56eba79:…",
  "created_at": "…"
}
```

- `decision` is `accepted`, `edited` (with `edited_title` and/or
  `edited_statement`), `disputed` (with `reason`, one of `overstated`,
  `wrong_contributor`, `not_valuable`, `factually_wrong`, `duplicate`) or
  `rejected`. A `note` is optional.
- `kind: survival` entries carry `survival: {window, result, ratio}` where
  `result` is `surviving`, `decayed` or `reverted`.
- `claim_hash` MUST equal the content hash of `claim` (section 1).
- `entry_hash = hash(entry)` with `entry_hash` and `signature` set to `""`.
- `prev_entry_hash` is the previous entry's `entry_hash` (empty for the
  first).
- `signature = "ed25519:" + key_id + ":" + base64(Ed25519(entry_hash as UTF-8 bytes))`
  where `key_id` is the first 12 hex chars of SHA-256 of the 32-byte public
  key.

### 8.1 Export

`credence export --format json` writes:

```json
{"spec":"credence.ledger/0.9","exported_at":"…","public_key":"ed25519:<base64>",
 "key_id":"7cf4f56eba79","head":"sha256:…","entries":[…]}
```

A verifier checks, for every entry in order: the chain link, `entry_hash`,
`claim_hash` and the signature against `public_key`.
`credence ledger --verify export.json` does exactly this.

### 8.2 Git notes mirror

When a repository's policy sets `ledger.git_notes: true`, each entry is also
appended to `refs/notes/credence` on each of its commits as one JSON line:

```json
{"entry": { "…": "full ledger entry" }, "public_key": "ed25519:<base64>"}
```

Anyone with the repository can verify these offline. The Credence GitHub
Action reads them to comment on pull requests. Pushing the notes ref is
always a manual, explicit step. A signature shows an entry is unaltered,
not who holds the key; consumers SHOULD let maintainers pin trusted key ids
(the Action's `trusted-keys` input).

### 8.3 Team rollups

`credence team rollup` aggregates exports from several people, and
optionally a repository's notes, without any transcript. Every source is
verified as in 8.1; an export that fails is excluded whole, a note that
fails is dropped alone. Entries are deduplicated by `entry_hash`, and the
latest decision per claim counts.

Rollups group by agent configuration, repo, harness or model. A person
appears by name only if their export carries a signed opt-in, made with
`credence export --share-person`:

```json
"person_opt_in": {"person":"human:maya","head":"sha256:…",
                  "signature":"ed25519:<key_id>:<base64>"}
```

`signature` is over the UTF-8 bytes of
`credence.person-opt-in/0.9\n<person>\n<head>` with the export's key, and
`head` MUST equal the export's chain head; it names only decisions whose
`human_decision.by` is that person. Everyone else is counted in one
"not opted in" row, and person rows are ordered by name, never by value.

## 9. Policy: `credence.yaml`

```yaml
version: 1
claim_types:
  fix:       { weight: 3, require: [resolve, tests], human_review: required }
  diagnosis: { weight: 3, require: [resolve, judge] }
  docs:      { weight: 1, require: [resolve], auto_accept: { max_brier: 0.15 } }
survival:
  windows: [7d, 30d]
  on_revert: zero_credit
tests:
  command: "python -m pytest {test}"
  default_runs: 3
  budget_seconds: 300
  mutation: { enabled: true, max_mutants: 8, min_score: 0.6 }
ledger:
  git_notes: false
```

`require` lists verifiers that must pass for `verified`: `resolve`, `git`,
`tests`, `mutation`, `ci`, `judge`, `human` or `plugin:<name>`. A `judge` requirement is also met by
a tier-1 before/after transition.

A repository policy can make verification stricter, choose test commands and
turn model calls off. It MUST NOT be able to choose a model provider or
endpoint, register plugins, or relax verification of the session that
edited it: when a session changes `credence.yaml`, its claims are checked
against the policy from before the session.

## 10. The `credence_claim` MCP tool

Agents file claims through an MCP server (`credence mcp`) exposing one tool,
`credence_claim`, whose input is:

```json
{"type":"fix","title":"…","statement":"…","rationale":"…",
 "assertions":[{"check":"…"}],"evidence_hints":["src/x.py","abc1234"],
 "co_contributors":[{"id":"maya","basis":"…"}],"confidence":0.85}
```

The server validates the input and returns a text result. Invalid claims
return `isError: true` with a message beginning `Claim not recorded:` so the
agent can correct them; such calls are ignored. The server does no network
or disk I/O: the daemon reads the call from the transcript, resolves the
hints into evidence and assigns the claim id.

## 11. Verifier plugins

A plugin is an executable registered in the user's config (never by a
repository). It speaks JSON-RPC 2.0 over stdio, one message per line.

```
-> {"jsonrpc":"2.0","id":1,"method":"describe"}
<- {"jsonrpc":"2.0","id":1,"result":{"name":"todos","tier":1,"checks":["no_new_todos"]}}
-> {"jsonrpc":"2.0","id":2,"method":"verify","params":{
     "claim_id":"clm_…","assertion":{"check":"no_new_todos","commit":"f7539d5"},
     "workspace":"/…/worktrees/wt-f7539d5-…","budget_seconds":300,
     "claim":{…},"repo":"/path","commit":"f7539d5…"}}
<- {"jsonrpc":"2.0","id":2,"result":{"result":"fail","outcome":"todos_added",
     "details":"the commit adds 1 TODO/FIXME marker: scheduler/queue.py:88",
     "verifier":"credence/todos@0.1.0"}}
```

`name` MUST match `^[a-z0-9][a-z0-9._-]{0,63}$`. A plugin's verdicts carry
the verifier id `plugin:<name>`, which is also how a policy requires it; a
plugin can therefore never satisfy a built-in requirement such as `tests`.
`result` is `pass`, `fail` or `inconclusive` (anything else is recorded as
`inconclusive`), and a `fail` is a tier-1 factual failure like any other.

`workspace` is a clean, detached worktree of the assertion's commit, created
before the call and removed after it. Plugins MUST finish within
`budget_seconds`, and SHOULD answer unknown methods with JSON-RPC error
`-32601`.

### 11.1 Registry

[`plugins/registry.json`](../plugins/registry.json) lists known plugins:
name, description, checks, source, install instructions and the command to
run. Listing a plugin is a pull request to that file. Credence never
installs or runs a listed plugin on its own; `credence plugin add <name>`
runs the conformance checks below and asks before adding it to the user's
config. Plugins bundled with Credence (currently `todos`, a stdlib-only
Python template) are written to `~/.credence/plugins`.

### 11.2 Plugin conformance

`credence plugin check -- <command…>` checks that a plugin:

1. answers `describe` within 10 seconds,
2. has a valid `name`, a `tier` of 1 or 2 and at least one check,
3. answers `verify` on a scratch repository within `budget_seconds` with a
   valid `result`, explaining it in `details` (advisory), with every artifact
   carrying `kind` and `locator`,
4. answers an unknown method with a JSON-RPC error (advisory).

Declaring a built-in check is allowed but advisory: both verdicts are
recorded.

## 12. Conformance

[`conformance/`](conformance) holds fixtures with expected outputs:

- `canonical.json`: values and their canonical encodings and hashes,
- `claims.json`: claims and their content hashes and ids,
- `targets.json`: evidence targets and their parsed parts,
- `ledger.json`: a signed ledger export that MUST verify, and tampered
  copies that MUST NOT.

`go test ./spec/...` runs them against the reference implementation.

Adapters are checked with the session corpus in the repository's top-level
`conformance/` directory. It has one directory per harness, holding
synthetic transcripts in that harness's native format. Next to each
transcript, `*.expected.json` gives the session metadata, token totals and
normalized events (kind, role, source line, timestamp, payload) that a
compatible adapter emits. Plugins have their own conformance checks
(section 11.2).
