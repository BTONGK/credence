import { useState } from 'react'
import { cx, who } from '../lib'
import type { Share } from '../types'

function role(id: string): string {
  if (id.startsWith('human:')) return 'human'
  if (id.startsWith('subagent:')) return 'subagent'
  if (id.startsWith('agent:')) return 'agent'
  return 'other'
}

// A stacked bar of the credit split plus the direction sentence.
export function CreditBar({ shares, credit, method }: { shares: Share[]; credit: string; method: string }) {
  const why = credit.includes(': ') ? credit.slice(credit.indexOf(': ') + 2) : ''
  const override = method.startsWith('human_override')
  return (
    <span className="credit" title={`Credit by ${method.replace(/_/g, ' ')}`}>
      <span className="credit-bar" aria-hidden>
        {shares.map((s) => (
          <span key={s.contributor} className={cx('credit-seg', `role-${role(s.contributor)}`)} style={{ flexGrow: Math.max(s.share, 0.001) }} />
        ))}
      </span>
      <span className="credit-legend">
        {shares.map((s) => (
          <span key={s.contributor} className="credit-item" title={s.basis ? `${s.contributor}: ${s.basis}` : s.contributor}>
            <span className={cx('credit-swatch', `role-${role(s.contributor)}`)} aria-hidden />
            {who(s.contributor)} {Math.round(s.share * 100)}%
          </span>
        ))}
        {override && <span className="tag">set by reviewer</span>}
      </span>
      {why && <span className="credit-why muted">{why}</span>}
    </span>
  )
}

interface Row {
  contributor: string
  pct: number
  basis: string
}

export function CreditEditor({ shares, human, onSave, onCancel }: {
  shares: Share[]
  human: string
  onSave: (s: Share[]) => void
  onCancel: () => void
}) {
  const [rows, setRows] = useState<Row[]>(() => shares.map((s) => ({ contributor: s.contributor, pct: Math.round(s.share * 100), basis: s.basis })))
  const [adding, setAdding] = useState('')
  const total = rows.reduce((n, r) => n + (Number.isFinite(r.pct) ? r.pct : 0), 0)
  const ok = total === 100 && rows.every((r) => r.pct >= 0 && r.contributor)

  // With two contributors, moving one share moves the other.
  const set = (i: number, pct: number) =>
    setRows((rs) => {
      const v = Math.max(0, Math.min(100, Math.round(pct || 0)))
      const next = rs.map((r, j) => (j === i ? { ...r, pct: v } : r))
      if (rs.length === 2) next[1 - i] = { ...next[1 - i], pct: 100 - v }
      return next
    })

  return (
    <div className="inline-form credit-editor" onKeyDown={(e) => e.key === 'Escape' && onCancel()}>
      <div className="dispute-label">Split credit <span className="muted">Shares must add up to 100%.</span></div>
      {rows.map((r, i) => (
        <div key={r.contributor} className="split-row">
          <span className={cx('credit-swatch', `role-${role(r.contributor)}`)} aria-hidden />
          <span className="split-name" title={r.contributor}>{who(r.contributor)}</span>
          <input type="range" min={0} max={100} value={r.pct} onChange={(e) => set(i, Number(e.target.value))}
            aria-label={`${who(r.contributor)} share`} />
          <input type="number" className="input input-num" min={0} max={100} value={r.pct}
            onChange={(e) => set(i, Number(e.target.value))} aria-label={`${who(r.contributor)} percent`} />
          <span className="muted">%</span>
          {rows.length > 1 && (
            <button className="icon-btn" title="Remove" aria-label={`Remove ${who(r.contributor)}`}
              onClick={() => setRows((rs) => rs.filter((_, j) => j !== i))}>×</button>
          )}
        </div>
      ))}
      <form className="split-add" onSubmit={(e) => {
        e.preventDefault()
        const name = adding.trim()
        if (!name) return
        const id = name.includes(':') ? name : `human:${name.toLowerCase()}`
        if (!rows.some((r) => r.contributor === id)) setRows((rs) => [...rs, { contributor: id, pct: 0, basis: 'added by reviewer' }])
        setAdding('')
      }}>
        <input className="input" placeholder={`Add a person (e.g. ${human ? who(human).toLowerCase() : 'alex'})`} value={adding}
          onChange={(e) => setAdding(e.target.value)} aria-label="Add contributor" />
        <button className="btn btn-sm">Add</button>
      </form>
      <div className={cx('split-total', !ok && 'is-bad')}>Total {total}%</div>
      <div className="form-actions">
        <button className="btn btn-primary btn-sm" disabled={!ok}
          onClick={() => onSave(rows.filter((r) => r.pct > 0).map((r) => ({ contributor: r.contributor, share: r.pct / 100, basis: r.basis || 'set by reviewer' })))}>
          Save split
        </button>
        <button className="btn btn-sm" onClick={onCancel}>Cancel <kbd>esc</kbd></button>
      </div>
    </div>
  )
}
