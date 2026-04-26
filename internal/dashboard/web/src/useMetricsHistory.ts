import { useEffect, useRef, useState } from 'react'
import type { State } from './types'

// MetricPoint is one rollup of the gateway's totals between two state
// pushes. The chart consumes these directly.
export interface MetricPoint {
  t: number          // unix ms
  ts: string         // hh:mm:ss for x-axis labels
  rps: number        // invokes since last sample / dt
  eps: number        // errors since last sample / dt
  warm: number       // count of running functions
  avgMs: number      // mean of per-function avg_duration_ms (warm only)
  totalInvokes: number
  totalErrors: number
}

const MAX_POINTS = 120 // ~2 minutes at 1 Hz

function now(state: State): number {
  return Date.parse(state.now)
}

function totals(state: State): { invokes: number; errors: number; warm: number; avgMs: number } {
  let invokes = 0,
    errors = 0,
    warm = 0,
    sumAvg = 0,
    countAvg = 0
  for (const f of state.functions) {
    invokes += f.invokes || 0
    errors += f.errors || 0
    if (f.running) {
      warm++
      if (f.avg_duration_ms != null) {
        sumAvg += f.avg_duration_ms
        countAvg++
      }
    }
  }
  return {
    invokes,
    errors,
    warm,
    avgMs: countAvg > 0 ? sumAvg / countAvg : 0,
  }
}

export function useMetricsHistory(state: State | null): MetricPoint[] {
  const [points, setPoints] = useState<MetricPoint[]>([])
  const lastRef = useRef<{ t: number; invokes: number; errors: number } | null>(null)

  useEffect(() => {
    if (!state) return
    const t = now(state)
    const tot = totals(state)
    const last = lastRef.current

    let rps = 0
    let eps = 0
    if (last && t > last.t) {
      const dt = (t - last.t) / 1000
      rps = Math.max(0, (tot.invokes - last.invokes) / dt)
      eps = Math.max(0, (tot.errors - last.errors) / dt)
    }

    const point: MetricPoint = {
      t,
      ts: new Date(t).toLocaleTimeString(),
      rps,
      eps,
      warm: tot.warm,
      avgMs: tot.avgMs,
      totalInvokes: tot.invokes,
      totalErrors: tot.errors,
    }

    setPoints((prev) => {
      const next = prev.length >= MAX_POINTS ? prev.slice(-MAX_POINTS + 1) : prev
      return [...next, point]
    })

    lastRef.current = { t, invokes: tot.invokes, errors: tot.errors }
  }, [state])

  return points
}
