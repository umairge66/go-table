import type { Driver } from '../types'

// Which editor a cell gets, from its column's type: a calendar for dates,
// time fields for times, a list for enums and booleans, checkboxes for sets.

export type CellKind =
  | { kind: 'text' }
  | { kind: 'date' }
  | { kind: 'datetime' }
  | { kind: 'time' }
  | { kind: 'choice'; options: { value: string; label: string }[] }
  | { kind: 'set'; options: string[] }

export function cellKind(type = '', values: string[] | undefined, driver: Driver): CellKind {
  const t = type.trim().toLowerCase()
  if (values?.length) {
    return t.startsWith('set(') ? { kind: 'set', options: values } : { kind: 'choice', options: values.map((v) => ({ value: v, label: v === '' ? '(empty)' : v })) }
  }
  // PostgreSQL reads booleans as true/false; MySQL's BOOLEAN is TINYINT(1) and SQLite stores 1/0.
  if (driver === 'postgres' && (t === 'boolean' || t === 'bool')) return { kind: 'choice', options: [{ value: 'true', label: 'true' }, { value: 'false', label: 'false' }] }
  if (/^(tinyint\(1\)|bool|boolean)(\s+unsigned)?$/.test(t)) return { kind: 'choice', options: [{ value: '1', label: '1 (true)' }, { value: '0', label: '0 (false)' }] }
  if (t === 'date') return { kind: 'date' }
  if (/^(datetime|timestamptz|timestamp|smalldatetime)\b/.test(t)) return { kind: 'datetime' }
  if (/^(timetz|time)\b/.test(t)) return { kind: 'time' }
  return { kind: 'text' }
}

/** Parts of a date, date-time or time value as the server shows it. */
export interface DateParts { y: number; m: number; d: number; h: number; mi: number; s: number; frac: string; tz: string }

const DATE_RE = /^(\d{4})-(\d{2})-(\d{2})(?:[ T](\d{2}):(\d{2})(?::(\d{2}))?(\.\d+)?)?\s*(Z|[+-]\d{2}(?::?\d{2})?)?$/
const TIME_RE = /^(\d{1,2}):(\d{2})(?::(\d{2}))?(\.\d+)?\s*(Z|[+-]\d{2}(?::?\d{2})?)?$/

export function parseDate(v: string | null): DateParts | null {
  const m = DATE_RE.exec((v ?? '').trim())
  if (!m) return null
  const p = { y: +m[1], m: +m[2], d: +m[3], h: +(m[4] ?? 0), mi: +(m[5] ?? 0), s: +(m[6] ?? 0), frac: m[7] ?? '', tz: m[8] ?? '' }
  return p.m >= 1 && p.m <= 12 && p.d >= 1 && p.d <= 31 ? p : null
}

export function parseTime(v: string | null): Pick<DateParts, 'h' | 'mi' | 's' | 'frac' | 'tz'> | null {
  const m = TIME_RE.exec((v ?? '').trim())
  return m ? { h: +m[1], mi: +m[2], s: +(m[3] ?? 0), frac: m[4] ?? '', tz: m[5] ?? '' } : null
}

const p2 = (n: number) => String(n).padStart(2, '0')
export const fmtDate = (p: Pick<DateParts, 'y' | 'm' | 'd'>) => `${String(p.y).padStart(4, '0')}-${p2(p.m)}-${p2(p.d)}`
export const fmtTime = (p: Pick<DateParts, 'h' | 'mi' | 's' | 'frac'>) => `${p2(p.h)}:${p2(p.mi)}:${p2(p.s)}${p.frac}`
export const fmtDateTime = (p: DateParts) => `${fmtDate(p)} ${fmtTime(p)}${p.tz}`

export function nowParts(): DateParts {
  const n = new Date()
  return { y: n.getFullYear(), m: n.getMonth() + 1, d: n.getDate(), h: n.getHours(), mi: n.getMinutes(), s: n.getSeconds(), frac: '', tz: '' }
}

/** setMembers returns the members of a SET value ("a,b") as a list. */
export const setMembers = (v: string | null) => (v ? v.split(',') : [])
