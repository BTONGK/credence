import { useEffect, useMemo, useState } from 'react'
import { api } from '../api'
import { Link } from '../components/Link'
import { cx, day, dur, money, navigate, plural, useAsync, useDebounced, useLive, useLocation } from '../lib'
import type { Summary, SummaryNote, SummaryRef } from '../types'

const RANGES = [
  { key: '7d', label: '7 days', words: 'the last 7 days' },
  { key: '30d', label: '30 days', words: 'the last 30 days' },
  { key: 'all', label: 'All time', words: 'all time' },
]

export function SummaryPage() {
  const { query } = useLocation()
  const range = RANGES.find((r) => r.key === query.get('range')) ?? RANGES[0]
  const { data, error, loading, reload } = useAsync(() => api.summary(range.key), [range.key])
  const refresh = useDebounced(reload, 800)
  useLive((m) => {
    if (m.kind === 'session' || m.kind === 'report') refresh()
  })
  const [copied, setCopied] = useState(false)

  useEffect(() => {
    document.title = 'Summary · Credence'
  }, [])

  const titles = useMemo(() => new Map((data?.requests ?? []).map((r) => [r.session_id, r.title])), [data])

  return (
    <div className="page page-narrow summary">
      <header className="sm-head">
        <div>
          <h1>Summary</h1>
          <p className="i2-lede">
            {loading && !data ? 'Loading…' : data ? lede(data, range.words) : ''}
          </p>
        </div>
        <div className="sm-actions">
          <div className="segmented" role="tablist" aria-label="Period">
            {RANGES.map((r) => (
              <button key={r.key} role="tab" aria-selected={r.key === range.key} className={cx('seg', r.key === range.key && 'active')}
                onClick={() => navigate(r.key === '7d' ? '/summary' : `/summary?range=${r.key}`, true)}>{r.label}</button>
            ))}
          </div>
          {data && data.sessions > 0 && (
            <button className="btn btn-sm" onClick={async () => {
              try {
                await navigator.clipboard.writeText(asText(data, range.words))
                setCopied(true)
                window.setTimeout(() => setCopied(false), 1800)
              } catch {
                /* clipboard blocked: the button stays as it was */
              }
            }}>{copied ? 'Copied' : 'Copy as text'}</button>
          )}
        </div>
      </header>

      {error && <div className="banner banner-bad">Loading the summary failed: {error.message}</div>}

      {data && data.sessions === 0 && (
        <div className="empty empty-big">
          <h2>A quiet stretch</h2>
          <p>
            Credence sums up every session from {range.words} here: emails sent, files written, tests run, sites published.
            {range.key !== 'all' && ' Pick a longer period to see earlier work.'}
          </p>
        </div>
      )}

      {data && data.sessions > 0 && (
        <>
          {data.tiles.length > 0 && (
            <section className="sm-tiles" aria-label="Totals">
              {data.tiles.map((t) => (
                <div key={t.key} className="sm-tile">
                  <div className="sm-value">{t.value.toLocaleString()}</div>
                  <div className="sm-label">{t.label}</div>
                  {t.detail && <div className="sm-detail">{t.detail}</div>}
                  {t.items && t.items.length > 0 && (
                    <div className="sm-items" title={t.items.join(', ')}>{t.items.join(' · ')}</div>
                  )}
                </div>
              ))}
            </section>
          )}

          <div className="sm-cols">
            <section className="sm-col v-backed" aria-label="Went well">
              <h2><span className="vmark" aria-hidden>✓</span> Went well</h2>
              {data.well.length === 0 ? <p className="muted small">These sessions were conversation.</p> : (
                <ul className="sm-notes">{data.well.map((n) => <NoteItem key={n.text} n={n} titles={titles} />)}</ul>
              )}
            </section>
            <section className="sm-col v-partly" aria-label="Needs a look">
              <h2><span className="vmark" aria-hidden>!</span> Needs a look</h2>
              {data.look.length === 0 ? <p className="muted small">All clear for {range.words}.</p> : (
                <ul className="sm-notes">{data.look.map((n) => <NoteItem key={n.text} n={n} titles={titles} />)}</ul>
              )}
            </section>
          </div>

          {data.apps.length > 0 && (
            <section className="sm-section">
              <h2>Apps it worked in</h2>
              <div className="sm-apps">
                {data.apps.map((a) => (
                  <span key={a.name} className="sm-app">{a.name} <span className="muted">{plural(a.actions, 'action')}</span></span>
                ))}
              </div>
            </section>
          )}

          <section className="sm-section">
            <h2>What you asked for</h2>
            <ul className="i2-list">
              {data.requests.map((r) => (
                <li key={r.session_id}>
                  <Link to={`/sessions/${r.session_id}`} className="i2-row">
                    <span className="i2-main">
                      <span className="i2-title">{r.title}</span>
                      <span className="sm-facts">{r.facts.length ? r.facts.join(' · ') : 'Conversation'}</span>
                    </span>
                    <span className="sm-when">
                      {r.look && <span className="tag tag-warn">needs a look</span>}
                      <span className="muted small">{day(r.when)}{r.active_sec >= 60 && <> · {dur(r.active_sec)}</>}</span>
                    </span>
                  </Link>
                </li>
              ))}
            </ul>
          </section>

          <p className="s2-foot">
            Every number here comes from the session record: the files, commands, tests and app actions the agent ran.
            Active time counts the stretches where the agent was working.
          </p>
        </>
      )}
    </div>
  )
}

function lede(s: Summary, words: string): string {
  if (!s.sessions) return `Everything your agents did in ${words}, in plain numbers.`
  const parts = [plural(s.sessions, 'session')]
  if (s.active_sec >= 60) parts.push(`${dur(s.active_sec)} of active work`)
  if (s.cost_usd > 0) parts.push(`${money(s.cost_usd)} in model usage`)
  return `Over ${words}: ${parts.join(' · ')}.`
}

function NoteItem({ n, titles }: { n: SummaryNote; titles: Map<string, string> }) {
  const details = n.details ?? []
  const [open, setOpen] = useState(details.length > 0 && details.length <= 3)
  return (
    <li className="sm-note">
      <div className="sm-note-text">{n.text}</div>
      {details.length > 0 && (
        <>
          <button className="sm-more" onClick={() => setOpen((o) => !o)} aria-expanded={open}>
            <span aria-hidden>{open ? '▾' : '▸'}</span> {open ? 'Hide' : 'Show'} {details.length === 1 ? 'the case' : `${details.length} cases`}
          </button>
          {open && (
            <ul className="sm-details">
              {details.map((d, i) => (
                <li key={i}>
                  {d.said && <div className="sm-said">It said: “{plain(d.said)}”</div>}
                  <div>{plain(d.text)}</div>
                  {d.session && <Link to={`/sessions/${d.session}`} className="sm-ref">{titles.get(d.session) ?? 'Open session'}</Link>}
                </li>
              ))}
            </ul>
          )}
        </>
      )}
      {(n.sessions?.length ?? 0) > 0 && <Refs refs={n.sessions!} />}
    </li>
  )
}

function Refs({ refs }: { refs: SummaryRef[] }) {
  const [all, setAll] = useState(false)
  const shown = all ? refs : refs.slice(0, 2)
  return (
    <div className="sm-refs">
      {shown.map((r) => <Link key={r.id} to={`/sessions/${r.id}`} className="sm-ref">{r.title}</Link>)}
      {refs.length > shown.length && (
        <button className="sm-more" onClick={() => setAll(true)}>+{refs.length - shown.length} more</button>
      )}
    </div>
  )
}

// plain drops the code marks agents put around file names and commands.
function plain(s: string): string {
  return s.replace(/`/g, '')
}

// asText is the summary as plain text, ready to paste into a message or doc.
function asText(s: Summary, words: string): string {
  const out = [`AI work summary for ${words}`, lede(s, words).replace(/^Over [^:]+: /, ''), '']
  for (const t of s.tiles) out.push(`${t.label}: ${t.value.toLocaleString()}${t.detail ? ` (${t.detail})` : ''}`)
  const notes = (title: string, mark: string, ns: SummaryNote[]) => {
    if (!ns.length) return
    out.push('', title)
    for (const n of ns) {
      out.push(`${mark} ${n.text}`)
      for (const d of n.details ?? []) out.push(d.said ? `   - It said “${plain(d.said)}” ${plain(d.text)}` : `   - ${plain(d.text)}`)
    }
  }
  notes('Went well', '✓', s.well)
  notes('Needs a look', '!', s.look)
  if (s.apps.length) out.push('', `Apps: ${s.apps.map((a) => a.name).join(', ')}`)
  out.push('', 'What was asked for')
  for (const r of s.requests) out.push(`- ${r.title}${r.facts.length ? `: ${r.facts.join(', ')}` : ''}`)
  return out.join('\n') + '\n'
}
