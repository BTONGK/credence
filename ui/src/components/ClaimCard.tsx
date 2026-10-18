import { useEffect, useRef, useState } from 'react'
import type { ReviewRequest } from '../api'
import { cx, pct, typeLabel, who } from '../lib'
import { DISPUTE_REASONS, type CheckView, type ClaimCard, type EvidenceView, type Share } from '../types'
import { CreditBar, CreditEditor } from './Credit'
import { ReviewBadge, StatusBadge, statusTone } from './Status'

export type Mode = 'edit' | 'dispute' | 'split' | null

interface Props {
  card: ClaimCard
  selected: boolean
  mode: Mode
  busy: boolean
  human: string
  onSelect: () => void
  onMode: (m: Mode) => void
  onDecide: (r: ReviewRequest) => void
  onReverify: () => void
  onSaveShares: (s: Share[]) => void
  onOpen: (e: EvidenceView) => void
}

export function ClaimCardView(p: Props) {
  const c = p.card
  const contradicted = c.status === 'contradicted' || c.status === 'overstated'
  const reviewed = c.review_state !== 'pending'
  const checks = c.checks ?? []
  // Notes often restate a verifier result shown above; keep only the new ones.
  const notes = (c.notes ?? []).filter((n) => n && !checks.some((k) => same(n, k.plain) || same(n, k.details)))
  const members = c.members ?? []
  const edited = c.review_state === 'edited' && c.display_title !== c.claim.title
  const formRef = useRef<HTMLDivElement>(null)
  useEffect(() => {
    if (p.mode) formRef.current?.scrollIntoView({ block: 'nearest', behavior: 'smooth' })
  }, [p.mode])

  return (
    <article
      id={`claim-${c.claim.id}`}
      className={cx('card claim', p.selected && 'selected', reviewed && 'is-reviewed', `status-${statusTone(c.status)}`)}
      onClick={(e) => {
        if ((e.target as HTMLElement).closest('button, a, input, textarea, select, label')) return
        p.onSelect()
      }}
      aria-current={p.selected}
    >
      <header className="claim-head">
        <div className="claim-tags">
          <span className={cx('type', `type-${c.claim.type}`)}>{typeLabel(c.claim.type)}</span>
          {c.claim.origin === 'auditor' && <span className="tag tag-auditor" title="The agent didn’t claim this; the independent auditor found it">found by auditor</span>}
          {c.claim.origin === 'human' && <span className="tag">added by {who(c.claim.claimant)}</span>}
          {c.claim.origin === 'self' && c.cluster_origin === 'both' && <span className="tag" title="The independent auditor found the same work">auditor agrees</span>}
        </div>
        <StatusBadge status={c.status} />
      </header>

      <h3 className="claim-title">{c.display_title}</h3>
      {edited && <div className="claim-orig muted small">Agent’s wording: <s>{c.claim.title}</s></div>}
      {c.display_statement && c.display_statement !== c.display_title && <p className="claim-statement">{rich(c.display_statement)}</p>}

      {checks.length > 0 && (
        <ul className="checks" aria-label="Verification">
          {checks.map((k, i) => <Check key={i} k={k} onOpen={p.onOpen} />)}
        </ul>
      )}
      {notes.length > 0 && (
        <ul className="claim-notes">
          {notes.map((n) => <li key={n}>{rich(n)}</li>)}
        </ul>
      )}

      {(c.evidence?.length ?? 0) > 0 && (
        <div className="chips" aria-label="Evidence">
          {c.evidence!.map((ev, i) => (
            <button key={i} className={cx('chip', `chip-${ev.kind}`)} onClick={() => p.onOpen(ev)} title={ev.note ? `${ev.target} — ${ev.note}` : ev.target}>
              <ChipIcon kind={ev.kind} />
              {ev.label}
            </button>
          ))}
        </div>
      )}

      <div className="claim-facts">
        {c.self_confidence !== undefined && c.self_confidence !== null && <Confidence p={c.self_confidence} status={c.status} />}
        {c.attribution && c.attribution.shares.length > 0 && <CreditBar shares={c.attribution.shares} credit={c.credit} method={c.attribution.method} />}
        <span className="value" title="Credit value: type weight × verification × review × survival">value {c.value.toFixed(2)}</span>
      </div>

      {members.length > 0 && (
        <div className="members small muted">
          {members.map((m) => (
            <div key={m.id}>
              Also {m.origin === 'auditor' ? 'found by the auditor' : m.origin === 'human' ? 'added by a reviewer' : 'claimed by the agent'} as “{m.title}”
            </div>
          ))}
        </div>
      )}

      {reviewed && c.review && (
        <div className="claim-review">
          <ReviewBadge state={c.review_state} by={c.review.by} reason={c.review.reason} />
          {c.review.note && <span className="muted small"> “{c.review.note}”</span>}
          {c.survival_state && <span className={cx('tag', c.survival_state === 'surviving' ? 'tag-good' : 'tag-bad')}>{c.survival_state}</span>}
        </div>
      )}

      <div ref={formRef}>
      {p.mode === 'edit' && (
        <EditForm card={c} onCancel={() => p.onMode(null)}
          onSave={(t, s) => p.onDecide({ decision: 'edited', edited_title: t, edited_statement: s })} />
      )}
      {p.mode === 'dispute' && (
        <DisputeForm onCancel={() => p.onMode(null)} onSubmit={(reason, note) => p.onDecide({ decision: 'disputed', reason, note })} />
      )}
      {p.mode === 'split' && c.attribution && (
        <CreditEditor shares={c.attribution.shares} human={p.human} onCancel={() => p.onMode(null)} onSave={p.onSaveShares} />
      )}
      </div>

      {!p.mode && (
        <div className={cx('actions', p.selected && 'actions-visible')}>
          <button className="btn btn-sm btn-good" disabled={p.busy || contradicted} onClick={() => p.onDecide({ decision: 'accepted' })}
            title={contradicted ? 'The evidence disagrees: edit the claim to what it supports, or dispute it' : 'Accept (a)'}>
            {reviewed ? 'Accept again' : 'Accept'} <kbd>a</kbd>
          </button>
          <button className="btn btn-sm" onClick={() => p.onMode('edit')} disabled={p.busy} title="Edit and accept (e)">Edit <kbd>e</kbd></button>
          <button className="btn btn-sm" onClick={() => p.onMode('split')} disabled={p.busy || !c.attribution} title="Change the credit split (s)">Split credit <kbd>s</kbd></button>
          <button className="btn btn-sm" onClick={() => p.onMode('dispute')} disabled={p.busy} title="Dispute (d)">Dispute <kbd>d</kbd></button>
          <button className="btn btn-sm btn-quiet" onClick={() => p.onDecide({ decision: 'rejected' })} disabled={p.busy} title="Reject (r)">Reject <kbd>r</kbd></button>
          <button className="btn btn-sm btn-quiet" onClick={p.onReverify} title="Re-run verification (v)">Re-verify <kbd>v</kbd></button>
        </div>
      )}
    </article>
  )
}

const RESULT: Record<string, { glyph: string; tone: string; label: string }> = {
  pass: { glyph: '✓', tone: 'good', label: 'passed' },
  fail: { glyph: '✗', tone: 'bad', label: 'failed' },
  inconclusive: { glyph: '·', tone: 'neutral', label: 'inconclusive' },
  blocked: { glyph: '!', tone: 'warn', label: 'blocked' },
}

const TIER = ['resolve', 'checks', 'judge', 'human']

function Check({ k, onOpen }: { k: CheckView; onOpen: (e: EvidenceView) => void }) {
  const r = RESULT[k.result] ?? RESULT.inconclusive
  const runs = (k.artifacts ?? []).filter((a) => a.kind === 'test_run')
  const [open, setOpen] = useState(false)
  const hasDetails = k.details && k.details !== k.plain
  return (
    <li className={cx('check', `check-${r.tone}`)}>
      <span className="check-glyph" aria-label={r.label}>{r.glyph}</span>
      <span className="check-body">
        <span className="check-text">{rich(k.plain || k.details)}</span>
        <span className="check-meta">
          <span title={`Tier ${k.tier}: ${TIER[k.tier] ?? ''} · ${k.verifier}`}>tier {k.tier} · {k.verifier}</span>
          {runs.length > 0 && (
            <button className="link-btn" onClick={() => onOpen({
              kind: 'test_run', target: runs.map((a) => a.locator).join(' '), relation: 'verified',
              label: 'test runs', id: runs.map((a) => a.locator.replace(/^test_run:/, '')).join(','),
            })}>
              view {runs.length === 1 ? 'run' : `${runs.length} runs`}
            </button>
          )}
          {hasDetails && <button className="link-btn" onClick={() => setOpen((v) => !v)} aria-expanded={open}>{open ? 'less' : 'details'}</button>}
        </span>
        {open && <pre className="check-details">{k.details}</pre>}
      </span>
    </li>
  )
}

function Confidence({ p, status }: { p: number; status: string }) {
  const outcome = status === 'verified' || status === 'corroborated' ? 1 : status === 'contradicted' || status === 'overstated' ? 0 : null
  const gap = outcome === null ? null : p - outcome
  return (
    <span className="confidence" title="The agent’s stated confidence compared with what verification found">
      <span className="muted">agent said</span> <strong>{pct(p)}</strong>
      <span className="conf-track" aria-hidden>
        <span className="conf-fill" style={{ width: `${Math.round(p * 100)}%` }} />
        {outcome !== null && <span className={cx('conf-mark', outcome ? 'is-good' : 'is-bad')} style={{ left: `${outcome * 100}%` }} />}
      </span>
      <span className="muted">→</span> <span className={cx('conf-out', `tone-${statusTone(status)}`)}>{status}</span>
      {gap !== null && gap > 0.5 && <span className="tag tag-bad">over-confident</span>}
    </span>
  )
}

function EditForm({ card, onSave, onCancel }: { card: ClaimCard; onSave: (t: string, s: string) => void; onCancel: () => void }) {
  const [t, setT] = useState(card.display_title)
  const [s, setS] = useState(card.display_statement)
  const ref = useRef<HTMLInputElement>(null)
  useEffect(() => {
    ref.current?.focus()
    ref.current?.select()
  }, [])
  return (
    <form className="inline-form" onSubmit={(e) => { e.preventDefault(); onSave(t.trim(), s.trim()) }}
      onKeyDown={(e) => {
        if (e.key === 'Escape') onCancel()
        if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) onSave(t.trim(), s.trim())
      }}>
      <label>Title <input ref={ref} className="input" value={t} onChange={(e) => setT(e.target.value)} /></label>
      <label>Statement <textarea className="input" rows={3} value={s} onChange={(e) => setS(e.target.value)} /></label>
      <p className="muted small">Saving accepts the claim with your wording. The agent’s original stays in the ledger.</p>
      <div className="form-actions">
        <button className="btn btn-primary btn-sm" disabled={!t.trim()}>Save and accept <kbd>⌘↵</kbd></button>
        <button type="button" className="btn btn-sm" onClick={onCancel}>Cancel <kbd>esc</kbd></button>
      </div>
    </form>
  )
}

function DisputeForm({ onSubmit, onCancel }: { onSubmit: (reason: string, note: string) => void; onCancel: () => void }) {
  const [note, setNote] = useState('')
  return (
    <div className="inline-form" onKeyDown={(e) => e.key === 'Escape' && onCancel()}>
      <div className="dispute-label">Why are you disputing this? <span className="muted">Press 1–5</span></div>
      <div className="reasons">
        {DISPUTE_REASONS.map((r, i) => (
          <button key={r.key} className="btn btn-sm" onClick={() => onSubmit(r.key, note)}>
            <kbd>{i + 1}</kbd> {r.label}
          </button>
        ))}
      </div>
      <input className="input" placeholder="Optional note for the ledger" value={note} onChange={(e) => setNote(e.target.value)}
        aria-label="Dispute note" />
      <div className="form-actions">
        <button className="btn btn-sm" onClick={onCancel}>Cancel <kbd>esc</kbd></button>
      </div>
    </div>
  )
}

function ChipIcon({ kind }: { kind: string }) {
  const g: Record<string, string> = { events: '#', commit: '◆', file: '▤', test_run: '▶', ci: '●', pr: '⇄', issue: '◎', unresolved: '?' }
  return <span className="chip-icon" aria-hidden>{g[kind] ?? '·'}</span>
}

function same(a: string, b: string): boolean {
  if (!a || !b) return false
  const n = Math.min(48, a.length, b.length)
  return a.slice(0, n) === b.slice(0, n)
}

// Render `code` spans in verifier text.
export function rich(text: string) {
  const parts = text.split('`')
  return parts.map((p, i) => (i % 2 ? <code key={i}>{p}</code> : <span key={i}>{p}</span>))
}
