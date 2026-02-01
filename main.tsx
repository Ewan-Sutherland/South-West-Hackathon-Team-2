import React, { useEffect, useMemo, useState } from 'react'
import ReactDOM from 'react-dom/client'
import { NimChat } from '@liminalcash/nim-chat'
import '@liminalcash/nim-chat/styles.css'
import './styles.css'

type StateResp = {
  wallet_balance: number
  savings_balance: number
  today: string
  lead_days: number
  loaded: boolean
}

type AnyObj = any

function formatGBP(x: number | null | undefined) {
  const n = Number(x)
  if (!Number.isFinite(n)) return '£0.00'
  return '£' + n.toFixed(2)
}

function clampStr(s: string, n: number) {
  if (!s) return ''
  return s.length > n ? s.slice(0, n - 1) + '…' : s
}

function safeGet(obj: any, path: (string | number)[], fallback: any) {
  try {
    let cur = obj
    for (const k of path) {
      if (cur == null) return fallback
      cur = cur[k as any]
    }
    return cur === undefined ? fallback : cur
  } catch {
    return fallback
  }
}

function Card({
  title,
  children,
}: {
  title: string
  children: React.ReactNode
}) {
  return (
    <div style={styles.card}>
      <div style={styles.cardTitle}>{title}</div>
      {children}
    </div>
  )
}

function MiniList({
  items,
  leftKey,
  rightKey,
  leftFallbackKey,
  rightFallbackKey,
  rightFormat,
}: {
  items: any[]
  leftKey: string
  rightKey: string
  leftFallbackKey?: string
  rightFallbackKey?: string
  rightFormat?: (x: any) => string
}) {
  const rows = Array.isArray(items) ? items : []
  if (!rows.length) return <div style={styles.muted}>—</div>

  return (
    <div style={{ display: 'grid', gap: 10 }}>
      {rows.map((r, i) => {
        const left =
          r && r[leftKey] !== undefined
            ? String(r[leftKey])
            : leftFallbackKey && r && r[leftFallbackKey] !== undefined
              ? String(r[leftFallbackKey])
              : '—'

        const rawRight =
          r && r[rightKey] !== undefined
            ? r[rightKey]
            : rightFallbackKey && r && r[rightFallbackKey] !== undefined
              ? r[rightFallbackKey]
              : null

        const rightStr =
          rightFormat ? rightFormat(rawRight) : formatGBP(Number(rawRight))

        return (
          <div key={i} style={{ display: 'flex', justifyContent: 'space-between', gap: 12 }}>
            <div style={{ fontSize: 14, color: '#111' }}>{clampStr(left, 40)}</div>
            <div style={{ fontSize: 13, color: '#6b7280' }}>
              {rawRight === null || rawRight === undefined ? '—' : rightStr}
            </div>
          </div>
        )
      })}
    </div>
  )
}

function App() {
  const wsUrl = import.meta.env.VITE_WS_URL || 'ws://localhost:8080/ws'
  const apiUrl = import.meta.env.VITE_API_URL || 'https://api.liminal.cash'

  const stateUrl = import.meta.env.VITE_STATE_URL || 'http://localhost:8090/state.json'
  const incomeUrl = import.meta.env.VITE_INCOME_URL || 'http://localhost:8090/income.json'
  const billsUrl = import.meta.env.VITE_BILLS_URL || 'http://localhost:8090/bills.json'
  const dashUrl = import.meta.env.VITE_DASHBOARD_JSON_URL || 'http://localhost:8090/dashboard.json'
  const allocatorUrl = import.meta.env.VITE_ALLOCATOR_JSON_URL || 'http://localhost:8090/allocator.json'

  const [page, setPage] = useState<'home' | 'insights' | 'allocator'>('home')

  // Start with 0/0
  const [state, setState] = useState<StateResp>({
    wallet_balance: 0,
    savings_balance: 0,
    today: '',
    lead_days: 3,
    loaded: false,
  })

  const [income, setIncome] = useState<AnyObj | null>(null)
  const [bills, setBills] = useState<AnyObj | null>(null)
  const [dash, setDash] = useState<AnyObj | null>(null)
  const [alloc, setAlloc] = useState<AnyObj | null>(null)

  // Send modal
  const [sendOpen, setSendOpen] = useState(false)
  const [to, setTo] = useState('@alice')
  const [amount, setAmount] = useState('300')
  const [copied, setCopied] = useState<string | null>(null)

  const amtNum = Number(amount)
  const validAmount = Number.isFinite(amtNum) && amtNum > 0
  const validTo = to.trim().length > 0
  const sendCmd = useMemo(() => {
    const a = validAmount ? amtNum : 300
    return `demo_send_money to ${to.trim()} amount ${a}`
  }, [amtNum, to, validAmount])

  async function copyText(text: string) {
    try {
      await navigator.clipboard.writeText(text)
      setCopied('Copied!')
      setTimeout(() => setCopied(null), 1200)
    } catch {
      setCopied('Copy blocked — manually copy command')
      setTimeout(() => setCopied(null), 2500)
    }
  }

  async function loadState() {
    try {
      const res = await fetch(stateUrl)
      if (!res.ok) return
      const json = (await res.json()) as StateResp
      setState({
        wallet_balance: Number(json.wallet_balance ?? 0),
        savings_balance: Number(json.savings_balance ?? 0),
        today: String(json.today ?? ''),
        lead_days: Number(json.lead_days ?? 3),
        loaded: Boolean(json.loaded),
      })
    } catch {
      // keep last
    }
  }

  async function loadIncome() {
    try {
      const res = await fetch(incomeUrl)
      if (!res.ok) return
      setIncome(await res.json())
    } catch {
      setIncome(null)
    }
  }

  async function loadBills() {
    try {
      const res = await fetch(billsUrl)
      if (!res.ok) return
      setBills(await res.json())
    } catch {
      setBills(null)
    }
  }

  async function loadDash() {
    try {
      const res = await fetch(dashUrl)
      if (!res.ok) return
      setDash(await res.json())
    } catch {
      setDash(null)
    }
  }

  async function loadAlloc() {
    try {
      const res = await fetch(allocatorUrl)
      if (!res.ok) return
      setAlloc(await res.json())
    } catch {
      setAlloc(null)
    }
  }

  // Poll balances always; poll analytics only when loaded
  useEffect(() => {
    loadState()
    const id = window.setInterval(loadState, 1200)

    const id2 = window.setInterval(() => {
      if (state.loaded) {
        loadIncome()
        loadBills()
        loadDash()
        loadAlloc()
      }
    }, 2500)

    return () => {
      window.clearInterval(id)
      window.clearInterval(id2)
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [state.loaded])

  return (
    <>
      <main style={styles.pageWrap}>
        {page === 'home' ? (
          <HomePage
            state={state}
            onOpenSend={() => setSendOpen(true)}
            onGoInsights={() => setPage('insights')}
            onGoAllocator={() => setPage('allocator')}
          />
        ) : page === 'insights' ? (
          <InsightsPage
            state={state}
            income={income}
            bills={bills}
            dash={dash}
            onBack={() => setPage('home')}
          />
        ) : (
          <AllocatorPage
            alloc={alloc}
            loaded={state.loaded}
            onBack={() => setPage('home')}
            onOpenBackend={() => window.open('http://localhost:8090/allocator', '_blank', 'noreferrer')}
          />
        )}
      </main>

      {/* SEND MODAL */}
      {sendOpen ? (
        <div style={styles.modalBackdrop} onMouseDown={() => setSendOpen(false)}>
          <div style={styles.modalCard} onMouseDown={(e) => e.stopPropagation()}>
            <div style={{ display: 'flex', justifyContent: 'space-between', gap: 10, alignItems: 'center' }}>
              <div>
                <div style={{ fontSize: 16, fontWeight: 800 }}>Send money (demo)</div>
                <div style={styles.muted}>
                  Generates a Nim command — guardrails still run before funds move.
                </div>
              </div>
              <button style={styles.xBtn} onClick={() => setSendOpen(false)}>✕</button>
            </div>

            <div style={{ display: 'grid', gap: 10, marginTop: 14 }}>
              <label style={styles.label}>Recipient</label>
              <input value={to} onChange={(e) => setTo(e.target.value)} style={styles.input} placeholder="@alice" />

              <label style={styles.label}>Amount (GBP)</label>
              <input value={amount} onChange={(e) => setAmount(e.target.value)} style={styles.input} placeholder="300" inputMode="decimal" />

              <button
                style={{ ...styles.primaryBtn, width: '100%', opacity: (state.loaded && validTo && validAmount) ? 1 : 0.55 }}
                disabled={!(state.loaded && validTo && validAmount)}
                onClick={() => copyText(sendCmd)}
              >
                Copy guardrail command
              </button>

              <div style={{ marginTop: 6, fontSize: 13, color: '#111' }}>
                Command:
                <div style={styles.codeBox}>{sendCmd}</div>
              </div>

              {copied ? <div style={{ fontSize: 13, color: '#111' }}>{copied}</div> : null}

              {!state.loaded ? (
                <div style={{ fontSize: 13, color: '#b45309', lineHeight: 1.5 }}>
                  Demo not loaded yet. Run <code>ready_for_demo</code> in Nim chat first.
                </div>
              ) : null}
            </div>
          </div>
        </div>
      ) : null}

      <NimChat
        wsUrl={wsUrl}
        apiUrl={apiUrl}
        title="Nim"
        position="bottom-right"
        defaultOpen={false}
      />
    </>
  )
}

function HomePage({
  state,
  onOpenSend,
  onGoInsights,
  onGoAllocator,
}: {
  state: StateResp
  onOpenSend: () => void
  onGoInsights: () => void
  onGoAllocator: () => void
}) {
  return (
    <>
      <header style={styles.headerCentered}>
        <h1 style={styles.title}>Pinnacle Solutions</h1>
      </header>

      <div style={{ height: 18 }} />

      <section style={styles.balancesRow}>
        <div style={{ ...styles.card, textAlign: 'center', minWidth: 320 }}>
          <div style={styles.cardTitle}>Bank balance</div>
          <div style={styles.balanceAmount}>{formatGBP(state.wallet_balance)}</div>
          <div style={styles.muted}>{state.today ? `As of ${state.today}` : '—'}</div>
        </div>

        <div style={{ ...styles.card, textAlign: 'center', minWidth: 320 }}>
          <div style={styles.cardTitle}>Savings balance</div>
          <div style={styles.balanceAmount}>{formatGBP(state.savings_balance)}</div>
          <div style={styles.muted}>Lead window: {state.lead_days} days</div>
        </div>
      </section>

      <section style={styles.grid3}>
        <aside>
          <Card title="Income insights">
            <div style={styles.muted}>
              {state.loaded ? 'Auto-populates on Insights.' : <>Run <code>ready_for_demo</code> in Nim chat.</>}
            </div>
          </Card>
        </aside>

        <section>
          <div style={{ ...styles.card, textAlign: 'center' }}>
            <div style={styles.cardTitle}>Payments</div>

            <button style={{ ...styles.primaryBtn, width: '100%', marginTop: 14 }} onClick={onOpenSend}>
              Send money
            </button>

            <button style={{ ...styles.secondaryBtn, width: '100%', marginTop: 10 }} onClick={onGoInsights}>
              View insights
            </button>

            <button
              style={{ ...styles.secondaryBtn, width: '100%', marginTop: 10 }}
              onClick={() => window.open('http://localhost:8090/dashboard', '_blank', 'noreferrer')}
            >
              Open backend dashboard
            </button>

            <button style={{ ...styles.secondaryBtn, width: '100%', marginTop: 10 }} onClick={onGoAllocator}>
              Crypto allocator
            </button>

            <div style={{ marginTop: 12, ...styles.muted }}>
              Nim chat is bottom-right.
            </div>
          </div>
        </section>

        <aside>
          <Card title="Expense insights">
            <div style={styles.muted}>
              {state.loaded ? 'Auto-populates on Insights.' : <>Run <code>ready_for_demo</code> in Nim chat.</>}
            </div>
          </Card>
        </aside>
      </section>
    </>
  )
}

function InsightsPage({
  state,
  income,
  bills,
  dash,
  onBack,
}: {
  state: StateResp
  income: AnyObj | null
  bills: AnyObj | null
  dash: AnyObj | null
  onBack: () => void
}) {
  const incomeTotal = income ? safeGet(income, ['income_total'], safeGet(income, ['total_income'], null)) : null
  const incomeSources = income ? safeGet(income, ['top_sources'], safeGet(income, ['sources'], [])) : []

  const upcomingBills = bills ? safeGet(bills, ['upcoming_bills'], []) : []
  const recurringBills = bills ? safeGet(bills, ['recurring_bills'], safeGet(bills, ['bills'], [])) : []

  const dashCtx = dash ? safeGet(dash, ['context'], null) : null
  const analytics = dash ? safeGet(dash, ['analytics'], dash) : null

  const topCats = analytics ? safeGet(analytics, ['rankings', 'top_categories'], []) : []
  const topMerchants = analytics ? safeGet(analytics, ['rankings', 'top_merchants'], []) : []
  const cuts = analytics ? safeGet(analytics, ['spending_cuts'], []) : []

  return (
    <>
      <header style={styles.headerRow}>
        <h2 style={styles.pageTitle}>Insights</h2>
        <div style={{ display: 'flex', gap: 10, flexWrap: 'wrap' }}>
          <button style={styles.secondaryBtn} onClick={onBack}>Back</button>
          <button
            style={styles.secondaryBtn}
            onClick={() => window.open('http://localhost:8090/dashboard', '_blank', 'noreferrer')}
          >
            Open backend dashboard
          </button>
        </div>
      </header>

      {!state.loaded ? (
        <div style={{ ...styles.card, marginTop: 14 }}>
          <div style={styles.cardTitle}>Demo not loaded</div>
          <div style={styles.muted}>
            Run <code>ready_for_demo</code> in Nim chat, then this page will auto-populate.
          </div>
        </div>
      ) : null}

      <section style={styles.grid2}>
        <Card title="Income summary">
          <div style={{ fontSize: 14, color: '#111', marginBottom: 10 }}>
            Total income: <b>{incomeTotal !== null ? formatGBP(Number(incomeTotal)) : '—'}</b>
          </div>
          <div style={styles.muted}>Top income sources</div>
          <div style={{ height: 10 }} />
          <MiniList
            items={(incomeSources || []).slice(0, 10)}
            leftKey="source"
            rightKey="total"
            leftFallbackKey="merchant"
            rightFallbackKey="amount"
          />
        </Card>

        <Card title="Upcoming bills (lead window)">
          <div style={styles.muted}>Lead window: {state.lead_days} days</div>
          <div style={{ height: 10 }} />
          <MiniList
            items={(upcomingBills || []).slice(0, 10)}
            leftKey="name"
            rightKey="amount"
            leftFallbackKey="merchant"
            rightFallbackKey="monthly_amount"
          />
        </Card>

        <Card title="Recurring bills">
          <MiniList
            items={(recurringBills || []).slice(0, 12)}
            leftKey="name"
            rightKey="monthly_amount"
            leftFallbackKey="merchant"
            rightFallbackKey="total"
          />
        </Card>

        <Card title="Spending cuts (hints)">
          <MiniList
            items={(cuts || []).slice(0, 12)}
            leftKey="category"
            rightKey="monthly_saving_hint"
            rightFallbackKey="total_6mo"
          />
        </Card>

        <Card title="Top categories">
          <MiniList items={(topCats || []).slice(0, 12)} leftKey="category" rightKey="total" />
        </Card>

        <Card title="Top merchants">
          <MiniList items={(topMerchants || []).slice(0, 12)} leftKey="merchant" rightKey="total" />
        </Card>
      </section>

      {dashCtx ? (
        <div style={{ marginTop: 18, ...styles.muted }}>
          Context: bank={formatGBP(Number(safeGet(dashCtx, ['bank_balance'], 0)))} savings={formatGBP(Number(safeGet(dashCtx, ['savings_balance'], 0)))}
        </div>
      ) : null}
    </>
  )
}

function AllocatorPage({
  alloc,
  loaded,
  onBack,
  onOpenBackend,
}: {
  alloc: AnyObj | null
  loaded: boolean
  onBack: () => void
  onOpenBackend: () => void
}) {
  const status = alloc ? safeGet(alloc, ['status'], '') : ''
  const err = alloc ? safeGet(alloc, ['error'], null) : null

  const finalWeightsObj = alloc ? safeGet(alloc, ['final_weights'], {}) : {}
  const finalWeights = Object.entries(finalWeightsObj || {})
    .map(([k, v]) => [k, Number(v)] as const)
    .filter((x) => Number.isFinite(x[1]))
    .sort((a, b) => b[1] - a[1])

  const port = alloc ? safeGet(alloc, ['portfolio'], {}) : {}
  const volBefore = safeGet(port, ['vol_before'], null)
  const volAfter = safeGet(port, ['vol_after'], null)
  const cashW = safeGet(port, ['cash_weight'], null)

  return (
    <>
      <header style={styles.headerRow}>
        <h2 style={styles.pageTitle}>Crypto Allocator</h2>
        <div style={{ display: 'flex', gap: 10, flexWrap: 'wrap' }}>
          <button style={styles.secondaryBtn} onClick={onBack}>Back</button>
          <button style={styles.secondaryBtn} onClick={onOpenBackend}>Open backend page</button>
        </div>
      </header>

      {!loaded ? (
        <div style={{ ...styles.card, marginTop: 14 }}>
          <div style={styles.cardTitle}>Note</div>
          <div style={styles.muted}>
            The allocator can run without demo data, but your backend may be set to CSV mode.
          </div>
        </div>
      ) : null}

      {status && status !== 'ok' ? (
        <div style={{ ...styles.card, marginTop: 14 }}>
          <div style={styles.cardTitle}>Allocator error</div>
          <div style={{ ...styles.muted, color: '#b91c1c' }}>{String(err || 'Unknown error')}</div>
        </div>
      ) : null}

      <section style={styles.grid2}>
        <Card title="Portfolio">
          <div style={{ display: 'grid', gap: 8 }}>
            <div><span style={styles.muted}>Vol before:</span> <b>{Number.isFinite(Number(volBefore)) ? Number(volBefore).toFixed(4) : '—'}</b></div>
            <div><span style={styles.muted}>Vol after:</span> <b>{Number.isFinite(Number(volAfter)) ? Number(volAfter).toFixed(4) : '—'}</b></div>
            <div><span style={styles.muted}>Cash weight:</span> <b>{Number.isFinite(Number(cashW)) ? Number(cashW).toFixed(4) : '—'}</b></div>
          </div>
        </Card>

        <Card title="Final weights (incl CASH)">
          {finalWeights.length ? (
            <div style={{ display: 'grid', gap: 10 }}>
              {finalWeights.slice(0, 16).map(([k, v]) => (
                <div key={k} style={{ display: 'flex', justifyContent: 'space-between', gap: 12 }}>
                  <div style={{ fontSize: 14 }}>{k}</div>
                  <div style={{ fontSize: 13, color: '#111' }}>{v.toFixed(4)}</div>
                </div>
              ))}
            </div>
          ) : (
            <div style={styles.muted}>—</div>
          )}
        </Card>
      </section>

      <div style={{ marginTop: 14, ...styles.muted }}>
        Tip: open <code>http://localhost:8090/allocator</code> for the full backend view.
      </div>
    </>
  )
}

/* ----------------- styles ----------------- */

const styles: Record<string, React.CSSProperties> = {
  pageWrap: {
    width: '100%',
    maxWidth: 1200,
    margin: '0 auto',
    padding: '26px 18px 90px',
  },
  headerCentered: {
    display: 'flex',
    justifyContent: 'center',
  },
  title: {
    margin: 0,
    textAlign: 'center',
    fontSize: 44,
    letterSpacing: '-0.02em',
  },
  pageTitle: {
    margin: 0,
    fontSize: 28,
    letterSpacing: '-0.02em',
  },
  headerRow: {
    display: 'flex',
    justifyContent: 'space-between',
    alignItems: 'center',
    gap: 12,
    flexWrap: 'wrap',
    marginBottom: 14,
  },
  balancesRow: {
    display: 'flex',
    justifyContent: 'center',
    gap: 18,
    flexWrap: 'wrap',
    marginTop: 12,
    marginBottom: 18,
  },
  balanceAmount: {
    fontSize: 34,
    fontWeight: 850,
    marginTop: 8,
    marginBottom: 6,
  },
  grid3: {
    display: 'grid',
    gridTemplateColumns: '1fr minmax(360px, 420px) 1fr',
    gap: 18,
    alignItems: 'start',
    width: '100%',
  },
  grid2: {
    display: 'grid',
    gridTemplateColumns: '1fr 1fr',
    gap: 18,
    width: '100%',
    marginTop: 14,
  },
  card: {
    border: '1px solid #e5e7eb',
    borderRadius: 18,
    padding: 16,
    background: '#fff',
    boxShadow: '0 1px 2px rgba(0,0,0,0.04)',
  },
  cardTitle: {
    fontSize: 14,
    color: '#374151',
    marginBottom: 10,
  },
  muted: {
    fontSize: 13,
    color: '#6b7280',
    lineHeight: 1.5,
  },
  label: {
    fontSize: 13,
    color: '#374151',
  },
  input: {
    padding: '10px 12px',
    borderRadius: 12,
    border: '1px solid #e5e7eb',
    fontSize: 14,
    width: '100%',
  },
  primaryBtn: {
    padding: '12px 16px',
    borderRadius: 12,
    border: '1px solid #e5e7eb',
    background: '#111',
    color: '#fff',
    cursor: 'pointer',
    fontSize: 14,
  },
  secondaryBtn: {
    padding: '12px 16px',
    borderRadius: 12,
    border: '1px solid #e5e7eb',
    background: '#fff',
    color: '#111',
    cursor: 'pointer',
    fontSize: 14,
  },
  modalBackdrop: {
    position: 'fixed',
    inset: 0,
    background: 'rgba(0,0,0,0.25)',
    display: 'flex',
    alignItems: 'center',
    justifyContent: 'center',
    padding: 16,
    zIndex: 50,
  },
  modalCard: {
    width: 'min(640px, 100%)',
    background: '#fff',
    borderRadius: 18,
    border: '1px solid #e5e7eb',
    boxShadow: '0 10px 30px rgba(0,0,0,0.18)',
    padding: 16,
  },
  xBtn: {
    border: '1px solid #e5e7eb',
    background: '#fff',
    borderRadius: 10,
    cursor: 'pointer',
    padding: '6px 10px',
    fontSize: 13,
  },
  codeBox: {
    marginTop: 6,
    background: '#f3f4f6',
    padding: 10,
    borderRadius: 12,
    fontFamily:
      'ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, "Liberation Mono", "Courier New", monospace',
    fontSize: 12,
    whiteSpace: 'pre-wrap',
  },
}

ReactDOM.createRoot(document.getElementById('root')!).render(<App />)
