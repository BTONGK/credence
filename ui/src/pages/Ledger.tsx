import { useMemo, useState } from 'react'
import { api } from '../api'
import { Link } from '../components/Link'
import { cx, money, parseAgent, ratio, short, useAsync, useDebounced, useLive, who } from '../lib'
import type { AgentConfig, Stat } from '../types'

const GROUPS = [
  { key: 'agent', label: 'Agent config' },
  { key: 'model', label: 'Model' },
  { key: 'harness', label: 'Harness' },
  { key: 'repo', label: 'Repo' },
]

export function Ledger() {
  const [by, setBy] = useState('agent')
  const rep = useAsync(() => api.reputation(by), [by])
  const entries = useAsync(() => api.ledgerEntries(200), [])
  const [verify, setVerify] = useState<Awaited<ReturnType<typeof api.ledgerVerify>> | null>(null)
  const [verifying, setVerifying] = useState(false)
  const [err, setErr] = useState('')
  const refresh = useDebounced(() => {
    rep.reload()
    entries.reload()
  }, 500)
  useLive((m) => {
    if (m.kind === 'report' || m.kind === 'ledger') refresh()
  })

  const cfgs = useMemo(() => new Map((rep.data?.agent_configs ?? []).map((c) => [c.id, c])), [rep.data])
  const stats = rep.data?.stats ?? []
  const es = [...(entries.data?.entries ?? [])].reverse()

  return (
    <div className="page ledger">
      <div className="page-head">
        <div>
          <h1>Ledger</h1>
          <p className="muted">
            Reputation is computed from claims and your reviews. Every review is a signed, hash-chained ledger entry you can export and verify.
          </p>
        </div>
        <div className="page-actions">
          <button className="btn" disabled={verifying} onClick={async () => {
            setVerifying(true)
            try {
              setVerify(await api.ledgerVerify())
            } catch (e) {
              setErr((e as Error).message)
            } finally {
              setVerifying(false)
            }
          }}>{verifying ? 'Verifying…' : 'Verify chain'}</button>
          <button className="btn" onClick={() => api.download('json').catch((e: Error) => setErr(e.message))}>Export JSON</button>
          <button className="btn" onClick={() => api.download('csv').catch((e: Error) => setErr(e.message))}>Export CSV</button>
        </div>
      </div>
      {err && <div className="banner banner-bad">{err}</div>}
      {verify && (
        <div className={cx('banner', verify.ok ? 'banner-good' : 'banner-bad')}>
          {verify.ok
            ? verify.entries
              ? `✓ ${verify.entries} entries verify: hashes link and every signature checks out with key ${verify.key_id}. Head ${short(verify.head, 19)}.`
              : `✓ No entries yet. Entries are written when you review claims. Signing key ${verify.key_id}.`
            : `✗ ${verify.problems.length} problems: ${verify.problems.slice(0, 3).map((p) => `#${p.seq} ${p.reason}`).join('; ')}`}
        </div>
      )}

      <section className="card">
        <div className="section-bar">
          <h2>Reputation</h2>
          <div className="segmented" role="tablist" aria-label="Group by">
            {GROUPS.map((g) => (
              <button key={g.key} role="tab" aria-selected={by === g.key} className={cx('seg', by === g.key && 'active')} onClick={() => setBy(g.key)}>{g.label}</button>
            ))}
          </div>
        </div>
        {rep.error && <div className="form-error">{rep.error.message}</div>}
        {stats.length === 0 && rep.data && <div className="empty">No claims yet.</div>}
        {stats.length > 0 && (
          <div className="table-wrap">
            <table className="table">
              <thead>
                <tr>
                  <th>{GROUPS.find((g) => g.key === by)?.label}</th>
                  <th className="num">Sessions</th>
                  <th className="num">Claims</th>
                  <th>Verified</th>
                  <th className="num">Contradicted</th>
                  <th className="num" title="Accepted or edited / disputed or rejected">Reviews</th>
                  <th className="num" title="Brier score of the agent’s stated confidence against outcomes; lower is better">Calibration</th>
                  <th className="num">Value</th>
                  <th className="num">Cost</th>
                  <th className="num">Value / $</th>
                </tr>
              </thead>
              <tbody>
                {stats.map((s) => <StatRow key={s.key} s={s} by={by} cfg={cfgs.get(s.key)} />)}
              </tbody>
            </table>
          </div>
        )}
        <p className="muted small">
          Value weights each claim by type, verification status, your review and whether the code survived 7 and 30 days.
          Calibration compares the confidence agents state when filing a claim with what verification and review found.
        </p>
      </section>

      <section className="card">
        <div className="section-bar">
          <h2>Entries</h2>
          <span className="muted small">{entries.data ? `${entries.data.entries.length} most recent` : ''}</span>
        </div>
        {es.length === 0 && entries.data && <div className="empty">No entries yet. Review a session’s claims to write the first one.</div>}
        {es.length > 0 && (
          <div className="table-wrap">
            <table className="table entries">
              <thead>
                <tr>
                  <th className="num">#</th>
                  <th>Claim</th>
                  <th>Decision</th>
                  <th>Status</th>
                  <th>Credit</th>
                  <th>Hash</th>
                  <th>When</th>
                </tr>
              </thead>
              <tbody>
                {es.map((e) => {
                  const title = e.human_decision?.edited_title || e.claim?.title || e.claim_id
                  return (
                    <tr key={e.seq}>
                      <td className="num mono">{e.seq}</td>
                      <td>
                        <Link to={`/claims/${e.claim_id}`}>{title}</Link>
                        <div className="muted small">{e.claim?.type?.replace('_', ' ')}{e.commits?.length ? ` · ${e.commits.map((c) => short(c)).join(', ')}` : ''}</div>
                      </td>
                      <td>
                        {e.kind === 'survival' ? `survival ${e.survival?.window}: ${e.survival?.result}` : e.human_decision?.decision}
                        {e.human_decision?.reason ? <span className="muted"> · {e.human_decision.reason.replace(/_/g, ' ')}</span> : null}
                        {e.human_decision?.by ? <div className="muted small">{who(e.human_decision.by)}</div> : null}
                      </td>
                      <td>{e.status}</td>
                      <td className="small">{e.attribution?.shares.map((s) => `${who(s.contributor)} ${Math.round(s.share * 100)}%`).join(' · ')}</td>
                      <td className="mono small" title={`${e.entry_hash}\nsigned ${e.signature ? e.signature.split(':').slice(0, 2).join(':') : '—'}`}>
                        {short(e.entry_hash.replace(/^sha256:/, ''), 10)}
                      </td>
                      <td className="small muted">{new Date(e.created_at).toLocaleString()}</td>
                    </tr>
                  )
                })}
              </tbody>
            </table>
          </div>
        )}
      </section>
    </div>
  )
}

function StatRow({ s, by, cfg }: { s: Stat; by: string; cfg?: AgentConfig }) {
  const verified = s.verified + s.corroborated
  const a = by === 'agent' ? parseAgent(s.key) : null
  return (
    <tr>
      <td>
        {a ? (
          <>
            <div><span className="mono">{a.cfg || 'unknown config'}</span></div>
            <div className="muted small">{a.harness} · {a.model}</div>
          </>
        ) : (
          <>
            <div>{by === 'agent' && cfg ? `${cfg.harness}/${cfg.model}` : s.key}</div>
            {by !== 'harness' && by !== 'model' && s.harness && <div className="muted small">{s.harness}{s.model ? ` · ${s.model}` : ''}</div>}
          </>
        )}
      </td>
      <td className="num">{s.sessions}</td>
      <td className="num">
        {s.claims}
        {s.found_by_auditor > 0 && <div className="muted small" title="Claims the auditor found that the agent didn’t make">{s.found_by_auditor} unclaimed</div>}
      </td>
      <td>
        <div className="rate">
          <span className="rate-bar" aria-hidden><span style={{ width: ratio(verified, s.claims) }} /></span>
          <span>{ratio(verified, s.claims)}</span>
        </div>
      </td>
      <td className={cx('num', s.contradicted > 0 && 'tone-bad')}>{ratio(s.contradicted, s.claims)}</td>
      <td className="num">
        <span className="tone-good">{s.accepted + s.edited}</span> / <span className={cx(s.disputed + s.rejected > 0 && 'tone-bad')}>{s.disputed + s.rejected}</span>
      </td>
      <td className="num">
        {s.brier_n > 0 ? (
          <>
            {s.brier.toFixed(3)} <span className="muted small">({s.brier_n})</span>
            {s.overconfidence > 0.1 && <div className="small tone-warn">over-confident by {Math.round(s.overconfidence * 100)} pts</div>}
            {s.overconfidence < -0.1 && <div className="small muted">under-confident by {Math.round(-s.overconfidence * 100)} pts</div>}
          </>
        ) : '—'}
      </td>
      <td className="num">{s.value.toFixed(1)}</td>
      <td className="num">{s.cost_usd > 0 ? money(s.cost_usd) : '—'}</td>
      <td className="num">{s.cost_usd > 0 ? s.value_per_dollar.toFixed(1) : '—'}</td>
    </tr>
  )
}
