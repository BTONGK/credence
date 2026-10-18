package store

const schema = `
CREATE TABLE IF NOT EXISTS meta (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS sessions (
  id                 TEXT PRIMARY KEY,
  harness            TEXT NOT NULL,
  harness_session_id TEXT NOT NULL,
  parent_session_id  TEXT NOT NULL DEFAULT '',
  agent_type         TEXT NOT NULL DEFAULT '',
  repo               TEXT NOT NULL DEFAULT '',
  repo_name          TEXT NOT NULL DEFAULT '',
  branch             TEXT NOT NULL DEFAULT '',
  cwd                TEXT NOT NULL DEFAULT '',
  title              TEXT NOT NULL DEFAULT '',
  started_at         TEXT NOT NULL,
  ended_at           TEXT NOT NULL DEFAULT '',
  last_activity      TEXT NOT NULL,
  state              TEXT NOT NULL DEFAULT 'open',
  models             TEXT NOT NULL DEFAULT '[]',
  human              TEXT NOT NULL DEFAULT '',
  agent_config       TEXT NOT NULL DEFAULT '',
  harness_version    TEXT NOT NULL DEFAULT '',
  tokens             TEXT NOT NULL DEFAULT '{}',
  cost_usd           REAL NOT NULL DEFAULT 0,
  transcript_path    TEXT NOT NULL DEFAULT '',
  transcript_sha256  TEXT NOT NULL DEFAULT '',
  transcript_lines   INTEGER NOT NULL DEFAULT 0,
  event_count        INTEGER NOT NULL DEFAULT 0,
  head_hash          TEXT NOT NULL DEFAULT '',
  start_commit       TEXT NOT NULL DEFAULT '',
  skipped            INTEGER NOT NULL DEFAULT 0,
  skip_reason        TEXT NOT NULL DEFAULT '',
  flags              TEXT NOT NULL DEFAULT '[]',
  stop_seen_at       TEXT NOT NULL DEFAULT '',
  report_state       TEXT NOT NULL DEFAULT 'none',
  report_hash        TEXT NOT NULL DEFAULT '',
  report_at          TEXT NOT NULL DEFAULT '',
  UNIQUE (harness, harness_session_id)
);
CREATE INDEX IF NOT EXISTS sessions_activity ON sessions (last_activity);
CREATE INDEX IF NOT EXISTS sessions_parent ON sessions (parent_session_id);

CREATE TABLE IF NOT EXISTS events (
  session_id  TEXT NOT NULL,
  seq         INTEGER NOT NULL,
  ts          TEXT NOT NULL,
  kind        TEXT NOT NULL,
  actor       TEXT NOT NULL,
  payload     TEXT NOT NULL,
  payload_ref TEXT NOT NULL,
  src_path    TEXT NOT NULL DEFAULT '',
  src_line    INTEGER NOT NULL DEFAULT 0,
  hash        TEXT NOT NULL,
  prev_hash   TEXT NOT NULL,
  PRIMARY KEY (session_id, seq)
);
CREATE INDEX IF NOT EXISTS events_kind ON events (session_id, kind);

CREATE TABLE IF NOT EXISTS transcripts (
  path        TEXT PRIMARY KEY,
  harness     TEXT NOT NULL,
  session_id  TEXT NOT NULL DEFAULT '',
  offset      INTEGER NOT NULL DEFAULT 0,
  lines       INTEGER NOT NULL DEFAULT 0,
  size        INTEGER NOT NULL DEFAULT 0,
  mtime       TEXT NOT NULL DEFAULT '',
  updated_at  TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS transcript_lines (
  path    TEXT NOT NULL,
  line_no INTEGER NOT NULL,
  hash    TEXT NOT NULL,
  PRIMARY KEY (path, line_no)
);

CREATE TABLE IF NOT EXISTS spool_offsets (
  path   TEXT PRIMARY KEY,
  offset INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS hook_events (
  id                 INTEGER PRIMARY KEY AUTOINCREMENT,
  harness            TEXT NOT NULL,
  event              TEXT NOT NULL,
  harness_session_id TEXT NOT NULL DEFAULT '',
  transcript_path    TEXT NOT NULL DEFAULT '',
  cwd                TEXT NOT NULL DEFAULT '',
  ts                 TEXT NOT NULL,
  raw                TEXT NOT NULL DEFAULT '{}'
);
CREATE INDEX IF NOT EXISTS hook_events_session ON hook_events (harness, harness_session_id);

CREATE TABLE IF NOT EXISTS artifacts (
  id           TEXT PRIMARY KEY,
  kind         TEXT NOT NULL,
  locator      TEXT NOT NULL UNIQUE,
  content_hash TEXT NOT NULL DEFAULT '',
  repo         TEXT NOT NULL DEFAULT '',
  session_id   TEXT NOT NULL DEFAULT '',
  meta         TEXT NOT NULL DEFAULT '{}',
  created_at   TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS session_artifacts (
  session_id  TEXT NOT NULL,
  artifact_id TEXT NOT NULL,
  relation    TEXT NOT NULL,
  PRIMARY KEY (session_id, artifact_id)
);

CREATE TABLE IF NOT EXISTS claims (
  id              TEXT PRIMARY KEY,
  session_id      TEXT NOT NULL,
  cluster_id      TEXT NOT NULL DEFAULT '',
  origin          TEXT NOT NULL,
  claimant        TEXT NOT NULL,
  type            TEXT NOT NULL,
  title           TEXT NOT NULL,
  statement       TEXT NOT NULL,
  body            TEXT NOT NULL,
  claim_hash      TEXT NOT NULL,
  status          TEXT NOT NULL DEFAULT 'proposed',
  section         TEXT NOT NULL DEFAULT '',
  flags           TEXT NOT NULL DEFAULT '[]',
  notes           TEXT NOT NULL DEFAULT '[]',
  review_state    TEXT NOT NULL DEFAULT 'pending',
  review_body     TEXT NOT NULL DEFAULT '',
  survival_state  TEXT NOT NULL DEFAULT '',
  value           REAL NOT NULL DEFAULT 0,
  self_confidence REAL,
  verify_run      INTEGER NOT NULL DEFAULT 0,
  active          INTEGER NOT NULL DEFAULT 1,
  primary_claim   INTEGER NOT NULL DEFAULT 1,
  created_at      TEXT NOT NULL,
  updated_at      TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS claims_session ON claims (session_id);
CREATE INDEX IF NOT EXISTS claims_review ON claims (review_state);

CREATE TABLE IF NOT EXISTS clusters (
  id               TEXT PRIMARY KEY,
  session_id       TEXT NOT NULL,
  primary_claim_id TEXT NOT NULL,
  origin           TEXT NOT NULL,
  members          TEXT NOT NULL DEFAULT '[]',
  flags            TEXT NOT NULL DEFAULT '[]'
);
CREATE INDEX IF NOT EXISTS clusters_session ON clusters (session_id);

CREATE TABLE IF NOT EXISTS verdicts (
  id         TEXT PRIMARY KEY,
  claim_id   TEXT NOT NULL,
  run        INTEGER NOT NULL DEFAULT 0,
  verifier   TEXT NOT NULL,
  tier       INTEGER NOT NULL,
  check_name TEXT NOT NULL,
  result     TEXT NOT NULL,
  outcome    TEXT NOT NULL DEFAULT '',
  details    TEXT NOT NULL DEFAULT '',
  body       TEXT NOT NULL,
  created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS verdicts_claim ON verdicts (claim_id, run);

CREATE TABLE IF NOT EXISTS attributions (
  claim_id   TEXT PRIMARY KEY,
  method     TEXT NOT NULL,
  body       TEXT NOT NULL,
  overridden INTEGER NOT NULL DEFAULT 0,
  updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS ledger (
  seq             INTEGER PRIMARY KEY AUTOINCREMENT,
  kind            TEXT NOT NULL,
  claim_id        TEXT NOT NULL,
  entry_hash      TEXT NOT NULL UNIQUE,
  prev_entry_hash TEXT NOT NULL,
  body            TEXT NOT NULL,
  created_at      TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS ledger_claim ON ledger (claim_id);
CREATE TRIGGER IF NOT EXISTS ledger_no_update BEFORE UPDATE ON ledger
BEGIN SELECT RAISE(ABORT, 'ledger is append-only'); END;
CREATE TRIGGER IF NOT EXISTS ledger_no_delete BEFORE DELETE ON ledger
BEGIN SELECT RAISE(ABORT, 'ledger is append-only'); END;

CREATE TABLE IF NOT EXISTS jobs (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  kind       TEXT NOT NULL,
  key        TEXT NOT NULL,
  session_id TEXT NOT NULL DEFAULT '',
  claim_id   TEXT NOT NULL DEFAULT '',
  status     TEXT NOT NULL DEFAULT 'pending',
  attempts   INTEGER NOT NULL DEFAULT 0,
  run_after  TEXT NOT NULL,
  last_error TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  UNIQUE (kind, key)
);
CREATE INDEX IF NOT EXISTS jobs_pending ON jobs (status, run_after);

CREATE TABLE IF NOT EXISTS agent_configs (
  id         TEXT PRIMARY KEY,
  harness    TEXT NOT NULL,
  model      TEXT NOT NULL,
  components TEXT NOT NULL DEFAULT '{}',
  first_seen TEXT NOT NULL,
  last_seen  TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS test_runs (
  id         TEXT PRIMARY KEY,
  claim_id   TEXT NOT NULL DEFAULT '',
  repo       TEXT NOT NULL DEFAULT '',
  commit_sha TEXT NOT NULL DEFAULT '',
  target     TEXT NOT NULL DEFAULT '',
  command    TEXT NOT NULL DEFAULT '',
  runs       INTEGER NOT NULL DEFAULT 0,
  passes     INTEGER NOT NULL DEFAULT 0,
  output     TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS model_usage (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  ts         TEXT NOT NULL,
  purpose    TEXT NOT NULL,
  provider   TEXT NOT NULL,
  model      TEXT NOT NULL,
  tokens_in  INTEGER NOT NULL DEFAULT 0,
  tokens_out INTEGER NOT NULL DEFAULT 0,
  cost_usd   REAL NOT NULL DEFAULT 0,
  session_id TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS review_timings (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  session_id TEXT NOT NULL,
  ms         INTEGER NOT NULL,
  claims     INTEGER NOT NULL DEFAULT 0,
  surface    TEXT NOT NULL DEFAULT 'ui',
  at         TEXT NOT NULL
);
`
