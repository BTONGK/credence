import { useCallback, useEffect, useMemo, useState } from 'react'
import { api, ApiError, type ReviewRequest } from '../api'
import { ClaimItem, VERDICT, VERDICT_ORDER, VerdictMark } from '../components/ClaimItem'
import { EvidencePanel, type Opened } from '../components/EvidencePanel'
import { Link } from '../components/Link'
import { Timeline } from '../components/Timeline'
import { cx, day, dur, isTyping, money, navigate, plural, useAsync, useDebounced, useKeys, useLive, useLocation } from '../lib'
import type { ClaimCard, EvidenceView, Verdict } from '../types'

export function SessionPage({ id }: { id: string }) {
  const { query } = useLocation()
  const { data: rep, error, reload, setData } = useAsync(() => api.session(id), [id])
  const refresh = useDebounced(reload, 300)
  const [open, setOpen] = useState<string | null>(query.get('claim'))
  const [opened, setOpened] = useState<Opened | null>(null)
  const [toast, setToast] = useState<{ text: string; bad?: boolean } | null>(null)
  const [busy, setBusy] = useState<string | null>(null)
  const [showNoticed, setShowNoticed] = useState(false)
  const [showConvo, setShowConvo] = useState(false)

  useLive((m) => {
    if ((m.kind === 'report' || m.kind === 'session') && rep && (m.id === rep.session.id || rep.children?.some((c) => c.id === m.id))) {
      refresh()
    }
  })

  // The agent's claims (stated in replies, filed with the tool, or added by a
  // person) lead; what the auditor noticed on its own comes after.
  const { claimed, noticed } = useMemo(() => {
    const all = rep ? rep.sections.flatMap((s) => s.claims) : []
    const isNoticed = (c: ClaimCard) => c.section === 'found_by_auditor' || c.section === 'reported_failures'
    const rank = (c: ClaimCard) => VERDICT_ORDER.indexOf(c.verdict ?? 'pending')
    const byTime = (a: ClaimCard, b: ClaimCard) => rank(a) - rank(b) || a.claim.created_at.localeCompare(b.claim.created_at)
    return { claimed: all.filter((c) => !isNoticed(c)).sort(byTime), noticed: all.filter(isNoticed) }
  }, [rep])

  useEffect(() => {
    if (rep) document.title = `${rep.session.title || 'Session'} · Credence`
  }, [rep])

  useEffect(() => {
    if (open) {
      navigate(`/sessions/${id}?claim=${open}`, true)
      if (noticed.some((c) => c.claim.id === open)) setShowNoticed(true)
    }
  }, [open, id, noticed])

  const flash = useCallback((text: string, bad = false) => {
    setToast({ text, bad })
    window.setTimeout(() => setToast((t) => (t?.text === text ? null : t)), bad ? 6000 : 2500)
  }, [])

  const patchCard = useCallback(
    (card: ClaimCard) => {
      setData((r) => r && {
        ...r,
        sections: r.sections.map((s) => ({ ...s, claims: s.claims.map((c) => (c.claim.id === card.claim.id ? { ...c, ...card, members: c.members } : c)) })),
      })
    },
    [setData],
  )

  const decide = useCallback(
    async (c: ClaimCard, r: ReviewRequest) => {
      setBusy(c.claim.id)
      try {
        const res = await api.review(c.claim.id, r)
        patchCard(res.card)
        flash(r.decision === 'accepted' || r.decision === 'edited' ? 'Marked as right' : 'Marked as wrong')
      } catch (e) {
        if (e instanceof ApiError && e.status === 409) flash('The record contradicts this claim. Edit the wording to what actually happened, or mark it wrong.', true)
        else flash((e as Error).message, true)
      } finally {
        setBusy(null)
      }
    },
    [flash, patchCard],
  )

  const reverify = useCallback(async (c: ClaimCard) => {
    try {
      await api.verify(c.claim.id, true)
      flash('Checking again…')
    } catch (e) {
      flash((e as Error).message, true)
    }
  }, [flash])

  const rebuild = useCallback(async () => {
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

  const show = useCallback((c: ClaimCard) => (ev: EvidenceView) => setOpened({ evidence: ev, claim: c }), [])

  useKeys((e) => {
    if (isTyping(e)) return
    if (e.key === 'Escape') {
      if (opened) setOpened(null)
      else setOpen(null)
      return
    }
    const list = showNoticed ? [...claimed, ...noticed] : claimed
    const i = list.findIndex((c) => c.claim.id === open)
    if (e.key === 'j' || e.key === 'ArrowDown') {
      if (list.length) setOpen(list[Math.min(list.length - 1, i + 1)].claim.id)
      e.preventDefault()
    } else if (e.key === 'k' || e.key === 'ArrowUp') {
      if (list.length) setOpen(list[Math.max(0, i - 1)].claim.id)
      e.preventDefault()
    } else if (e.key === 'u') navigate('/')
  })

  useEffect(() => {
    if (open) document.getElementById(`claim-${open}`)?.scrollIntoView({ block: 'nearest', behavior: 'smooth' })
  }, [open])

  if (error) {
    return (
      <div className="page page-narrow">
        <Link to="/" className="back">← All sessions</Link>
        <div className="banner banner-bad">Loading this session failed: {error.message}</div>
      </div>
    )
  }
  if (!rep) return <div className="page page-narrow"><div className="empty">Loading…</div></div>

  const s = rep.session
  const counts = countBy(claimed)
  const where = s.repo_name || folder(s.cwd)

  return (
    <div className="page page-narrow session2">
      <Link to="/" className="back">← All sessions</Link>
      <header className="s2-head">
        <h1>{s.title || 'Untitled session'}</h1>
        <div className="s2-meta">
          <span>{day(s.started_at)}</span>
          {s.duration_sec > 0 && <span title="From the first message to the last">{dur(s.duration_sec)}</span>}
          {s.cost_usd > 0 && <span title="Estimated at API prices">{money(s.cost_usd)}</span>}
          {where && <span title={s.repo || s.cwd}>{where}</span>}
          {s.state !== 'closed' && <span className="tag tag-live">still running</span>}
        </div>
      </header>

      <section className="s2-summary" aria-label="Summary">
        {claimed.length === 0 ? (
          <p className="s2-lede">
            This session is all conversation, with zero claims to check.
          </p>
        ) : (
          <>
            <p className="s2-lede">{summarySentence(claimed.length, counts)}</p>
            <div className="s2-bar" aria-hidden>
              {VERDICT_ORDER.filter((v) => counts[v]).map((v) => (
                <span key={v} className={`v-${v}`} style={{ flexGrow: counts[v] }} />
              ))}
            </div>
            <div className="s2-legend">
              {VERDICT_ORDER.filter((v) => counts[v]).map((v) => (
                <span key={v}><VerdictMark v={v} /> {counts[v]} {VERDICT[v].short}</span>
              ))}
            </div>
          </>
        )}
      </section>

      {claimed.length > 0 && (
        <section aria-labelledby="claimed-h">
          <h2 id="claimed-h" className="s2-h">What the agent claimed</h2>
          <ul className="clist">
            {claimed.map((c) => (
              <ClaimItem key={c.claim.id} card={c} start={s.started_at} open={open === c.claim.id} busy={busy === c.claim.id}
                quote={c.claim.origin === 'stated'}
                onToggle={() => setOpen((o) => (o === c.claim.id ? null : c.claim.id))}
                onDecide={(r) => void decide(c, r)} onShow={show(c)} onReverify={() => void reverify(c)} />
            ))}
          </ul>
        </section>
      )}

      {noticed.length > 0 && (
        <section className="s2-fold">
          <button className="s2-fold-btn" onClick={() => setShowNoticed((v) => !v)} aria-expanded={showNoticed}>
            <span aria-hidden>{showNoticed ? '▾' : '▸'}</span> Other things Credence noticed <span className="muted">({noticed.length})</span>
          </button>
          {showNoticed && (
            <>
              <p className="muted small s2-fold-note">Found by reading the session itself: files changed, approaches tried, research done.</p>
              <ul className="clist">
                {noticed.map((c) => (
                  <ClaimItem key={c.claim.id} card={c} start={s.started_at} open={open === c.claim.id} busy={busy === c.claim.id} quote={false}
                    onToggle={() => setOpen((o) => (o === c.claim.id ? null : c.claim.id))}
                    onDecide={(r) => void decide(c, r)} onShow={show(c)} onReverify={() => void reverify(c)} />
                ))}
              </ul>
            </>
          )}
        </section>
      )}

      <section className="s2-fold">
        <button className="s2-fold-btn" onClick={() => setShowConvo((v) => !v)} aria-expanded={showConvo}>
          <span aria-hidden>{showConvo ? '▾' : '▸'}</span> The full conversation <span className="muted">({plural(s.event_count, 'step')})</span>
        </button>
        {showConvo && (
          <div className="s2-convo">
            <Timeline report={rep} highlight={[]}
              onOpen={(from, to, session) => setOpened({ evidence: { kind: 'events', target: `event:${session}#${from}-${to}`, relation: 'informed', label: '', session, from, to } })} />
          </div>
        )}
      </section>

      <footer className="s2-foot">
        <button className="link-btn" onClick={() => void rebuild()} disabled={busy === 'report'}>
          {busy === 'report' ? 'Rebuilding…' : 'Rebuild this report'}
        </button>
      </footer>

      {opened && <EvidencePanel opened={opened} sessionId={s.id} cwd={s.cwd} onClose={() => setOpened(null)} />}
      {toast && <div className={cx('toast', toast.bad && 'toast-bad')} role="status">{toast.text}</div>}
    </div>
  )
}

function countBy(cs: ClaimCard[]): Partial<Record<Verdict, number>> {
  const out: Partial<Record<Verdict, number>> = {}
  for (const c of cs) out[c.verdict ?? 'pending'] = (out[c.verdict ?? 'pending'] ?? 0) + 1
  return out
}

function summarySentence(n: number, counts: Partial<Record<Verdict, number>>): string {
  const parts: string[] = []
  if (counts.backed) parts.push(`${counts.backed} ${counts.backed === 1 ? 'is' : 'are'} backed by the session record`)
  if (counts.partly) parts.push(`${counts.partly} ${counts.partly === 1 ? 'is' : 'are'} partly backed`)
  if (counts.unchecked) parts.push(`${counts.unchecked} ${counts.unchecked === 1 ? 'is' : 'are'} yours to confirm`)
  if (counts.pending) parts.push(`${counts.pending} ${counts.pending === 1 ? 'is' : 'are'} still being checked`)
  if (counts.contradicted) parts.push(`${counts.contradicted} ${counts.contradicted === 1 ? 'is' : 'are'} contradicted by it`)
  const list = parts.length > 1 ? `${parts.slice(0, -1).join(', ')} and ${parts[parts.length - 1]}` : parts[0]
  return `The agent claimed ${plural(n, 'thing')}. ${list[0].toUpperCase()}${list.slice(1)}.`
}

function folder(p: string): string {
  const parts = (p || '').split('/').filter(Boolean)
  return parts.length > 2 ? parts[parts.length - 1] : ''
}
