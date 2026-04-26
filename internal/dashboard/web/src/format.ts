export function fmtMs(n: number | undefined | null, digits = 0): string {
  if (n == null) return '—'
  return Number(n).toFixed(digits)
}

export function fmtIdle(ms: number | undefined | null): string {
  if (ms == null) return '—'
  if (ms < 1000) return `${ms}ms`
  if (ms < 60_000) return `${Math.floor(ms / 1000)}s`
  if (ms < 3_600_000) return `${Math.floor(ms / 60_000)}m${Math.floor((ms % 60_000) / 1000)}s`
  return `${Math.floor(ms / 3_600_000)}h${Math.floor((ms % 3_600_000) / 60_000)}m`
}

export function fmtTime(iso: string | undefined | null): string {
  if (!iso) return '—'
  const d = new Date(iso)
  return d.toLocaleTimeString()
}

export function fmtUptime(startedAt: string, now: string): string {
  const ms = Date.parse(now) - Date.parse(startedAt)
  return fmtIdle(ms)
}
