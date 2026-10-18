import { useEffect, useMemo, useRef, useState } from 'react'
import { api } from '../api'
import { clock, cx, who } from '../lib'
import type { Episode, Report } from '../types'
import { EventView } from './EventView'

interface Span {
  session: string
  from: number
  to: number
}

const ICON: Record<string, string> = {
  prompt: '›', message: '“', read: '▤', search: '⌕', test: '▶', command: '$', edit: '✎', subagent: '⑂', commit: '◆', claim: '✦',
}

export function Timeline({ report, highlight, onOpen }: {
  report: Report
  highlight: Span[]
  onOpen: (from: number, to: number, session: string) => void
}) {
  const eps = report.episodes ?? []
  const start = report.session.started_at
  const children = useMemo(() => new Map((report.children ?? []).map((c) => [c.id, c])), [report.children])
  const list = useRef<HTMLOListElement>(null)
  const hit = (e: Episode) => highlight.some((h) => h.session === e.session && h.from <= e.to && h.to >= e.from)

  // Bring the selected claim's evidence into view.
  const key = highlight.map((h) => `${h.session}${h.from}`).join()
  useEffect(() => {
    const el = list.current?.querySelector('.ep.is-hit')
    el?.scrollIntoView({ block: 'nearest', behavior: 'smooth' })
  }, [key])

  if (!eps.length) return <div className="empty">No events captured.</div>
  return (
    <div className="timeline">
      <div className="pane-head">
        <h2>Timeline</h2>
        <span className="muted small">{report.session.event_count} events, grouped</span>
      </div>
      <ol className="eps" ref={list}>
        {eps.map((e, i) => (
          <EpisodeRow key={`${e.session}-${e.from}-${i}`} e={e} start={start} hit={hit(e)}
            child={e.session !== report.session.id ? children.get(e.session)?.agent_type || 'subagent' : ''}
            cwd={report.session.cwd} onOpen={onOpen} />
        ))}
      </ol>
    </div>
  )
}

function EpisodeRow({ e, start, hit, child, cwd, onOpen }: {
  e: Episode
  start: string
  hit: boolean
  child: string
  cwd: string
  onOpen: (from: number, to: number, session: string) => void
}) {
  const [open, setOpen] = useState(false)
  const [events, setEvents] = useState<import('../types').Event[] | null>(null)
  const [err, setErr] = useState('')
  useEffect(() => {
    if (open && !events) {
      api.events(e.session, e.from, e.to).then((r) => setEvents(r.events)).catch((x: Error) => setErr(x.message))
    }
  }, [open, events, e.session, e.from, e.to])
  const actor = who(e.actor)
  const human = e.actor.startsWith('human:')
  return (
    <li className={cx('ep', `ep-${e.kind}`, hit && 'is-hit', human && 'is-human', child && 'is-child', (e.failures ?? 0) > 0 && 'has-fail')}>
      <button className="ep-row" onClick={() => setOpen((v) => !v)} aria-expanded={open}>
        <span className="ep-time mono">{clock(e.ts, start)}</span>
        <span className="ep-icon" aria-hidden>{ICON[e.kind] ?? '·'}</span>
        <span className="ep-label">
          {(human || e.kind === 'subagent' || child) && <span className="ep-actor">{child ? `${child} · ` : human ? '' : `${actor} · `}</span>}
          {e.label}
          {(e.failures ?? 0) > 0 && <span className="ep-fail">{e.failures} failed</span>}
        </span>
        <span className="ep-seq mono muted">#{e.from}{e.to !== e.from ? `–${e.to}` : ''}</span>
      </button>
      {open && (
        <div className="ep-detail">
          {err && <div className="form-error">{err}</div>}
          {!events && !err && <div className="muted small">Loading…</div>}
          {events?.map((ev) => <EventView key={`${ev.session_id}-${ev.seq}`} ev={ev} cwd={cwd} compact />)}
          <button className="link-btn small" onClick={() => onOpen(e.from, e.to, e.session)}>Open in panel</button>
        </div>
      )}
    </li>
  )
}
