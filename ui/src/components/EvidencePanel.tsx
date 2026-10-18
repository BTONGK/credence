import { useEffect, useRef, useState } from 'react'
import { api } from '../api'
import { cx, short, useAsync } from '../lib'
import type { ClaimCard, EvidenceView, TestRun } from '../types'
import { EventView } from './EventView'

export interface Opened {
  evidence: EvidenceView
  claim?: ClaimCard
}

// A side drawer showing what an evidence chip points at.
export function EvidencePanel({ opened, sessionId, cwd, onClose }: {
  opened: Opened
  sessionId: string
  cwd: string
  onClose: () => void
}) {
  const ev = opened.evidence
  const ref = useRef<HTMLDivElement>(null)
  useEffect(() => {
    ref.current?.focus()
  }, [ev.target])
  return (
    <div className="drawer-wrap" onClick={onClose} role="presentation">
      <aside className="drawer" role="dialog" aria-modal="true" aria-label={ev.label} tabIndex={-1} ref={ref}
        onClick={(e) => e.stopPropagation()} onKeyDown={(e) => e.key === 'Escape' && onClose()}>
        <div className="drawer-head">
          <div>
            <div className="muted small">{opened.claim ? opened.claim.display_title : 'Evidence'}</div>
            <h2>{ev.label}</h2>
            {ev.note && <div className="muted small">{ev.note}</div>}
          </div>
          <button className="icon-btn" onClick={onClose} aria-label="Close">×</button>
        </div>
        <div className="drawer-body">
          {ev.kind === 'events' && ev.session && <Events session={ev.session} from={ev.from ?? 0} to={ev.to ?? ev.from ?? 0} cwd={cwd} />}
          {ev.kind === 'commit' && ev.commit && <Diff session={sessionId} commit={ev.commit} />}
          {ev.kind === 'file' && ev.commit && ev.path && <FileEvidence session={sessionId} commit={ev.commit} path={ev.path} />}
          {ev.kind === 'test_run' && <TestRuns ids={(ev.id ?? ev.target.replace(/^test_run:/, '')).split(/[,\s]+/).filter(Boolean)} />}
          {['ci', 'pr', 'issue'].includes(ev.kind) && <External target={ev.target} />}
          {ev.kind === 'unresolved' && (
            <div className="banner banner-warn">This evidence points outside what Credence captured: <code>{ev.target}</code></div>
          )}
        </div>
      </aside>
    </div>
  )
}

function Events({ session, from, to, cwd }: { session: string; from: number; to: number; cwd: string }) {
  // Show a little context around the cited span.
  const lo = Math.max(1, from - 1)
  const { data, error } = useAsync(() => api.events(session, lo, to + 1), [session, lo, to])
  if (error) return <div className="form-error">{error.message}</div>
  if (!data) return <div className="muted">Loading events…</div>
  return (
    <div className="events">
      {data.events.map((e) => (
        <div key={e.seq} className={cx('ev-wrap', (e.seq < from || e.seq > to) && 'is-context')}>
          <EventView ev={e} cwd={cwd} />
        </div>
      ))}
      <p className="muted small">Events are hash-chained as captured; the dimmed ones are context around the cited span.</p>
    </div>
  )
}

function Diff({ session, commit, path }: { session: string; commit: string; path?: string }) {
  const { data, error } = useAsync(() => api.diff(session, commit, path), [session, commit, path])
  if (error) return <div className="form-error">{error.message}</div>
  if (!data) return <div className="muted">Loading diff…</div>
  const c = data.commit
  return (
    <div>
      <div className="commit-head">
        <div><span className="mono">{short(c.sha)}</span> <strong>{c.subject}</strong></div>
        <div className="muted small">
          {c.author} · {new Date(c.author_time).toLocaleString()} · {c.files.length} files ·{' '}
          <span className="tone-good">+{c.added}</span> <span className="tone-bad">−{c.deleted}</span>
        </div>
      </div>
      <DiffView text={data.diff} />
    </div>
  )
}

function FileEvidence({ session, commit, path }: { session: string; commit: string; path: string }) {
  const [tab, setTab] = useState<'diff' | 'file'>('diff')
  return (
    <div>
      <div className="tabs" role="tablist">
        <button role="tab" aria-selected={tab === 'diff'} className={cx('tab', tab === 'diff' && 'active')} onClick={() => setTab('diff')}>Change</button>
        <button role="tab" aria-selected={tab === 'file'} className={cx('tab', tab === 'file' && 'active')} onClick={() => setTab('file')}>File at {short(commit)}</button>
      </div>
      {tab === 'diff' ? <Diff session={session} commit={commit} path={path} /> : <FileAt session={session} commit={commit} path={path} />}
    </div>
  )
}

function FileAt({ session, commit, path }: { session: string; commit: string; path: string }) {
  const { data, error } = useAsync(() => api.file(session, commit, path), [session, commit, path])
  if (error) return <div className="form-error">{error.message}</div>
  if (!data) return <div className="muted">Loading file…</div>
  const lines = data.content.split('\n')
  return (
    <pre className="code numbered">
      {lines.map((l, i) => (
        <span key={i} className="line"><span className="ln">{i + 1}</span>{l || ' '}{'\n'}</span>
      ))}
    </pre>
  )
}

export function DiffView({ text }: { text: string }) {
  if (!text.trim()) return <div className="muted">The diff for this change is empty.</div>
  return (
    <pre className="code diff">
      {text.split('\n').map((l, i) => {
        const cls = l.startsWith('+++') || l.startsWith('---') ? 'd-file'
          : l.startsWith('+') ? 'd-add' : l.startsWith('-') ? 'd-del' : l.startsWith('@@') ? 'd-hunk'
          : l.startsWith('diff ') || l.startsWith('index ') ? 'd-meta' : ''
        return <span key={i} className={cx('line', cls)}>{l || ' '}{'\n'}</span>
      })}
    </pre>
  )
}

function TestRuns({ ids }: { ids: string[] }) {
  const { data, error } = useAsync(() => Promise.all(ids.map((id) => api.testRun(id))), [ids.join()])
  if (error) return <div className="form-error">{error.message}</div>
  if (!data) return <div className="muted">Loading test runs…</div>
  const order = (r: TestRun) => (r.target === 'before' ? 0 : mutantOf(r) ? 2 : 1)
  return (
    <div className="runs">
      <p className="muted small">Credence re-ran these in a clean git worktree, so the results come from Credence itself.</p>
      {[...data].sort((a, b) => order(a) - order(b)).map((r) => {
        const m = mutantOf(r)
        return (
        <div key={r.id} className="run">
          <div className="run-head">
            {m ? (
              <span className={cx('tag', r.passes === 0 ? 'tag-good' : 'tag-bad')} title="A test should fail when the code it covers is broken">
                mutant {m.at} <code>{m.from}</code> → <code>{m.to || '∅'}</code> · {r.passes === 0 ? 'caught' : 'survived'}
              </span>
            ) : (
              <span className={cx('tag', r.passes === r.runs ? 'tag-good' : r.passes === 0 ? 'tag-bad' : 'tag-warn')}>
                {r.target === 'before' ? 'before (parent)' : 'after'} · {r.passes}/{r.runs} passed
              </span>
            )}
            <span className="mono small">{short(r.commit)}</span>
          </div>
          <pre className="ev-code">$ {r.command}</pre>
          <RunBar runs={r.runs} passes={r.passes} />
          {r.output && <pre className="code run-out">{r.output}</pre>}
        </div>
        )
      })}
    </div>
  )
}

// Mutant runs carry "+mutant:<path>:<line>:<col>:<from>→<to>" in their target.
function mutantOf(r: TestRun): { at: string; from: string; to: string } | null {
  const m = r.target.match(/\+mutant:(.+):(\d+):\d+:(.*)→(.*)$/)
  return m ? { at: `${m[1]}:${m[2]}`, from: m[3].trim(), to: m[4].trim() } : null
}

function RunBar({ runs, passes }: { runs: number; passes: number }) {
  if (runs <= 1) return null
  return (
    <div className="runbar" aria-label={`${passes} of ${runs} runs passed`}>
      {Array.from({ length: runs }, (_, i) => <span key={i} className={i < passes ? 'is-pass' : 'is-fail'} />)}
    </div>
  )
}

// pr:github/<owner>/<repo>#<n>, issue:github/<owner>/<repo>#<n>, ci:github/<owner>/<repo>/<sha>
export function githubURL(target: string): string {
  const pr = target.match(/^(pr|issue):github\/([^/]+\/[^/#]+)#(\d+)$/)
  if (pr) return `https://github.com/${pr[2]}/${pr[1] === 'pr' ? 'pull' : 'issues'}/${pr[3]}`
  const ci = target.match(/^ci:github\/([^/]+\/[^/]+)\/([0-9a-f]{7,40})$/)
  if (ci) return `https://github.com/${ci[1]}/commit/${ci[2]}`
  return ''
}

function External({ target }: { target: string }) {
  const url = githubURL(target)
  return (
    <div>
      <p><code>{target}</code></p>
      {url && <a className="btn" href={url} target="_blank" rel="noreferrer noopener">Open on GitHub ↗</a>}
    </div>
  )
}
