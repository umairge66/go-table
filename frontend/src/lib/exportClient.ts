import type { Cell } from '../types'

/** Client-side export of rows already shown (query results). */

export function toCSV(columns: string[], rows: Cell[][], delimiter = ','): string {
  const field = (v: Cell) => {
    if (v === null) return 'NULL'
    return /[",\r\n]/.test(v) || v.includes(delimiter) || v !== v.trim() ? `"${v.replace(/"/g, '""')}"` : v
  }
  return [columns, ...rows].map((r) => r.map(field).join(delimiter)).join('\n') + '\n'
}

export function toJSON(columns: string[], rows: Cell[][]): string {
  return JSON.stringify(rows.map((r) => Object.fromEntries(columns.map((c, i) => [c, r[i]]))), null, 2) + '\n'
}

export function toInserts(table: string, columns: string[], rows: Cell[][]): string {
  const lit = (v: Cell) => (v === null ? 'NULL' : `'${v.replace(/'/g, "''")}'`)
  return rows.map((r) => `INSERT INTO ${table} (${columns.join(', ')}) VALUES (${r.map(lit).join(', ')});`).join('\n') + '\n'
}
