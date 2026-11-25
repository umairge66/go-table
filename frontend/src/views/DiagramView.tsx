import clsx from 'clsx'
import { Download, Link2, ListOrdered, Maximize2, Minus, Pencil, Plus, Search, Table2, Trash2, Wrench, Workflow } from 'lucide-react'
import { memo, useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react'
import { api, type TableRef } from '../api'
import { Button, IconButton, Input, RefreshButton, Select, Spinner, errorParts, useMenu, useToast } from '../components/ui'
import { freeSpot, layout, type Box, type Positions } from '../lib/diagramLayout'
import { saveTextAs } from '../lib/saveFile'
import { features, fixedStructure, type Column, type DbObject, type DiagramTable, type ForeignRef, type SessionInfo } from '../types'
import { ColumnDialog } from './ColumnDialog'
import { NameDialog } from './SidebarPanels'
import { ForeignKeyDialog, IndexDialog, useAlter } from './StructureDialogs'

type Detail = 'all' | 'keys' | 'names'
type Key = DiagramTable['foreignKeys'][number]
interface Edge { id: string; from: string; to: string; fk: Key }

const HEAD = 34
const ROW = 24
const PAD = 5
/** Below this zoom the columns can't be read: tables are drawn as names only. */
const FAR = 0.42
/** The background's dots are this far apart. */
const DOTS = 24
/** The dots reach this far over the top and left edge, to slide in from there. */
const DOT_EDGE = 64
/** How far beyond the window tables are kept drawn, in screen pixels. */
const MARGIN = 500

const shortType = (t: string) => t.toLowerCase().replace('timestamp with time zone', 'timestamptz').replace('timestamp without time zone', 'timestamp').replace('time without time zone', 'time')
  .replace('character varying', 'varchar').replace('double precision', 'float8')

const isFk = (t: DiagramTable, c: string) => t.foreignKeys.some((f) => f.from.includes(c))

/** columnsShown: which columns of a table are drawn. */
const columnsShown = (t: DiagramTable, d: Detail) => (d === 'names' ? [] : d === 'all' ? t.columns : t.columns.filter((c) => c.primaryKey || isFk(t, c.name)))

function boxOf(t: DiagramTable, d: Detail): Box {
  const cols = columnsShown(t, d)
  const w = Math.max(t.name.length * 7.8 + 74, ...cols.map((c) => c.name.length * 7 + Math.min(shortType(c.type).length, 20) * 6.7 + 64))
  return { id: t.name, w: Math.round(Math.max(150, Math.min(380, w))), h: HEAD + (cols.length ? cols.length * ROW + PAD * 2 : 0) }
}

const rowY = (t: DiagramTable, column: string, d: Detail) => {
  const i = columnsShown(t, d).findIndex((c) => c.name === column)
  return i < 0 ? HEAD / 2 : HEAD + PAD + i * ROW + ROW / 2
}

interface Line { e: Edge; p: number[]; d: string; marks: string; ring: string; mid: { x: number; y: number } }

/** lineOf: how a key is drawn between two tables where they stand. */
function lineOf(e: Edge, a: DiagramTable, b: DiagramTable, ba: Box, bb: Box, pa: { x: number; y: number }, pb: { x: number; y: number }, detail: Detail): Line {
  const ya = pa.y + rowY(a, e.fk.from[0], detail), yb = pb.y + rowY(b, e.fk.columns[0] ?? '', detail)
  const self = a.name === b.name
  // From the side that faces the other table.
  const ca = pa.x + ba.w / 2, cb = pb.x + bb.w / 2
  const sa = self || pa.x + ba.w < pb.x ? 1 : pb.x + bb.w < pa.x ? -1 : ca < cb ? -1 : 1
  const sb = self ? 1 : sa === 1 && pa.x + ba.w < pb.x ? -1 : sa === -1 && pb.x + bb.w < pa.x ? 1 : sa
  const xa = sa === 1 ? pa.x + ba.w : pa.x, xb = sb === 1 ? pb.x + bb.w : pb.x
  const bend = self ? 46 : sa === sb ? 60 : Math.max(36, Math.min(160, Math.abs(xa - xb) / 2))
  const p = [xa, ya, xa + sa * bend, ya, xb + sb * bend, yb, xb, yb]
  const nullable = e.fk.from.some((c) => a.columns.find((x) => x.name === c)?.nullable)
  // Many (a crow's foot) at the referring end, one at the other.
  const pk = a.columns.filter((c) => c.primaryKey)
  const unique = e.fk.from.length === pk.length && pk.every((c) => e.fk.from.includes(c.name))
  const foot = unique ? `M${xa + sa * 9} ${ya - 5}v10` : `M${xa} ${ya - 5}L${xa + sa * 11} ${ya}L${xa} ${ya + 5}`
  return { e, p, d: `M${xa} ${ya}C${p[2]} ${p[3]} ${p[4]} ${p[5]} ${xb} ${yb}`, marks: `${foot}M${xb + sb * 8} ${yb - 5}v10`,
    ring: nullable ? `M${xb + sb * 14 - 3.5} ${yb}a3.5 3.5 0 1 0 7 0a3.5 3.5 0 1 0 -7 0` : '', mid: { x: (xa + xb) / 2 + (sa === sb ? sa * bend * 0.75 : 0), y: (ya + yb) / 2 } }
}

/** distance from a point to a line, roughly: enough to tell what is clicked. */
function distance(l: Line, x: number, y: number) {
  const [x0, y0, x1, y1, x2, y2, x3, y3] = l.p
  if (x < Math.min(x0, x1, x2, x3) - 20 || x > Math.max(x0, x1, x2, x3) + 20 || y < Math.min(y0, y3) - 20 || y > Math.max(y0, y3) + 20) return Infinity
  let best = Infinity, px = x0, py = y0
  for (let i = 1; i <= 18; i++) {
    const t = i / 18, u = 1 - t
    const qx = u * u * u * x0 + 3 * u * u * t * x1 + 3 * u * t * t * x2 + t * t * t * x3
    const qy = u * u * u * y0 + 3 * u * u * t * y1 + 3 * u * t * t * y2 + t * t * t * y3
    const dx = qx - px, dy = qy - py
    const k = Math.max(0, Math.min(1, ((x - px) * dx + (y - py) * dy) / (dx * dx + dy * dy || 1)))
    best = Math.min(best, Math.hypot(x - px - k * dx, y - py - k * dy))
    px = qx; py = qy
  }
  return best
}

/**
 * DiagramView draws a schema's tables and foreign keys. Tables are dragged
 * to where they suit; a key is made by dragging from one column to another;
 * everything else is in the menus. All of it goes through the structure
 * dialogs, which show the SQL before it runs.
 */
export function DiagramView({ session, schema, objects, active, onOpenTable, onNewTable, onDeleteTables, onChanged }: {
  session: SessionInfo
  schema: string
  /** objects: the schema's tables as the sidebar has them; the diagram is read again when they change. */
  objects: DbObject[] | null
  active: boolean
  onOpenTable: (o: DbObject, structure: boolean) => void
  onNewTable: () => void
  onDeleteTables: (o: DbObject[]) => void
  onChanged: () => void
}) {
  const driver = session.connection.driver
  const f = features(driver)
  const canAlter = !fixedStructure(driver)
  const toast = useToast()
  const menu = useMenu()
  const storeKey = `gotable:er:${session.connection.id}:${session.database}:${schema}`
  const [tables, setTables] = useState<DiagramTable[] | null>(null)
  const [error, setError] = useState<[string, string] | null>(null)
  const [busy, setBusy] = useState(false)
  const [detail, setDetail] = useState<Detail>(() => (localStorage.getItem('gotable:erDetail') as Detail) || 'all')
  const [moved, setMoved] = useState<Positions>(() => { try { return JSON.parse(localStorage.getItem(storeKey) ?? '{}') as Positions } catch { return {} } })
  const [selEdge, setSelEdge] = useState<string | null>(null)
  const [selTable, setSelTable] = useState<string | null>(null)
  const [search, setSearch] = useState('')
  const [dialog, setDialog] = useState<
    | { kind: 'column'; table: TableRef; columns: Column[]; editing?: Column }
    | { kind: 'fk'; table: TableRef; columns: Column[]; column?: string; ref?: { table: string; column: string } }
    | { kind: 'index'; table: TableRef; columns: Column[]; column: string }
    | { kind: 'rename'; table: TableRef } | null>(null)
  const [alterTable, setAlterTable] = useState<TableRef>({ schema, name: '' })
  // Moving around changes these elements directly, sixty times a second;
  // React only hears of it when other tables come into view.
  const host = useRef<HTMLDivElement>(null)
  const world = useRef<HTMLDivElement>(null)
  const dots = useRef<HTMLCanvasElement>(null)
  const percent = useRef<HTMLButtonElement>(null)
  const basePath = useRef<SVGPathElement>(null)
  const baseRings = useRef<SVGPathElement>(null)
  const hotPath = useRef<SVGPathElement>(null)
  const dragPath = useRef<SVGPathElement>(null)
  const view = useRef({ x: 40, y: 40, k: 1 })
  const [drawn, setDrawn] = useState<{ ids: Set<string>; far: boolean }>({ ids: new Set(), far: false })
  const hovered = useRef<string | null>(null)

  const load = useCallback(async () => {
    setBusy(true)
    try {
      setTables((await api.diagram(session.id, schema)).tables)
      setError(null)
    } catch (e) { setError(errorParts(e)) } finally { setBusy(false) }
  }, [session.id, schema])
  // Again when the sidebar's list changed: a table was made, renamed or deleted.
  useEffect(() => { if (objects !== null) load() }, [load, objects])
  useEffect(() => {
    const on = () => { if (active) load() }
    window.addEventListener('gotable:refresh-shortcut', on)
    return () => window.removeEventListener('gotable:refresh-shortcut', on)
  }, [active, load])

  const boxes = useMemo(() => (tables ?? []).map((t) => boxOf(t, detail)), [tables, detail])
  const boxById = useMemo(() => new Map(boxes.map((b) => [b.id, b])), [boxes])
  const edges = useMemo<Edge[]>(() => (tables ?? []).flatMap((t) => t.foreignKeys.filter((k) => k.schema === schema || !k.schema).map((fk, i) => ({ id: `${t.name}.${fk.name || i}`, from: t.name, to: fk.table, fk }))), [tables, schema])
  const auto = useMemo(() => layout(boxes, edges), [boxes, edges])
  // Where a table was dragged to; else its place in the layout. A new table
  // in a diagram that was arranged goes below it.
  const positions = useMemo(() => {
    const out: Positions = {}
    const arranged = Object.keys(moved).some((id) => boxById.has(id))
    for (const b of boxes) if (moved[b.id]) out[b.id] = moved[b.id]
    for (const b of boxes) out[b.id] ??= arranged ? freeSpot(out, boxes) : auto[b.id]
    return out
  }, [boxes, boxById, moved, auto])
  const keep = (p: Positions) => { setMoved(p); try { localStorage.setItem(storeKey, JSON.stringify(p)) } catch { /* not remembered */ } }

  const bounds = useMemo(() => {
    let x0 = Infinity, y0 = Infinity, x1 = -Infinity, y1 = -Infinity
    for (const b of boxes) { const p = positions[b.id]; x0 = Math.min(x0, p.x); y0 = Math.min(y0, p.y); x1 = Math.max(x1, p.x + b.w); y1 = Math.max(y1, p.y + b.h) }
    return boxes.length ? { x0, y0, x1, y1 } : { x0: 0, y0: 0, x1: 1, y1: 1 }
  }, [boxes, positions])
  const boxesRef = useRef(boxes)
  boxesRef.current = boxes
  const positionsRef = useRef(positions)
  positionsRef.current = positions

  // ---------------------------------------------------------------- moving around

  // covered: the part of the diagram whose tables are drawn.
  const covered = useRef<{ x0: number; y0: number; x1: number; y1: number; far: boolean } | null>(null)
  const draw = useCallback(() => {
    const el = host.current
    if (!el || !el.clientWidth) return
    const { x, y, k } = view.current
    const far = covered.current?.far ? k < FAR + 0.04 : k < FAR
    const x0 = -x / k, y0 = -y / k, x1 = x0 + el.clientWidth / k, y1 = y0 + el.clientHeight / k
    const c = covered.current
    if (c && c.far === far && x0 >= c.x0 && y0 >= c.y0 && x1 <= c.x1 && y1 <= c.y1) return
    const m = MARGIN / k
    covered.current = { x0: x0 - m, y0: y0 - m, x1: x1 + m, y1: y1 + m, far }
    const ids = new Set<string>()
    for (const b of boxesRef.current) {
      const p = positionsRef.current[b.id]
      if (p && p.x < x1 + m && p.x + b.w > x0 - m && p.y < y1 + m && p.y + b.h > y0 - m) ids.add(b.id)
    }
    setDrawn((was) => (was.far === far && was.ids.size === ids.size && [...ids].every((id) => was.ids.has(id)) ? was : { ids, far }))
  }, [])
  const frame = useRef(0)
  const dotStep = useRef(0)
  // Drawn only when the zoom, the size or the theme changes: a picture that
  // is moved costs nothing, where a CSS pattern is painted again each time.
  const drawDots = useCallback((step = dotStep.current) => {
    const c = dots.current, el = host.current, g = c?.getContext('2d', { alpha: false })
    if (!c || !el || !g || !step) return
    dotStep.current = step
    const r = window.devicePixelRatio || 1
    const w = el.clientWidth + DOT_EDGE, h = el.clientHeight + DOT_EDGE
    if (c.width !== Math.round(w * r) || c.height !== Math.round(h * r)) {
      c.width = Math.round(w * r); c.height = Math.round(h * r)
      c.style.width = `${w}px`; c.style.height = `${h}px`
    }
    g.setTransform(r, 0, 0, r, 0, 0)
    // Opaque and moved by whole pixels: the quickest there is to put on screen.
    const css = getComputedStyle(c)
    g.fillStyle = css.backgroundColor
    g.fillRect(0, 0, w, h)
    g.fillStyle = css.color
    // Starts one step outside, where it slides in from.
    const from = DOT_EDGE - step
    g.beginPath()
    for (let y = from; y < h; y += step) for (let x = from; x < w; x += step) g.rect(x - 0.9, y - 0.9, 1.8, 1.8)
    g.fill()
  }, [])
  const settle = useRef<ReturnType<typeof setTimeout>>(undefined)
  const apply = useCallback(() => {
    if (frame.current) return
    frame.current = requestAnimationFrame(() => {
      frame.current = 0
      const w = world.current
      if (!w) return
      const { x, y, k } = view.current
      // A layer of its own while it moves; sharp text again once it rests.
      w.style.willChange = 'transform'
      w.style.transform = `translate(${x}px, ${y}px) scale(${k})`
      // The dots slide by less than one step, so panning repaints nothing;
      // far out every second (fourth…) one is left.
      if (dots.current) {
        let step = DOTS * k
        while (step < 16) step *= 2
        if (step !== dotStep.current) drawDots(step)
        dots.current.style.transform = `translate(${Math.round(((x % step) + step) % step)}px, ${Math.round(((y % step) + step) % step)}px)`
      }
      if (percent.current) percent.current.textContent = `${Math.round(k * 100)}%`
      clearTimeout(settle.current)
      settle.current = setTimeout(() => { if (world.current) { world.current.style.willChange = ''; world.current.style.setProperty('--inv', String(1 / view.current.k)) } }, 180)
      draw()
    })
  }, [draw, drawDots])
  const setView = useCallback((v: { x: number; y: number; k: number }) => { view.current = v; apply() }, [apply])
  useEffect(() => () => { cancelAnimationFrame(frame.current); clearTimeout(settle.current) }, [])
  // Other tables, or elsewhere: what is in view may be others now.
  useLayoutEffect(() => { covered.current = null; draw(); apply() }, [boxes, positions, active, draw, apply])
  useEffect(() => {
    const el = host.current
    if (!el) return
    const ro = new ResizeObserver(() => { draw(); drawDots() })
    ro.observe(el)
    const theme = new MutationObserver(() => drawDots())
    theme.observe(document.documentElement, { attributes: true, attributeFilter: ['data-theme'] })
    return () => { ro.disconnect(); theme.disconnect() }
  }, [tables === null, draw, drawDots])

  const fit = useCallback(() => {
    const el = host.current
    if (!el || !el.clientWidth) return
    const k = Math.max(0.08, Math.min(1, (el.clientWidth - 64) / (bounds.x1 - bounds.x0), (el.clientHeight - 64) / (bounds.y1 - bounds.y0)))
    setView({ k, x: (el.clientWidth - (bounds.x1 - bounds.x0) * k) / 2 - bounds.x0 * k, y: Math.max(32, (el.clientHeight - (bounds.y1 - bounds.y0) * k) / 2) - bounds.y0 * k })
  }, [bounds, setView])
  const fitted = useRef(false)
  // Arrange: fit once the new layout is there.
  const fitAfter = useRef(false)
  useLayoutEffect(() => { if (fitAfter.current) { fitAfter.current = false; fit() } }, [fit, moved])
  useLayoutEffect(() => { if (tables?.length && active && !fitted.current) { fitted.current = true; fit() } }, [tables, active, fit])

  const zoomAt = useCallback((factor: number, cx?: number, cy?: number) => {
    const el = host.current
    if (!el) return
    const v = view.current
    const x = cx ?? el.clientWidth / 2, y = cy ?? el.clientHeight / 2
    const k = Math.max(0.08, Math.min(2.5, v.k * factor))
    setView({ k, x: x - ((x - v.x) / v.k) * k, y: y - ((y - v.y) / v.k) * k })
  }, [setView])
  // Not React's onWheel, which can't keep the page from zooming.
  useEffect(() => {
    const el = host.current
    if (!el) return
    const on = (e: WheelEvent) => {
      e.preventDefault()
      const r = el.getBoundingClientRect()
      const unit = e.deltaMode === 1 ? 16 : 1
      const dx = e.deltaX * unit, dy = e.deltaY * unit
      if (e.ctrlKey || e.metaKey) zoomAt(Math.exp(-dy * 0.0022), e.clientX - r.left, e.clientY - r.top)
      else { const v = view.current; setView({ ...v, x: v.x - (e.shiftKey && !dx ? dy : dx), y: v.y - (e.shiftKey && !dx ? 0 : dy) }) }
    }
    el.addEventListener('wheel', on, { passive: false })
    return () => el.removeEventListener('wheel', on)
  }, [tables === null, setView, zoomAt])
  const track = (move: (e: MouseEvent) => void, up?: (e: MouseEvent) => void) => {
    const end = (e: MouseEvent) => { window.removeEventListener('mousemove', move); window.removeEventListener('mouseup', end); up?.(e) }
    window.addEventListener('mousemove', move)
    window.addEventListener('mouseup', end)
  }
  const worldPoint = (ev: { clientX: number; clientY: number }) => {
    const r = host.current!.getBoundingClientRect(), v = view.current
    return { x: (ev.clientX - r.left - v.x) / v.k, y: (ev.clientY - r.top - v.y) / v.k }
  }

  // ---------------------------------------------------------------- the keys

  const byName = useMemo(() => new Map((tables ?? []).map((t) => [t.name, t])), [tables])
  const lines = useMemo(() => edges.flatMap((e) => {
    const a = byName.get(e.from), b = byName.get(e.to)
    return a && b ? [lineOf(e, a, b, boxById.get(a.name)!, boxById.get(b.name)!, positions[a.name], positions[b.name], detail)] : []
  }), [edges, byName, boxById, positions, detail])
  const linesRef = useRef(lines)
  linesRef.current = lines
  const far = drawn.far
  const farRef = useRef(far)
  farRef.current = far
  /** paint draws the keys: those of one table (or one key) stand out. */
  const paint = useCallback((moving?: Line[]) => {
    const gone = new Set(moving?.map((l) => l.e.id))
    const shape = (ls: Line[]) => ls.map((l) => l.d + (farRef.current ? '' : l.marks)).join('')
    const rest = moving ? linesRef.current.filter((l) => !gone.has(l.e.id)) : linesRef.current
    basePath.current?.setAttribute('d', shape(rest))
    baseRings.current?.setAttribute('d', farRef.current ? '' : rest.map((l) => l.ring).join(''))
    const on = hovered.current ?? selRef.current.table
    const hot = moving ?? linesRef.current.filter((l) => l.e.id === selRef.current.edge || (on !== null && (l.e.from === on || l.e.to === on)))
    hotPath.current?.setAttribute('d', shape(hot))
  }, [])
  const selRef = useRef({ edge: selEdge, table: selTable })
  selRef.current = { edge: selEdge, table: selTable }
  useLayoutEffect(() => paint(), [lines, far, selEdge, selTable, paint])

  const moveTable = (e: React.MouseEvent, id: string) => {
    const start = { x: e.clientX, y: e.clientY }
    const at = positionsRef.current[id]
    const el = world.current?.querySelector<HTMLElement>(`[data-er-box="${CSS.escape(id)}"]`)
    const its = linesRef.current.filter((l) => l.e.from === id || l.e.to === id).map((l) => l.e)
    let now = at
    let raf = 0
    const show = () => {
      raf = 0
      if (el) el.style.transform = `translate(${now.x}px, ${now.y}px)`
      const pos = (n: string) => (n === id ? now : positionsRef.current[n])
      paint(its.map((x) => lineOf(x, byName.get(x.from)!, byName.get(x.to)!, boxById.get(x.from)!, boxById.get(x.to)!, pos(x.from), pos(x.to), detail)))
    }
    track((ev) => {
      const k = view.current.k
      const dx = (ev.clientX - start.x) / k, dy = (ev.clientY - start.y) / k
      if (now === at && Math.abs(dx) + Math.abs(dy) < 4) return
      now = { x: Math.round(at.x + dx), y: Math.round(at.y + dy) }
      if (!raf) raf = requestAnimationFrame(show)
    }, () => {
      cancelAnimationFrame(raf)
      // The first table that is moved pins the others where they are.
      if (now !== at) keep({ ...positionsRef.current, [id]: now })
    })
  }

  // Dragging from a column's dot to another column makes a foreign key.
  const startKey = (e: React.MouseEvent, table: string, column: string) => {
    e.preventDefault()
    const t = byName.get(table)!, p = positionsRef.current[table], b = boxById.get(table)!
    const y = p.y + rowY(t, column, detail)
    let over: HTMLElement | null = null
    track((ev) => {
      const el = (document.elementFromPoint(ev.clientX, ev.clientY) as HTMLElement | null)?.closest<HTMLElement>('[data-er-column]') ?? null
      const next = el && !(el.dataset.erTable === table && el.dataset.erColumn === column) ? el : null
      if (next !== over) { over?.removeAttribute('data-over'); next?.setAttribute('data-over', ''); over = next }
      const q = worldPoint(ev)
      dragPath.current?.setAttribute('d', `M${q.x > p.x + b.w / 2 ? p.x + b.w : p.x} ${y}L${q.x} ${q.y}`)
    }, () => {
      dragPath.current?.setAttribute('d', '')
      over?.removeAttribute('data-over')
      if (over) addKey(table, column, { table: over.dataset.erTable!, column: over.dataset.erColumn! })
    })
  }

  /** at: the table, column and header a mouse event is on. */
  const at = (e: React.MouseEvent) => {
    const el = e.target as HTMLElement
    const row = el.closest<HTMLElement>('[data-er-column]')
    return { table: el.closest<HTMLElement>('[data-er-box]')?.dataset.erBox, column: row?.dataset.erColumn, row, menu: !!el.closest('[data-er-menu]') }
  }
  const nearest = (e: { clientX: number; clientY: number }) => {
    const q = worldPoint(e)
    let best: Line | null = null, d = 8 / view.current.k
    for (const l of linesRef.current) { const x = distance(l, q.x, q.y); if (x < d) { d = x; best = l } }
    return best
  }
  const onMouseDown = (e: React.MouseEvent) => {
    if (e.button > 1) return
    const on = at(e)
    if (on.table && e.button === 0) {
      if (on.menu) return
      setSelTable(on.table)
      setSelEdge(null)
      if (on.row && canAlter && f.foreignKeys && e.clientX > on.row.getBoundingClientRect().right - 12 * view.current.k) startKey(e, on.table, on.column!)
      else if (!on.column) moveTable(e, on.table)
      return
    }
    const start = { x: e.clientX, y: e.clientY }, from = view.current
    let moved = false
    track((ev) => {
      if (!moved && Math.abs(ev.clientX - start.x) + Math.abs(ev.clientY - start.y) < 3) return
      moved = true
      setView({ ...from, x: from.x + ev.clientX - start.x, y: from.y + ev.clientY - start.y })
    }, (ev) => {
      if (moved) return
      setSelTable(null)
      setSelEdge(nearest(ev)?.e.id ?? null)
    })
  }
  const onMouseOver = (e: React.MouseEvent) => {
    const t = (e.target as HTMLElement).closest<HTMLElement>('[data-er-box]')?.dataset.erBox ?? null
    if (t !== hovered.current) { hovered.current = t; paint() }
  }
  const onMouseMove = (e: React.MouseEvent) => {
    if (e.buttons || hovered.current || !host.current) return
    host.current.style.cursor = nearest(e) ? 'pointer' : ''
  }

  // ---------------------------------------------------------------- changing the structure

  const ref = (name: string): TableRef => ({ schema, name })
  const structure = async (name: string) => (await api.structure(session.id, ref(name))).columns
  const open = async (fn: () => Promise<void>) => { try { await fn() } catch (e) { toast('error', ...errorParts(e)) } }
  // What is drawn stays where it is when something changes; Arrange starts over.
  const reload = () => { keep(positionsRef.current); load(); onChanged() }
  const changed = () => { setDialog(null); reload() }
  const alter = useAlter(session, alterTable, () => reload())
  const runAlter = useRef(alter.run)
  runAlter.current = alter.run
  // The review runs on the table it is about.
  const alterOn = (name: string, ops: Parameters<typeof alter.run>[0], title: string, done: string) => {
    setAlterTable(ref(name))
    setTimeout(() => runAlter.current(ops, title, { danger: true, done }))
  }
  const addColumn = (name: string) => open(async () => setDialog({ kind: 'column', table: ref(name), columns: await structure(name) }))
  const editColumn = (name: string, column: string) => open(async () => {
    const columns = await structure(name)
    setDialog({ kind: 'column', table: ref(name), columns, editing: columns.find((c) => c.name === column) })
  })
  const addKey = (name: string, column?: string, to?: { table: string; column: string }) => open(async () => setDialog({ kind: 'fk', table: ref(name), columns: await structure(name), column, ref: to }))
  const addIndex = (name: string, column: string) => open(async () => setDialog({ kind: 'index', table: ref(name), columns: await structure(name), column }))
  const dropKey = (e: Edge) => alterOn(e.from, [{ kind: 'drop_foreign_key', name: e.fk.name }], `Drop the foreign key on ${e.fk.from.join(', ')}`, 'Foreign key dropped')

  const tableMenu = useCallback((e: { clientX: number; clientY: number; preventDefault?: () => void }, name: string) => {
    setSelTable(name)
    const o: DbObject = { schema, name, kind: 'table' }
    menu.open(e, [
      { label: 'Open data', icon: <Table2 />, onSelect: () => onOpenTable(o, false) },
      { label: 'Open structure', icon: <Wrench />, onSelect: () => onOpenTable(o, true) },
      ...(canAlter ? [
        'sep' as const,
        { label: 'Add column…', icon: <Plus />, onSelect: () => addColumn(name) },
        ...(f.foreignKeys ? [{ label: 'Add foreign key…', icon: <Link2 />, onSelect: () => addKey(name) }] : []),
        { label: 'Rename…', icon: <Pencil />, onSelect: () => setDialog({ kind: 'rename', table: ref(name) }) },
        'sep' as const,
        { label: 'Delete table…', icon: <Trash2 />, danger: true, onSelect: () => onDeleteTables([o]) },
      ] : []),
    ])
  }, [schema, canAlter, f.foreignKeys, onOpenTable, onDeleteTables]) // eslint-disable-line react-hooks/exhaustive-deps
  const columnMenu = useCallback((e: React.MouseEvent, name: string, column: string) => {
    const t = tables?.find((x) => x.name === name)
    const fks = edges.filter((x) => x.from === name && x.fk.from.includes(column))
    menu.open(e, [
      ...(canAlter ? [
        { label: 'Edit column…', icon: <Pencil />, onSelect: () => editColumn(name, column) },
        { label: 'Add column…', icon: <Plus />, onSelect: () => addColumn(name) },
        ...(f.foreignKeys ? [{ label: 'Foreign key…', icon: <Link2 />, onSelect: () => addKey(name, column) }] : []),
        { label: 'Index…', icon: <ListOrdered />, onSelect: () => addIndex(name, column) },
      ] : []),
      ...fks.filter((k) => k.fk.name).map((k) => ({ label: `Drop foreign key to ${k.to}`, icon: <Link2 />, danger: true, onSelect: () => dropKey(k) })),
      ...(canAlter ? ['sep' as const, { label: 'Drop column', icon: <Trash2 />, danger: true, disabled: (t?.columns.length ?? 0) < 2, onSelect: () => alterOn(name, [{ kind: 'drop_column', column }], `Drop column ${column}`, `Column “${column}” dropped`) }] : []),
    ])
  }, [tables, edges, canAlter, f.foreignKeys]) // eslint-disable-line react-hooks/exhaustive-deps

  const q = search.trim().toLowerCase()
  const found = useMemo(() => (q ? new Set((tables ?? []).filter((t) => t.name.toLowerCase().includes(q) || t.columns.some((c) => c.name.toLowerCase().includes(q))).map((t) => t.name)) : null), [q, tables])
  const show = (name: string) => {
    const el = host.current, p = positions[name], b = boxById.get(name)
    if (!el || !p || !b) return
    const k = Math.max(view.current.k, 0.7)
    setView({ k, x: el.clientWidth / 2 - (p.x + b.w / 2) * k, y: el.clientHeight / 2 - (p.y + Math.min(b.h, 300) / 2) * k })
    setSelTable(name)
  }
  const selected = lines.find((l) => l.e.id === selEdge)

  const exportSVG = async () => {
    try {
      const saved = await saveTextAs(`${schema || session.database}-diagram.svg`, 'svg', toSVG(tables ?? [], boxes, positions, lines, bounds, detail), 'image/svg+xml')
      if (saved) toast('ok', 'Diagram saved', saved.name)
    } catch (e) { toast('error', 'Could not save the diagram', String((e as Error).message ?? e)) }
  }
  const copyMermaid = () => navigator.clipboard?.writeText(toMermaid(tables ?? [], edges)).then(() => toast('ok', 'Copied as Mermaid', 'Paste it into a README or a wiki page'))

  return (
    <div className="flex h-full flex-col" data-testid="diagram" aria-busy={busy}>
      <div className="flex h-11 shrink-0 items-center gap-1.5 border-b border-line px-2">
        {canAlter && <Button size="sm" variant="ghost" icon={<Plus className="size-3.5" />} onClick={onNewTable}>New table</Button>}
        <Input className="w-56" icon={<Search />} value={search} placeholder="Find a table or column" aria-label="Find in the diagram" onChange={(e) => setSearch(e.target.value)}
          onKeyDown={(e) => { if (e.key === 'Enter' && found?.size) show([...found].find((n) => n.toLowerCase().includes(q)) ?? [...found][0]); if (e.key === 'Escape') setSearch('') }} />
        {found && <span className="text-[12px] text-muted">{found.size} found</span>}
        <div className="flex-1" />
        <span className="text-[12px] text-muted">{tables ? `${tables.length} table${tables.length === 1 ? '' : 's'} · ${edges.length} key${edges.length === 1 ? '' : 's'}` : ''}</span>
        <Select aria-label="Columns shown" className="w-36" value={detail} onChange={(e) => { setDetail(e.target.value as Detail); localStorage.setItem('gotable:erDetail', e.target.value) }}>
          <option value="all">All columns</option><option value="keys">Keys only</option><option value="names">Table names</option>
        </Select>
        <Button size="sm" variant="ghost" icon={<Workflow className="size-3.5" />} title="Arrange the tables anew" onClick={() => { localStorage.removeItem(storeKey); setMoved({}); fitAfter.current = true }}>Arrange</Button>
        <RefreshButton label="Refresh (Ctrl+R)" busy={busy} onRefresh={load} />
        <Button size="sm" variant="ghost" icon={<Download className="size-3.5" />} disabled={!tables?.length}
          onClick={(e) => menu.open(e, [{ label: 'Save as SVG image…', onSelect: exportSVG }, { label: 'Copy as Mermaid', onSelect: copyMermaid }])}>Export</Button>
      </div>
      <div className="relative min-h-0 flex-1 overflow-hidden bg-bg">
        {error ? (
          <div className="flex h-full flex-col items-center justify-center gap-2 p-6 text-center">
            <div className="text-[14px] font-semibold text-err">{error[0]}</div>
            <div className="max-w-xl text-[12.5px] text-muted">{error[1]}</div>
            <Button size="sm" className="mt-2" onClick={load}>Try again</Button>
          </div>
        ) : !tables ? <div className="flex h-full items-center justify-center"><Spinner className="size-5" /></div>
        : !tables.length ? (
          <div className="flex h-full flex-col items-center justify-center gap-3 text-center text-[13px] text-muted">
            <Table2 className="size-8 text-faint" />No tables to draw yet.
            {canAlter && <Button size="sm" variant="primary" icon={<Plus className="size-3.5" />} onClick={onNewTable}>New table</Button>}
          </div>
        ) : (
          <div ref={host} className="absolute inset-0 cursor-grab overflow-hidden active:cursor-grabbing" onMouseDown={onMouseDown} onMouseOver={onMouseOver} onMouseMove={onMouseMove}
            onMouseLeave={() => { hovered.current = null; paint() }}
            onDoubleClick={(e) => { const on = at(e); if (on.column && on.table) { if (canAlter) editColumn(on.table, on.column) } else if (on.table) onOpenTable({ schema, name: on.table, kind: 'table' }, false) }}
            onContextMenu={(e) => { const on = at(e); if (!on.table) return; e.preventDefault(); if (on.column) columnMenu(e, on.table, on.column); else tableMenu(e, on.table) }}
            onClick={(e) => { const on = at(e); if (on.menu && on.table) tableMenu(e, on.table) }}>
            <canvas ref={dots} className="er-dots" aria-hidden="true" />
            <div ref={world} className="absolute left-0 top-0 origin-top-left" data-testid="er-world" data-edges={lines.length} data-far={far || undefined}>
              <svg className="pointer-events-none absolute left-0 top-0 overflow-visible" width="1" height="1" fill="none" aria-hidden="true">
                <path ref={basePath} stroke="var(--faint)" strokeWidth={far ? 2.5 : 1.5} opacity={found || selTable || selEdge ? 0.5 : 1} />
                <path ref={baseRings} stroke="var(--faint)" strokeWidth="1.5" fill="var(--bg)" />
                <path ref={hotPath} stroke="var(--accent)" strokeWidth={far ? 4 : 2.2} />
              </svg>
              {tables.map((t) => drawn.ids.has(t.name) && (
                <TableBox key={t.name} t={t} box={boxById.get(t.name)!} x={positions[t.name].x} y={positions[t.name].y} detail={detail} far={far}
                  state={selTable === t.name ? 'selected' : found ? (found.has(t.name) ? 'found' : 'dim') : 'plain'} match={found?.has(t.name) ? q : ''} canKey={canAlter && f.foreignKeys} />
              ))}
              <svg className="pointer-events-none absolute left-0 top-0 overflow-visible" width="1" height="1" fill="none" aria-hidden="true"><path ref={dragPath} stroke="var(--accent)" strokeWidth="2" strokeDasharray="5 4" /></svg>
              {selected && <EdgeCard e={selected.e} style={{ left: selected.mid.x, top: selected.mid.y, transform: 'scale(var(--inv, 1)) translate(-50%, 8px)', transformOrigin: 'top left' }} onDrop={canAlter && selected.e.fk.name ? () => { setSelEdge(null); dropKey(selected.e) } : undefined} />}
            </div>
            <div className="absolute bottom-3 right-3 flex cursor-default items-center gap-0.5 rounded-lg border border-line bg-panel p-0.5" data-bottom-bar onMouseDown={(e) => e.stopPropagation()}>
              <IconButton label="Zoom out" onClick={() => zoomAt(1 / 1.25)}><Minus className="size-4" /></IconButton>
              <button ref={percent} className="w-12 text-center text-[12px] tabular-nums text-muted hover:text-fg" title="Actual size" onClick={() => zoomAt(1 / view.current.k)}>100%</button>
              <IconButton label="Zoom in" onClick={() => zoomAt(1.25)}><Plus className="size-4" /></IconButton>
              <IconButton label="Fit everything" onClick={fit}><Maximize2 className="size-3.5" /></IconButton>
            </div>
            <div className="pointer-events-none absolute bottom-3 left-3 rounded bg-bg/80 px-1 text-[11.5px] text-muted">
              Drag tables to arrange them{canAlter && f.foreignKeys && detail !== 'names' ? ' · drag from a column’s dot to another column for a foreign key' : ''} · Ctrl+wheel zooms
            </div>
          </div>
        )}
      </div>
      {dialog?.kind === 'column' && <ColumnDialog session={session} table={dialog.table} columns={dialog.columns} editing={dialog.editing} onClose={() => setDialog(null)} onDone={changed} />}
      {dialog?.kind === 'fk' && <ForeignKeyDialog session={session} table={dialog.table} columns={dialog.columns} initialColumn={dialog.column} initialRef={dialog.ref} onClose={() => setDialog(null)} onDone={changed} />}
      {dialog?.kind === 'index' && <IndexDialog session={session} table={dialog.table} columns={dialog.columns} initial={{ columns: [dialog.column], kind: 'index' }} onClose={() => setDialog(null)} onDone={changed} />}
      {dialog?.kind === 'rename' && (
        <NameDialog title={`Rename ${dialog.table.name}`} initial={dialog.table.name} confirmLabel="Rename" onClose={() => setDialog(null)} onSubmit={async (name) => {
          if (name === dialog.table.name) { setDialog(null); return }
          await api.alter(session.id, dialog.table, [{ kind: 'rename_table', newName: name }], false)
          // It stays where it was.
          const { [dialog.table.name]: at, ...rest } = positionsRef.current
          keep({ ...rest, [name]: at })
          toast('ok', 'Table renamed', name)
          setDialog(null)
          load()
          onChanged()
        }} />
      )}
      {alter.node}
      {menu.node}
    </div>
  )
}

/** TableBox only draws: the diagram itself hears the mouse (see data-er-*). */
const TableBox = memo(function TableBox({ t, box, x, y, detail, far, state, match, canKey }: {
  t: DiagramTable; box: Box; x: number; y: number; detail: Detail; far: boolean
  state: 'plain' | 'selected' | 'found' | 'dim'
  match: string
  canKey: boolean
}) {
  const cols = columnsShown(t, detail)
  return (
    <div data-testid={`er-${t.name}`} data-er-box={t.name} className={clsx('er-box group/t', cols.length > 0 && 'er-cols', state === 'selected' || state === 'found' ? 'er-on' : state === 'dim' && 'er-dim')}
      style={{ width: box.w, height: box.h, transform: `translate(${x}px, ${y}px)` }}>
      {far ? (
        // Larger than it would be, to be read from afar.
        <div className="flex h-full items-center justify-center overflow-hidden px-2 text-center font-semibold leading-tight" style={{ fontSize: Math.max(15, Math.min(30, (box.w * 1.5) / Math.max(6, t.name.length))) }}><span className="max-w-full break-words">{t.name}</span></div>
      ) : (
        <>
          <div className="er-head" title={t.name}><span className="min-w-0 flex-1 truncate">{t.name}</span><button data-er-menu aria-label={`Menu of ${t.name}`} className="rounded px-1 text-muted opacity-0 hover:bg-hover group-hover/t:opacity-100">⋯</button></div>
          {cols.map((c) => (
            <div key={c.name} data-er-column={c.name} data-er-table={t.name} data-testid={`er-${t.name}-${c.name}`} title={`${c.name}: ${c.type}${c.nullable ? '' : ', not null'}`}
              className={clsx('er-row', canKey && 'er-key', match && c.name.toLowerCase().includes(match) && 'bg-edited')}>
              <i className={c.primaryKey ? 'er-pk' : 'er-fk'}>{c.primaryKey ? 'PK' : isFk(t, c.name) ? 'FK' : ''}</i>
              <span className={clsx('max-w-[78%] shrink-0 truncate', !c.nullable && 'font-medium')}>{c.name}</span>
              <span className="min-w-0 flex-1 truncate pl-2 text-right font-mono text-[11px] text-muted">{shortType(c.type)}</span>
            </div>
          ))}
        </>
      )}
    </div>
  )
})

function EdgeCard({ e, style, onDrop }: { e: Edge; style: React.CSSProperties; onDrop?: () => void }) {
  return (
    <div className="absolute z-10 w-64 cursor-default rounded-xl border border-line bg-panel p-3 text-[12px] shadow-pop animate-pop" style={style} onMouseDown={(ev) => ev.stopPropagation()} role="dialog" aria-label="Foreign key">
      <div className="mb-1.5 flex items-center gap-1.5 font-semibold"><Link2 className="size-3.5 text-accent" /><span className="truncate">{e.fk.name || 'Foreign key'}</span></div>
      <div className="break-words font-mono text-[11.5px] text-fg-2">{e.from}({e.fk.from.join(', ')})<br />→ {e.to}({e.fk.columns.join(', ')})</div>
      <div className="mt-1.5 text-muted">{[e.fk.onDelete && e.fk.onDelete !== 'NO ACTION' && `On delete: ${e.fk.onDelete.toLowerCase()}`, e.fk.onUpdate && e.fk.onUpdate !== 'NO ACTION' && `on update: ${e.fk.onUpdate.toLowerCase()}`].filter(Boolean).join(' · ') || 'No action on delete or update'}</div>
      {onDrop && <Button size="sm" variant="ghost" className="mt-2 text-err" icon={<Trash2 className="size-3.5" />} onClick={onDrop}>Drop foreign key…</Button>}
    </div>
  )
}

const esc = (s: string) => s.replace(/[&<>"]/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;' })[c]!)

/** toSVG draws the diagram by itself, in light colours, to print or share. */
function toSVG(tables: DiagramTable[], boxes: Box[], at: Positions, edges: Line[], b: { x0: number; y0: number; x1: number; y1: number }, detail: Detail) {
  const m = 40
  const w = b.x1 - b.x0 + m * 2, h = b.y1 - b.y0 + m * 2
  const clip = (s: string, n: number) => (s.length > n ? s.slice(0, Math.max(1, n - 1)) + '…' : s)
  const parts = [`<svg xmlns="http://www.w3.org/2000/svg" width="${Math.round(w)}" height="${Math.round(h)}" viewBox="${b.x0 - m} ${b.y0 - m} ${w} ${h}" font-family="Inter, system-ui, sans-serif">`,
    `<rect x="${b.x0 - m}" y="${b.y0 - m}" width="${w}" height="${h}" fill="#ffffff"/>`, '<g fill="none" stroke="#8b8b9c" stroke-width="1.5">']
  for (const e of edges) parts.push(`<path d="${e.d}"/><path d="${e.marks}"/>${e.ring ? `<path d="${e.ring}" fill="#ffffff"/>` : ''}`)
  parts.push('</g>')
  tables.forEach((t, i) => {
    const { w: bw, h: bh } = boxes[i], p = at[t.name]
    parts.push(`<g transform="translate(${p.x} ${p.y})"><rect width="${bw}" height="${bh}" rx="10" fill="#ffffff" stroke="#d5d5de"/><path d="M0 ${HEAD} V10 a10 10 0 0 1 10 -10 H${bw - 10} a10 10 0 0 1 10 10 V${HEAD} Z" fill="#f0f0f8"/><text x="12" y="22" font-size="13" font-weight="600" fill="#1b1b23">${esc(clip(t.name, Math.floor(bw / 8)))}</text>`)
    columnsShown(t, detail).forEach((c, k) => {
      const y = HEAD + PAD + k * ROW + 16
      const mark = c.primaryKey ? 'PK' : isFk(t, c.name) ? 'FK' : ''
      parts.push(`<text x="10" y="${y}" font-size="9" font-weight="600" fill="${c.primaryKey ? '#c97c10' : '#5b5bd6'}">${mark}</text><text x="30" y="${y}" font-size="12" ${c.nullable ? '' : 'font-weight="500" '}fill="#1b1b23">${esc(clip(c.name, 30))}</text><text x="${bw - 10}" y="${y}" font-size="11" text-anchor="end" font-family="monospace" fill="#7a7a8c">${esc(clip(shortType(c.type), Math.max(4, Math.floor((bw - 50 - c.name.length * 7) / 6.4))))}</text>`)
    })
    parts.push('</g>')
  })
  return parts.join('\n') + '\n</svg>\n'
}

/** toMermaid writes the diagram as text GitHub and wikis draw. */
function toMermaid(tables: DiagramTable[], edges: Edge[]) {
  const id = (s: string) => (/^[A-Za-z_][\w-]*$/.test(s) ? s : `"${s.replace(/"/g, "'")}"`)
  const word = (s: string) => s.replace(/[^\w()[\],-]+/g, '_').replace(/^[^A-Za-z]+/, '') || 'type'
  const out = ['erDiagram']
  for (const t of tables) {
    out.push(`  ${id(t.name)} {`)
    for (const c of t.columns) out.push(`    ${word(c.type.toLowerCase())} ${word(c.name)}${c.primaryKey || isFk(t, c.name) ? ' ' + [c.primaryKey && 'PK', isFk(t, c.name) && 'FK'].filter(Boolean).join(', ') : ''}`)
    out.push('  }')
  }
  for (const e of edges as { from: string; to: string; fk: ForeignRef }[]) out.push(`  ${id(e.to)} ||--o{ ${id(e.from)} : "${e.fk.from.join(', ')}"`)
  return out.join('\n') + '\n'
}
