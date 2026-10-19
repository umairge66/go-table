// Saving files the way a desktop app does. In the GoTable window the Go
// side shows the system "Save As" dialog and writes the file itself; in a
// browser the File System Access API (Chrome/Edge) is used, with a download
// as the last fallback.

import { nativeDialogs } from './native'

export type FileKind = 'sql' | 'csv' | 'json' | 'zip' | 'sqlite'

const TYPES: Record<FileKind, { description: string; accept: Record<string, string[]> }> = {
  sql: { description: 'SQL file', accept: { 'application/sql': ['.sql'] } },
  csv: { description: 'CSV file', accept: { 'text/csv': ['.csv'] } },
  json: { description: 'JSON file', accept: { 'application/json': ['.json'] } },
  zip: { description: 'ZIP archive', accept: { 'application/zip': ['.zip'] } },
  sqlite: { description: 'SQLite database', accept: { 'application/vnd.sqlite3': ['.sqlite', '.db'] } },
}

interface WritableFile { write(data: Blob | string): Promise<void>; close(): Promise<void>; abort(): Promise<void> }
interface FileHandle { name: string; createWritable(): Promise<WritableFile & WritableStream> }
type SavePicker = (o: { suggestedName: string; types: (typeof TYPES)[FileKind][]; id?: string }) => Promise<FileHandle>

/** A place chosen by the user (native handle or browser file handle), or a name for a download. */
export type SaveTarget = { native: string; name: string } | { handle: FileHandle; name: string } | { download: string; name: string }

async function nativeCall<T>(path: string, init: RequestInit): Promise<T> {
  const res = await fetch(path, { credentials: 'same-origin', ...init })
  const body = await res.json().catch(() => ({}))
  if (!res.ok) throw new Error(body?.error?.title ?? body?.error?.detail ?? res.statusText)
  return body as T
}

const picker = () => (window as unknown as { showSaveFilePicker?: SavePicker }).showSaveFilePicker


/**
 * chooseSaveLocation opens the system Save As dialog. It must be called
 * from a click. Returns null when the user cancels.
 */
export async function chooseSaveLocation(suggestedName: string, kind: FileKind): Promise<SaveTarget | null> {
  if (await nativeDialogs()) {
    const r = await nativeCall<{ handle?: string; name?: string; cancelled?: boolean }>('/api/dialog/save', {
      method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ name: suggestedName, kind }),
    })
    return r.cancelled || !r.handle ? null : { native: r.handle, name: r.name ?? suggestedName }
  }
  const show = picker()
  if (!show) return { download: suggestedName, name: suggestedName }
  try {
    const handle = await show({ suggestedName, types: [TYPES[kind]], id: 'gotable-export' })
    return { handle, name: handle.name }
  } catch (e) {
    if ((e as Error).name === 'AbortError') return null
    throw e
  }
}

/** saveJob writes a finished export to the target. */
export async function saveJob(target: SaveTarget, jobId: string): Promise<void> {
  if ('native' in target) {
    await nativeCall(`/api/jobs/${jobId}/save`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ handle: target.native }) })
    return
  }
  const url = jobDownloadURL(jobId)
  if ('download' in target) {
    const a = document.createElement('a')
    a.href = url
    a.download = target.download
    a.click()
    return
  }
  const res = await fetch(url, { credentials: 'same-origin' })
  if (!res.ok || !res.body) throw new Error(`could not read the export (${res.status})`)
  const w = await target.handle.createWritable()
  await res.body.pipeTo(w) // closes the file when done
}

/** saveText writes text produced in the browser to the target. */
export async function saveText(target: SaveTarget, text: string, type = 'text/plain'): Promise<void> {
  if ('native' in target) {
    await nativeCall(`/api/files?handle=${encodeURIComponent(target.native)}`, { method: 'POST', body: new Blob([text], { type }) })
    return
  }
  if ('download' in target) {
    const a = document.createElement('a')
    a.href = URL.createObjectURL(new Blob([text], { type }))
    a.download = target.download
    a.click()
    setTimeout(() => URL.revokeObjectURL(a.href), 5000)
    return
  }
  const w = await target.handle.createWritable()
  await w.write(new Blob([text], { type }))
  await w.close()
}

/** saveTextAs asks where to save, then writes; false if the user cancelled. */
export async function saveTextAs(suggestedName: string, kind: FileKind, text: string, type?: string): Promise<SaveTarget | null> {
  const target = await chooseSaveLocation(suggestedName, kind)
  if (target) await saveText(target, text, type)
  return target
}

export const jobDownloadURL = (jobId: string) => `/api/jobs/${jobId}/download`

export function timestamp(d = new Date()) {
  const p = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}-${p(d.getHours())}${p(d.getMinutes())}`
}
