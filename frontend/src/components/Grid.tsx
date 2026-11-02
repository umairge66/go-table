import { useVirtualizer } from '@tanstack/react-virtual'
import clsx from 'clsx'
import { ArrowDown, ArrowUp, ArrowUpRight, KeyRound, Link2 } from 'lucide-react'
import { memo, useCallback, useEffect, useMemo, useRef, useState } from 'react'
import type { CellKind } from '../lib/cellKind'
import type { CellStatus } from '../lib/edits'
import { CellPicker, useChoices } from './CellPicker'
import type { Cell, Sort } from '../types'

export interface GridColumn {
  name: string
  type?: string
  pk?: boolean
  /** ref names the referenced table.column of a foreign key column. */
  ref?: string
  /** edit picks the editor: a calendar, time fields, a list of choices… (text when unset). */
  edit?: CellKind
  nullable?: boolean
}

export interface GridProps {
  columns: GridColumn[]
  rowCount: number
  cell: (r: number, c: number) => { value: Cell; status: CellStatus }
  /** rowOffset is added to the row numbers (paging). */
  rowOffset?: number
  sort?: Sort[]
  onSort?: (column: string) => void
  readOnly?: boolean
  onEdit?: (r: number, c: number, v: Cell) => void
  onDeleteRows?: (rows: number[]) => void
  onContextMenu?: (e: React.MouseEvent, r: number, c: number, rows: number[]) => void
  onSelectionChange?: (r: number, c: number, rows: number[]) => void
  /** onFollow opens the row a foreign key cell points to. */
  onFollow?: (r: number, c: number) => void
  /** onLookup replaces the text editor of foreign key cells (double-click, Enter). */
  onLookup?: (r: number, c: number, rect: DOMRect) => void
  /** Bumps when data changes so selection resets. */
  dataKey?: unknown
  /** When set (with a new key), selects the cell, scrolls to it and optionally edits it (text: as plain text). */
  focus?: { r: number; c: number; edit?: boolean; text?: boolean; key: number }
}

const ROW_H = 30
const GUTTER = 52

const shortType = (t: string) => t.toLowerCase().replace('timestamp with time zone', 'timestamptz').replace('timestamp without time zone', 'timestamp')
  .replace('character varying', 'varchar').replace('double precision', 'float8')

const display = (v: Cell) => (v === null ? '' : v.length > 300 ? v.slice(0, 300) + '…' : v.replace(/\r?\n/g, ' ↵ '))

const NUMERIC = /^(int|integer|smallint|bigint|tinyint|mediumint|int2|int4|int8|serial|bigserial|numeric|decimal|real|float|float4|float8|double|money)/i
export const isNumeric = (type?: string) => !!type && NUMERIC.test(type)

function measure(columns: GridColumn[], rowCount: number, cell: GridProps['cell']) {
  const sample = Math.min(rowCount, 60)
  return columns.map((col, c) => {
    // Header: name (semibold, wider) plus a short type label.
    let n = Math.ceil(col.name.length * 1.1) + Math.min((col.type ?? '').length, 10) + (col.pk || col.ref ? 3 : 0) + 4
    for (let r = 0; r < sample; r++) {
      const v = cell(r, c).value
      n = Math.max(n, Math.min(v === null ? 4 : v.length, 48))
    }
    return Math.max(72, Math.min(420, n * 7.4 + 22))
  })
}

export const Grid = memo(function Grid(p: GridProps) {
  const { columns, rowCount, cell } = p
  const scrollRef = useRef<HTMLDivElement>(null)
  const [widths, setWidths] = useState<number[]>(() => measure(columns, rowCount, cell))
  const [sel, setSel] = useState<{ r: number; c: number } | null>(null)
  const [rows, setRows] = useState<Set<number>>(new Set())
  const [anchor, setAnchor] = useState<number | null>(null)
  const [editing, setEditing] = useState<{ r: number; c: number; value: string } | null>(null)

  const colKey = columns.map((c) => c.name).join('\u0000')
  useEffect(() => {
    setWidths(measure(columns, rowCount, cell))
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [colKey])
  useEffect(() => {
    setSel(null)
    setRows(new Set())
    setEditing(null)
  }, [p.dataKey, colKey])

  const total = useMemo(() => GUTTER + widths.reduce((a, b) => a + b, 0), [widths])
  const numeric = useMemo(() => columns.map((c) => isNumeric(c.type)), [columns])
  const virt = useVirtualizer({ count: rowCount, getScrollElement: () => scrollRef.current, estimateSize: () => ROW_H, overscan: 12 })

  const select = useCallback((r: number, c: number, mode: 'set' | 'toggle' | 'range' = 'set') => {
    let next: Set<number>
    if (mode === 'toggle') { next = new Set(rows); if (next.has(r)) next.delete(r); else next.add(r) }
    else if (mode === 'range' && anchor !== null) {
      next = new Set()
      for (let i = Math.min(anchor, r); i <= Math.max(anchor, r); i++) next.add(i)
    } else next = new Set([r])
    setSel({ r, c })
    setRows(next)
    if (mode !== 'range') setAnchor(r)
    p.onSelectionChange?.(r, c, [...next].sort((a, b) => a - b))
  }, [anchor, rows, p])

  const startEdit = useCallback((r: number, c: number, initial?: string, text = false) => {
    if (p.readOnly || !p.onEdit) return
    const { value, status } = cell(r, c)
    if (status === 'deleted') return
    // Foreign keys pick from the referenced table; typing still edits as text.
    if (initial === undefined && !text && columns[c]?.ref && p.onLookup) {
      const el = scrollRef.current?.querySelector(`[data-row="${r}"] [data-col="${c}"]`)
      if (el) { p.onLookup(r, c, el.getBoundingClientRect()); return }
    }
    setEditing({ r, c, value: initial ?? value ?? '' })
  }, [cell, columns, p])

  const commitEdit = (value: Cell) => {
    if (editing) p.onEdit?.(editing.r, editing.c, value)
    setEditing(null)
    scrollRef.current?.focus()
  }

  const ensureVisible = (r: number, c: number) => {
    const el = scrollRef.current
    if (!el) return
    // Rows sit below the sticky header (ROW_H), which the virtualizer
    // doesn't know about: keep the row clear of it and of the bottom edge.
    const top = r * ROW_H
    const bottom = top + 2 * ROW_H
    if (top < el.scrollTop) el.scrollTop = top
    else if (bottom > el.scrollTop + el.clientHeight) el.scrollTop = bottom - el.clientHeight
    const left = GUTTER + widths.slice(0, c).reduce((a, b) => a + b, 0)
    const right = left + widths[c]
    if (left - GUTTER < el.scrollLeft) el.scrollLeft = left - GUTTER
    else if (right > el.scrollLeft + el.clientWidth) el.scrollLeft = right - el.clientWidth
  }

  useEffect(() => {
    const f = p.focus
    if (!f || f.r >= rowCount) return
    select(f.r, f.c)
    // Wait for the virtualizer to render the row before editing it.
    requestAnimationFrame(() => {
      ensureVisible(f.r, f.c)
      if (f.edit) requestAnimationFrame(() => startEdit(f.r, f.c, undefined, f.text))
    })
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [p.focus?.key])

  const onKeyDown = (e: React.KeyboardEvent) => {
    if (editing || !sel) return
    // WebKitGTK reports Shift+Tab as key "Unidentified"; the code is "Tab".
    const key = e.code === 'Tab' ? 'Tab' : e.key
    if (e.ctrlKey && key === 'Tab') return // switches tabs (Workspace)
    const move = (dr: number, dc: number) => {
      e.preventDefault()
      const r = Math.max(0, Math.min(rowCount - 1, sel.r + dr))
      const c = Math.max(0, Math.min(columns.length - 1, sel.c + dc))
      select(r, c, e.shiftKey && dr ? 'range' : 'set')
      ensureVisible(r, c)
    }
    switch (key) {
      case 'ArrowDown': return move(1, 0)
      case 'ArrowUp': return move(-1, 0)
      case 'ArrowLeft': return move(0, -1)
      case 'ArrowRight': case 'Tab': return move(0, e.shiftKey && key === 'Tab' ? -1 : 1)
      case 'PageDown': return move(20, 0)
      case 'PageUp': return move(-20, 0)
      case 'Home': return move(-rowCount, 0)
      case 'End': return move(rowCount, 0)
      case 'Enter': case 'F2': e.preventDefault(); return startEdit(sel.r, sel.c)
      case 'Delete': case 'Backspace':
        if (!p.readOnly && p.onDeleteRows) { e.preventDefault(); p.onDeleteRows(rows.size ? [...rows] : [sel.r]) }
        return
    }
    if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === 'c') {
      const v = cell(sel.r, sel.c).value
      navigator.clipboard?.writeText(v ?? 'NULL')
      return
    }
    // Typing starts editing with the typed character.
    if (e.key.length === 1 && !e.ctrlKey && !e.metaKey && !e.altKey) {
      e.preventDefault()
      startEdit(sel.r, sel.c, e.key)
    }
  }

  const resize = (c: number) => (e: React.MouseEvent) => {
    e.preventDefault()
    e.stopPropagation()
    const start = e.clientX
    const w0 = widths[c]
    const move = (ev: MouseEvent) => setWidths((w) => w.map((x, i) => (i === c ? Math.max(48, w0 + ev.clientX - start) : x)))
    const up = () => { window.removeEventListener('mousemove', move); window.removeEventListener('mouseup', up) }
    window.addEventListener('mousemove', move)
    window.addEventListener('mouseup', up)
  }

  const sortOf = (name: string) => p.sort?.find((s) => s.column === name)?.dir

  // Columns are virtualised too: only those in (or near) the viewport are
  // drawn. lefts[c] is where column c starts, after the row-number gutter.
  const lefts = useMemo(() => {
    const out = new Array<number>(widths.length + 1)
    out[0] = 0
    for (let i = 0; i < widths.length; i++) out[i + 1] = out[i] + widths[i]
    return out
  }, [widths])
  const [view, setView] = useState({ left: 0, width: 1600 })
  useEffect(() => {
    const el = scrollRef.current
    if (!el) return
    let frame = 0
    const update = () => {
      frame = 0
      setView((v) => (v.left === el.scrollLeft && v.width === el.clientWidth ? v : { left: el.scrollLeft, width: el.clientWidth }))
    }
    const later = () => { if (!frame) frame = requestAnimationFrame(update) }
    update()
    el.addEventListener('scroll', later, { passive: true })
    const ro = new ResizeObserver(later)
    ro.observe(el)
    return () => { el.removeEventListener('scroll', later); ro.disconnect(); cancelAnimationFrame(frame) }
  }, [])
  // The visible range, with a few columns either side so Tab and quick
  // horizontal scrolls don't show gaps; it only changes as columns enter.
  const [first, last] = useMemo(() => {
    const from = Math.max(0, view.left - 300)
    const to = view.left + view.width + 300
    let a = 0
    while (a < widths.length - 1 && lefts[a + 1] <= from) a++
    let b = a
    while (b < widths.length - 1 && lefts[b + 1] < to) b++
    // Keep the edited column drawn even if it scrolls out of view.
    if (editing) { a = Math.min(a, editing.c); b = Math.max(b, editing.c) }
    return [a, b]
  }, [view, lefts, widths.length, editing])

  // Rows call back through a ref, so their props stay equal between renders
  // and React skips the rows that didn't change.
  const handlers = useRef<RowHandlers>(null!)
  handlers.current = {
    mouseDown: (e, r, c) => { if (e.button === 0) select(r, c, e.shiftKey ? 'range' : e.metaKey || e.ctrlKey ? 'toggle' : 'set') },
    gutterDown: (e, r) => select(r, sel?.c ?? 0, e.shiftKey ? 'range' : e.metaKey || e.ctrlKey ? 'toggle' : 'set'),
    doubleClick: (r, c) => startEdit(r, c),
    contextMenu: (e, r, c) => {
      if (!rows.has(r)) select(r, c)
      p.onContextMenu?.(e, r, c, rows.has(r) ? [...rows] : [r])
    },
    follow: (r, c) => p.onFollow?.(r, c),
  }
  const editor = editing && (
    <CellEditor key={editing.c} width={widths[editing.c]} initial={editing.value} wasNull={cell(editing.r, editing.c).value === null}
      spec={columns[editing.c].edit} nullable={columns[editing.c].nullable ?? true}
      onDone={commitEdit} onCancel={() => { setEditing(null); scrollRef.current?.focus() }} />
  )
  const pad = lefts[first]
  const cols = columns.slice(first, last + 1)

  return (
    <div
      ref={scrollRef}
      tabIndex={0}
      onKeyDown={onKeyDown}
      data-testid="grid"
      className="relative h-full overflow-auto bg-panel font-mono text-[12.5px] outline-none"
    >
      <div style={{ width: total, height: virt.getTotalSize() + ROW_H, position: 'relative' }}>
        {/* header */}
        <div className="sticky top-0 z-20 flex h-[30px] border-b border-line bg-panel-2 font-sans text-[12px] font-semibold text-fg-2" style={{ width: total }}>
          <div className="sticky left-0 z-10 shrink-0 border-r border-line bg-panel-2" style={{ width: GUTTER }} />
          {pad > 0 && <div className="shrink-0" style={{ width: pad }} />}
          {cols.map((col, i) => {
            const c = first + i
            return (
              <div
                key={col.name + c}
                role="columnheader"
                title={(col.type ? `${col.name} · ${col.type}` : col.name) + (col.ref ? `\nReferences ${col.ref}` : '')}
                onClick={() => p.onSort?.(col.name)}
                className={clsx('group relative flex shrink-0 items-center gap-1 border-r border-line-2 px-2.5', p.onSort && 'cursor-pointer hover:bg-hover')}
                style={{ width: widths[c] }}
              >
                {col.pk && <KeyRound className="size-3 shrink-0 text-warn" />}
                {col.ref && !col.pk && <Link2 className="size-3 shrink-0 text-accent" aria-label="foreign key" />}
                <span className="truncate">{col.name}</span>
                {col.type && <span className="truncate font-normal text-faint text-[11px]">{shortType(col.type)}</span>}
                <span className="ml-auto shrink-0 text-accent">
                  {sortOf(col.name) === 'asc' && <ArrowUp className="size-3.5" />}
                  {sortOf(col.name) === 'desc' && <ArrowDown className="size-3.5" />}
                </span>
                <div onMouseDown={resize(c)} onClick={(e) => e.stopPropagation()} className="absolute -right-1 top-0 z-10 h-full w-2 cursor-col-resize hover:bg-accent/40" />
              </div>
            )
          })}
        </div>
        {/* rows */}
        {virt.getVirtualItems().map((vr) => {
          const r = vr.index
          return (
            <GridRow key={r} r={r} top={vr.start + ROW_H} total={total} label={(p.rowOffset ?? 0) + r + 1}
              columns={columns} widths={widths} first={first} last={last} pad={pad} numeric={numeric} cell={cell}
              rowSel={rows.has(r)} selCol={sel?.r === r ? sel.c : -1}
              editor={editing?.r === r ? editor : null} editCol={editing?.r === r ? editing.c : -1}
              canFollow={!!p.onFollow} handlers={handlers} />
          )
        })}
      </div>
    </div>
  )
})

interface RowHandlers {
  mouseDown: (e: React.MouseEvent, r: number, c: number) => void
  gutterDown: (e: React.MouseEvent, r: number) => void
  doubleClick: (r: number, c: number) => void
  contextMenu: (e: React.MouseEvent, r: number, c: number) => void
  follow: (r: number, c: number) => void
}

/**
 * GridRow draws one row's visible cells. It is memoised: scrolling and
 * moving the selection only re-render the rows whose props change.
 */
const GridRow = memo(function GridRow({ r, top, total, label, columns, widths, first, last, pad, numeric, cell, rowSel, selCol, editor, editCol, canFollow, handlers }: {
  r: number; top: number; total: number; label: number
  columns: GridColumn[]; widths: number[]; first: number; last: number; pad: number; numeric: boolean[]
  cell: GridProps['cell']; rowSel: boolean; selCol: number; editor: React.ReactNode; editCol: number; canFollow: boolean
  handlers: React.RefObject<RowHandlers>
}) {
  const h = () => handlers.current!
  const cells = []
  for (let c = first; c <= last; c++) {
    if (c === editCol) {
      cells.push(editor)
      continue
    }
    const { value, status } = cell(r, c)
    const isSel = selCol === c
    const ref = columns[c].ref
    cells.push(
      <div
        key={c}
        role="gridcell"
        data-col={c}
        onMouseDown={(e) => h().mouseDown(e, r, c)}
        onDoubleClick={() => h().doubleClick(r, c)}
        onContextMenu={(e) => h().contextMenu(e, r, c)}
        className={clsx('group/cell relative flex shrink-0 items-center overflow-hidden border-r border-line-2 px-2.5',
          status === 'edited' && 'bg-edited', status === 'inserted' && 'bg-inserted',
          status === 'deleted' && 'bg-deleted text-faint line-through',
          rowSel && status === 'clean' && 'bg-selected/60',
          isSel && 'z-[5] outline-2 -outline-offset-2 outline-accent')}
        style={{ width: widths[c] }}
      >
        {value === null
          ? <span className="rounded bg-hover px-1 font-sans text-[10.5px] font-medium uppercase tracking-wide text-faint">null</span>
          : <span className={clsx('truncate', numeric[c] && 'ml-auto tabular-nums')}>{display(value)}</span>}
        {ref && value !== null && canFollow && status !== 'deleted' && (
          <button
            aria-label={ref.includes('(') ? `Open ${ref}` : `Open ${ref} = ${value}`}
            title={ref.includes('(') ? `Open ${ref}` : `Open ${ref} = ${value}`}
            onMouseDown={(e) => e.stopPropagation()}
            onDoubleClick={(e) => e.stopPropagation()}
            onClick={(e) => { e.stopPropagation(); h().follow(r, c) }}
            className={clsx('ml-1 flex size-[18px] shrink-0 items-center justify-center rounded bg-accent/10 font-sans text-accent hover:bg-accent hover:text-accent-fg',
              isSel ? 'flex' : 'hidden group-hover/cell:flex')}
          >
            <ArrowUpRight className="size-3" />
          </button>
        )}
      </div>,
    )
  }
  return (
    <div
      data-row={r}
      className={clsx('absolute left-0 flex border-b border-line-2', r % 2 === 1 && 'bg-zebra')}
      style={{ top, height: ROW_H, width: total }}
    >
      <div className={clsx('sticky left-0 z-10 flex shrink-0 items-center justify-end border-r border-line pr-2 font-sans text-[11px] text-faint',
        rowSel ? 'bg-selected text-accent' : 'bg-panel-2')} style={{ width: GUTTER }}
        onMouseDown={(e) => h().gutterDown(e, r)}
      >
        {label}
      </div>
      {pad > 0 && <div className="shrink-0" style={{ width: pad }} />}
      {cells}
    </div>
  )
})

function CellEditor({ width, initial, wasNull, spec, nullable, onDone, onCancel }: {
  width: number; initial: string; wasNull: boolean; spec?: CellKind; nullable: boolean; onDone: (v: Cell) => void; onCancel: () => void
}) {
  const [v, setV] = useState(initial)
  const ref = useRef<HTMLInputElement>(null)
  const pickerRef = useRef<HTMLDivElement>(null)
  const picker = spec && spec.kind !== 'text' ? spec : null
  // Blur also fires after Enter/Escape unmount the editor; act only once.
  const done = useRef(false)
  const finish = (val: Cell | undefined) => {
    if (done.current) return
    done.current = true
    if (val === undefined) onCancel()
    else onDone(val)
  }
  // Moving the focus into the picker (its time fields) keeps editing.
  const focusOut = (to: EventTarget | null) => {
    if (to instanceof Node && (to === ref.current || pickerRef.current?.contains(to))) return
    finish(v)
  }
  // Choices: the typed text filters them (unless it is one of them); arrows move the highlight.
  const { choices, hi, arrow, picked } = useChoices(picker, v)
  const onKey = (e: React.KeyboardEvent) => {
    if (e.key === 'Enter') { e.preventDefault(); finish(picked) }
    if (e.key === 'Escape') { e.preventDefault(); finish(undefined) }
    // Ctrl+Shift+N stores NULL.
    if (e.key.toLowerCase() === 'n' && e.ctrlKey && e.shiftKey) { e.preventDefault(); finish(null) }
  }
  // Caret at the end: double-click/Enter continue the value, typing a
  // character starts with just that character.
  useEffect(() => {
    const el = ref.current
    if (!el) return
    el.focus()
    el.setSelectionRange(el.value.length, el.value.length)
  }, [initial])
  return (
    <div className="relative z-10 shrink-0" style={{ width }}>
      <input
        ref={ref}
        aria-label="Edit cell"
        value={v}
        placeholder={wasNull ? 'NULL' : ''}
        onChange={(e) => setV(e.target.value)}
        onBlur={(e) => focusOut(e.relatedTarget)}
        onKeyDown={(e) => {
          e.stopPropagation()
          if (!arrow(e)) onKey(e)
        }}
        className="h-[30px] w-full border-2 border-accent bg-panel px-2 font-mono text-[12.5px] shadow-pop outline-none"
      />
      {picker && (
        <CellPicker spec={picker} anchor={ref} pickerRef={pickerRef} value={v} nullable={nullable} choices={choices} highlight={hi}
          onChange={setV} onCommit={finish} onKeyDown={onKey} onFocusOut={focusOut} />
      )}
    </div>
  )
}
