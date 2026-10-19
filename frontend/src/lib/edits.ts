import type { ChangeReq } from '../api'
import type { Cell } from '../types'

export type CellStatus = 'clean' | 'edited' | 'deleted' | 'inserted'

/** Pending edits of a table page. Immutable: every operation returns a new value. */
export interface Edits {
  updates: Map<number, Map<number, Cell>> // row -> col -> new value
  deleted: Set<number>
  inserted: { values: Cell[]; set: Set<number> }[] // set = columns the user filled
}

export const emptyEdits = (): Edits => ({ updates: new Map(), deleted: new Set(), inserted: [] })

export const pendingCount = (e: Edits) => e.updates.size + e.deleted.size + e.inserted.length

export const rowCount = (rows: Cell[][], e: Edits) => rows.length + e.inserted.length

export function cellAt(rows: Cell[][], e: Edits, r: number, c: number): { value: Cell; status: CellStatus } {
  if (r >= rows.length) return { value: e.inserted[r - rows.length]?.values[c] ?? null, status: 'inserted' }
  if (e.deleted.has(r)) return { value: rows[r][c], status: 'deleted' }
  const u = e.updates.get(r)
  if (u?.has(c)) return { value: u.get(c)!, status: 'edited' }
  return { value: rows[r][c], status: 'clean' }
}

export const rowStatus = (rows: Cell[][], e: Edits, r: number): CellStatus =>
  r >= rows.length ? 'inserted' : e.deleted.has(r) ? 'deleted' : e.updates.has(r) ? 'edited' : 'clean'

/** setCell records an edit; setting the original value back removes it. */
export function setCell(rows: Cell[][], e: Edits, r: number, c: number, v: Cell): Edits {
  if (r >= rows.length) {
    const i = r - rows.length
    if (!e.inserted[i]) return e
    const inserted = e.inserted.slice()
    const values = inserted[i].values.slice()
    values[c] = v
    inserted[i] = { values, set: new Set(inserted[i].set).add(c) }
    return { ...e, inserted }
  }
  if (e.deleted.has(r)) return e
  const updates = new Map(e.updates)
  const row = new Map(updates.get(r))
  if (rows[r][c] === v) row.delete(c)
  else row.set(c, v)
  if (row.size) updates.set(r, row)
  else updates.delete(r)
  return { ...e, updates }
}

export function addRow(e: Edits, ncols: number): Edits {
  return { ...e, inserted: [...e.inserted, { values: Array(ncols).fill(null), set: new Set() }] }
}

/** toggleDelete marks or unmarks rows; new (inserted) rows are dropped. */
export function toggleDelete(rows: Cell[][], e: Edits, rs: number[]): Edits {
  const deleted = new Set(e.deleted)
  const updates = new Map(e.updates)
  const drop = new Set<number>()
  for (const r of rs) {
    if (r >= rows.length) drop.add(r - rows.length)
    else if (deleted.has(r)) deleted.delete(r)
    else { deleted.add(r); updates.delete(r) }
  }
  return { updates, deleted, inserted: e.inserted.filter((_, i) => !drop.has(i)) }
}

/** toChanges converts edits into API change requests: updates, deletes, inserts. */
export function toChanges(rows: Cell[][], e: Edits, columns: string[], pk: number[]): ChangeReq[] {
  const keyCols = pk.length ? pk : columns.map((_, i) => i)
  const key = (r: number) => Object.fromEntries(keyCols.map((i) => [columns[i], rows[r][i]]))
  const out: ChangeReq[] = []
  for (const r of [...e.updates.keys()].sort((a, b) => a - b)) {
    const values = Object.fromEntries([...e.updates.get(r)!].map(([c, v]) => [columns[c], v]))
    out.push({ kind: 'update', key: key(r), values })
  }
  for (const r of [...e.deleted].sort((a, b) => a - b)) out.push({ kind: 'delete', key: key(r) })
  for (const ins of e.inserted) {
    out.push({ kind: 'insert', values: Object.fromEntries([...ins.set].sort().map((c) => [columns[c], ins.values[c]])) })
  }
  return out
}
