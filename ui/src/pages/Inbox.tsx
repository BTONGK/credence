import { useEffect, useMemo, useRef, useState } from 'react'
import { api } from '../api'
import { Link } from '../components/Link'
import { SectionCounts } from '../components/Status'
import { ago, cx, dur, isTyping, money, navigate, plural, useAsync, useDebounced, useKeys, useLive, useLocation } from '../lib'
import type { SessionItem } from '../types'

export function Inbox() {
  const { query } = useLocation()
  const [q, setQ] = useState(query.get('q') ?? '')
  const [needs, setNeeds] = useState(query.get('needs_review') === '1')
  const [repo, setRepo] = useState(query.get('repo') ?? '')
  const [harness, setHarness] = useState(query.get('harness') ?? '')
  const [showMuted, setShowMuted] = useState(false)
  const [sel, setSel] = useState(0)
  const search = useRef<HTMLInputElement>(null)

  const params = useMemo(() => {
    const p = new URLSearchParams()
    if (q.trim()) p.set('q', q.trim())
    if (needs) p.set('needs_review', '1')
    if (repo) p.set('repo', repo)
    if (harness) p.set('harness', harness)
    return p
  }, [q, needs, repo, harness])

  // Keep filters in the URL so back/forward and reloads keep them.
  useEffect(() => {
    const s = params.toString()
    navigate(s ? `/?${s}` : '/', true)
  }, [params])

  const { data, error, loading, reload } = useAsync(() => api.sessions(params), [params.toString()])
  const refresh = useDebounced(reload, 400)
  useLive((m) => {
    if (m.kind === 'session' || m.kind === 'report') refresh()
  })

  const all = data?.sessions ?? []
  const top = all.filter((s) => !s.parent_session_id)
  const active = top.filter((s) => !s.muted)
  const muted = top.filter((s) => s.muted)
  const visible = showMuted ? [...active, ...muted] : active
  const repos = useMemo(() => uniq(all.map((s) => s.repo_name).filter(Boolean)), [all])
  const harnesses = useMemo(() => uniq(all.map((s) => s.harness)), [all])
  const toReview = active.reduce((n, s) => n + s.pending, 0)

  useEffect(() => {
    if (sel >= visible.length) setSel(Math.max(0, visible.length - 1))
  }, [visible.length, sel])

  useKeys((e) => {
    if (isTyping(e)) {
      if (e.key === 'Escape') (e.target as HTMLElement).blur()
      return
    }
    switch (e.key) {
      case 'j':
      case 'ArrowDown':
        setSel((i) => Math.min(visible.length - 1, i + 1))
        e.preventDefault()
        break
      case 'k':
      case 'ArrowUp':
        setSel((i) => Math.max(0, i - 1))
        e.preventDefault()
        break
      case 'Enter':
      case 'o':
        if (visible[sel]) navigate(`/sessions/${visible[sel].id}`)
        break
      case '/':
        search.current?.focus()
        e.preventDefault()
        break
      case 'n':
        setNeeds((v) => !v)
        break
    }
  })

  useEffect(() => {
    document.querySelector(`[data-row="${sel}"]`)?.scrollIntoView({ block: 'nearest' })
  }, [sel])

  useEffect(() => {
    document.title = toReview ? `(${toReview}) Credence` : 'Credence'
  }, [toReview])

  return (
    <div className="page inbox">
      <div className="page-head">
        <div>
          <h1>Sessions</h1>
          <p className="muted">
            {loading && !data
              ? 'Loading…'
              : toReview
                ? `${plural(toReview, 'claim')} waiting for your review`
                : active.length
                  ? 'All caught up'
                  : ''}
          </p>
        </div>
      </div>
      <div className="filters" role="search">
        <input
          ref={search}
          className="input search"
          placeholder="Search titles, repos, claims  ( / )"
          value={q}
          onChange={(e) => setQ(e.target.value)}
          aria-label="Search sessions"
        />
        <label className="toggle">
          <input type="checkbox" checked={needs} onChange={(e) => setNeeds(e.target.checked)} />
          Needs review
        </label>
        {repos.length > 1 || repo ? (
          <select className="input select" value={repo} onChange={(e) => setRepo(e.target.value)} aria-label="Repository">
            <option value="">All repos</option>
            {repos.map((r) => <option key={r} value={r}>{r}</option>)}
          </select>
        ) : null}
        {harnesses.length > 1 || harness ? (
          <select className="input select" value={harness} onChange={(e) => setHarness(e.target.value)} aria-label="Harness">
            <option value="">All harnesses</option>
            {harnesses.map((h) => <option key={h} value={h}>{h}</option>)}
          </select>
        ) : null}
      </div>

      {error && <div className="banner banner-bad">Couldn’t load sessions: {error.message}</div>}

      {data && active.length === 0 && muted.length === 0 && <EmptyInbox filtered={params.toString() !== ''} />}

      <ul className="sessions" role="listbox" aria-label="Sessions">
        {active.map((s, i) => <Row key={s.id} s={s} index={i} selected={i === sel} onHover={() => setSel(i)} />)}
      </ul>

      {muted.length > 0 && (
        <div className="muted-group">
          <button className="link-btn" onClick={() => setShowMuted((v) => !v)} aria-expanded={showMuted}>
            {showMuted ? '▾' : '▸'} {plural(muted.length, 'session')} with nothing to claim
          </button>
          {showMuted && (
            <ul className="sessions sessions-muted">
              {muted.map((s, i) => (
                <Row key={s.id} s={s} index={active.length + i} selected={active.length + i === sel}
                  onHover={() => setSel(active.length + i)} />
              ))}
            </ul>
          )}
        </div>
      )}
    </div>
  )
}

function Row({ s, index, selected, onHover }: { s: SessionItem; index: number; selected: boolean; onHover: () => void }) {
  const flags = s.flags ?? []
  return (
    <li data-row={index} role="option" aria-selected={selected} onMouseEnter={onHover}>
      <Link to={`/sessions/${s.id}`} className={cx('session-row', selected && 'selected', s.muted && 'is-muted')}>
        <div className="session-main">
          <div className="session-title">
            {s.title || s.id}
            {s.state !== 'closed' && <span className="tag tag-live">{s.state === 'open' ? 'in progress' : s.state}</span>}
            {s.report_state === 'provisional' && <span className="tag">provisional</span>}
            {flags.includes('transcript_rewritten') && <span className="tag tag-bad" title="Lines of the transcript changed after capture">transcript rewritten</span>}
          </div>
          <div className="session-meta">
            {s.repo_name && <span>{s.repo_name}{s.branch ? `@${s.branch}` : ''}</span>}
            <span>{s.harness}</span>
            {s.model && <span>{s.model}</span>}
            <span>{dur(s.duration_sec)}</span>
            {s.cost_usd > 0 && <span>{money(s.cost_usd)}</span>}
          </div>
          <div className="session-summary">
            {s.muted ? <span className="muted">{s.summary}</span> : <SectionCounts counts={s.counts} total={s.claims} />}
          </div>
        </div>
        <div className="session-side">
          {s.pending > 0 ? (
            <span className="pending" title={`${s.pending} claims to review`}>{s.pending}</span>
          ) : s.claims > 0 ? (
            <span className="reviewed" title="All claims reviewed">✓</span>
          ) : null}
          <span className="muted small">{ago(s.last_activity)}</span>
        </div>
      </Link>
    </li>
  )
}

function EmptyInbox({ filtered }: { filtered: boolean }) {
  if (filtered) return <div className="empty">No sessions match these filters.</div>
  return (
    <div className="empty empty-big">
      <h2>No sessions yet</h2>
      <p>
        Credence captures Claude Code and Codex sessions as they happen. Run <code>credence init</code> to install the
        capture hooks, or <code>credence demo</code> to see an example credit report.
      </p>
      <p className="muted">
        Older sessions can be captured with <code>credence backfill --since 30d</code>.
      </p>
    </div>
  )
}

function uniq(xs: string[]): string[] {
  return [...new Set(xs)].sort()
}
