import { useEffect, useRef, useState } from 'react'
import type { LogEvent, State, WSMessage } from './types'
import { getToken } from './auth'

// Picks the WS URL from the current page so the bundle works whichever
// prefix the gateway mounts the dashboard at (default /_/). The browser
// WebSocket constructor cannot set custom headers, so we pass the token
// via query string — the server accepts both forms.
function wsURL(): string {
  const proto = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
  const path = window.location.pathname.replace(/[^/]*$/, '')
  const tok = getToken()
  const q = tok ? `?token=${encodeURIComponent(tok)}` : ''
  return `${proto}//${window.location.host}${path}ws${q}`
}

export interface DashboardConn {
  state: State | null
  logs: LogEvent[]
  connected: boolean
  clearLogs: () => void
}

const MAX_LOGS = 2000

export function useDashboardSocket(): DashboardConn {
  const [state, setState] = useState<State | null>(null)
  const [logs, setLogs] = useState<LogEvent[]>([])
  const [connected, setConnected] = useState(false)
  const wsRef = useRef<WebSocket | null>(null)
  const retryRef = useRef<number | null>(null)

  useEffect(() => {
    let cancelled = false

    const connect = () => {
      if (cancelled) return
      const ws = new WebSocket(wsURL())
      wsRef.current = ws

      ws.onopen = () => setConnected(true)
      ws.onclose = () => {
        setConnected(false)
        retryRef.current = window.setTimeout(connect, 1500)
      }
      ws.onerror = () => ws.close()
      ws.onmessage = (msg) => {
        try {
          const m = JSON.parse(msg.data) as WSMessage
          if (m.type === 'state') {
            setState(m.data)
          } else if (m.type === 'log') {
            setLogs((prev) => {
              const next = prev.length >= MAX_LOGS ? prev.slice(-MAX_LOGS + 1) : prev
              return [...next, m.data]
            })
          } else if (m.type === 'hello') {
            setLogs(m.data.backlog.slice(-MAX_LOGS))
          }
        } catch {
          /* ignore */
        }
      }
    }
    connect()

    return () => {
      cancelled = true
      if (retryRef.current) window.clearTimeout(retryRef.current)
      wsRef.current?.close()
    }
  }, [])

  return {
    state,
    logs,
    connected,
    clearLogs: () => setLogs([]),
  }
}
