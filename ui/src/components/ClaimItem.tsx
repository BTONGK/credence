import { useState } from 'react'
import type { ReviewRequest } from '../api'
import { clock, cx } from '../lib'
import type { CheckView, ClaimCard, EvidenceView, Verdict } from '../types'

export const VERDICT: Record<Verdict, { glyph: string; label: string; short: string }> = {
  contradicted: { glyph: '✗', label: 'Contradicted', short: 'contradicted' },
  backed: { glyph: '✓', label: 'Backed by evidence', short: 'backed' },
  partly: { glyph: '◐', label: 'Partly backed', short: 'partly backed' },
  unchecked: { glyph: '?', label: 'Yours to confirm', short: 'yours to confirm' },
  pending: { glyph: '…', label: 'Checking', short: 'checking' },
}

// Contradictions first: they are what a reader most needs to see.
export const VERDICT_ORDER: Verdict[] = ['contradicted', 'partly', 'unchecked', 'pending', 'backed']

export function VerdictMark({ v }: { v: Verdict }) {
  return <span className={cx('vmark', `v-${v}`)} aria-label={VERDICT[v].label} title={VERDICT[v].label}>{VERDICT[v].glyph}</span>
}

interface Props {
  card: ClaimCard
  start: string
  open: boolean
  busy: boolean
  quote: boolean // the agent's own words, shown in quotes
  onToggle: () => void
  onDecide: (r: ReviewRequest) => void
  onShow: (e: EvidenceView) => void
  onReverify: () => void
}

export function ClaimItem(p: Props) {
  const c = p.card
  const v = c.verdict ?? 'pending'
  const [editing, setEditing] = useState(false)
  const checks = (c.checks ?? []).filter((k) => !(k.verifier === 'resolve' && k.result === 'pass'))
  const reply = (c.evidence ?? []).find((e) => e.kind === 'events' && e.relation === 'informed')
  const work = (c.evidence ?? []).find((e) => e.kind === 'events' && e.relation !== 'informed')
  const reviewed = c.review_state !== 'pending'
  return (
    <li id={`claim-${c.claim.id}`} className={cx('citem', `v-${v}`, p.open && 'is-open', reviewed && 'is-reviewed')}>
      <button className="citem-row" onClick={p.onToggle} aria-expanded={p.open}>
        <VerdictMark v={v} />
        <span className="citem-body">
          <span className="citem-title">{p.quote ? <>“{rich(c.display_title)}”</> : rich(c.display_title)}</span>
          <span className="citem-reason">
            <span className="citem-verdict">{VERDICT[v].label}.</span> {c.reason ? rich(c.reason) : null}
          </span>
        </span>
        <span className="citem-side">
          {reviewed && <span className={cx('you', c.review_state === 'accepted' || c.review_state === 'edited' ? 'you-ok' : 'you-bad')}>
            {c.review_state === 'accepted' || c.review_state === 'edited' ? 'You: right' : 'You: wrong'}
          </span>}
          <span className="citem-time mono">{clock(c.claim.created_at, p.start)}</span>
        </span>
      </button>
      {p.open && (
        <div className="citem-detail">
          {c.display_statement && c.display_statement !== c.display_title && <p className="citem-full">{rich(c.display_statement)}</p>}
          {checks.length > 0 && (
            <>
              <h4>What Credence checked</h4>
              <ul className="cchecks">{checks.map((k, i) => <CheckLine key={i} k={k} />)}</ul>
            </>
          )}
          <div className="citem-actions">
            {reply && <button className="btn btn-sm" onClick={() => p.onShow(reply)}>Show the agent’s reply</button>}
            {work && <button className="btn btn-sm" onClick={() => p.onShow(work)}>Show the work</button>}
            <span className="grow" />
            {editing ? null : (
              <>
                {v !== 'contradicted' && (
                  <button className="btn btn-sm btn-good" disabled={p.busy} onClick={() => p.onDecide({ decision: 'accepted' })}>Looks right</button>
                )}
                <button className="btn btn-sm btn-bad" disabled={p.busy}
                  onClick={() => p.onDecide({ decision: 'disputed', reason: 'factually_wrong' })}>
                  {v === 'contradicted' ? 'Agree it’s wrong' : 'It’s wrong'}
                </button>
                <button className="btn btn-sm btn-quiet" disabled={p.busy} onClick={() => setEditing(true)}>Edit wording</button>
                <button className="btn btn-sm btn-quiet" disabled={p.busy} onClick={p.onReverify}>Check again</button>
              </>
            )}
          </div>
          {editing && (
            <EditWording card={c} onCancel={() => setEditing(false)}
              onSave={(t) => {
                setEditing(false)
                p.onDecide({ decision: 'edited', edited_title: t, edited_statement: t })
              }} />
          )}
        </div>
      )}
    </li>
  )
}

function CheckLine({ k }: { k: CheckView }) {
  const tone = k.result === 'pass' ? 'backed' : k.result === 'fail' ? 'contradicted' : 'unchecked'
  return (
    <li className={cx('ccheck', `v-${tone}`)}>
      <span className="ccheck-glyph" aria-hidden>{tone === 'backed' ? '✓' : tone === 'contradicted' ? '✗' : '?'}</span>
      <span>{rich(k.plain || k.details)}</span>
    </li>
  )
}

function EditWording({ card, onSave, onCancel }: { card: ClaimCard; onSave: (t: string) => void; onCancel: () => void }) {
  const [t, setT] = useState(card.display_statement || card.display_title)
  return (
    <form className="edit-wording" onSubmit={(e) => { e.preventDefault(); if (t.trim()) onSave(t.trim()) }}
      onKeyDown={(e) => e.key === 'Escape' && onCancel()}>
      <label className="muted small" htmlFor={`edit-${card.claim.id}`}>Say what actually happened. Saving marks the claim right with your wording.</label>
      <textarea id={`edit-${card.claim.id}`} className="input" rows={2} autoFocus value={t} onChange={(e) => setT(e.target.value)} />
      <div className="form-actions">
        <button className="btn btn-primary btn-sm" disabled={!t.trim()}>Save</button>
        <button type="button" className="btn btn-sm" onClick={onCancel}>Cancel</button>
      </div>
    </form>
  )
}

// Render `code` spans in verifier text.
export function rich(text: string) {
  const parts = text.split('`')
  return parts.map((p, i) => (i % 2 ? <code key={i}>{p}</code> : <span key={i}>{p}</span>))
}
