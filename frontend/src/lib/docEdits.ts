import type { ChangeReq } from '../api'
import { compactJSON, parseJSON } from './json'
import type { Doc } from '../types'

/** Pending edits of a page of documents. Immutable, like those of rows (edits.ts). */
export interface DocEdits {
  updates: Map<number, string>
  deleted: Set<number>
  inserted: { key: number; json: string }[]
}

export const emptyDocEdits = (): DocEdits => ({ updates: new Map(), deleted: new Set(), inserted: [] })
export const docPending = (e: DocEdits) => e.updates.size + e.deleted.size + e.inserted.length

const compact = (s: string) => { try { return compactJSON(parseJSON(s)) } catch { return s } }

export function setDoc(docs: Doc[], e: DocEdits, i: number, json: string): DocEdits {
  if (i >= docs.length) return { ...e, inserted: e.inserted.map((x, k) => (k === i - docs.length ? { ...x, json } : x)) }
  const updates = new Map(e.updates)
  if (compact(json) === compact(docs[i].json)) updates.delete(i)
  else updates.set(i, json)
  return { ...e, updates }
}

/** toggleDoc marks a document to be deleted, or not; a new one goes at once. */
export function toggleDoc(docs: Doc[], e: DocEdits, i: number): DocEdits {
  if (i >= docs.length) return { ...e, inserted: e.inserted.filter((_, k) => k !== i - docs.length) }
  const deleted = new Set(e.deleted)
  const updates = new Map(e.updates)
  if (deleted.has(i)) deleted.delete(i)
  else { deleted.add(i); updates.delete(i) }
  return { ...e, deleted, updates }
}

let seq = 0
export const addDoc = (e: DocEdits, json: string): DocEdits => ({ ...e, inserted: [...e.inserted, { key: ++seq, json }] })

export function docChanges(docs: Doc[], e: DocEdits): ChangeReq[] {
  return [
    ...[...e.updates].sort((a, b) => a[0] - b[0]).map(([i, json]): ChangeReq => ({ kind: 'update', doc: { id: docs[i].id, json, original: docs[i].json } })),
    ...[...e.deleted].sort((a, b) => a - b).map((i): ChangeReq => ({ kind: 'delete', doc: { id: docs[i].id, json: '', original: docs[i].json } })),
    ...e.inserted.map((x): ChangeReq => ({ kind: 'insert', doc: { id: '', json: x.json, original: '' } })),
  ]
}

/** One document as it is shown: read, edited, deleted or new. */
export interface ShownDoc { key: string; label: string; meta?: string; json: string; readOnly?: string; status: 'clean' | 'edited' | 'deleted' | 'inserted' }

export function shownDocs(docs: Doc[], e: DocEdits): ShownDoc[] {
  return [
    ...docs.map((d, i): ShownDoc => ({ key: d.id || `#${i}`, label: d.label, meta: d.meta, readOnly: d.readOnly, json: e.updates.get(i) ?? d.json,
      status: e.deleted.has(i) ? 'deleted' : e.updates.has(i) ? 'edited' : 'clean' })),
    ...e.inserted.map((x): ShownDoc => ({ key: `new-${x.key}`, label: 'New document', json: x.json, status: 'inserted' })),
  ]
}
