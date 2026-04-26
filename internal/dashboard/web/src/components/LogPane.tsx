// SPDX-License-Identifier: Apache-2.0

import { useEffect, useRef, useState } from 'react'
import type { LogEvent } from '../types'

interface Props {
  logs: LogEvent[]
  onClear: () => void
}

export function LogPane({ logs, onClear }: Props) {
  const [follow, setFollow] = useState(true)
  const [filter, setFilter] = useState('')
  const [level, setLevel] = useState<'ALL' | 'INFO' | 'WARN' | 'ERROR'>('ALL')
  const paneRef = useRef<HTMLDivElement>(null)

  const filtered = logs.filter((ev) => {
    if (level !== 'ALL' && ev.level !== level) return false
    if (filter && !logLineText(ev).toLowerCase().includes(filter.toLowerCase()))
      return false
    return true
  })

  useEffect(() => {
    if (follow && paneRef.current) {
      paneRef.current.scrollTop = paneRef.current.scrollHeight
    }
  }, [filtered.length, follow])

  return (
    <div className="flex flex-col h-full min-h-0">
      <div className="flex items-center gap-3 mb-2 flex-wrap">
        <h2 className="m-0 text-[11px] uppercase tracking-wider text-[var(--color-muted)] font-semibold">
          Live logs
        </h2>
        <span className="text-[11px] text-[var(--color-muted)]">
          {filtered.length}/{logs.length}
        </span>

        <input
          value={filter}
          onChange={(e) => setFilter(e.target.value)}
          placeholder="filter…"
          className="ml-auto bg-[var(--color-panel-2)] border border-[var(--color-border)] rounded px-2 py-1 text-xs w-40 focus:outline-none focus:border-[var(--color-accent)]"
        />

        <select
          value={level}
          onChange={(e) => setLevel(e.target.value as typeof level)}
          className="bg-[var(--color-panel-2)] border border-[var(--color-border)] rounded px-2 py-1 text-xs"
        >
          <option>ALL</option>
          <option>INFO</option>
          <option>WARN</option>
          <option>ERROR</option>
        </select>

        <label className="text-xs text-[var(--color-muted)] flex items-center gap-1.5 select-none">
          <input
            type="checkbox"
            checked={follow}
            onChange={(e) => setFollow(e.target.checked)}
          />
          follow
        </label>

        <button
          onClick={onClear}
          className="bg-[var(--color-panel-2)] border border-[var(--color-border)] rounded px-2 py-1 text-xs hover:border-[var(--color-accent)]"
        >
          clear
        </button>
      </div>

      <div
        ref={paneRef}
        className="flex-1 min-h-[280px] overflow-auto bg-[var(--color-panel-2)] border border-[var(--color-border)] rounded p-2.5 font-mono text-[12px] leading-relaxed"
      >
        {filtered.length === 0 ? (
          <div className="text-[var(--color-muted)] italic">No log entries.</div>
        ) : (
          filtered.map((ev, i) => <LogLine key={i} ev={ev} />)
        )}
      </div>
    </div>
  )
}

function LogLine({ ev }: { ev: LogEvent }) {
  const ts = new Date(ev.time).toLocaleTimeString()
  const color =
    ev.level === 'ERROR'
      ? 'text-[var(--color-bad)]'
      : ev.level === 'WARN'
        ? 'text-[var(--color-warn)]'
        : 'text-[var(--color-text)]'
  const attrs = ev.attrs
    ? Object.entries(ev.attrs)
        .map(([k, v]) => `${k}=${typeof v === 'object' ? JSON.stringify(v) : v}`)
        .join(' ')
    : ''
  return (
    <div className={`whitespace-pre-wrap break-words ${color}`}>
      <span className="text-[var(--color-muted)] mr-2">{ts}</span>
      <span className="font-semibold mr-2">{ev.level}</span>
      {ev.message}
      {attrs && <span className="text-[var(--color-muted)] ml-2">{attrs}</span>}
    </div>
  )
}

function logLineText(ev: LogEvent): string {
  const a = ev.attrs
    ? Object.entries(ev.attrs)
        .map(([k, v]) => `${k}=${typeof v === 'object' ? JSON.stringify(v) : v}`)
        .join(' ')
    : ''
  return `${ev.level} ${ev.message} ${a}`
}
