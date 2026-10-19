import clsx from 'clsx'
import {
  ArrowUpRight, ChevronLeft, ChevronRight, Copy, Download, Filter as FilterIcon, ListFilter, Minus, Plus, Rows3, Search, Trash2, Undo2, Upload,
} from 'lucide-react'
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { api, type PageReq } from '../api'
import { useConfirm } from '../components/Confirm'
import { Grid } from '../components/Grid'
import { SqlEditor } from '../components/SqlEditor'
import { Button, IconButton, RefreshButton, Input, Modal, Select, Spinner, errorParts, useMenu, useToast } from '../components/ui'
import { addRow, cellAt, emptyEdits, pendingCount, rowCount, setCell, toChanges, toggleDelete, type Edits } from '../lib/edits'
import { FILTER_OPS, opNeedsValue, type Cell, type Column, type DbObject, type Filter, type ForeignRef, type FilterOp, type Index, type RowsResult, type SessionInfo, type Sort } from '../types'
import { AddColumnDialog } from './AddColumnDialog'
import { ExportDialog } from './ExportDialog'
import { FkLookup } from './FkLookup'
import { ImportCSVDialog } from './ImportCSVDialog'
import { HISTORY_EVENT, notify } from './SidebarPanels'
import { StructureView } from './StructureView'

let filterSeq = 0
const newFilter = (column = '*'): Filter => ({ id: String(++filterSeq), column, op: 'Contains', value: '', value2: '', enabled: true })

export const fmt = (n: number) => n.toLocaleString('en-US')

export function DataView({ session, object, active, tabId, inspector, onDirty, openQuery, onOpenReference, focusFilter }: {
  session: SessionInfo
  object: DbObject
  active: boolean
  tabId: string
  inspector: boolean
  onDirty: (dirty: boolean) => void
  openQuery: (sql: string) => void
  /** onOpenReference opens the row a foreign key value points to. */
  onOpenReference?: (ref: ForeignRef, values: Cell[]) => void
  /** focusFilter shows only rows matching the pairs (a followed foreign key); key re-applies it. */
  focusFilter?: { pairs: { column: string; value: Cell }[]; key: number }
}) {
  const toast = useToast()
  const confirm = useConfirm()
  const menu = useMenu()
  const table = useMemo(() => ({ schema: object.schema, name: object.name }), [object])
  const readOnly = object.kind === 'view'
  const pageSize = session.connection.rows_per_page || 300

  const [mode, setMode] = useState<'data' | 'structure' | 'ddl'>('data')
  const [columns, setColumns] = useState<Column[] | null>(null)
  const [indexes, setIndexes] = useState<Index[]>([])
  const [data, setData] = useState<RowsResult | null>(null)
  const [edits, setEdits] = useState<Edits>(emptyEdits)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<[string, string] | null>(null)
  const [sort, setSort] = useState<Sort[]>([])
  const [offset, setOffset] = useState(0)
  const [quick, setQuick] = useState('')
  // A followed foreign key opens the table filtered to the referenced row.
  const focusFilterOf = (f?: { pairs: { column: string; value: Cell }[] }): Filter[] =>
    (f?.pairs ?? []).map((x) => ({ ...newFilter(x.column), op: x.value === null ? 'IS NULL' : '=', value: x.value ?? '' }))
  const [filters, setFilters] = useState<Filter[]>(() => focusFilterOf(focusFilter))
  const [rawWhere, setRawWhere] = useState('')
  const [showFilters, setShowFilters] = useState(!!focusFilter)
  const [applied, setApplied] = useState<{ filters: Filter[]; rawWhere: string; quick: string }>(() => ({ filters: focusFilterOf(focusFilter), rawWhere: '', quick: '' }))
  const [review, setReview] = useState<string[] | null>(null)
  const [committing, setCommitting] = useState(false)
  const [exportOpen, setExportOpen] = useState(false)
  const [addColumnOpen, setAddColumnOpen] = useState(false)
  const [importOpen, setImportOpen] = useState(false)
  const [newColumn, setNewColumn] = useState<string>()
  const [sel, setSel] = useState<{ r: number; c: number; rows: number[] } | null>(null)
  const [dataKey, setDataKey] = useState(0)
  const abort = useRef<AbortController | null>(null)
  const quickRef = useRef<HTMLInputElement>(null)

  const names = useMemo(() => columns?.map((c) => c.name) ?? [], [columns])
  const pk = useMemo(() => (columns ?? []).map((c, i) => (c.primaryKey ? i : -1)).filter((i) => i >= 0), [columns])
  const pending = pendingCount(edits)
  const onDirtyRef = useRef(onDirty)
  onDirtyRef.current = onDirty
  useEffect(() => {
    onDirtyRef.current(pending > 0)
  }, [pending])

  const pageReq = useCallback((o = offset): PageReq => {
    const fs = applied.filters.filter((f) => f.enabled && (f.value !== '' || !opNeedsValue(f.op) || f.op === '=' || f.op === '<>'))
      .map(({ column, op, value, value2 }) => ({ column, op, value, value2 }))
    if (applied.quick.trim()) fs.unshift({ column: '*', op: 'Contains', value: applied.quick.trim(), value2: '' })
    return { filters: fs, rawWhere: applied.rawWhere, sort, limit: pageSize, offset: o }
  }, [applied, sort, pageSize, offset])

  const load = useCallback(async (o = offset, refreshStructure = false) => {
    abort.current?.abort()
    const ac = new AbortController()
    abort.current = ac
    setLoading(true)
    setError(null)
    try {
      let cols = refreshStructure ? null : columns
      if (!cols) {
        const st = await api.structure(session.id, table)
        cols = st.columns
        setColumns(st.columns)
        setIndexes(st.indexes)
      }
      const res = await api.rows(session.id, table, pageReq(o), cols.map((c) => c.name), ac.signal)
      setData(res)
      setEdits(emptyEdits())
      setDataKey((k) => k + 1)
      setOffset(o)
    } catch (e) {
      if ((e as Error).name !== 'AbortError') setError(errorParts(e))
    } finally {
      if (abort.current === ac) setLoading(false)
    }
  }, [columns, offset, pageReq, session.id, table])

  // Reload when sorting or filters change.
  useEffect(() => { load(0) }, [sort, applied]) // eslint-disable-line react-hooks/exhaustive-deps

  // guard asks before an action that would throw away uncommitted edits,
  // offering to commit them instead — like a desktop app's "unsaved changes".
  const guard = async (fn: () => void) => {
    if (!pending) { fn(); return }
    const r = await confirm({
      title: `Unsaved changes in ${object.name}`,
      message: <>You have <b>{pending}</b> change{pending === 1 ? '' : 's'} that {pending === 1 ? 'isn’t' : 'aren’t'} committed yet. What do you want to do?</>,
      confirmLabel: 'Discard changes', danger: true, altLabel: 'Review & commit…',
    })
    if (r === 'confirm') fn()
    else if (r === 'alt') startReview()
  }

  useEffect(() => {
    const onRefresh = (e: Event) => { if ((e as CustomEvent).detail === tabId) guard(() => load()) }
    window.addEventListener('gotable:refresh', onRefresh)
    return () => window.removeEventListener('gotable:refresh', onRefresh)
  })

  const rows = data?.rows ?? []
  const total = rowCount(rows, edits)
  const cell = useCallback((r: number, c: number) => cellAt(rows, edits, r, c), [rows, edits])

  const startReview = async () => {
    if (!pending) return
    try {
      const r = await api.changes(session.id, table, toChanges(rows, edits, names, pk), pk.length > 0, true)
      setReview(r.statements)
    } catch (e) { toast('error', ...errorParts(e)) }
  }
  const commit = async () => {
    setCommitting(true)
    try {
      const r = await api.changes(session.id, table, toChanges(rows, edits, names, pk), pk.length > 0, false)
      setReview(null)
      toast('ok', `Saved ${r.statements.length} change${r.statements.length === 1 ? '' : 's'}`, object.name)
      notify(HISTORY_EVENT)
      await load()
    } catch (e) {
      toast('error', ...errorParts(e))
    } finally {
      setCommitting(false)
    }
  }

  // Ctrl+S review & commit, Ctrl+F quick search, Ctrl+Shift+F filters (when this tab is active).
  useEffect(() => {
    if (!active) return
    const onKey = (e: KeyboardEvent) => {
      const mod = e.ctrlKey || e.metaKey
      if (mod && e.key.toLowerCase() === 's') { e.preventDefault(); startReview() }
      else if (mod && !e.shiftKey && e.key.toLowerCase() === 'f') { e.preventDefault(); quickRef.current?.focus() }
      else if (mod && e.key === 'Enter' && showFilters) { e.preventDefault(); apply() }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  })

  const apply = () => guard(() => setApplied({ filters, rawWhere, quick }))
  const clearFilters = () => guard(() => { setFilters([]); setRawWhere(''); setQuick(''); setApplied({ filters: [], rawWhere: '', quick: '' }) })
  const activeFilterCount = applied.filters.filter((f) => f.enabled).length + (applied.rawWhere.trim() ? 1 : 0)

  // Following another foreign key into this (already open) table.
  const focusKey = useRef(focusFilter?.key)
  useEffect(() => {
    if (!focusFilter || focusFilter.key === focusKey.current) return
    focusKey.current = focusFilter.key
    guard(() => {
      const fs = focusFilterOf(focusFilter)
      setFilters(fs); setShowFilters(true); setOffset(0); setApplied({ filters: fs, rawWhere: '', quick: '' }); setRawWhere(''); setQuick('')
    })
  }, [focusFilter?.key]) // eslint-disable-line react-hooks/exhaustive-deps

  const toggleSort = (column: string) => guard(() => setSort((s) => {
    const cur = s.find((x) => x.column === column)
    if (!cur) return [{ column, dir: 'asc' }]
    if (cur.dir === 'asc') return [{ column, dir: 'desc' }]
    return []
  }))

  const edit = (r: number, c: number, v: Cell) => setEdits((e) => setCell(rows, e, r, c, v))
  const deleteRows = (rs: number[]) => setEdits((e) => toggleDelete(rows, e, rs))
  const [focus, setFocus] = useState<{ r: number; c: number; edit?: boolean; text?: boolean; key: number }>()
  const insertRow = (values?: Cell[]) => {
    let next = addRow(edits, names.length)
    const r = rows.length + next.inserted.length - 1
    if (values) values.forEach((v, c) => { if (!pk.includes(c) && v !== null) next = setCell(rows, next, r, c, v) })
    setEdits(next)
    // Jump to the new row and start typing in its first non-key column.
    const c = Math.max(0, names.findIndex((_, i) => !pk.includes(i)))
    setFocus({ r, c, edit: !values, key: Date.now() })
  }

  const rowObject = (r: number) => Object.fromEntries(names.map((n, c) => [n, cell(r, c).value]))
  const sqlLiteral = (v: Cell) => (v === null ? 'NULL' : `'${v.replace(/'/g, "''")}'`)
  const cellMenu = (e: React.MouseEvent, r: number, c: number, rs: number[]) => {
    const v = cell(r, c).value
    menu.open(e, [
      { label: 'Copy value', icon: <Copy />, kbd: 'Ctrl C', onSelect: () => navigator.clipboard?.writeText(v ?? 'NULL') },
      { label: rs.length > 1 ? `Copy ${rs.length} rows as JSON` : 'Copy row as JSON', onSelect: () => navigator.clipboard?.writeText(JSON.stringify(rs.length > 1 ? rs.map(rowObject) : rowObject(r), null, 2)) },
      { label: 'Copy row as INSERT', onSelect: () => navigator.clipboard?.writeText(rs.map((x) => `INSERT INTO ${object.name} (${names.join(', ')}) VALUES (${names.map((_, i) => sqlLiteral(cell(x, i).value)).join(', ')});`).join('\n')) },
      'sep',
      ...(columns?.[c]?.references && v !== null && onOpenReference ? (() => {
        const fk = columns[c].references!
        const vs = refValues(r, fk)
        const where = fk.columns.map((k, i) => `${k} = ${vs[i] === null ? 'NULL' : vs[i]!.length > 24 ? vs[i]!.slice(0, 24) + '…' : vs[i]}`).join(', ')
        return [
          { label: `Open ${fk.table} where ${where}`, icon: <ArrowUpRight />, onSelect: () => follow(r, c) },
          ...(readOnly ? [] : [{ label: `Choose from ${fk.table}…`, icon: <Search />, onSelect: () => setFocus({ r, c, edit: true, key: Date.now() }) }]),
          'sep' as const,
        ]
      })() : []),
      { label: `Filter where ${names[c]} = this`, icon: <FilterIcon />, onSelect: () => guard(() => {
        const f = { ...newFilter(names[c]), op: (v === null ? 'IS NULL' : '=') as FilterOp, value: v ?? '' }
        setFilters([f]); setShowFilters(true); setApplied({ filters: [f], rawWhere, quick })
      }) },
      ...(readOnly ? [] : [
        'sep' as const,
        { label: 'Set to NULL', disabled: cell(r, c).status === 'deleted', onSelect: () => edit(r, c, null) },
        { label: 'Set to empty text', disabled: cell(r, c).status === 'deleted', onSelect: () => edit(r, c, '') },
        { label: 'Duplicate row', icon: <Rows3 />, onSelect: () => insertRow(names.map((_, i) => cell(r, i).value)) },
        { label: rs.length > 1 ? `Delete ${rs.length} rows` : 'Delete row', icon: <Trash2 />, kbd: 'Del', danger: true, onSelect: () => deleteRows(rs) },
      ]),
    ])
  }

  const pages = Math.max(1, Math.ceil((data?.total ?? 0) / pageSize))
  const page = Math.floor(offset / pageSize)
  const refName = (f: ForeignRef) => `${f.schema && f.schema !== object.schema ? f.schema + '.' : ''}${f.table}`
  const gridColumns = useMemo(() => (columns ?? []).map((c) => ({ name: c.name, type: c.type, pk: c.primaryKey,
    ref: c.references ? (c.references.columns.length > 1 ? `${refName(c.references)}(${c.references.columns.join(', ')})` : `${refName(c.references)}.${c.references.columns[0]}`) : undefined,
  })), [columns, object.schema]) // eslint-disable-line react-hooks/exhaustive-deps
  // The values of a row's foreign key columns (in key order).
  const refValues = (r: number, fk: ForeignRef) => fk.from.map((f) => cell(r, names.indexOf(f)).value)
  const follow = (r: number, c: number) => {
    const fk = columns?.[c]?.references
    if (!fk) return
    const vs = refValues(r, fk)
    if (vs.some((v) => v !== null)) onOpenReference?.(fk, vs)
  }
  const [lookup, setLookup] = useState<{ r: number; c: number; rect: DOMRect } | null>(null)
  const pickRef = (values: Cell[]) => {
    if (!lookup) return
    const fk = columns![lookup.c].references!
    let next = edits
    fk.from.forEach((f, i) => { next = setCell(rows, next, lookup.r, names.indexOf(f), values[i]) })
    setEdits(next)
    setLookup(null)
  }

  return (
    <div className="flex h-full flex-col" data-testid={`data-${object.name}`}>
      {mode === 'data' && (
        <>
          {/* Toolbar */}
          <div className="flex h-11 shrink-0 items-center gap-1.5 border-b border-line px-2">
            <Button size="sm" variant={showFilters || activeFilterCount ? 'subtle' : 'ghost'} icon={<ListFilter className="size-3.5" />}
              onClick={() => { setShowFilters(!showFilters); if (!filters.length) setFilters([newFilter()]) }}>
              Filter{activeFilterCount ? ` · ${activeFilterCount}` : ''}
            </Button>
            <Input ref={quickRef} icon={<Search />} className="w-64" value={quick} placeholder={`Search ${object.name}…`} aria-label="Quick search"
              onChange={(e) => setQuick(e.target.value)} onKeyDown={(e) => { if (e.key === 'Enter') apply(); if (e.key === 'Escape' && quick) { setQuick(''); guard(() => setApplied({ ...applied, quick: '' })) } }} />
            {applied.quick && <button className="text-[12px] text-accent hover:underline" onClick={() => { setQuick(''); guard(() => setApplied({ ...applied, quick: '' })) }}>clear</button>}
            <div className="flex-1" />
            {loading && <Spinner />}
            {!readOnly && <>
              <Button size="sm" variant="ghost" icon={<Plus className="size-3.5" />} onClick={() => insertRow()}>Row</Button>
              <Button size="sm" variant="ghost" icon={<Trash2 className="size-3.5" />} disabled={!sel} onClick={() => sel && deleteRows(sel.rows)}>Delete</Button>
            </>}
            <RefreshButton label="Refresh (Ctrl+R)" busy={loading} onRefresh={() => guard(() => load())} />
            {!readOnly && <Button size="sm" variant="ghost" icon={<Upload className="size-3.5" />} onClick={() => guard(() => setImportOpen(true))}>Import</Button>}
            <Button size="sm" variant="ghost" icon={<Download className="size-3.5" />} onClick={() => setExportOpen(true)}>Export</Button>
          </div>

          {showFilters && (
            <div className="shrink-0 space-y-1.5 border-b border-line bg-panel-2 px-2.5 py-2 animate-fade" aria-label="Filters">
              {filters.map((f, i) => (
                <div key={f.id} className="flex items-center gap-1.5">
                  <input type="checkbox" aria-label="Enable filter" checked={f.enabled} className="accent-[var(--accent)]"
                    onChange={(e) => setFilters(filters.map((x) => (x.id === f.id ? { ...x, enabled: e.target.checked } : x)))} />
                  <Select aria-label="Filter column" className="w-44" value={f.column} onChange={(e) => setFilters(filters.map((x) => (x.id === f.id ? { ...x, column: e.target.value } : x)))}>
                    <option value="*">Any column</option>
                    {names.map((n) => <option key={n} value={n}>{n}</option>)}
                  </Select>
                  <Select aria-label="Filter operator" className="w-36" value={f.op} onChange={(e) => setFilters(filters.map((x) => (x.id === f.id ? { ...x, op: e.target.value as FilterOp } : x)))}>
                    {FILTER_OPS.map((o) => <option key={o}>{o}</option>)}
                  </Select>
                  {opNeedsValue(f.op) && <Input aria-label="Filter value" className="flex-1" value={f.value} placeholder={f.op.includes('IN') ? 'a, b, c' : 'value'}
                    onChange={(e) => setFilters(filters.map((x) => (x.id === f.id ? { ...x, value: e.target.value } : x)))} onKeyDown={(e) => e.key === 'Enter' && apply()} />}
                  {f.op === 'BETWEEN' && <Input aria-label="Filter second value" className="flex-1" value={f.value2} placeholder="and"
                    onChange={(e) => setFilters(filters.map((x) => (x.id === f.id ? { ...x, value2: e.target.value } : x)))} onKeyDown={(e) => e.key === 'Enter' && apply()} />}
                  {!opNeedsValue(f.op) && <div className="flex-1" />}
                  <IconButton label="Remove filter" onClick={() => setFilters(filters.filter((x) => x.id !== f.id))}><Minus className="size-3.5" /></IconButton>
                  <IconButton label="Add filter" onClick={() => setFilters([...filters.slice(0, i + 1), newFilter(), ...filters.slice(i + 1)])}><Plus className="size-3.5" /></IconButton>
                </div>
              ))}
              <div className="flex items-center gap-1.5">
                <span className="w-[13px]" />
                <span className="w-[calc(11rem+0.375rem+9rem)] shrink-0 font-mono text-[11.5px] font-semibold text-muted">WHERE (raw SQL, optional)</span>
                <Input mono className="flex-1" aria-label="Raw WHERE" value={rawWhere} placeholder="created_at > now() - interval '7 days'" onChange={(e) => setRawWhere(e.target.value)} onKeyDown={(e) => e.key === 'Enter' && apply()} />
                <Button size="sm" variant="ghost" onClick={clearFilters}>Clear</Button>
                <Button size="sm" variant="primary" onClick={apply} kbd="Ctrl ↵">Apply</Button>
              </div>
            </div>
          )}

          {pending > 0 && (
            <div className="flex h-10 shrink-0 items-center gap-2 border-b border-warn/30 bg-warn/10 px-3 text-[12.5px] animate-fade" role="status">
              <span className="size-2 rounded-full bg-warn" />
              <span><b>{pending}</b> unsaved change{pending === 1 ? '' : 's'} — nothing is written until you commit.</span>
              <div className="flex-1" />
              <Button size="sm" variant="ghost" icon={<Undo2 className="size-3.5" />} onClick={() => setEdits(emptyEdits())}>Discard</Button>
              <Button size="sm" variant="primary" onClick={startReview} kbd="Ctrl S">Review & commit</Button>
            </div>
          )}

          <div className="flex min-h-0 flex-1">
            <div className="min-w-0 flex-1">
              {error ? (
                <div className="flex h-full flex-col items-center justify-center gap-2 p-6 text-center">
                  <div className="text-[14px] font-semibold text-err">{error[0]}</div>
                  <div className="max-w-xl break-words text-[12.5px] text-muted">{error[1]}</div>
                  <Button size="sm" className="mt-2" onClick={() => load()}>Try again</Button>
                </div>
              ) : columns && data ? (
                total === 0 ? (
                  <div className="flex h-full flex-col items-center justify-center gap-2 text-center text-muted">
                    <Rows3 className="size-8 text-faint" />
                    <div className="text-[13px] font-medium text-fg-2">{activeFilterCount || applied.quick ? 'No rows match your filter' : 'This table is empty'}</div>
                    <div className="flex gap-2">
                      {(activeFilterCount > 0 || applied.quick) && <Button size="sm" onClick={clearFilters}>Clear filter</Button>}
                      {!readOnly && <Button size="sm" variant="primary" icon={<Plus className="size-3.5" />} onClick={() => insertRow()}>Add a row</Button>}
                    </div>
                  </div>
                ) : (<>
                  <Grid columns={gridColumns} rowCount={total} cell={cell} rowOffset={offset} sort={sort} onSort={toggleSort}
                    readOnly={readOnly} onEdit={edit} onDeleteRows={deleteRows} onContextMenu={cellMenu} dataKey={dataKey} onFollow={follow} onLookup={(r, c, rect) => setLookup({ r, c, rect })}
                    onSelectionChange={(r, c, rs) => setSel({ r, c, rows: rs })} focus={focus} />
                  {lookup && columns?.[lookup.c]?.references && (
                    <FkLookup sid={session.id} fk={columns[lookup.c].references!} anchor={lookup.rect}
                      current={refValues(lookup.r, columns[lookup.c].references!)}
                      nullable={columns[lookup.c].references!.from.every((f) => columns.find((x) => x.name === f)?.nullable)}
                      onPick={pickRef} onClose={() => setLookup(null)}
                      onText={() => { const l = lookup; setLookup(null); setFocus({ r: l.r, c: l.c, edit: true, text: true, key: Date.now() }) }} />
                  )}
                </>)
              ) : (
                <div className="flex h-full items-center justify-center"><Spinner className="size-5" /></div>
              )}
            </div>
            {inspector && columns && (
              <Inspector columns={columns} row={sel?.r ?? null} cell={cell} readOnly={readOnly} onEdit={edit} />
            )}
          </div>
        </>
      )}
      {mode !== 'data' && (
        <div className="min-h-0 flex-1">
          {mode === 'structure'
            ? <StructureView columns={columns ?? []} indexes={indexes} highlight={newColumn}
                onAddColumn={readOnly ? undefined : () => guard(() => setAddColumnOpen(true))} />
            : <DefinitionView session={session} object={object} />}
        </div>
      )}

      {/* Bottom bar */}
      <div className="flex h-9 shrink-0 items-center gap-2 border-t border-line bg-panel-2 px-2">
        <div className="flex rounded-lg border border-line bg-panel p-0.5" role="tablist" aria-label="View">
          {(['data', 'structure', 'ddl'] as const).map((m) => (
            <button key={m} role="tab" aria-selected={mode === m} onClick={() => setMode(m)}
              className={clsx('h-6 rounded-md px-2.5 text-[12px] font-medium', mode === m ? 'bg-accent text-accent-fg' : 'text-muted hover:text-fg')}>
              {m === 'data' ? 'Data' : m === 'structure' ? 'Structure' : 'Definition'}
            </button>
          ))}
        </div>
        {mode === 'data' && data && (
          <>
            <span className="text-[12px] text-muted">
              {data.total === 0 ? 'No rows' : `${fmt(offset + 1)}–${fmt(offset + data.rows.length)} of ${fmt(data.total)} rows`}
              {activeFilterCount + (applied.quick ? 1 : 0) > 0 && <span className="ml-1.5 rounded bg-accent-soft px-1.5 py-0.5 text-[11px] font-medium text-accent">filtered</span>}
            </span>
            <div className="flex-1" />
            {!pk.length && !readOnly && <span className="text-[11.5px] text-warn" title="Edits match rows on every column">No primary key</span>}
            <span className="text-[11.5px] text-faint">{data.ms} ms</span>
            <div className="flex items-center gap-0.5">
              <IconButton label="Previous page" disabled={page === 0} onClick={() => guard(() => load(offset - pageSize))}><ChevronLeft className="size-4" /></IconButton>
              <span className="min-w-16 text-center text-[12px] text-muted">{page + 1} / {fmt(pages)}</span>
              <IconButton label="Next page" disabled={page >= pages - 1} onClick={() => guard(() => load(offset + pageSize))}><ChevronRight className="size-4" /></IconButton>
            </div>
          </>
        )}
        {mode !== 'data' && <div className="flex-1" />}
        <Button size="sm" variant="ghost" onClick={() => openQuery(`SELECT *\nFROM ${object.name}\nLIMIT 100;`)}>Open in SQL</Button>
      </div>

      {review && (
        <Modal width={720} title="Review changes" subtitle={`${review.length} statement${review.length === 1 ? '' : 's'} will run in one transaction on ${object.name}.`}
          onClose={() => setReview(null)}
          footer={<>
            {session.connection.tag === 'production' && <span className="mr-auto text-[12px] font-medium text-err">⚠ This is PRODUCTION.</span>}
            {!pk.length && <span className="mr-auto text-[12px] text-warn">No primary key: rows are matched on every column.</span>}
            <Button variant="ghost" onClick={() => setReview(null)}>Cancel</Button>
            <Button variant={session.connection.tag === 'production' ? 'danger' : 'primary'} loading={committing} onClick={commit} autoFocus>Commit</Button>
          </>}>
          <div className="h-[280px] overflow-hidden rounded-lg border border-line">
            <SqlEditor value={review.join('\n')} driver={session.connection.driver} readOnly />
          </div>
        </Modal>
      )}
      {addColumnOpen && columns && (
        <AddColumnDialog session={session} table={table} columns={columns} onClose={() => setAddColumnOpen(false)}
          onAdded={(name) => { setAddColumnOpen(false); setNewColumn(name); load(0, true) }} />
      )}
      {importOpen && columns && (
        <ImportCSVDialog session={session} table={table} columns={columns} onClose={() => setImportOpen(false)} onImported={() => load(0)} />
      )}
      {exportOpen && <ExportDialog session={session} title={object.name} onClose={() => setExportOpen(false)}
        source={{ table, page: pageReq(), columns: names }} />}
      {menu.node}
    </div>
  )
}

function Inspector({ columns, row, cell, readOnly, onEdit }: {
  columns: Column[]; row: number | null; cell: (r: number, c: number) => { value: Cell; status: string }; readOnly: boolean; onEdit: (r: number, c: number, v: Cell) => void
}) {
  const [q, setQ] = useState('')
  return (
    <aside className="flex w-[300px] shrink-0 flex-col border-l border-line bg-panel-2" aria-label="Row inspector">
      <div className="flex h-10 items-center gap-2 border-b border-line px-3">
        <span className="text-[12.5px] font-semibold">Row</span>
        {row !== null && <span className="text-[12px] text-muted">#{row + 1}</span>}
        <div className="flex-1" />
        <Input className="w-32" icon={<Search />} value={q} placeholder="field" onChange={(e) => setQ(e.target.value)} />
      </div>
      {row === null ? (
        <div className="p-6 text-center text-[12.5px] text-muted">Select a row to see every field here.</div>
      ) : (
        <div className="min-h-0 flex-1 space-y-2.5 overflow-y-auto p-3">
          {columns.map((col, c) => {
            if (q && !col.name.toLowerCase().includes(q.toLowerCase())) return null
            const { value, status } = cell(row, c)
            return (
              <div key={col.name}>
                <div className="mb-1 flex items-center gap-1.5 text-[11.5px]">
                  <span className="font-semibold text-fg-2">{col.name}</span>
                  <span className="text-faint">{col.type.toLowerCase()}</span>
                  {status === 'edited' && <span className="text-warn">edited</span>}
                  {!readOnly && status !== 'deleted' && value !== null && col.nullable && (
                    <button className="ml-auto text-[11px] text-muted hover:text-accent" onClick={() => onEdit(row, c, null)}>set NULL</button>
                  )}
                </div>
                <InspectorField value={value} readOnly={readOnly || status === 'deleted'} onCommit={(v) => onEdit(row, c, v)} />
              </div>
            )
          })}
        </div>
      )}
    </aside>
  )
}

function InspectorField({ value, readOnly, onCommit }: { value: Cell; readOnly: boolean; onCommit: (v: Cell) => void }) {
  const [v, setV] = useState(value ?? '')
  useEffect(() => setV(value ?? ''), [value])
  return (
    <textarea
      value={v}
      readOnly={readOnly}
      placeholder={value === null ? 'NULL' : ''}
      rows={Math.min(6, Math.max(1, Math.ceil((v.length || 1) / 34)))}
      onChange={(e) => setV(e.target.value)}
      onBlur={() => { if ((value ?? '') !== v || (value === null && v !== '')) onCommit(v) }}
      onKeyDown={(e) => { if (e.key === 'Enter' && (e.ctrlKey || e.metaKey)) (e.target as HTMLTextAreaElement).blur(); if (e.key === 'Escape') { setV(value ?? ''); (e.target as HTMLTextAreaElement).blur() } }}
      className="w-full resize-none rounded-lg border border-line bg-panel px-2 py-1.5 font-mono text-[12px] placeholder:italic placeholder:text-faint focus:border-accent focus:outline-none"
    />
  )
}

function DefinitionView({ session, object }: { session: SessionInfo; object: DbObject }) {
  const [ddl, setDdl] = useState<string | null>(null)
  const [err, setErr] = useState('')
  useEffect(() => {
    api.definition(session.id, object, object.kind).then((r) => setDdl(r.ddl)).catch((e) => setErr(errorParts(e).join(': ')))
  }, [session.id, object])
  if (err) return <div className="p-6 text-[12.5px] text-err">{err}</div>
  if (ddl === null) return <div className="flex h-full items-center justify-center"><Spinner /></div>
  return (
    <div className="relative h-full">
      <SqlEditor value={ddl} driver={session.connection.driver} readOnly />
      <Button size="sm" className="absolute right-3 top-3" icon={<Copy className="size-3.5" />} onClick={() => navigator.clipboard?.writeText(ddl)}>Copy</Button>
    </div>
  )
}

