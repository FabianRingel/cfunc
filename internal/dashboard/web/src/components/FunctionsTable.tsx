import { useState } from 'react'
import type { FunctionStats } from '../types'
import { fmtIdle, fmtMs } from '../format'

interface Props {
  functions: FunctionStats[]
}

export function FunctionsTable({ functions }: Props) {
  const [expanded, setExpanded] = useState<string | null>(null)

  if (functions.length === 0) {
    return (
      <div className="text-[var(--color-muted)] italic text-sm">
        No functions registered.
      </div>
    )
  }

  return (
    <div className="overflow-x-auto">
      <table className="w-full text-[12.5px] font-mono border-collapse">
        <thead>
          <tr className="text-left text-[10px] uppercase tracking-wider text-[var(--color-muted)] font-medium">
            <Th>Name</Th>
            <Th>Endpoint</Th>
            <Th>State</Th>
            <Th>Pool</Th>
            <Th>Mode</Th>
            <Th align="right">Cold (ms)</Th>
            <Th align="right">Avg (ms)</Th>
            <Th align="right">Invokes</Th>
            <Th align="right">Errors</Th>
            <Th>Idle</Th>
            <Th>Layers</Th>
          </tr>
        </thead>
        <tbody>
          {functions.map((fn) => {
            const isOpen = expanded === fn.name
            return (
              <FnRow
                key={fn.name}
                fn={fn}
                expanded={isOpen}
                onToggle={() => setExpanded(isOpen ? null : fn.name)}
              />
            )
          })}
        </tbody>
      </table>
    </div>
  )
}

function FnRow({
  fn,
  expanded,
  onToggle,
}: {
  fn: FunctionStats
  expanded: boolean
  onToggle: () => void
}) {
  return (
    <>
      <tr
        onClick={onToggle}
        className="cursor-pointer border-t border-[var(--color-border)] hover:bg-[var(--color-panel-2)] transition-colors"
      >
        <Td>{fn.name}</Td>
        <Td className="text-[var(--color-accent)]">{fn.endpoint}</Td>
        <Td>
          <StateBadge running={fn.running} />
        </Td>
        <Td className="tabular-nums text-[var(--color-muted)]">
          {fn.running ? `${fn.pool_size}/${fn.max_concurrency}` : `0/${fn.max_concurrency}`}
        </Td>
        <Td>{fn.mode || '—'}</Td>
        <Td align="right">{fn.running ? fmtMs(fn.cold_start_ms) : ''}</Td>
        <Td align="right">{fn.running ? fmtMs(fn.avg_duration_ms, 1) : ''}</Td>
        <Td align="right">{fn.invokes}</Td>
        <Td align="right" className={fn.errors > 0 ? 'text-[var(--color-bad)]' : ''}>
          {fn.errors}
        </Td>
        <Td>{fn.running ? fmtIdle(fn.idle_ms) : ''}</Td>
        <Td className="text-[var(--color-muted)]">
          {fn.layers && fn.layers.length > 0 ? `${fn.layers.length} layer(s)` : ''}
        </Td>
      </tr>
      {expanded && (
        <tr className="bg-[var(--color-panel-2)] border-t border-[var(--color-border)]">
          <td colSpan={11} className="px-3 py-3 text-xs">
            <FnDetails fn={fn} />
          </td>
        </tr>
      )}
    </>
  )
}

function FnDetails({ fn }: { fn: FunctionStats }) {
  return (
    <div className="grid grid-cols-2 gap-x-8 gap-y-1.5 max-w-3xl">
      <KV k="Binary"      v={fn.binary} />
      <KV k="Started at"  v={fn.started_at ?? '—'} />
      <KV k="Last used"   v={fn.last_used_at ?? '—'} />
      <KV k="Mode"        v={fn.mode ?? '—'} />
      {fn.layers && fn.layers.length > 0 && (
        <div className="col-span-2 mt-1.5">
          <div className="text-[10px] uppercase tracking-wider text-[var(--color-muted)] mb-1">
            Layers
          </div>
          <table className="w-full text-[11px]">
            <thead>
              <tr className="text-[var(--color-muted)] text-left">
                <th className="font-normal pr-4">name</th>
                <th className="font-normal pr-4">mount</th>
                <th className="font-normal">host path</th>
              </tr>
            </thead>
            <tbody>
              {fn.layers.map((l, i) => (
                <tr key={i}>
                  <td className="pr-4">{l.name}</td>
                  <td className="pr-4 text-[var(--color-accent)]">{l.mount_path}</td>
                  <td className="text-[var(--color-muted)] truncate max-w-md">
                    {l.host_path}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  )
}

function StateBadge({ running }: { running: boolean }) {
  if (running) {
    return (
      <span className="inline-block px-2 py-0.5 rounded-full text-[10px] font-semibold bg-[rgba(81,207,102,0.12)] text-[var(--color-good)]">
        warm
      </span>
    )
  }
  return (
    <span className="inline-block px-2 py-0.5 rounded-full text-[10px] font-semibold bg-[var(--color-panel-2)] text-[var(--color-muted)] border border-[var(--color-border)]">
      cold
    </span>
  )
}

function Th({
  children,
  align = 'left',
}: { children: React.ReactNode; align?: 'left' | 'right' }) {
  return (
    <th
      className={`px-3 py-2 ${align === 'right' ? 'text-right' : 'text-left'} font-medium`}
    >
      {children}
    </th>
  )
}

function Td({
  children,
  className = '',
  align = 'left',
}: {
  children: React.ReactNode
  className?: string
  align?: 'left' | 'right'
}) {
  return (
    <td
      className={`px-3 py-2 whitespace-nowrap ${
        align === 'right' ? 'text-right tabular-nums' : ''
      } ${className}`}
    >
      {children}
    </td>
  )
}

function KV({ k, v }: { k: string; v: string }) {
  return (
    <div className="flex gap-3">
      <span className="text-[var(--color-muted)] w-24 shrink-0">{k}</span>
      <span className="text-[var(--color-text)] truncate">{v}</span>
    </div>
  )
}
