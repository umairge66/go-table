import clsx from 'clsx'
import { AlertTriangle, ArrowDown, ArrowUp, Check as CheckIcon, Copy, KeyRound, Plus, Settings2, SlidersHorizontal, Table2, Trash2 } from 'lucide-react'
import { useEffect, useRef, useState, type ReactNode } from 'react'
import { api, ApiError, type NewColumn, type TypeInfo } from '../api'
import { Button, IconButton, Select, Switch, errorParts, useToast } from '../components/ui'
import { TypeSelect } from '../components/TypeSelect'
import { category, findType, groupOf, has, paramProblem, typeString, type TypeSpec } from '../lib/sqlTypes'
import type { Driver, SessionInfo } from '../types'
import { EXPRESSIONS } from './ColumnDialog'

type IndexKind = '' | 'primary' | 'unique' | 'index' | 'fulltext'

interface Row {
  key: number
  name: string
  spec: TypeSpec
  nullable: boolean
  defaultKind: NewColumn['defaultKind']
  default: string
  index: IndexKind
  autoIncrement: boolean
  collation: string
  onUpdateNow: boolean
  comment: string
  open: boolean // "more options" shown
}

const ID: Record<Driver, TypeSpec> = {
  postgres: { base: 'bigint', param: '', unsigned: false, array: false, custom: false },
  mysql: { base: 'INT', param: '', unsigned: true, array: false, custom: false },
  mariadb: { base: 'INT', param: '', unsigned: true, array: false, custom: false },
  sqlite: { base: 'INTEGER', param: '', unsigned: false, array: false, custom: false },
}
const TEXT: Record<Driver, TypeSpec> = {
  postgres: { base: 'text', param: '', unsigned: false, array: false, custom: false },
  mysql: { base: 'VARCHAR', param: '255', unsigned: false, array: false, custom: false },
  mariadb: { base: 'VARCHAR', param: '255', unsigned: false, array: false, custom: false },
  sqlite: { base: 'TEXT', param: '', unsigned: false, array: false, custom: false },
}

/** Colour per type family, readable in light and dark themes. */
const FAMILY: Record<ReturnType<typeof category>, { color: string; label: string }> = {
  number: { color: '#3b82f6', label: 'number' },
  text: { color: '#10b981', label: 'text' },
  time: { color: '#f59e0b', label: 'date/time' },
  json: { color: '#a855f7', label: 'JSON' },
  bool: { color: '#14b8a6', label: 'boolean' },
  other: { color: '#94a3b8', label: 'other' },
}

let rowSeq = 0
const newRow = (r: Partial<Row>): Row => ({
  key: ++rowSeq, name: '', spec: { base: '', param: '', unsigned: false, array: false, custom: true }, nullable: true, defaultKind: '', default: '',
  index: '', autoIncrement: false, collation: '', onUpdateNow: false, comment: '', open: false, ...r,
})

// name · type · length · default · NULL · AI · index · actions
const GRID = 'grid grid-cols-[22px_minmax(130px,1.3fr)_minmax(130px,1fr)_88px_minmax(200px,1.5fr)_56px_44px_124px_64px] items-center gap-2'

/**
 * CreateTableView is the "New table" tab, modelled on phpMyAdmin's create
 * table page: a row per column (name, type from the database's list,
 * length/values, default, NULL, auto-increment, index) with more options per
 * column, table options, and the exact SQL shown live. Ctrl+Enter creates it.
 */
export function CreateTableView({ session, schema, label, existing, active, onClose, onDirty, onCreated }: {
  session: SessionInfo
  schema: string
  /** label names the database the table goes into. */
  label: string
  existing: string[]
  active: boolean
  onClose: () => void
  /** onDirty reports whether there is a design that closing would lose. */
  onDirty: (dirty: boolean) => void
  onCreated: (name: string) => void
}) {
  const toast = useToast()
  const driver = session.connection.driver
  const mysql = driver === 'mysql' || driver === 'mariadb'
  const [info, setInfo] = useState<TypeInfo | null>(null)
  const [name, setName] = useState('')
  const [comment, setComment] = useState('')
  const [engine, setEngine] = useState('')
  const [charset, setCharset] = useState('')
  const [collation, setCollation] = useState('')
  const [strict, setStrict] = useState(false)
  const [rows, setRows] = useState<Row[]>(() => [
    newRow({ name: 'id', spec: ID[driver], nullable: false, index: 'primary', autoIncrement: true }),
    newRow({ spec: TEXT[driver] }),
  ])
  const [addCount, setAddCount] = useState(1)
  const [preview, setPreview] = useState<string[]>([])
  const [problem, setProblem] = useState('')
  const [saving, setSaving] = useState(false)
  const [copied, setCopied] = useState(false)
  const firstNew = useRef<number | null>(null)
  const nameRefs = useRef(new Map<number, HTMLInputElement>())

  useEffect(() => {
    api.typeInfo(session.id, schema).then((i) => {
      setInfo(i)
      if (mysql) { setEngine((e) => e || i.defaultEngine); setCharset((c) => c || i.defaultCharset); setCollation((c) => c || i.defaultCollation) }
    }).catch(() => setInfo({ customTypes: [], collations: [], charsets: [], engines: [], defaultEngine: '', defaultCharset: '', defaultCollation: '', strict: false }))
  }, [session.id, schema, mysql])
  const custom = info?.customTypes ?? []

  const set = (key: number, patch: Partial<Row>) => setRows((rs) => rs.map((r) => {
    if (r.key !== key) return r
    const n = { ...r, ...patch }
    if (patch.index === 'primary' || patch.autoIncrement) n.nullable = false
    if (patch.autoIncrement) { n.defaultKind = ''; if (!n.index) n.index = 'primary' }
    if (patch.spec && !has(findType(driver, patch.spec.base, custom), 'int')) n.autoIncrement = false
    if (patch.spec && !has(findType(driver, patch.spec.base, custom), 'time')) n.onUpdateNow = false
    return n
  }))
  const move = (key: number, by: number) => setRows((rs) => {
    const i = rs.findIndex((r) => r.key === key)
    const j = i + by
    if (j < 0 || j >= rs.length) return rs
    const next = [...rs]
    ;[next[i], next[j]] = [next[j], next[i]]
    return next
  })
  const addRows = (n = addCount) => setRows((rs) => {
    const added = Array.from({ length: Math.max(1, Math.min(50, n)) }, () => newRow({ spec: TEXT[driver] }))
    firstNew.current = added[0].key
    return [...rs, ...added]
  })
  useEffect(() => {
    if (firstNew.current !== null) { nameRefs.current.get(firstNew.current)?.focus(); firstNew.current = null }
  }, [rows.length])

  // Rows without a name are ignored, so spare empty rows are harmless.
  const named = rows.filter((r) => r.name.trim())
  const columns: NewColumn[] = named.map((r) => ({
    name: r.name, type: typeString(r.spec), nullable: r.nullable, defaultKind: r.defaultKind, default: r.default, comment: r.comment, position: '',
    collation: r.collation.trim(), onUpdateNow: r.onUpdateNow, primaryKey: r.index === 'primary', autoIncrement: r.autoIncrement,
    index: r.index === 'primary' ? '' : r.index,
  }))
  const table = { name, columns, comment: driver === 'sqlite' ? '' : comment, engine, charset, collation, strict }
  const taken = existing.some((t) => t.toLowerCase() === name.trim().toLowerCase())
  // Problems the page can explain on the spot (the server checks the rest).
  const rowProblem = (r: Row) => (r.spec.custom ? (!r.spec.base.trim() ? 'type the column type' : '') : paramProblem(findType(driver, r.spec.base, custom), r.spec.param))
  const localProblem = taken ? `A table named “${name.trim()}” already exists.`
    : named.map((r) => (rowProblem(r) ? `${r.name.trim()}: ${rowProblem(r)}` : '')).find(Boolean) ?? ''

  useEffect(() => {
    if (!name.trim() || !columns.length || localProblem) { setPreview([]); setProblem(localProblem); return }
    const t = setTimeout(async () => {
      try {
        setPreview((await api.createTable(session.id, schema, table, true)).statements)
        setProblem('')
      } catch (e) {
        setPreview([])
        setProblem((e as ApiError).detail ?? errorParts(e)[1])
      }
    }, 200)
    return () => clearTimeout(t)
  }, [JSON.stringify(table), session.id, schema, localProblem]) // eslint-disable-line react-hooks/exhaustive-deps

  const create = async () => {
    setSaving(true)
    try {
      await api.createTable(session.id, schema, table, false)
      toast('ok', `Table “${name.trim()}” created`, label)
      onCreated(name.trim())
    } catch (e) {
      setProblem(errorParts(e).filter(Boolean).join(' — '))
    } finally {
      setSaving(false)
    }
  }

  const canCreate = !!name.trim() && columns.length > 0 && !problem && preview.length > 0
  const production = session.connection.tag === 'production'

  // Something typed is worth a question before the tab closes.
  const touched = !!name.trim() || !!comment.trim() || rows.length !== 2 || !!rows[1]?.name.trim()
  const onDirtyRef = useRef(onDirty)
  onDirtyRef.current = onDirty
  useEffect(() => onDirtyRef.current(touched), [touched])

  const createRef = useRef(() => {})
  createRef.current = () => { if (canCreate && !saving) create() }
  useEffect(() => {
    if (!active) return
    const onKey = (e: KeyboardEvent) => {
      if ((e.ctrlKey || e.metaKey) && e.key === 'Enter') { e.preventDefault(); createRef.current() }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [active])

  const pk = named.filter((r) => r.index === 'primary').map((r) => r.name.trim())
  const indexes = named.filter((r) => r.index && r.index !== 'primary').length
  const field = 'h-8 w-full min-w-0 rounded-lg border border-transparent bg-transparent px-2 text-[13px] transition-colors hover:border-line focus:border-accent focus:bg-panel focus:outline-none disabled:opacity-40 disabled:hover:border-transparent'
  const head = 'text-[10.5px] font-semibold uppercase tracking-wider text-faint'

  return (
    <div className="flex h-full flex-col bg-bg" data-testid="create-table">
      {/* Top bar */}
      <div className="flex h-12 shrink-0 items-center gap-2.5 border-b border-line bg-panel px-4">
        <span className="flex size-7 items-center justify-center rounded-lg bg-accent-soft text-accent"><Table2 className="size-4" /></span>
        <div className="flex min-w-0 items-center gap-1.5 text-[13px]">
          <span className="truncate text-muted">{label}</span>
          <span className="text-faint">›</span>
          <span className="font-semibold">New table</span>
        </div>
        {production && <span className="ml-2 flex items-center gap-1.5 rounded-full bg-err/10 px-2.5 py-1 text-[11.5px] font-semibold text-err"><AlertTriangle className="size-3.5" /> PRODUCTION</span>}
        <div className="ml-auto flex items-center gap-2">
          <Button variant="ghost" onClick={onClose}>Cancel</Button>
          <Button variant={production ? 'danger' : 'primary'} disabled={!canCreate} loading={saving} onClick={create} kbd="Ctrl ↵">Create table</Button>
        </div>
      </div>

      <div className="min-h-0 flex-1 overflow-y-auto">
        <form className="px-8 py-7" aria-label="New table form" onSubmit={(e) => { e.preventDefault(); if (canCreate) create() }}>
          <div className="mx-auto max-w-[1240px] space-y-6">
            <div>
              <input autoFocus value={name} placeholder="Untitled table" aria-label="Table name" spellCheck={false}
                onChange={(e) => setName(e.target.value)}
                className={clsx('w-full bg-transparent text-[26px] font-semibold tracking-tight placeholder:text-faint focus:outline-none', taken && 'text-err')} />
              {driver !== 'sqlite'
                ? <input value={comment} placeholder="Add a description (table comment)…" aria-label="Table comment" onChange={(e) => setComment(e.target.value)}
                    className="mt-1 w-full bg-transparent text-[13.5px] text-fg-2 placeholder:text-faint focus:outline-none" />
                : <div className="mt-1 text-[13px] text-faint">SQLite table in {label}</div>}
            </div>

            <section className="overflow-hidden rounded-2xl border border-line bg-panel shadow-[0_1px_2px_rgba(0,0,0,.04)]" aria-label="Column designer">
              <div className="flex items-center gap-2 border-b border-line-2 px-4 py-3">
                <h2 className="text-[13.5px] font-semibold">Columns</h2>
                <span className="rounded-full bg-hover px-2 py-0.5 text-[11px] font-medium text-muted">{named.length}</span>
                <div className="ml-auto flex items-center gap-1.5 text-[12.5px]">
                  <span className="text-muted">Add</span>
                  <input type="number" min={1} max={50} value={addCount} aria-label="How many columns to add"
                    onChange={(e) => setAddCount(Number(e.target.value) || 1)}
                    className="h-7 w-12 rounded-md border border-line bg-panel text-center text-[12.5px] focus:border-accent focus:outline-none" />
                  <Button type="button" size="sm" icon={<Plus className="size-3.5" />} onClick={() => addRows()}>column{addCount === 1 ? '' : 's'}</Button>
                </div>
              </div>

              <div className={clsx(GRID, 'border-b border-line-2 bg-panel-2/60 px-4 py-2')}>
                <span /><span className={head}>Name</span><span className={head}>Type</span><span className={head}>Length</span>
                <span className={head}>Default</span>
                <span className={clsx(head, 'text-center')}>Null</span>
                <span className={clsx(head, 'text-center')} title="Auto-increment">AI</span>
                <span className={head}>Index</span>
                <span />
              </div>

              <div role="list" aria-label="Columns">
                {rows.map((r, idx) => {
                  const t = r.spec.custom ? undefined : findType(driver, r.spec.base, custom)
                  const fam = FAMILY[category(r.spec.custom ? '' : groupOf(driver, r.spec.base, custom))]
                  const prob = r.name.trim() ? rowProblem(r) : ''
                  const exprs = [...(has(t, 'time') && !EXPRESSIONS[driver].includes('CURRENT_TIMESTAMP') ? ['CURRENT_TIMESTAMP'] : []), ...EXPRESSIONS[driver]]
                  const primary = r.index === 'primary'
                  return (
                    <div key={r.key} role="listitem" aria-label={`Column ${idx + 1}`}
                      className={clsx('group/row border-b border-line-2 px-4 py-2 transition-colors last:border-b-0', r.open ? 'bg-accent-soft/40' : 'hover:bg-hover/50')}>
                      <div className={GRID}>
                        <span className="flex justify-center" title={primary ? 'Primary key' : fam.label}>
                          {primary ? <KeyRound className="size-3.5 text-warn" /> : <span className="size-2 rounded-full" style={{ background: fam.color }} />}
                        </span>
                        <input ref={(el) => { if (el) nameRefs.current.set(r.key, el); else nameRefs.current.delete(r.key) }}
                          className={clsx(field, 'font-mono font-medium')} value={r.name} placeholder="column_name" aria-label="Column name" spellCheck={false}
                          onChange={(e) => set(r.key, { name: e.target.value })}
                          onKeyDown={(e) => { if (e.key === 'Enter' && idx === rows.length - 1 && r.name.trim()) { e.preventDefault(); addRows(1) } }} />
                        <TypeSelect driver={driver} value={r.spec} custom={custom} onChange={(spec) => set(r.key, { spec })} />
                        <input className={clsx(field, 'font-mono', prob && '!border-err')} aria-label="Length/Values" disabled={r.spec.custom || !t?.param}
                          value={r.spec.custom || !t?.param ? '' : r.spec.param}
                          placeholder={t?.param ? (t.param.kind === 'fsp' ? '0–6' : t.param.placeholder) : '—'}
                          onChange={(e) => set(r.key, { spec: { ...r.spec, param: e.target.value } })} />
                        <div className="flex min-w-0 gap-1">
                          <Select className="w-[80px] shrink-0" selectClassName="rounded-lg pl-2 pr-6 text-[12.5px]" value={r.defaultKind} aria-label="Default kind" disabled={r.autoIncrement}
                            onChange={(e) => set(r.key, { defaultKind: e.target.value as NewColumn['defaultKind'] })}>
                            <option value="">None</option><option value="value">Value</option><option value="expression">Expr</option><option value="null">NULL</option>
                          </Select>
                          {(r.defaultKind === 'value' || r.defaultKind === 'expression') && (
                            <input className={clsx(field, 'border-line font-mono')} value={r.default} aria-label="Default value"
                              list={r.defaultKind === 'expression' ? `exprs-${r.key}` : undefined}
                              placeholder={r.defaultKind === 'value' ? 'value' : exprs[0] ?? 'expression'} onChange={(e) => set(r.key, { default: e.target.value })} />
                          )}
                          <datalist id={`exprs-${r.key}`}>{exprs.map((x) => <option key={x} value={x} />)}</datalist>
                        </div>
                        <Pill label="Allow NULL" text="NULL" checked={r.nullable} disabled={primary} onChange={(v) => set(r.key, { nullable: v })} />
                        <Pill label="Auto-increment" text="AI" checked={r.autoIncrement} disabled={!has(t, 'int') && !r.autoIncrement} onChange={(v) => set(r.key, { autoIncrement: v })} />
                        <Select selectClassName={clsx('rounded-lg text-[12px] font-semibold tracking-wide', INDEX_STYLE[r.index])} value={r.index} aria-label="Index"
                          onChange={(e) => set(r.key, { index: e.target.value as IndexKind })}>
                          <option value="">—</option>
                          <option value="primary">PRIMARY</option>
                          <option value="unique">UNIQUE</option>
                          <option value="index">INDEX</option>
                          {mysql && <option value="fulltext">FULLTEXT</option>}
                        </Select>
                        <div className="flex justify-end gap-0.5">
                          <IconButton label={r.open ? 'Fewer options' : 'More options'} active={r.open} onClick={() => set(r.key, { open: !r.open })}>
                            <SlidersHorizontal className="size-3.5" />
                          </IconButton>
                          <IconButton label="Remove column" disabled={rows.length === 1} className="opacity-0 transition-opacity group-hover/row:opacity-100 focus-visible:opacity-100 hover:!text-err"
                            onClick={() => setRows((rs) => rs.filter((x) => x.key !== r.key))}><Trash2 className="size-3.5" /></IconButton>
                        </div>
                      </div>
                      {prob && <div className="mt-1 pl-[30px] text-[11.5px] font-medium text-err">{prob}</div>}
                      {r.open && (
                        <div className="mt-2 ml-[30px] flex flex-wrap items-end gap-x-5 gap-y-3 rounded-xl border border-line bg-panel p-3 animate-fade" aria-label={`More options for column ${idx + 1}`}>
                          <Labeled label="Collation" className="w-56">
                            <input className={clsx(field, 'border-line font-mono')} list={`colls-${r.key}`} value={r.collation} aria-label="Collation"
                              placeholder={driver === 'sqlite' ? 'BINARY' : 'default'} onChange={(e) => set(r.key, { collation: e.target.value })} />
                            <datalist id={`colls-${r.key}`}>{(info?.collations ?? []).slice(0, 500).map((c) => <option key={c} value={c} />)}</datalist>
                          </Labeled>
                          {driver !== 'sqlite' && (
                            <Labeled label="Comment" className="min-w-48 flex-1">
                              <input className={clsx(field, 'border-line')} value={r.comment} placeholder="What this column holds" aria-label="Column comment"
                                onChange={(e) => set(r.key, { comment: e.target.value })} />
                            </Labeled>
                          )}
                          <div className="flex flex-wrap items-center gap-4 pb-1.5">
                            {mysql && has(t, 'unsigned') && <Switch checked={r.spec.unsigned} label="UNSIGNED" onChange={(v) => set(r.key, { spec: { ...r.spec, unsigned: v } })} />}
                            {mysql && has(t, 'time') && <Switch checked={r.onUpdateNow} label="On update: CURRENT_TIMESTAMP" onChange={(v) => set(r.key, { onUpdateNow: v })} />}
                            {driver === 'postgres' && !r.spec.custom && <Switch checked={r.spec.array} label="Array ( [ ] )" onChange={(v) => set(r.key, { spec: { ...r.spec, array: v } })} />}
                          </div>
                          <div className="ml-auto flex gap-0.5 pb-0.5">
                            <IconButton label="Move up" disabled={idx === 0} onClick={() => move(r.key, -1)}><ArrowUp className="size-3.5" /></IconButton>
                            <IconButton label="Move down" disabled={idx === rows.length - 1} onClick={() => move(r.key, 1)}><ArrowDown className="size-3.5" /></IconButton>
                          </div>
                        </div>
                      )}
                    </div>
                  )
                })}
              </div>
              <button type="button" onClick={() => addRows(1)}
                className="flex w-full items-center gap-2 border-t border-dashed border-line px-4 py-2.5 text-[12.5px] font-medium text-muted transition-colors hover:bg-hover/50 hover:text-accent">
                <Plus className="size-3.5" /> Add a column
              </button>
            </section>

            <div className="flex flex-wrap gap-x-4 gap-y-1 text-[11.5px] text-faint">
              {Object.values(FAMILY).map((f) => <span key={f.label} className="flex items-center gap-1.5"><span className="size-2 rounded-full" style={{ background: f.color }} />{f.label}</span>)}
              <span className="flex items-center gap-1.5"><KeyRound className="size-3 text-warn" />primary key</span>
            </div>

            {/* Summary and options next to the SQL that will run. */}
            <div className="grid items-start gap-4 lg:grid-cols-[minmax(0,5fr)_minmax(0,7fr)]">
            <div className="space-y-4">
          <div className="grid grid-cols-3 gap-2">
            <Stat label="Columns" value={String(named.length)} />
            <Stat label="Primary key" value={pk.length ? pk.join(', ') : 'none'} muted={!pk.length} />
            <Stat label="Indexes" value={String(indexes)} />
          </div>

          {(mysql || (driver === 'sqlite' && info?.strict)) && (
            <section className="rounded-xl border border-line bg-panel p-3.5" aria-label="Table options">
              <div className="mb-3 flex items-center gap-2 text-[12.5px] font-semibold"><Settings2 className="size-3.5 text-muted" /> Table options</div>
              {mysql ? (
                <div className="space-y-2.5">
                  <Labeled label="Storage engine">
                    <Select value={engine} onChange={(e) => setEngine(e.target.value)} aria-label="Storage engine">
                      {(info?.engines ?? []).map((e) => <option key={e} value={e}>{e}{e === info?.defaultEngine ? ' (default)' : ''}</option>)}
                    </Select>
                  </Labeled>
                  <div className="grid grid-cols-2 gap-2">
                    <Labeled label="Character set">
                      <Select value={charset} aria-label="Character set" onChange={(e) => { setCharset(e.target.value); setCollation('') }}>
                        {(info?.charsets ?? []).map((c) => <option key={c} value={c}>{c}</option>)}
                      </Select>
                    </Labeled>
                    <Labeled label="Collation">
                      <Select value={collation} onChange={(e) => setCollation(e.target.value)} aria-label="Table collation">
                        <option value="">default</option>
                        {/* Always show the current value (e.g. the database's default). */}
                        {[...new Set([...(collation ? [collation] : []), ...(info?.collations ?? []).filter((c) => !charset || c.startsWith(charset + '_'))])]
                          .map((c) => <option key={c} value={c}>{c}{c === info?.defaultCollation ? ' (database default)' : ''}</option>)}
                      </Select>
                    </Labeled>
                  </div>
                </div>
              ) : (
                <Switch checked={strict} onChange={setStrict}
                  label={<span>STRICT table <span className="block text-[11.5px] text-muted">SQLite enforces the column types (INT, INTEGER, REAL, TEXT, BLOB, ANY)</span></span>} />
              )}
            </section>
          )}

            </div>
            <div className="space-y-4">
          {problem && (
            <div className="flex gap-2.5 rounded-xl border border-err/25 bg-err/[.06] p-3 text-[12.5px] text-err" role="alert">
              <AlertTriangle className="mt-0.5 size-4 shrink-0" />
              <div><div className="font-semibold">Can’t create this table yet</div><div className="mt-0.5 text-err/90">{problem}</div></div>
            </div>
          )}

          <section className="flex min-h-0 flex-col overflow-hidden rounded-xl border border-line bg-panel">
            <div className="flex items-center gap-2 border-b border-line-2 px-3.5 py-2">
              <span className="text-[12.5px] font-semibold">SQL preview</span>
              {preview.length > 0 && <span className="text-[11px] text-ok">ready</span>}
              <div className="ml-auto">
                <IconButton label={copied ? 'Copied' : 'Copy SQL'} disabled={!preview.length}
                  onClick={() => { navigator.clipboard?.writeText(preview.join('\n')); setCopied(true); setTimeout(() => setCopied(false), 1200) }}>
                  {copied ? <CheckIcon className="size-3.5 text-ok" /> : <Copy className="size-3.5" />}
                </IconButton>
              </div>
            </div>
            <pre data-testid="table-sql" className={clsx('min-h-[150px] overflow-auto px-3.5 py-3 font-mono text-[12px] leading-relaxed', problem ? 'whitespace-pre-wrap text-err' : 'whitespace-pre text-fg-2')}>
              {problem || (preview.length ? highlight(preview.join('\n')) : <span className="text-faint">Name the table to see the statement.</span>)}
            </pre>
          </section>
            </div>
            </div>
          </div>
        </form>
      </div>
    </div>
  )
}

const INDEX_STYLE: Record<IndexKind, string> = {
  '': 'text-faint',
  primary: '!border-warn/40 !bg-warn/10 text-warn',
  unique: '!border-accent/40 !bg-accent-soft text-accent',
  index: 'text-fg-2',
  fulltext: 'text-fg-2',
}

/** Pill is a compact on/off toggle (a checkbox to assistive technology). */
function Pill({ label, text, checked, disabled, onChange }: { label: string; text: string; checked: boolean; disabled?: boolean; onChange: (v: boolean) => void }) {
  return (
    <button type="button" role="checkbox" aria-checked={checked} aria-label={label} title={label} disabled={disabled} onClick={() => onChange(!checked)}
      className={clsx('mx-auto h-6 rounded-full border px-2 text-[10.5px] font-bold tracking-wide transition-colors disabled:cursor-not-allowed disabled:opacity-35',
        checked ? 'border-accent bg-accent text-accent-fg' : 'border-line text-faint hover:border-faint hover:text-fg')}>
      {text}
    </button>
  )
}

function Labeled({ label, className, children }: { label: string; className?: string; children: ReactNode }) {
  return (
    <label className={clsx('flex flex-col gap-1', className)}>
      <span className="text-[11px] font-medium text-muted">{label}</span>
      {children}
    </label>
  )
}

function Stat({ label, value, muted }: { label: string; value: string; muted?: boolean }) {
  return (
    <div className="min-w-0 rounded-xl border border-line bg-panel px-3 py-2.5">
      <div className="text-[10.5px] font-semibold uppercase tracking-wider text-faint">{label}</div>
      <div className={clsx('mt-0.5 truncate font-mono text-[14px] font-semibold', muted && 'font-sans font-normal text-faint')} title={value}>{value}</div>
    </div>
  )
}

const KEYWORDS = /^(CREATE|TABLE|NOT|NULL|DEFAULT|PRIMARY|KEY|UNIQUE|CONSTRAINT|INDEX|ON|UPDATE|AUTO_INCREMENT|AUTOINCREMENT|GENERATED|BY|AS|IDENTITY|COLLATE|COMMENT|ENGINE|CHARSET|STRICT|FULLTEXT|IS|UNSIGNED|COLUMN)$/i

/** highlight colours SQL keywords, strings, identifiers and numbers (text stays the same). */
function highlight(sql: string): ReactNode[] {
  const out: ReactNode[] = []
  const re = /('(?:[^']|'')*')|(`[^`]*`|"[^"]*")|([A-Za-z_]+)|(\d+)/g
  let last = 0
  for (let m = re.exec(sql); m; m = re.exec(sql)) {
    if (m.index > last) out.push(sql.slice(last, m.index))
    const [tok, str, ident, word, num] = m
    const cls = str ? 'text-ok' : ident ? 'text-fg' : num ? 'text-warn' : word && KEYWORDS.test(word) ? 'font-semibold text-accent' : ''
    out.push(cls ? <span key={m.index} className={cls}>{tok}</span> : tok)
    last = m.index + tok.length
  }
  if (last < sql.length) out.push(sql.slice(last))
  return out
}
