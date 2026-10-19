import type {
  Attribution, ClaimCard, Commit, Event, LedgerEntry, Report, SessionItem, Share, Stat, AgentConfig, Status, Summary,
  TestRun, VerifyReport,
} from './types'

const KEY = 'credence.token'
let token = ''

// The daemon hands the UI its token in the URL fragment (never sent to the
// server); keep it for this tab and drop it from the address bar.
export function bootstrapToken(): string {
  const m = location.hash.match(/token=([0-9a-fA-F]+)/)
  if (m) {
    token = m[1]
    try {
      sessionStorage.setItem(KEY, token)
    } catch {
      /* storage unavailable: keep it in memory */
    }
    history.replaceState(null, '', location.pathname + location.search)
    return token
  }
  try {
    token = sessionStorage.getItem(KEY) ?? ''
  } catch {
    token = ''
  }
  return token
}

export function forgetToken() {
  token = ''
  try {
    sessionStorage.removeItem(KEY)
  } catch {
    /* ignore */
  }
}

export class ApiError extends Error {
  status: number
  constructor(status: number, message: string) {
    super(message)
    this.status = status
  }
}

async function req<T>(method: string, path: string, body?: unknown): Promise<T> {
  const headers: Record<string, string> = { Authorization: `Bearer ${token}` }
  if (body !== undefined) headers['Content-Type'] = 'application/json'
  const res = await fetch(path, { method, headers, body: body === undefined ? undefined : JSON.stringify(body) })
  const text = await res.text()
  let data: unknown = null
  try {
    data = text ? JSON.parse(text) : null
  } catch {
    data = text
  }
  if (!res.ok) {
    const msg = data && typeof data === 'object' && 'error' in data ? String((data as { error: string }).error) : res.statusText
    throw new ApiError(res.status, msg)
  }
  return data as T
}

const enc = encodeURIComponent

export interface ReviewRequest {
  decision: 'accepted' | 'edited' | 'disputed' | 'rejected'
  reason?: string
  note?: string
  edited_title?: string
  edited_statement?: string
}

export const api = {
  status: () => req<Status>('GET', '/api/status'),
  sessions: (q: URLSearchParams) => req<{ sessions: SessionItem[] }>('GET', `/api/sessions?${q}`),
  session: (id: string) => req<Report>('GET', `/api/sessions/${enc(id)}`),
  summary: (range: string) => req<Summary>('GET', `/api/summary?range=${enc(range)}`),
  events: (id: string, from: number, to: number) =>
    req<{ events: Event[] }>('GET', `/api/sessions/${enc(id)}/events?from=${from}&to=${to}`),
  rereport: (id: string) => req<unknown>('POST', `/api/sessions/${enc(id)}/report`, {}),
  addClaim: (id: string, c: { type: string; title: string; statement: string; evidence: string[] }) =>
    req<unknown>('POST', `/api/sessions/${enc(id)}/claims`, c),
  reviewTiming: (id: string, ms: number, claims: number) =>
    req<unknown>('POST', `/api/sessions/${enc(id)}/review_timing`, { ms, claims }),
  claim: (id: string) => req<{ card: ClaimCard; ledger: LedgerEntry[] }>('GET', `/api/claims/${enc(id)}`),
  review: (id: string, r: ReviewRequest) =>
    req<{ entry: LedgerEntry; card: ClaimCard }>('POST', `/api/claims/${enc(id)}/review`, r),
  verify: (id: string, rerun: boolean) => req<unknown>('POST', `/api/claims/${enc(id)}/verify`, { rerun }),
  setAttribution: (id: string, shares: Share[]) =>
    req<Attribution>('PUT', `/api/claims/${enc(id)}/attribution`, { shares }),
  testRun: (id: string) => req<TestRun>('GET', `/api/test_runs/${enc(id)}`),
  diff: (session: string, commit: string, path?: string) =>
    req<{ commit: Commit; diff: string }>(
      'GET', `/api/diff?session=${enc(session)}&commit=${enc(commit)}${path ? `&path=${enc(path)}` : ''}`),
  file: (session: string, commit: string, path: string) =>
    req<{ path: string; commit: string; content: string }>(
      'GET', `/api/file?session=${enc(session)}&commit=${enc(commit)}&path=${enc(path)}`),
  reputation: (by: string) => req<{ by: string; stats: Stat[] | null; agent_configs: AgentConfig[] | null }>(
    'GET', `/api/ledger?by=${enc(by)}`),
  ledgerEntries: (limit = 200) => req<{ entries: LedgerEntry[] }>('GET', `/api/ledger/entries?limit=${limit}`),
  ledgerVerify: () => req<VerifyReport>('GET', '/api/ledger/verify'),
  async download(format: 'json' | 'csv') {
    const res = await fetch(`/api/export?format=${format}`, { headers: { Authorization: `Bearer ${token}` } })
    if (!res.ok) throw new ApiError(res.status, res.statusText)
    const url = URL.createObjectURL(await res.blob())
    const a = document.createElement('a')
    a.href = url
    a.download = `credence-ledger.${format}`
    a.click()
    setTimeout(() => URL.revokeObjectURL(url), 1000)
  },
  streamURL: () => `/api/stream?token=${enc(token)}`,
}
