// Shapes served by the daemon's /api (see daemon/view and spec/types.go).

export interface Tokens {
  input: number
  output: number
  cache_read: number
  cache_write: number
}

export interface SessionRow {
  id: string
  harness: string
  harness_session_id: string
  parent_session_id?: string
  agent_type?: string
  repo: string
  repo_name: string
  branch: string
  cwd: string
  title: string
  started_at: string
  ended_at?: string
  last_activity: string
  state: 'open' | 'idle' | 'closed' | string
  models: string[]
  human: string
  agent_config: string
  harness_version?: string
  tokens: Tokens
  cost_usd: number
  event_count: number
  skipped: boolean
  skip_reason?: string
  flags?: string[]
  report_state: string
  report_at: string
}

export interface SessionItem extends SessionRow {
  duration_sec: number
  counts: Record<string, number>
  claims: number
  pending: number
  value: number
  summary: string
  model: string
  muted: boolean
  tally: Partial<Record<'backed' | 'unchecked' | 'contradicted', number>>
  noticed: number
}

export interface Evidence {
  target: string
  relation: string
  note?: string
}

export interface CoContributor {
  id: string
  basis?: string
}

export type Assertion = Record<string, unknown> & { check: string }

export interface Claim {
  spec: string
  id: string
  session_id: string
  claimant: string
  origin: 'self' | 'stated' | 'auditor' | 'human' | string
  type: string
  title: string
  statement: string
  assertions: Assertion[]
  evidence: Evidence[]
  evidence_hints?: string[]
  co_contributors?: CoContributor[]
  self_confidence?: number
  source_event?: number
  created_at: string
}

export interface Review {
  decision: string
  by: string
  reason?: string
  note?: string
  edited_title?: string
  edited_statement?: string
  at: string
}

export interface ClaimRow {
  claim: Claim
  cluster_id: string
  status: string
  section: string
  flags: string[] | null
  notes: string[] | null
  review_state: string
  review?: Review
  survival_state: string
  value: number
  verify_run: number
  active: boolean
  primary: boolean
  updated_at: string
}

export interface Artifact {
  id: string
  kind: string
  locator: string
  content_hash: string
  repo?: string
  session_id?: string
  meta?: unknown
  created_at: string
}

export interface EvidenceView extends Evidence {
  kind: 'events' | 'commit' | 'file' | 'test_run' | 'ci' | 'pr' | 'issue' | 'unresolved' | string
  label: string
  session?: string
  from?: number
  to?: number
  commit?: string
  path?: string
  id?: string
}

export interface CheckView {
  tier: number
  verifier: string
  check: string
  result: 'pass' | 'fail' | 'inconclusive' | 'blocked' | string
  outcome: string
  plain: string
  details: string
  artifacts?: Artifact[]
  duration_ms: number
}

export interface Member {
  id: string
  origin: string
  claimant: string
  title: string
  status: string
}

export interface Share {
  contributor: string
  share: number
  basis: string
}

export interface Attribution {
  claim_id: string
  shares: Share[]
  method: string
}

export interface ClaimCard extends ClaimRow {
  display_title: string
  display_statement: string
  cluster_origin: string
  evidence: EvidenceView[] | null
  checks: CheckView[] | null
  attribution?: Attribution
  credit: string
  members: Member[] | null
  self_confidence?: number
  verdict: Verdict
  reason: string
}

export type Verdict = 'contradicted' | 'backed' | 'partly' | 'unchecked' | 'pending'

export interface Episode {
  session: string
  from: number
  to: number
  kind: string
  actor: string
  label: string
  detail?: string
  count: number
  failures?: number
  ts: string
}

export interface SectionInfo {
  key: string
  title: string
  description: string
}

export interface Section extends SectionInfo {
  claims: ClaimCard[]
}

export interface Report {
  session: SessionItem
  sections: Section[]
  episodes: Episode[] | null
  children: SessionRow[] | null
  duplicates: ClaimCard[] | null
}

export interface Event {
  session_id: string
  seq: number
  ts: string
  kind: string
  actor: string
  payload: Record<string, unknown>
  payload_ref: string
  src_path?: string
  src_line?: number
  hash: string
  prev_hash: string
}

export interface TestRun {
  id: string
  claim_id: string
  repo: string
  commit: string
  target: 'before' | 'after' | string
  command: string
  runs: number
  passes: number
  output: string
  created_at: string
}

export interface Commit {
  sha: string
  parents: string[]
  author: string
  author_email: string
  author_time: string
  subject: string
  body: string
  files: string[]
  added: number
  deleted: number
}

export interface LedgerEntry {
  spec: string
  seq: number
  kind: 'decision' | 'survival' | string
  claim_id: string
  claim_hash: string
  claim: Claim
  status: string
  verdict_hashes: string[]
  attribution?: Attribution
  human_decision?: Review
  survival?: { window: string; result: string; ratio?: number }
  repo?: string
  commits?: string[]
  agent_config?: string
  prev_entry_hash: string
  entry_hash: string
  signature: string
  created_at: string
}

export interface Stat {
  key: string
  harness: string
  model: string
  sessions: number
  claims: number
  self_claims: number
  found_by_auditor: number
  verified: number
  corroborated: number
  contradicted: number
  unverified: number
  accepted: number
  edited: number
  disputed: number
  rejected: number
  surviving: number
  decayed: number
  reverted: number
  value: number
  brier: number
  brier_n: number
  overconfidence: number
  cost_usd: number
  value_per_dollar: number
}

export interface AgentConfig {
  id: string
  harness: string
  model: string
  first_seen: string
  last_seen: string
}

export interface VerifyReport {
  entries: number
  head: string
  key_id: string
  ok: boolean
  problems: { seq: number; reason: string }[]
}

export interface Status {
  version: string
  pid: number
  port: number
  home: string
  started_at: string
  jobs: Record<string, number>
  job_errors: { kind: string; key: string; error: string }[] | null
  hooks_24h: Record<string, Record<string, number>> | null
  month_spend_usd: number
  budget_usd: number
  auditor: string
  judge: string
  human: string
}

export const DISPUTE_REASONS: { key: string; label: string }[] = [
  { key: 'overstated', label: 'Overstated' },
  { key: 'wrong_contributor', label: 'Wrong contributor' },
  { key: 'not_valuable', label: 'Not valuable' },
  { key: 'factually_wrong', label: 'Factually wrong' },
  { key: 'duplicate', label: 'Duplicate' },
]

export const CLAIM_TYPES = [
  'fix', 'feature', 'diagnosis', 'test', 'refactor', 'docs', 'review', 'research', 'ops', 'decision', 'negative_result',
]
