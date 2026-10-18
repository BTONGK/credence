import { useEffect, useState } from 'react'
import { api, ApiError, bootstrapToken, forgetToken } from './api'
import { Help } from './components/Help'
import type { Status } from './types'
import { Link } from './components/Link'
import { connectLive, cx, isTyping, navigate, useAsync, useKeys, useLive, useLiveState, useLocation } from './lib'
import { Inbox } from './pages/Inbox'
import { Ledger } from './pages/Ledger'
import { SessionPage } from './pages/Session'

export function App() {
  const [token] = useState(() => bootstrapToken())
  const { path } = useLocation()
  const [help, setHelp] = useState(false)
  const status = useAsync(() => api.status(), [])

  useEffect(() => {
    if (token) connectLive()
  }, [token])
  useLive((m) => {
    if (m.kind === 'session' || m.kind === 'report') status.reload()
  })

  // g i / g l chords and ? for help.
  const [chord, setChord] = useState(false)
  useKeys((e) => {
    if (isTyping(e)) return
    if (e.key === '?') {
      setHelp((h) => !h)
      e.preventDefault()
      return
    }
    if (e.key === 'Escape' && help) {
      setHelp(false)
      return
    }
    if (chord) {
      setChord(false)
      if (e.key === 'i') navigate('/')
      if (e.key === 'l') navigate('/ledger')
      return
    }
    if (e.key === 'g') {
      setChord(true)
      window.setTimeout(() => setChord(false), 1200)
    }
  })

  if (!token || (status.error instanceof ApiError && status.error.status === 401)) {
    return <NoToken />
  }

  let page
  const sm = path.match(/^\/sessions\/([^/]+)/)
  const cm = path.match(/^\/claims\/([^/]+)/)
  if (sm) page = <SessionPage key={sm[1]} id={decodeURIComponent(sm[1])} />
  else if (cm) page = <ClaimRedirect id={decodeURIComponent(cm[1])} />
  else if (path.startsWith('/ledger')) page = <Ledger />
  else page = <Inbox />

  return (
    <div className="app">
      <header className="topbar">
        <Link to="/" className="brand" aria-label="Credence inbox">
          <span className="brand-mark" aria-hidden>
            <svg viewBox="0 0 32 32" width="20" height="20">
              <rect width="32" height="32" rx="7" fill="currentColor" />
              <path d="M9 16.5l4.5 4.5L23 11" fill="none" stroke="var(--bg)" strokeWidth="3.2" strokeLinecap="round" strokeLinejoin="round" />
            </svg>
          </span>
          Credence
        </Link>
        <nav className="nav">
          <Link to="/" className={cx('nav-link', (path === '/' || path.startsWith('/sessions')) && 'active')}>Sessions</Link>
          <Link to="/ledger" className={cx('nav-link', path.startsWith('/ledger') && 'active')}>Ledger</Link>
        </nav>
        <div className="topbar-right">
          <StatusPill status={status.data} />
          <button className="icon-btn" onClick={() => setHelp(true)} title="Keyboard shortcuts (?)" aria-label="Keyboard shortcuts">?</button>
        </div>
      </header>
      <main className="main">{page}</main>
      {help && <Help onClose={() => setHelp(false)} />}
    </div>
  )

}

function StatusPill({ status }: { status: Status | null }) {
  const live = useLiveState()
  const busy = status ? (status.jobs?.pending ?? 0) + (status.jobs?.running ?? 0) : 0
  const title = status ? `credenced ${status.version}` : 'connecting…'
  return (
    <div className="status" title={title}>
      <span className={cx('dot', live ? 'dot-live' : 'dot-off')} aria-hidden />
      <span>{busy > 0 ? 'checking…' : live ? 'up to date' : 'offline'}</span>
    </div>
  )
}

function ClaimRedirect({ id }: { id: string }) {
  const { data, error } = useAsync(() => api.claim(id), [id])
  useEffect(() => {
    if (data) navigate(`/sessions/${data.card.claim.session_id}?claim=${id}`, true)
  }, [data, id])
  if (error) return <div className="empty">Looking up that claim failed: {error.message}</div>
  return <div className="empty">Opening claim…</div>
}

function NoToken() {
  return (
    <div className="no-token">
      <div className="no-token-card">
        <h1>Open Credence from your terminal</h1>
        <p>
          The local UI needs the daemon’s access token. Run <code>credence ui</code> and it will open this page with
          the token attached.
        </p>
        <p className="muted">
          The token stays in this tab only. If the daemon restarted, its token changed; run <code>credence ui</code>{' '}
          again.
        </p>
        <button
          className="btn"
          onClick={() => {
            forgetToken()
            location.reload()
          }}
        >
          Retry
        </button>
      </div>
    </div>
  )
}
