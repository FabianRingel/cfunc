import type { State } from '../types'
import { fmtUptime } from '../format'

interface Props {
  state: State | null
  connected: boolean
}

export function Header({ state, connected }: Props) {
  return (
    <header className="flex items-center gap-6 px-6 py-3 border-b border-[var(--color-border)] bg-[var(--color-panel)]">
      <h1 className="m-0 text-[18px] font-semibold tracking-wider text-[var(--color-accent)]">
        cfunc
      </h1>

      <div className="flex items-center gap-2 text-xs font-mono text-[var(--color-muted)]">
        <span
          className={`inline-block w-2 h-2 rounded-full ${
            connected ? 'bg-[var(--color-good)]' : 'bg-[var(--color-bad)]'
          }`}
        />
        {connected ? 'connected' : 'reconnecting…'}
      </div>

      {state && (
        <div className="flex gap-5 ml-auto text-xs font-mono text-[var(--color-muted)]">
          <Stat label="uptime" value={fmtUptime(state.started_at, state.now)} />
          <Stat label="ttl"    value={`${(state.idle_ttl_ms / 1000).toFixed(0)}s`} />
          <Stat label="fns"    value={String(state.functions.length)} />
          <Stat label="warm"   value={String(state.functions.filter(f => f.running).length)} />
        </div>
      )}
    </header>
  )
}

function Stat({ label, value }: { label: string; value: string }) {
  return (
    <span>
      <span className="text-[10px] uppercase tracking-wider opacity-70 mr-1">{label}</span>
      <span className="text-[var(--color-text)]">{value}</span>
    </span>
  )
}
