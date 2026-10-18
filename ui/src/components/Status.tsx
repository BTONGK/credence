import { cx } from '../lib'

export const SECTION_ORDER = ['verified', 'unverified', 'contradicted', 'found_by_auditor', 'reported_failures']

export const SECTION_LABEL: Record<string, string> = {
  verified: 'verified',
  unverified: 'unverified',
  contradicted: 'contradicted',
  found_by_auditor: 'found by auditor',
  reported_failures: 'reported failures',
}

// "6 claims · 2 verified · 2 flagged · 1 found by auditor", with color.
export function SectionCounts({ counts, total }: { counts: Record<string, number>; total: number }) {
  if (!total) return <span className="muted">no claims</span>
  const flagged = (counts.unverified ?? 0) + (counts.contradicted ?? 0)
  const parts: [string, string, number][] = [
    ['verified', 'verified', counts.verified ?? 0],
    ['flagged', counts.contradicted ? 'contradicted' : 'unverified', flagged],
    ['found by auditor', 'found_by_auditor', counts.found_by_auditor ?? 0],
    [counts.reported_failures === 1 ? 'reported failure' : 'reported failures', 'reported_failures', counts.reported_failures ?? 0],
  ]
  return (
    <span className="counts">
      <span>{total} {total === 1 ? 'claim' : 'claims'}</span>
      {parts.filter(([, , n]) => n > 0).map(([label, key, n]) => (
        <span key={label} className={cx('count', `sec-${key}`)}>
          <span className="count-dot" aria-hidden />
          {n} {label}
        </span>
      ))}
    </span>
  )
}

const STATUS: Record<string, { glyph: string; label: string; tone: string }> = {
  verified: { glyph: '✓', label: 'Verified', tone: 'good' },
  corroborated: { glyph: '✓', label: 'Corroborated', tone: 'okay' },
  contradicted: { glyph: '✗', label: 'Contradicted', tone: 'bad' },
  overstated: { glyph: '✗', label: 'Overstated', tone: 'bad' },
  unsupported: { glyph: '○', label: 'Unsupported', tone: 'warn' },
  unverified: { glyph: '○', label: 'Unverified', tone: 'warn' },
  resolved: { glyph: '○', label: 'Evidence resolved', tone: 'warn' },
  blocked: { glyph: '!', label: 'Blocked', tone: 'warn' },
  proposed: { glyph: '…', label: 'Verifying', tone: 'neutral' },
}

export function StatusBadge({ status }: { status: string }) {
  const s = STATUS[status] ?? { glyph: '·', label: status, tone: 'neutral' }
  return (
    <span className={cx('status-badge', `tone-${s.tone}`)}>
      <span aria-hidden>{s.glyph}</span> {s.label}
    </span>
  )
}

export function statusTone(status: string): string {
  return STATUS[status]?.tone ?? 'neutral'
}

const REVIEW: Record<string, { label: string; tone: string }> = {
  accepted: { label: 'Accepted', tone: 'good' },
  edited: { label: 'Accepted with edits', tone: 'good' },
  disputed: { label: 'Disputed', tone: 'bad' },
  rejected: { label: 'Rejected', tone: 'bad' },
}

export function ReviewBadge({ state, by, reason }: { state: string; by?: string; reason?: string }) {
  const r = REVIEW[state]
  if (!r) return null
  const name = by?.startsWith('human:') ? by.slice(6) : by
  return (
    <span className={cx('review-badge', `tone-${r.tone}`)}>
      {r.label}
      {reason ? `: ${reason.replace(/_/g, ' ')}` : ''}
      {name ? <span className="muted"> · {name}</span> : null}
    </span>
  )
}
