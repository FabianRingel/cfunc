// SPDX-License-Identifier: Apache-2.0

import { useEffect, useState } from 'react'
import { Header } from './components/Header'
import { FunctionsTable } from './components/FunctionsTable'
import { LayersPanel } from './components/LayersPanel'
import { LogPane } from './components/LogPane'
import { Login } from './components/Login'
import { Overview } from './components/Overview'
import { useDashboardSocket } from './useDashboardSocket'
import { useMetricsHistory } from './useMetricsHistory'
import { probeAuth } from './auth'

type AuthState = 'probing' | 'unauth' | 'ok'

export function App() {
  const [authState, setAuthState] = useState<AuthState>('probing')

  useEffect(() => {
    let cancelled = false
    probeAuth().then((res) => {
      if (cancelled) return
      setAuthState(res === 'ok' ? 'ok' : res === 'unauth' ? 'unauth' : 'unauth')
    })
    return () => {
      cancelled = true
    }
  }, [])

  if (authState === 'probing') {
    return (
      <div className="min-h-screen flex items-center justify-center text-[var(--color-muted)]">
        loading…
      </div>
    )
  }
  if (authState === 'unauth') {
    return <Login onAuthed={() => setAuthState('ok')} />
  }
  return <Authed />
}

function Authed() {
  const { state, logs, connected, clearLogs } = useDashboardSocket()
  const history = useMetricsHistory(state)

  return (
    <div className="flex flex-col min-h-screen">
      <Header state={state} connected={connected} />

      <main className="flex-1 flex flex-col gap-4 p-5 min-h-0">
        <Overview state={state} history={history} />

        <section className="grid gap-4 grid-cols-1 lg:grid-cols-[minmax(0,2fr)_minmax(360px,1fr)] items-start">
          <div className="flex flex-col gap-4 min-w-0">
            <Panel title="Layers (page-cache sharing)">
              <LayersPanel layers={state?.layers ?? []} />
            </Panel>
            <Panel title="Functions">
              <FunctionsTable functions={state?.functions ?? []} />
            </Panel>
          </div>

          <Panel
            title={null}
            className="lg:sticky lg:top-4 self-start h-[calc(100vh-2rem)] min-h-[400px] max-h-[calc(100vh-2rem)]"
          >
            <LogPane logs={logs} onClear={clearLogs} />
          </Panel>
        </section>
      </main>
    </div>
  )
}

function Panel({
  title,
  children,
  className = '',
}: {
  title: string | null
  children: React.ReactNode
  className?: string
}) {
  return (
    <section
      className={`bg-[var(--color-panel)] border border-[var(--color-border)] rounded-lg p-4 flex flex-col min-w-0 min-h-0 ${className}`}
    >
      {title && (
        <h2 className="m-0 mb-3 text-[11px] uppercase tracking-wider text-[var(--color-muted)] font-semibold shrink-0">
          {title}
        </h2>
      )}
      {children}
    </section>
  )
}
