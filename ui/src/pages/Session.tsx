import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { api, ApiError, type ReviewRequest } from '../api'
import { ClaimCardView, type Mode } from '../components/ClaimCard'
import { EvidencePanel, type Opened } from '../components/EvidencePanel'
import { Link } from '../components/Link'
import { SectionCounts } from '../components/Status'
import { Timeline } from '../components/Timeline'
import {
  ago, cx, dur, isTyping, money, navigate, plural, useAsync, useDebounced, useKeys, useLive, useLocation,
} from '../lib'
import { CLAIM_TYPES, DISPUTE_REASONS, type ClaimCard, type Share } from '../types'

export function SessionPage({ id }: { id: string }) {
  const { query } = useLocation()
  const { data: rep, error, reload, setData } = useAsync(() => api.session(id), [id])
  const refresh = useDebounced(reload, 300)
  const [sel, setSel] = useState<string | null>(query.get('claim'))
  const [mode, setMode] = useState<Mode>(null)
  const [opened, setOpened] = useState<Opened | null>(null)
  const [toast, setToast] = useState<{ text: string; bad?: boolean } | null>(null)
  const [busy, setBusy] = useState<string | null>(null)
  const [adding, setAdding] = useState(false)
  const openedAt = useRef(Date.now())
  const decided = useRef(0)
  const reported = useRef(false)

  useLive((m) => {
    if ((m.kind === 'report' || m.kind === 'session') && rep && (m.id === rep.session.id || rep.children?.some((c) => c.id === m.id))) {
      refresh()
    }
  })

  const claims = useMemo(() => (rep ? rep.sections.flatMap((s) => s.claims) : []), [rep])
  const selected = claims.find((c) => c.claim.id === sel) ?? null

  useEffect(() => {
    if (rep) document.title = `${rep.session.title || rep.session.id} · Credence`
  }, [rep])

  // Select the first claim needing review once the report loads.
  useEffect(() => {
    if (!sel && claims.length) setSel((claims.find((c) => c.review_state === 'pending') ?? claims[0]).claim.id)
  }, [claims, sel])

  // Keep ?claim= in the URL so a selection can be linked to.
  useEffect(() => {
    if (sel) navigate(`/sessions/${id}?claim=${sel}`, true)
  }, [sel, id])

  useEffect(() => {
    if (sel) document.getElementById(`claim-${sel}`)?.scrollIntoView({ block: 'nearest', behavior: 'smooth' })
  }, [sel])

  const flash = useCallback((text: string, bad = false) => {
    setToast({ text, bad })
    window.setTimeout(() => setToast((t) => (t?.text === text ? null : t)), bad ? 6000 : 2500)
  }, [])

  // Replace one card in place after a decision so the page doesn't jump.
  const patchCard = useCallback(
    (card: ClaimCard) => {
      setData((r) =>
        r && {
          ...r,
          sections: r.sections.map((s) => ({ ...s, claims: s.claims.map((c) => (c.claim.id === card.claim.id ? { ...c, ...card, members: c.members } : c)) })),
        },
      )
    },
    [setData],
  )

  const recordTiming = useCallback(
    (pendingAfter: number) => {
      if (pendingAfter === 0 && decided.current > 0 && !reported.current && rep) {
        reported.current = true
        api.reviewTiming(rep.session.id, Date.now() - openedAt.current, decided.current).catch(() => {})
      }
    },
    [rep],
  )

  const decide = useCallback(
    async (c: ClaimCard, r: ReviewRequest, advance = true) => {
      setBusy(c.claim.id)
      try {
        const res = await api.review(c.claim.id, r)
        patchCard(res.card)
        decided.current++
        setMode(null)
        const pending = claims.filter((x) => x.claim.id !== c.claim.id && x.review_state === 'pending').length
        recordTiming(pending)
        flash(`${labelOf(r.decision)}: ${c.display_title}`)
        if (advance) {
          const next = nextPending(claims, c.claim.id)
          if (next) setSel(next.claim.id)
        }
      } catch (e) {
        if (e instanceof ApiError && e.status === 409) {
          flash('Contradicted claims can’t be accepted. Edit it to say what the evidence supports, or dispute it.', true)
          setMode('edit')
        } else flash((e as Error).message, true)
      } finally {
        setBusy(null)
      }
    },
    [claims, flash, patchCard, recordTiming],
  )

  const acceptAllVerified = useCallback(async () => {
    const todo = claims.filter((c) => c.section === 'verified' && c.review_state === 'pending')
    if (!todo.length) {
      flash('No verified claims waiting for review')
      return
    }
    let n = 0
    for (const c of todo) {
      try {
        const res = await api.review(c.claim.id, { decision: 'accepted', note: 'accepted with shift+A' })
        patchCard(res.card)
        n++
        decided.current++
      } catch (e) {
        flash((e as Error).message, true)
        break
      }
    }
    recordTiming(claims.filter((c) => c.review_state === 'pending').length - n)
    flash(`Accepted ${plural(n, 'verified claim')}`)
    const next = claims.find((c) => c.review_state === 'pending' && !todo.includes(c))
    if (next) setSel(next.claim.id)
  }, [claims, flash, patchCard, recordTiming])

  const reverify = useCallback(
    async (c: ClaimCard) => {
      try {
        await api.verify(c.claim.id, true)
        flash('Re-running verification…')
      } catch (e) {
        flash((e as Error).message, true)
      }
    },
    [flash],
  )

  const saveShares = useCallback(
    async (c: ClaimCard, shares: Share[]) => {
      try {
        const a = await api.setAttribution(c.claim.id, shares)
        patchCard({ ...c, attribution: a, credit: creditLine(a.shares) })
        setMode(null)
        flash('Credit split saved')
      } catch (e) {
        flash((e as Error).message, true)
      }
    },
    [flash, patchCard],
  )

  const rerun = useCallback(async () => {
    if (!rep) return
    setBusy('report')
    try {
      await api.rereport(rep.session.id)
      reload()
      flash('Report rebuilt')
    } catch (e) {
      flash((e as Error).message, true)
    } finally {
      setBusy(null)
    }
  }, [rep, reload, flash])

  useKeys((e) => {
    if (isTyping(e) || adding) return
    if (e.key === 'Escape') {
      if (opened) setOpened(null)
      else if (mode) setMode(null)
      return
    }
    if (mode === 'dispute' && selected && /^[1-5]$/.test(e.key)) {
      const reason = DISPUTE_REASONS[Number(e.key) - 1].key
      void decide(selected, { decision: 'disputed', reason })
      e.preventDefault()
      return
    }
    // Ignore decision keys while one is in flight so they can't land on the wrong claim.
    if (busy && busy !== 'report' && 'aedrxsvA'.includes(e.key)) return
    const i = claims.findIndex((c) => c.claim.id === sel)
    switch (e.key) {
      case 'j':
      case 'ArrowDown':
        if (claims.length) setSel(claims[Math.min(claims.length - 1, i + 1)].claim.id)
        setMode(null)
        e.preventDefault()
        break
      case 'k':
      case 'ArrowUp':
        if (claims.length) setSel(claims[Math.max(0, i - 1)].claim.id)
        setMode(null)
        e.preventDefault()
        break
      case 'a':
        if (selected) void decide(selected, { decision: 'accepted' })
        break
      case 'e':
        if (selected) setMode('edit')
        e.preventDefault()
        break
      case 'd':
        if (selected) setMode('dispute')
        break
      case 'r':
      case 'x':
        if (selected) void decide(selected, { decision: 'rejected' })
        break
      case 's':
        if (selected) setMode('split')
        e.preventDefault()
        break
      case 'v':
        if (selected) void reverify(selected)
        break
      case 'o': {
        const ev = selected?.evidence?.[0]
        if (selected && ev) setOpened({ evidence: ev, claim: selected })
        break
      }
      case 'A':
        void acceptAllVerified()
        break
      case 'u':
        navigate('/')
        break
    }
  })

  if (error) {
    return (
      <div className="page">
        <Link to="/" className="back">← Sessions</Link>
        <div className="banner banner-bad">Couldn’t load this session: {error.message}</div>
      </div>
    )
  }
  if (!rep) return <div className="page"><div className="empty">Loading credit report…</div></div>

  const s = rep.session
  const pending = claims.filter((c) => c.review_state === 'pending').length
  const verifiedPending = claims.filter((c) => c.section === 'verified' && c.review_state === 'pending').length
  const flags = s.flags ?? []
  const highlight = selected ? spans(selected) : []

  return (
    <div className="page session">
      <div className="session-head">
        <Link to="/" className="back">← Sessions</Link>
        <div className="session-head-row">
          <div className="session-head-text">
            <h1>{s.title || s.id}</h1>
            <div className="session-meta">
              {s.repo_name && <span title={s.repo}>{s.repo_name}{s.branch ? `@${s.branch}` : ''}</span>}
              <span>{s.harness}{s.harness_version ? ` ${s.harness_version}` : ''}</span>
              {s.model && <span>{s.model}</span>}
              <span>{dur(s.duration_sec)}</span>
              {s.cost_usd > 0 && <span title={`${s.tokens.input.toLocaleString()} in · ${s.tokens.output.toLocaleString()} out`}>{money(s.cost_usd)}</span>}
              <span>{plural(s.event_count, 'event')}</span>
              <span>{ago(s.last_activity)}</span>
              <span className="mono muted" title="Session id">{s.id}</span>
            </div>
            <div className="session-summary">
              <SectionCounts counts={s.counts} total={s.claims} />
              {s.report_state === 'provisional' && (
                <span className="tag" title="The session is still going; the final report runs when it closes">provisional</span>
              )}
            </div>
          </div>
          <div className="session-actions">
            <button className="btn btn-primary" onClick={() => void acceptAllVerified()} disabled={!verifiedPending}
              title="Accept every verified claim (Shift+A)">
              Accept {verifiedPending || ''} verified <kbd>⇧A</kbd>
            </button>
            <button className="btn" onClick={() => void rerun()} disabled={busy === 'report'} title="Re-extract, re-verify and rebuild this report">
              {busy === 'report' ? 'Rebuilding…' : 'Rebuild report'}
            </button>
          </div>
        </div>
        {flags.length > 0 && (
          <div className="banner banner-warn">
            {flags.includes('transcript_rewritten') && 'Some transcript lines changed after Credence captured them; claims citing those events may not match what you see in the harness. '}
            {flags.includes('transcript_truncated') && 'The transcript was truncated after capture. '}
            {flags.filter((f) => !f.startsWith('transcript_')).join(', ')}
          </div>
        )}
        {s.skipped && <div className="banner">Nothing to claim: {s.skip_reason || 'the session made no changes worth reviewing'}.</div>}
      </div>

      <div className="session-body">
        <aside className="timeline-pane" aria-label="Session timeline">
          <Timeline report={rep} highlight={highlight}
            onOpen={(from, to, session) => setOpened({ evidence: { kind: 'events', target: `event:${session}#${from}-${to}`, relation: 'informed', label: from === to ? `event #${from}` : `events #${from}–${to}`, session, from, to }, claim: selected ?? undefined })} />
        </aside>

        <section className="report-pane" aria-label="Credit report">
          {pending === 0 && claims.length > 0 && (
            <div className="banner banner-good">All {plural(claims.length, 'claim')} reviewed. Decisions are signed into your ledger.</div>
          )}
          {rep.sections.map((sec) => (
            <div key={sec.key} className={cx('report-section', `sec-${sec.key}`, !sec.claims.length && 'is-empty')}>
              <div className="section-head">
                <h2>
                  <span className="section-dot" aria-hidden />
                  {sec.title}
                  <span className="section-n">{sec.claims.length}</span>
                </h2>
                <p>{sec.description}</p>
              </div>
              {sec.claims.map((c) => (
                <ClaimCardView
                  key={c.claim.id}
                  card={c}
                  selected={c.claim.id === sel}
                  mode={c.claim.id === sel ? mode : null}
                  busy={busy === c.claim.id}
                  onSelect={() => {
                    if (c.claim.id !== sel) {
                      setSel(c.claim.id)
                      setMode(null)
                    }
                  }}
                  onMode={(m) => {
                    setSel(c.claim.id)
                    setMode(m)
                  }}
                  onDecide={(r) => void decide(c, r)}
                  onReverify={() => void reverify(c)}
                  onSaveShares={(sh) => void saveShares(c, sh)}
                  onOpen={(ev) => setOpened({ evidence: ev, claim: c })}
                  human={s.human}
                />
              ))}
            </div>
          ))}
          {(rep.duplicates?.length ?? 0) > 0 && (
            <details className="dupes">
              <summary>{plural(rep.duplicates!.length, 'merged duplicate')}</summary>
              <p className="muted small">Claims that describe the same work as a claim above. They share its review and credit.</p>
              <ul>
                {rep.duplicates!.map((d) => (
                  <li key={d.claim.id}>
                    <span className="tag">{d.claim.origin === 'auditor' ? 'auditor' : d.claim.origin === 'human' ? 'you' : 'agent'}</span>{' '}
                    {d.display_title} <span className="muted mono small">{d.claim.id}</span>
                  </li>
                ))}
              </ul>
            </details>
          )}
          {adding ? (
            <AddClaim sessionId={s.id} onDone={(ok) => {
              setAdding(false)
              if (ok) {
                flash('Claim added; verifying…')
                reload()
              }
            }} />
          ) : (
            <button className="link-btn add-claim" onClick={() => setAdding(true)}>+ Add a claim the agent and auditor missed</button>
          )}
        </section>
      </div>

      {opened && <EvidencePanel opened={opened} sessionId={s.id} cwd={s.cwd} onClose={() => setOpened(null)} />}
      {toast && <div className={cx('toast', toast.bad && 'toast-bad')} role="status">{toast.text}</div>}
    </div>
  )
}

function AddClaim({ sessionId, onDone }: { sessionId: string; onDone: (ok: boolean) => void }) {
  const [type, setType] = useState('fix')
  const [title, setTitle] = useState('')
  const [statement, setStatement] = useState('')
  const [evidence, setEvidence] = useState('')
  const [err, setErr] = useState('')
  const submit = async () => {
    try {
      await api.addClaim(sessionId, {
        type, title, statement,
        evidence: evidence.split(/[\s,]+/).filter(Boolean).map((t) => (t.includes(':') ? t : /^[0-9a-f]{7,40}$/.test(t) ? `git:commit/${t}` : t)),
      })
      onDone(true)
    } catch (e) {
      setErr((e as Error).message)
    }
  }
  return (
    <form className="card add-claim-form" onSubmit={(e) => { e.preventDefault(); void submit() }}
      onKeyDown={(e) => e.key === 'Escape' && onDone(false)}>
      <h3>Add a claim</h3>
      <div className="form-row">
        <label>Type
          <select className="input" value={type} onChange={(e) => setType(e.target.value)}>
            {CLAIM_TYPES.map((t) => <option key={t} value={t}>{t.replace('_', ' ')}</option>)}
          </select>
        </label>
        <label className="grow">Title
          <input className="input" autoFocus value={title} onChange={(e) => setTitle(e.target.value)} placeholder="What was accomplished" />
        </label>
      </div>
      <label>Statement
        <textarea className="input" rows={2} value={statement} onChange={(e) => setStatement(e.target.value)} placeholder="A specific, checkable statement" />
      </label>
      <label>Evidence <span className="muted">(commit shas or targets like event:{sessionId}#12-14, space separated)</span>
        <input className="input mono" value={evidence} onChange={(e) => setEvidence(e.target.value)} />
      </label>
      {err && <div className="form-error">{err}</div>}
      <div className="form-actions">
        <button className="btn btn-primary" disabled={!title.trim()}>Add claim</button>
        <button type="button" className="btn" onClick={() => onDone(false)}>Cancel</button>
      </div>
    </form>
  )
}

function nextPending(claims: ClaimCard[], after: string): ClaimCard | undefined {
  const i = claims.findIndex((c) => c.claim.id === after)
  return [...claims.slice(i + 1), ...claims.slice(0, i)].find((c) => c.review_state === 'pending')
}

function labelOf(d: string): string {
  return { accepted: 'Accepted', edited: 'Edited and accepted', disputed: 'Disputed', rejected: 'Rejected' }[d] ?? d
}

function creditLine(shares: Share[]): string {
  return shares.map((s) => `${s.contributor.replace(/^human:/, '')} ${Math.round(s.share * 100)}%`).join(' · ')
}

// Event spans a claim cites, for timeline highlighting.
function spans(c: ClaimCard): { session: string; from: number; to: number }[] {
  return (c.evidence ?? [])
    .filter((e) => e.kind === 'events' && e.session && e.from)
    .map((e) => ({ session: e.session!, from: e.from!, to: e.to ?? e.from! }))
}
