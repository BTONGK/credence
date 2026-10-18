import { useEffect, useRef } from 'react'

const groups: { title: string; keys: [string, string][] }[] = [
  {
    title: 'Everywhere',
    keys: [
      ['g i', 'Go to sessions'],
      ['g l', 'Go to ledger'],
      ['?', 'Show or hide shortcuts'],
      ['Esc', 'Close panel or cancel'],
    ],
  },
  {
    title: 'Sessions',
    keys: [
      ['j / k', 'Next / previous session'],
      ['Enter', 'Open session'],
      ['/', 'Search'],
      ['n', 'Toggle “needs review”'],
    ],
  },
  {
    title: 'Credit report',
    keys: [
      ['j / k', 'Next / previous claim'],
      ['a', 'Accept claim'],
      ['e', 'Edit claim'],
      ['d', 'Dispute, then 1–5 for the reason'],
      ['r', 'Reject claim'],
      ['s', 'Split credit'],
      ['v', 'Re-run verification'],
      ['o', 'Open first evidence'],
      ['Shift A', 'Accept all verified claims'],
      ['u', 'Back to sessions'],
    ],
  },
]

export function Help({ onClose }: { onClose: () => void }) {
  const ref = useRef<HTMLDivElement>(null)
  useEffect(() => {
    ref.current?.focus()
  }, [])
  return (
    <div className="overlay" onClick={onClose} role="presentation">
      <div
        className="help"
        role="dialog"
        aria-modal="true"
        aria-label="Keyboard shortcuts"
        tabIndex={-1}
        ref={ref}
        onClick={(e) => e.stopPropagation()}
        onKeyDown={(e) => e.key === 'Escape' && onClose()}
      >
        <div className="help-head">
          <h2>Keyboard shortcuts</h2>
          <button className="icon-btn" onClick={onClose} aria-label="Close">×</button>
        </div>
        <div className="help-grid">
          {groups.map((g) => (
            <section key={g.title}>
              <h3>{g.title}</h3>
              <dl>
                {g.keys.map(([k, v]) => (
                  <div className="help-row" key={k}>
                    <dt>{k.split(' ').map((p) => <kbd key={p}>{p}</kbd>)}</dt>
                    <dd>{v}</dd>
                  </div>
                ))}
              </dl>
            </section>
          ))}
        </div>
      </div>
    </div>
  )
}
