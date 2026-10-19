import { useEffect, useMemo, useRef, useState } from 'react'
import { api } from '../api'
import { VerdictMark } from '../components/ClaimItem'
import { Link } from '../components/Link'
import { cx, day, dur, isTyping, money, navigate, plural, useAsync, useDebounced, useKeys, useLive, useLocation } from '../lib'
import type { SessionItem } from '../types'

export function Inbox() {
  const { query } = useLocation()
  const [q, setQ] = useState(query.get('q') ?? '')
  const [showQuiet, setShowQuiet] = useState(false)
  const [sel, setSel] = useState(0)
  const search = useRef<HTMLInputElement>(null)

  const params = useMemo(() => {
    const p = new URLSearchParams()
    if (q.trim()) p.set('q', q.trim())
    return p
  }, [q])

  useEffect(() => {
    const s = params.toString()
    navigate(s ? `/?${s}` : '/', true)
  }, [params])

  const { data, error, loading, reload } = useAsync(() => api.sessions(params), [params.toString()])
  const refresh = useDebounced(reload, 400)
  useLive((m) => {
    if (m.kind === 'session' || m.kind === 'report') refresh()
  })

  const top = (data?.sessions ?? []).filter((s) => !s.parent_session_id)
  const withClaims = top.filter((s) => claimCount(s) > 0)
  const quiet = top.filter((s) => claimCount(s) === 0)
  const visible = showQuiet ? [...withClaims, ...quiet] : withClaims
  const total = withClaims.reduce(
    (t, s) => ({ backed: t.backed + (s.tally?.backed ?? 0), unchecked: t.unchecked + (s.tally?.unchecked ?? 0), contradicted: t.contradicted + (s.tally?.contradicted ?? 0) }),
    { backed: 0, unchecked: 0, contradicted: 0 },
  )
  const n = total.backed + total.unchecked + total.contradicted

  useEffect(() => {
    if (sel >= visible.length) setSel(Math.max(0, visible.length - 1))
  }, [visible.length, sel])

  useKeys((e) => {
    if (isTyping(e)) {
      if (e.key === 'Escape') (e.target as HTMLElement).blur()
      return
    }
    if (e.key === 'j' || e.key === 'ArrowDown') {
      setSel((i) => Math.min(visible.length - 1, i + 1))
      e.preventDefault()
    } else if (e.key === 'k' || e.key === 'ArrowUp') {
      setSel((i) => Math.max(0, i - 1))
      e.preventDefault()
    } else if ((e.key === 'Enter' || e.key === 'o') && visible[sel]) {
      navigate(`/sessions/${visible[sel].id}`)
    } else if (e.key === '/') {
      search.current?.focus()
      e.preventDefault()
    }
  })

  useEffect(() => {
    document.querySelector(`[data-row="${sel}"]`)?.scrollIntoView({ block: 'nearest' })
  }, [sel])

  useEffect(() => {
    document.title = 'Credence'
  }, [])

  return (
    <div className="page page-narrow inbox2">
      <header className="i2-head">
        <h1>Sessions</h1>
        <p className="i2-lede">
          {loading && !data ? 'Loading…' : n > 0 ? (
            <>
              Across {plural(top.length, 'session')}, agents claimed {plural(n, 'thing')}:{' '}
              <span className="i2-total"><VerdictMark v="backed" /> {total.backed} backed</span>{' '}
              <span className="i2-total"><VerdictMark v="unchecked" /> {total.unchecked} to confirm</span>{' '}
              <span className="i2-total"><VerdictMark v="contradicted" /> {total.contradicted} contradicted</span>
            </>
          ) : top.length ? `${plural(top.length, 'session')}, all conversation so far.` : ''}
        </p>
      </header>

      <input ref={search} className="input i2-search" placeholder="Search sessions and claims" value={q}
        onChange={(e) => setQ(e.target.value)} aria-label="Search sessions" />

      {error && <div className="banner banner-bad">Loading sessions failed: {error.message}</div>}
      {data && top.length === 0 && <EmptyInbox filtered={params.toString() !== ''} />}

      <ul className="i2-list" role="listbox" aria-label="Sessions">
        {withClaims.map((s, i) => <Row key={s.id} s={s} index={i} selected={i === sel} onHover={() => setSel(i)} />)}
      </ul>

      {quiet.length > 0 && (
        <div className="s2-fold">
          <button className="s2-fold-btn" onClick={() => setShowQuiet((v) => !v)} aria-expanded={showQuiet}>
            <span aria-hidden>{showQuiet ? '▾' : '▸'}</span> {plural(quiet.length, 'session')} of conversation only
          </button>
          {showQuiet && (
            <ul className="i2-list i2-quiet">
              {quiet.map((s, i) => (
                <Row key={s.id} s={s} index={withClaims.length + i} selected={withClaims.length + i === sel} onHover={() => setSel(withClaims.length + i)} />
              ))}
            </ul>
          )}
        </div>
      )}
    </div>
  )
}

function claimCount(s: SessionItem): number {
  const t = s.tally ?? {}
  return (t.backed ?? 0) + (t.unchecked ?? 0) + (t.contradicted ?? 0)
}

function Row({ s, index, selected, onHover }: { s: SessionItem; index: number; selected: boolean; onHover: () => void }) {
  const t = s.tally ?? {}
  return (
    <li data-row={index} role="option" aria-selected={selected} onMouseEnter={onHover}>
      <Link to={`/sessions/${s.id}`} className={cx('i2-row', selected && 'selected')}>
        <span className="i2-main">
          <span className="i2-title">
            {s.title || 'Untitled session'}
            {s.state !== 'closed' && <span className="tag tag-live">still running</span>}
          </span>
          <span className="i2-meta">
            {day(s.started_at)}
            {s.duration_sec > 0 && <> · {dur(s.duration_sec)}</>}
            {s.cost_usd > 0 && <> · {money(s.cost_usd)}</>}
            {s.repo_name && <> · {s.repo_name}</>}
          </span>
        </span>
        <span className="i2-tally" aria-label={`${t.backed ?? 0} backed, ${t.unchecked ?? 0} to confirm, ${t.contradicted ?? 0} contradicted`}>
          {claimCount(s) === 0 ? <span className="muted small">0 claims</span> : (
            <>
              {(t.contradicted ?? 0) > 0 && <span className="i2-n v-contradicted"><VerdictMark v="contradicted" />{t.contradicted}</span>}
              {(t.unchecked ?? 0) > 0 && <span className="i2-n v-unchecked"><VerdictMark v="unchecked" />{t.unchecked}</span>}
              {(t.backed ?? 0) > 0 && <span className="i2-n v-backed"><VerdictMark v="backed" />{t.backed}</span>}
            </>
          )}
        </span>
      </Link>
    </li>
  )
}

const HARNESS_NAMES: Record<string, string> = { 'claude-code': 'Claude Code', codex: 'Codex', cursor: 'Cursor' }

function EmptyInbox({ filtered }: { filtered: boolean }) {
  const { data: status } = useAsync(() => api.status(), [])
  if (filtered) return <div className="empty">Try a different search.</div>
  const heard = Object.entries(status?.hooks_24h ?? {})
    .filter(([, events]) => Object.values(events ?? {}).reduce((a, b) => a + b, 0) > 0)
    .map(([h]) => HARNESS_NAMES[h] ?? h)
  const working = (status?.jobs?.pending ?? 0) + (status?.jobs?.running ?? 0)
  return (
    <div className="empty empty-big">
      {working > 0 ? (
        <>
          <h2>Checking your sessions</h2>
          <p>Credence is reading {plural(working, 'session')} now. Reports appear here as each one finishes.</p>
        </>
      ) : heard.length > 0 ? (
        <>
          <h2>Capture is working</h2>
          <p>
            Credence heard from {heard.join(' and ')} in the last day. Each session&rsquo;s report appears here a few minutes
            after the session goes quiet.
          </p>
        </>
      ) : (
        <>
          <h2>Your sessions will appear here</h2>
          <p>
            Use Claude Code or Codex as usual. Each session&rsquo;s report appears here a few minutes after the session goes
            quiet.
          </p>
        </>
      )}
      <p className="muted">
        To check older sessions, run <code>credence backfill --since 7d</code>. To confirm capture is set up, run{' '}
        <code>credence doctor</code>. To see an example report, run <code>credence demo</code>.
      </p>
    </div>
  )
}
