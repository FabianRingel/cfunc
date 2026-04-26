// SPDX-License-Identifier: Apache-2.0

// Mirrors internal/gateway/stats.go and internal/dashboard/log_handler.go.

export interface LayerMount {
  name: string
  host_path: string
  mount_path: string
}

export interface FunctionStats {
  name: string
  endpoint: string
  binary: string
  layers?: LayerMount[]
  running: boolean
  pool_size: number
  max_concurrency: number
  mode?: string
  cold_start_ms?: number
  started_at?: string
  last_used_at?: string
  idle_ms?: number
  invokes: number
  errors: number
  avg_duration_ms?: number
}

export interface LayerSummary {
  name: string
  host_path: string
  mount_path: string
  ref_count: number
  warm_refs: number
  references: string[]
}

export interface State {
  started_at: string
  now: string
  idle_ttl_ms: number
  functions: FunctionStats[]
  layers: LayerSummary[]
}

export interface LogEvent {
  time: string
  level: string
  message: string
  attrs?: Record<string, unknown>
}

export type WSMessage =
  | { type: 'state'; data: State }
  | { type: 'log'; data: LogEvent }
  | { type: 'hello'; data: { backlog: LogEvent[] } }
