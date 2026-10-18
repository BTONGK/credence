import { useCallback, useEffect, useRef, useState } from 'react'
import { api } from './api'

// ---- routing ----

export function navigate(to: string, replace = false) {
  if (to === location.pathname + location.search) return
  if (replace) history.replaceState(null, '', to)
  else history.pushState(null, '', to)
  window.dispatchEvent(new PopStateEvent('popstate'))
}

export function useLocation(): { path: string; query: URLSearchParams } {
  const [loc, setLoc] = useState(() => location.pathname + location.search)
  useEffect(() => {
    const f = () => setLoc(location.pathname + location.search)
    window.addEventListener('popstate', f)
    return () => window.removeEventListener('popstate', f)
  }, [])
  const u = new URL(loc, location.origin)
  return { path: u.pathname, query: u.searchParams }
}

// ---- live updates ----

export interface LiveMessage {
  kind: 'session' | 'report' | 'ledger' | string
  id: string
}

const listeners = new Set<(m: LiveMessage) => void>()
const stateListeners = new Set<(live: boolean) => void>()
let source: EventSource | null = null
let live = false

function setLive(v: boolean) {
  live = v
  stateListeners.forEach((f) => f(v))
}

export function connectLive() {
  if (source) return
  source = new EventSource(api.streamURL())
  source.onopen = () => setLive(true)
  source.onerror = () => setLive(false)
  source.onmessage = (e) => {
    try {
      const m = JSON.parse(e.data) as LiveMessage
      listeners.forEach((f) => f(m))
    } catch {
      /* ignore malformed */
    }
  }
}

export function useLive(fn: (m: LiveMessage) => void) {
  const ref = useRef(fn)
  ref.current = fn
  useEffect(() => {
    const f = (m: LiveMessage) => ref.current(m)
    listeners.add(f)
    return () => {
      listeners.delete(f)
    }
  }, [])
}

export function useLiveState(): boolean {
  const [v, setV] = useState(live)
  useEffect(() => {
    stateListeners.add(setV)
    return () => {
      stateListeners.delete(setV)
    }
  }, [])
  return v
}

// ---- data loading ----

export function useAsync<T>(fn: () => Promise<T>, deps: unknown[]) {
  const [data, setData] = useState<T | null>(null)
  const [error, setError] = useState<Error | null>(null)
  const [loading, setLoading] = useState(true)
  const seq = useRef(0)
  // eslint-disable-next-line react-hooks/exhaustive-deps
  const run = useCallback(fn, deps)
  const reload = useCallback(() => {
    const n = ++seq.current
    setLoading(true)
    run()
      .then((d) => {
        if (n === seq.current) {
          setData(d)
          setError(null)
        }
      })
      .catch((e: Error) => n === seq.current && setError(e))
      .finally(() => n === seq.current && setLoading(false))
  }, [run])
  useEffect(() => {
    reload()
  }, [reload])
  return { data, error, loading, reload, setData }
}

// Debounce rapid live updates into one reload.
export function useDebounced(fn: () => void, ms: number) {
  const t = useRef<number>()
  const ref = useRef(fn)
  ref.current = fn
  useEffect(() => () => window.clearTimeout(t.current), [])
  return useCallback(() => {
    window.clearTimeout(t.current)
    t.current = window.setTimeout(() => ref.current(), ms)
  }, [ms])
}

// ---- keyboard ----

export function isTyping(e: KeyboardEvent): boolean {
  const el = e.target as HTMLElement | null
  if (!el) return false
  const tag = el.tagName
  return tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT' || el.isContentEditable
}

export function useKeys(handler: (e: KeyboardEvent) => void, enabled = true) {
  const ref = useRef(handler)
  ref.current = handler
  useEffect(() => {
    if (!enabled) return
    const f = (e: KeyboardEvent) => {
      if (e.metaKey || e.ctrlKey || e.altKey) return
      ref.current(e)
    }
    window.addEventListener('keydown', f)
    return () => window.removeEventListener('keydown', f)
  }, [enabled])
}

// ---- formatting ----

export function ago(iso: string | undefined): string {
  if (!iso) return ''
  const t = new Date(iso).getTime()
  if (!t || t < 0) return ''
  const s = Math.max(0, (Date.now() - t) / 1000)
  if (s < 45) return 'just now'
  if (s < 3600) return `${Math.round(s / 60)}m ago`
  if (s < 86400) return `${Math.round(s / 3600)}h ago`
  if (s < 7 * 86400) return `${Math.round(s / 86400)}d ago`
  return new Date(iso).toLocaleDateString(undefined, { month: 'short', day: 'numeric' })
}

export function dur(sec: number): string {
  if (!sec || sec < 1) return '—'
  if (sec < 60) return `${Math.round(sec)}s`
  const m = Math.round(sec / 60)
  if (m < 60) return `${m}m`
  return `${Math.floor(m / 60)}h ${m % 60}m`
}

export function clock(iso: string, start: string): string {
  const d = Math.max(0, (new Date(iso).getTime() - new Date(start).getTime()) / 1000)
  if (!isFinite(d)) return ''
  const h = Math.floor(d / 3600)
  const m = Math.floor((d % 3600) / 60)
  const s = Math.floor(d % 60)
  const ss = String(s).padStart(2, '0')
  return h ? `${h}:${String(m).padStart(2, '0')}:${ss}` : `${m}:${ss}`
}

// "Oct 17" this year, "Oct 17, 2025" otherwise.
export function day(iso: string | undefined): string {
  if (!iso) return ''
  const d = new Date(iso)
  if (!d.getTime()) return ''
  const sameYear = d.getFullYear() === new Date().getFullYear()
  return d.toLocaleDateString(undefined, sameYear ? { month: 'short', day: 'numeric' } : { month: 'short', day: 'numeric', year: 'numeric' })
}

export function money(usd: number): string {
  if (!usd) return '$0'
  return usd < 10 ? `$${usd.toFixed(2)}` : `$${Math.round(usd)}`
}

export function pct(x: number | undefined): string {
  if (x === undefined || x === null || !isFinite(x)) return '—'
  return `${Math.round(x * 100)}%`
}

export function ratio(a: number, b: number): string {
  return b ? `${Math.round((a * 100) / b)}%` : '—'
}

export function short(sha: string | undefined, n = 7): string {
  return sha ? sha.slice(0, n) : ''
}

export function cap(s: string): string {
  return s ? s[0].toUpperCase() + s.slice(1) : s
}

// Mirrors view.ShortContributor.
export function who(id: string | undefined): string {
  if (!id) return ''
  if (id.startsWith('human:')) return cap(id.slice(6)) || 'human'
  if (id.startsWith('agent:')) return 'agent'
  if (id.startsWith('subagent:')) return 'subagent ' + id.split('/').pop()
  if (id.startsWith('auditor:')) return 'auditor'
  if (id.startsWith('tool:')) return 'tool'
  if (id.startsWith('system:')) return 'system'
  return id
}

// agent:claude-code/claude-opus-5-5@cfg_82c2 -> { harness, model, cfg }
export function parseAgent(id: string): { harness: string; model: string; cfg: string } | null {
  const m = id.match(/^agent:([^/]+)\/([^@]+)(?:@(.+))?$/)
  return m ? { harness: m[1], model: m[2], cfg: m[3] ?? '' } : null
}

export function typeLabel(t: string): string {
  return t === 'negative_result' ? 'negative result' : t
}

export function plural(n: number, w: string): string {
  return `${n} ${w}${n === 1 ? '' : 's'}`
}

export function cx(...xs: (string | false | null | undefined)[]): string {
  return xs.filter(Boolean).join(' ')
}
