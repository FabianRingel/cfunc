// SPDX-License-Identifier: Apache-2.0

import { useState } from 'react'
import { setToken } from '../auth'

interface Props {
  onAuthed: () => void
  initialError?: string
}

export function Login({ onAuthed, initialError }: Props) {
  const [token, setLocal] = useState('')
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState(initialError ?? '')

  async function submit(e: React.FormEvent) {
    e.preventDefault()
    if (!token.trim()) {
      setErr('Token required')
      return
    }
    setBusy(true)
    setErr('')
    try {
      const r = await fetch('api/state', {
        headers: { Authorization: `Bearer ${token.trim()}` },
        cache: 'no-cache',
      })
      if (r.status === 401) {
        setErr('Token rejected')
        return
      }
      if (!r.ok) {
        setErr(`Server returned ${r.status}`)
        return
      }
      setToken(token.trim())
      onAuthed()
    } catch (e) {
      setErr(String(e))
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="min-h-screen flex items-center justify-center p-6">
      <form
        onSubmit={submit}
        className="w-full max-w-md bg-[var(--color-panel)] border border-[var(--color-border)] rounded-lg p-6"
      >
        <h1 className="m-0 text-[18px] font-semibold tracking-wider text-[var(--color-accent)]">
          cfunc
        </h1>
        <p className="text-[13px] text-[var(--color-muted)] mt-2 mb-5">
          This admin port requires a token. Paste it below.
        </p>

        <label className="block text-[10px] uppercase tracking-wider text-[var(--color-muted)] mb-1">
          Admin token
        </label>
        <input
          type="password"
          value={token}
          onChange={(e) => setLocal(e.target.value)}
          autoFocus
          autoComplete="off"
          className="w-full bg-[var(--color-panel-2)] border border-[var(--color-border)] rounded px-3 py-2 text-sm font-mono focus:outline-none focus:border-[var(--color-accent)]"
        />

        {err && (
          <div className="mt-3 text-[12px] text-[var(--color-bad)]">{err}</div>
        )}

        <button
          type="submit"
          disabled={busy}
          className="mt-5 w-full bg-[var(--color-accent)] text-[#0b0d12] rounded py-2 text-sm font-semibold disabled:opacity-50"
        >
          {busy ? 'verifying…' : 'unlock'}
        </button>

        <p className="mt-4 text-[11px] text-[var(--color-muted)]">
          The token is held in <code>sessionStorage</code> and cleared
          when this tab closes.
        </p>
      </form>
    </div>
  )
}
