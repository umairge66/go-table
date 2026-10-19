// Whether the page runs in the GoTable desktop window, which offers the
// operating system's file dialogs through the Go side.

export interface NativeInfo {
  /** dialogs: the system file dialogs are available. */
  dialogs: boolean
  /** window: the window has no system title bar; the page draws its own. */
  window: boolean
}

let cached: Promise<NativeInfo> | null = null

export function nativeInfo(): Promise<NativeInfo> {
  cached ??= fetch('/api/native', { credentials: 'same-origin' })
    .then((r) => (r.ok ? r.json() : {}))
    .then((j: Partial<NativeInfo>) => ({ dialogs: !!j.dialogs, window: !!j.window }))
    .catch(() => ({ dialogs: false, window: false }))
  return cached
}

export const nativeDialogs = () => nativeInfo().then((i) => i.dialogs)

/** windowAction moves, resizes, maximizes, minimizes or closes the window. */
export function windowAction(action: string) {
  return fetch('/api/native/window', {
    method: 'POST', credentials: 'same-origin', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ action }),
  }).catch(() => undefined)
}

/** openFileDialog shows the system "Open" dialog; null when cancelled. */
export async function openFileDialog(kinds: string[]): Promise<{ path: string; name: string; size: number } | null> {
  const res = await fetch('/api/dialog/open', {
    method: 'POST', credentials: 'same-origin', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ kinds }),
  })
  const body = await res.json().catch(() => ({}))
  if (!res.ok) throw new Error(body?.error?.title ?? res.statusText)
  return body.cancelled ? null : body
}
