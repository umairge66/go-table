// The query editor on InfluxDB 3: SQL statements, and blocks of line protocol
// after a line that says WRITE (internal/db/influx.go cuts a script the same way).

import { pickStatement, trimRange } from './mongoShell'

/** lineEnd: where a line of line protocol ends; strings in fields may hold newlines. */
function lineEnd(s: string, i: number): number {
  if (s[i] === '#') { const j = s.indexOf('\n', i); return j < 0 ? s.length : j + 1 }
  let fields = false
  let str = false
  for (; i < s.length; i++) {
    const ch = s[i]
    if (ch === '\\' && i + 1 < s.length && s[i + 1] !== '\n') i++
    else if (str) str = ch !== '"'
    else if (ch === '\n') return i + 1
    else if (ch === ' ') fields = true
    else if (fields && ch === '"' && s[i - 1] === '=') str = true
  }
  return s.length
}

/** writeBlocks: the ranges of the WRITE blocks. */
export function writeBlocks(text: string): [number, number][] {
  const out: [number, number][] = []
  const re = /^[ \t]*WRITE[ \t]*\r?\n/gim
  for (let m = re.exec(text); m; m = re.exec(text)) {
    let i = m.index + m[0].length
    while (i < text.length) {
      const end = lineEnd(text, i)
      if (!text.slice(i, end).trim()) break
      i = end
    }
    out.push(trimRange(text, [m.index, i]))
    re.lastIndex = Math.max(i, re.lastIndex)
  }
  return out
}

/** influxStatementRange: what Run takes at the cursor: a WRITE block or an SQL statement (which sql finds). */
export function influxStatementRange(text: string, pos: number, sql: (text: string, pos: number) => [number, number] | null): [number, number] | null {
  const blocks = writeBlocks(text)
  const hit = blocks.length ? pickStatement(text, blocks, pos) : null
  if (hit && pos >= hit[0] && pos <= hit[1]) return hit
  // SQL: the stretch between the blocks that the cursor is in.
  let from = 0
  let to = text.length
  for (const [a, b] of blocks) {
    if (b <= pos) from = b
    else if (a >= pos) { to = a; break }
  }
  const r = sql(text.slice(from, to), pos - from)
  return r ? [r[0] + from, r[1] + from] : null
}
