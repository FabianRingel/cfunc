// Token storage + helpers for authenticated dashboard access.
// Uses sessionStorage (cleared when tab closes) so a stolen token can't
// linger across browser restarts.

const KEY = 'cfunc.token'

export function getToken(): string | null {
  return sessionStorage.getItem(KEY)
}

export function setToken(t: string): void {
  sessionStorage.setItem(KEY, t)
}

export function clearToken(): void {
  sessionStorage.removeItem(KEY)
}

export function authHeaders(): Record<string, string> {
  const t = getToken()
  return t ? { Authorization: `Bearer ${t}` } : {}
}

export async function probeAuth(): Promise<'ok' | 'unauth' | 'down'> {
  try {
    const r = await fetch('api/state', { headers: authHeaders(), cache: 'no-cache' })
    if (r.status === 401) return 'unauth'
    if (!r.ok) return 'down'
    return 'ok'
  } catch {
    return 'down'
  }
}
