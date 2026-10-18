import { cx, who } from '../lib'
import type { Event } from '../types'

function s(v: unknown): string {
  if (v === undefined || v === null) return ''
  return typeof v === 'string' ? v : JSON.stringify(v, null, 2)
}

function rel(path: string, cwdHint?: string): string {
  if (cwdHint && path.startsWith(cwdHint + '/')) return path.slice(cwdHint.length + 1)
  return path
}

// One normalized event, rendered by kind.
export function EventView({ ev, compact = false, cwd }: { ev: Event; compact?: boolean; cwd?: string }) {
  const p = ev.payload ?? {}
  let body: JSX.Element | null = null
  switch (ev.kind) {
    case 'prompt':
    case 'message':
    case 'reasoning':
      body = <div className={cx('ev-text', ev.kind === 'reasoning' && 'muted')}>{s(p.text)}</div>
      break
    case 'command':
      body = <pre className="ev-code">$ {s(p.command)}</pre>
      break
    case 'tool_result': {
      const out = s(p.output)
      const exit = p.exit_code as number | undefined
      const outcome = p.test_outcome as { passed?: number; failed?: number } | undefined
      body = (
        <>
          <div className="ev-meta small">
            {p.ok === false || (exit !== undefined && exit !== 0) ? <span className="tone-bad">failed{exit !== undefined ? ` (exit ${exit})` : ''}</span> : <span className="tone-good">ok</span>}
            {outcome && <span> · {outcome.passed ?? 0} passed, {outcome.failed ?? 0} failed</span>}
          </div>
          {out && <pre className={cx('ev-code', 'ev-out', compact && 'clamp')}>{cwd ? out.split(cwd + '/').join('') : out}</pre>}
        </>
      )
      break
    }
    case 'file_edit':
      body = (
        <div className="ev-meta">
          <span className="mono">{rel(s(p.path), cwd)}</span>{' '}
          <span className="tone-good">+{s(p.added) || 0}</span> <span className="tone-bad">−{s(p.removed) || 0}</span>
          <span className="muted"> · {s(p.op) || 'edit'}</span>
        </div>
      )
      break
    case 'file_read':
      body = <div className="ev-meta mono">{rel(s(p.path), cwd)}</div>
      break
    case 'search':
      body = <div className="ev-meta mono">{s(p.pattern || p.query)}{p.path ? ` in ${rel(s(p.path), cwd)}` : ''}</div>
      break
    case 'commit':
      body = <div className="ev-meta"><span className="mono">{s(p.sha).slice(0, 7)}</span> {s(p.subject)}</div>
      break
    case 'claim_filed': {
      const c = (p.input ?? p.claim ?? p) as { title?: string; type?: string; confidence?: number }
      body = <div className="ev-meta">filed {c.type ? `${c.type} ` : ''}claim: “{s(c.title)}”{c.confidence !== undefined ? ` · confidence ${Math.round(c.confidence * 100)}%` : ''}</div>
      break
    }
    case 'subagent_start':
      body = (
        <div className="ev-text">
          <strong>{s(p.description || p.agent_type)}</strong>
          {p.prompt ? <div className="muted">{s(p.prompt).slice(0, 600)}</div> : null}
        </div>
      )
      break
    case 'subagent_stop':
      body = <div className="ev-text">{s(p.summary).slice(0, 800)}</div>
      break
    default:
      body = Object.keys(p).length ? <pre className={cx('ev-code', compact && 'clamp')}>{s(p)}</pre> : null
  }
  return (
    <div className={cx('ev', `ev-${ev.kind}`)}>
      <div className="ev-head small">
        <span className="mono">#{ev.seq}</span>
        <span className="ev-kind">{ev.kind.replace(/_/g, ' ')}</span>
        <span className="muted">{who(ev.actor)}</span>
        <span className="muted">{new Date(ev.ts).toLocaleTimeString()}</span>
      </div>
      {body}
    </div>
  )
}
