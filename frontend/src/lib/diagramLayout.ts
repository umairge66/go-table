// Placing the tables of an ER diagram: referenced tables on the left, those
// that refer to them to the right, in an order that crosses few lines.

export interface Box { id: string; w: number; h: number }
export interface Link { from: string; to: string }
export type Positions = Record<string, { x: number; y: number }>

const GAP_X = 96
const GAP_Y = 28
const GAP_GROUP = 72
/** A column of a group taller than this goes on beside itself. */
const MAX_H = 2200

export function layout(boxes: Box[], links: Link[]): Positions {
  const byId = new Map(boxes.map((b) => [b.id, b]))
  const real = links.filter((l) => l.from !== l.to && byId.has(l.from) && byId.has(l.to))
  const near = new Map<string, Set<string>>(boxes.map((b) => [b.id, new Set()]))
  for (const l of real) { near.get(l.from)!.add(l.to); near.get(l.to)!.add(l.from) }

  // Groups of tables that hang together; the rest goes last, as a grid.
  const groups: string[][] = []
  const seen = new Set<string>()
  const alone: string[] = []
  for (const b of boxes) {
    if (seen.has(b.id)) continue
    const group: string[] = []
    const todo = [b.id]
    seen.add(b.id)
    while (todo.length) {
      const id = todo.pop()!
      group.push(id)
      for (const n of near.get(id)!) if (!seen.has(n)) { seen.add(n); todo.push(n) }
    }
    if (group.length === 1) alone.push(b.id)
    else groups.push(group)
  }
  groups.sort((a, b) => b.length - a.length)
  const placed = groups.map((g) => placeGroup(g.map((id) => byId.get(id)!), real.filter((l) => g.includes(l.from))))
  if (alone.length) placed.push(placeGrid(alone.map((id) => byId.get(id)!)))

  // Shelves: groups side by side until a row is as wide as the whole is high.
  const area = placed.reduce((s, p) => s + (p.w + GAP_GROUP) * (p.h + GAP_GROUP), 0)
  const width = Math.max(Math.sqrt(area) * 1.5, ...placed.map((p) => p.w))
  const out: Positions = {}
  let x = 0, y = 0, rowH = 0
  for (const p of placed) {
    if (x > 0 && x + p.w > width) { x = 0; y += rowH + GAP_GROUP; rowH = 0 }
    for (const [id, at] of Object.entries(p.at)) out[id] = { x: Math.round(x + at.x), y: Math.round(y + at.y) }
    x += p.w + GAP_GROUP
    rowH = Math.max(rowH, p.h)
  }
  return out
}

interface Placed { w: number; h: number; at: Positions }

function placeGrid(boxes: Box[]): Placed {
  const at: Positions = {}
  const cols = Math.max(1, Math.round(Math.sqrt(boxes.length * 1.6)))
  const w = Math.max(...boxes.map((b) => b.w))
  const heights = Array<number>(cols).fill(0)
  for (const b of [...boxes].sort((p, q) => p.id.localeCompare(q.id))) {
    const c = heights.indexOf(Math.min(...heights))
    at[b.id] = { x: c * (w + GAP_Y * 1.5), y: heights[c] }
    heights[c] += b.h + GAP_Y
  }
  return { w: Math.min(cols, boxes.length) * (w + GAP_Y * 1.5), h: Math.max(...heights), at }
}

function placeGroup(boxes: Box[], links: Link[]): Placed {
  // rank: how many steps a table is from one that refers to nothing.
  const refs = new Map<string, string[]>(boxes.map((b) => [b.id, []]))
  for (const l of links) refs.get(l.from)!.push(l.to)
  const rank = new Map<string, number>()
  const visiting = new Set<string>()
  const rankOf = (id: string): number => {
    const known = rank.get(id)
    if (known !== undefined) return known
    if (visiting.has(id)) return -1 // a circle: this way round doesn't count
    visiting.add(id)
    let r = 0
    for (const to of refs.get(id)!) r = Math.max(r, rankOf(to) + 1)
    visiting.delete(id)
    rank.set(id, r)
    return r
  }
  for (const b of boxes) rankOf(b.id)
  // Tables nothing else depends on sit right beside what they refer to.
  const columns: Box[][] = []
  for (const b of boxes) (columns[rank.get(b.id)!] ??= []).push(b)
  const cols = columns.filter(Boolean)
  for (const c of cols) c.sort((p, q) => p.id.localeCompare(q.id))

  // Order each column by the middle of its neighbours, back and forth.
  const order = new Map<string, number>()
  const note = () => cols.forEach((c) => c.forEach((b, i) => order.set(b.id, (i + 0.5) / c.length)))
  note()
  const near = new Map<string, string[]>(boxes.map((b) => [b.id, []]))
  for (const l of links) { near.get(l.from)!.push(l.to); near.get(l.to)!.push(l.from) }
  for (let round = 0; round < 8; round++) {
    const seq = round % 2 ? [...cols].reverse() : cols
    for (const c of seq) {
      const key = new Map(c.map((b) => {
        const ns = near.get(b.id)!
        return [b.id, ns.length ? ns.reduce((s, n) => s + order.get(n)!, 0) / ns.length : order.get(b.id)!]
      }))
      c.sort((p, q) => key.get(p.id)! - key.get(q.id)! || p.id.localeCompare(q.id))
      note()
    }
  }

  // Columns that are too tall are split in two or more.
  const split: Box[][] = []
  for (const c of cols) {
    let cur: Box[] = [], h = 0
    for (const b of c) {
      if (cur.length && h + b.h > MAX_H) { split.push(cur); cur = []; h = 0 }
      cur.push(b)
      h += b.h + GAP_Y
    }
    split.push(cur)
  }
  const heights = split.map((c) => c.reduce((s, b) => s + b.h + GAP_Y, -GAP_Y))
  const H = Math.max(...heights)
  const at: Positions = {}
  let x = 0
  split.forEach((c, i) => {
    let y = (H - heights[i]) / 2
    for (const b of c) { at[b.id] = { x, y }; y += b.h + GAP_Y }
    x += Math.max(...c.map((b) => b.w)) + GAP_X
  })
  return { w: x - GAP_X, h: H, at }
}

/** freeSpot finds a place for a new table below what is there. */
export function freeSpot(positions: Positions, boxes: Box[]): { x: number; y: number } {
  let y = 0, x = Infinity
  for (const b of boxes) {
    const p = positions[b.id]
    if (!p) continue
    y = Math.max(y, p.y + b.h + GAP_Y * 2)
    x = Math.min(x, p.x)
  }
  return { x: x === Infinity ? 0 : x, y }
}
