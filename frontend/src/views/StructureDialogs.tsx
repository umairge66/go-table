import clsx from 'clsx'
import { AlertTriangle, ArrowRight, Plus, X } from 'lucide-react'
import { useEffect, useState, type ReactNode } from 'react'
import { api, ApiError, type AlterOp, type TableRef } from '../api'
import { SqlEditor } from '../components/SqlEditor'
import { Button, Field, IconButton, Input, Modal, Select, errorParts, useToast } from '../components/ui'
import type { Column, DbObject, Driver, SessionInfo } from '../types'

// Structure changes all go through POST /alter: a dry run shows the exact
// SQL, then the same request runs it.

const isMySQL = (d: Driver) => d === 'mysql' || d === 'mariadb'

/** useAlter reviews and runs one-click changes (drop, set primary key…). */
export function useAlter(session: SessionInfo, table: TableRef, onDone: () => void) {
  const toast = useToast()
  const [review, setReview] = useState<{ ops: AlterOp[]; title: string; statements: string[]; danger?: boolean; done: string; after?: () => void } | null>(null)
  const [busy, setBusy] = useState(false)
  /** run reviews ops, then runs them; after replaces onDone (e.g. a renamed table can't be reloaded by its old name). */
  const run = async (ops: AlterOp[], title: string, opts: { danger?: boolean; done?: string; after?: () => void } = {}) => {
    try {
      const r = await api.alter(session.id, table, ops, true)
      setReview({ ops, title, statements: r.statements, danger: opts.danger, done: opts.done ?? 'Structure changed', after: opts.after })
    } catch (e) {
      toast('error', ...errorParts(e))
    }
  }
  const apply = async () => {
    if (!review) return
    setBusy(true)
    try {
      await api.alter(session.id, table, review.ops, false)
      toast('ok', review.done, table.name)
      setReview(null)
      ;(review.after ?? onDone)()
    } catch (e) {
      toast('error', ...errorParts(e))
    } finally {
      setBusy(false)
    }
  }
  const production = session.connection.tag === 'production'
  const node = review && (
    <Modal width={680} title={review.title} subtitle={<>on <b className="font-medium text-fg-2">{table.name}</b> — this SQL will run:</>} onClose={() => setReview(null)}
      footer={<>
        {production && <span className="mr-auto flex items-center gap-1.5 text-[12px] font-medium text-err"><AlertTriangle className="size-3.5" /> This is PRODUCTION.</span>}
        <Button variant="ghost" onClick={() => setReview(null)}>Cancel</Button>
        <Button variant={review.danger || production ? 'danger' : 'primary'} loading={busy} onClick={apply} autoFocus>Run</Button>
      </>}>
      <div className="h-[200px] overflow-hidden rounded-lg border border-line" data-testid="alter-review">
        <SqlEditor value={review.statements.join('\n')} driver={session.connection.driver} readOnly />
      </div>
      {isMySQL(session.connection.driver) && review.statements.length > 1 && (
        <div className="mt-2 text-[12px] text-muted">MySQL applies each statement as it runs; if one fails, the ones before it stay applied.</div>
      )}
    </Modal>
  )
  return { run, node }
}

/**
 * AlterDialog is the frame of a structure dialog: the form, a live preview
 * of the SQL (from a dry run) and the button that runs it.
 */
export function AlterDialog({ session, table, title, submitLabel, done, ops, incomplete, width = 620, onClose, onDone, children }: {
  session: SessionInfo
  table: TableRef
  title: string
  submitLabel: string
  /** done is the message shown after it ran. */
  done: string
  /** ops: the change; null while the form is incomplete (incomplete says why). */
  ops: AlterOp[] | null
  incomplete?: string
  width?: number
  onClose: () => void
  onDone: () => void
  children: ReactNode
}) {
  const toast = useToast()
  const [preview, setPreview] = useState<string[]>([])
  const [problem, setProblem] = useState('')
  const [saving, setSaving] = useState(false)
  const key = JSON.stringify(ops)
  useEffect(() => {
    if (!ops) { setPreview([]); setProblem(''); return }
    const t = setTimeout(async () => {
      try {
        setPreview((await api.alter(session.id, table, ops, true)).statements)
        setProblem('')
      } catch (e) {
        setPreview([])
        setProblem((e as ApiError).detail ?? errorParts(e)[1])
      }
    }, 200)
    return () => clearTimeout(t)
  }, [key, session.id, table]) // eslint-disable-line react-hooks/exhaustive-deps
  // An edit dialog opens unchanged: that's not an error.
  const unchanged = problem === 'nothing to change'
  const ready = !!ops && preview.length > 0 && !problem
  const submit = async () => {
    if (!ready) return
    setSaving(true)
    try {
      await api.alter(session.id, table, ops!, false)
      toast('ok', done, table.name)
      onDone()
    } catch (e) {
      setProblem(errorParts(e).filter(Boolean).join(' — '))
    } finally {
      setSaving(false)
    }
  }
  const production = session.connection.tag === 'production'
  return (
    <Modal width={width} title={title} subtitle={<>on <b className="font-medium text-fg-2">{table.name}</b></>} onClose={onClose}
      footer={<>
        {production && <span className="mr-auto flex items-center gap-1.5 text-[12px] font-medium text-err"><AlertTriangle className="size-3.5" /> This is PRODUCTION.</span>}
        <Button variant="ghost" onClick={onClose}>Cancel</Button>
        <Button variant={production ? 'danger' : 'primary'} disabled={!ready} loading={saving} onClick={submit}>{submitLabel}</Button>
      </>}>
      <form className="space-y-3" onSubmit={(e) => { e.preventDefault(); submit() }} aria-label={title}>
        {children}
        <div>
          <div className="mb-1 text-[11.5px] font-medium text-muted">SQL that will run</div>
          <pre data-testid="alter-sql" className={clsx('min-h-[44px] overflow-x-auto whitespace-pre-wrap rounded-lg border px-3 py-2.5 font-mono text-[12px]',
            problem && !unchanged ? 'border-err/30 bg-err/5 text-err' : 'border-line bg-panel-2 text-fg-2', unchanged && 'text-muted')}>
            {unchanged ? 'No changes yet.' : problem || (preview.length ? preview.join('\n') : incomplete ?? '…')}
          </pre>
        </div>
        <button type="submit" hidden />
      </form>
    </Modal>
  )
}

/** ColumnPicker chooses columns in order (the order matters for indexes and keys). */
function ColumnPicker({ columns, value, onChange, label }: { columns: Column[]; value: string[]; onChange: (v: string[]) => void; label: string }) {
  return (
    <div role="group" aria-label={label} className="flex max-h-48 flex-wrap gap-1.5 overflow-y-auto rounded-lg border border-line bg-panel p-2">
      {columns.map((c) => {
        const i = value.indexOf(c.name)
        return (
          <button key={c.name} type="button" aria-pressed={i >= 0} onClick={() => onChange(i >= 0 ? value.filter((x) => x !== c.name) : [...value, c.name])}
            className={clsx('flex h-7 items-center gap-1.5 rounded-full border px-2.5 text-[12px]',
              i >= 0 ? 'border-accent bg-accent-soft font-medium text-accent' : 'border-line text-fg-2 hover:bg-hover')}>
            {i >= 0 && <span className="flex size-4 items-center justify-center rounded-full bg-accent text-[10px] text-accent-fg">{i + 1}</span>}
            {c.name}
          </button>
        )
      })}
    </div>
  )
}

const PG_METHODS = ['btree', 'hash', 'gin', 'gist', 'brin', 'spgist']

export function IndexDialog({ session, table, columns, initial, onClose, onDone }: {
  session: SessionInfo; table: TableRef; columns: Column[]
  initial?: { columns: string[]; kind?: 'index' | 'unique' | 'fulltext' }
  onClose: () => void; onDone: () => void
}) {
  const driver = session.connection.driver
  const [name, setName] = useState('')
  const [kind, setKind] = useState<'index' | 'unique' | 'fulltext' | 'spatial'>(initial?.kind ?? 'index')
  const [method, setMethod] = useState('')
  const [cols, setCols] = useState<string[]>(initial?.columns ?? [])
  const special = kind === 'fulltext' || kind === 'spatial'
  const ops: AlterOp[] | null = cols.length ? [{ kind: 'add_index', index: {
    name: name.trim(), columns: cols, unique: kind === 'unique', kind: special ? kind : '', method: special ? '' : method } }] : null
  return (
    <AlterDialog session={session} table={table} title="Add index" submitLabel="Add index" done="Index added" ops={ops} incomplete="Choose the columns to index." onClose={onClose} onDone={onDone}>
      <div className="grid grid-cols-[1fr_150px_130px] gap-3">
        <Field label="Name"><Input autoFocus value={name} placeholder="automatic" aria-label="Index name" onChange={(e) => setName(e.target.value)} /></Field>
        <Field label="Kind">
          <Select value={kind} aria-label="Index kind" onChange={(e) => setKind(e.target.value as typeof kind)}>
            <option value="index">Index</option>
            <option value="unique">Unique</option>
            {isMySQL(driver) && <option value="fulltext">Fulltext</option>}
            {isMySQL(driver) && <option value="spatial">Spatial</option>}
          </Select>
        </Field>
        {driver !== 'sqlite' && (
          <Field label="Method">
            <Select value={method} aria-label="Index method" disabled={special} onChange={(e) => setMethod(e.target.value)}>
              <option value="">default</option>
              {(driver === 'postgres' ? PG_METHODS : ['btree', 'hash']).map((m) => <option key={m} value={m}>{m.toUpperCase()}</option>)}
            </Select>
          </Field>
        )}
      </div>
      <Field label="Columns (in order)"><ColumnPicker columns={columns} value={cols} onChange={setCols} label="Index columns" /></Field>
    </AlterDialog>
  )
}

const ACTIONS = ['NO ACTION', 'RESTRICT', 'CASCADE', 'SET NULL', 'SET DEFAULT']

export function ForeignKeyDialog({ session, table, columns, initialColumn, onClose, onDone }: {
  session: SessionInfo; table: TableRef; columns: Column[]; initialColumn?: string; onClose: () => void; onDone: () => void
}) {
  const [name, setName] = useState('')
  const [tables, setTables] = useState<DbObject[]>([])
  const [refTable, setRefTable] = useState('')
  const [refColumns, setRefColumns] = useState<Column[]>([])
  const [pairs, setPairs] = useState<{ from: string; to: string }[]>([{ from: initialColumn ?? '', to: '' }])
  const [onDelete, setOnDelete] = useState('NO ACTION')
  const [onUpdate, setOnUpdate] = useState('NO ACTION')
  useEffect(() => { api.objects(session.id, table.schema).then((o) => setTables(o.filter((x) => x.kind === 'table'))).catch(() => {}) }, [session.id, table.schema])
  useEffect(() => {
    if (!refTable) { setRefColumns([]); return }
    api.structure(session.id, { schema: table.schema, name: refTable }).then((s) => {
      setRefColumns(s.columns)
      // Point at the referenced table's primary key by default.
      const pk = s.columns.filter((c) => c.primaryKey).map((c) => c.name)
      setPairs((ps) => ps.map((p, i) => ({ ...p, to: p.to && s.columns.some((c) => c.name === p.to) ? p.to : pk[i] ?? '' })))
    }).catch(() => setRefColumns([]))
  }, [refTable, session.id, table.schema])
  const complete = refTable && pairs.every((p) => p.from && p.to)
  const ops: AlterOp[] | null = complete ? [{ kind: 'add_foreign_key', foreignKey: {
    name: name.trim(), columns: pairs.map((p) => p.from), refSchema: table.schema, refTable, refColumns: pairs.map((p) => p.to), onDelete, onUpdate } }] : null
  const setPair = (i: number, p: Partial<{ from: string; to: string }>) => setPairs((ps) => ps.map((x, j) => (j === i ? { ...x, ...p } : x)))
  return (
    <AlterDialog session={session} table={table} title="Add foreign key" submitLabel="Add foreign key" done="Foreign key added" ops={ops} width={660}
      incomplete="Choose the referenced table and a column on each side." onClose={onClose} onDone={onDone}>
      <div className="grid grid-cols-2 gap-3">
        <Field label="Name"><Input value={name} placeholder="automatic" aria-label="Foreign key name" onChange={(e) => setName(e.target.value)} /></Field>
        <Field label="References table">
          <Select value={refTable} aria-label="Referenced table" onChange={(e) => setRefTable(e.target.value)}>
            <option value="">Choose a table…</option>
            {tables.map((t) => <option key={t.name} value={t.name}>{t.name}</option>)}
          </Select>
        </Field>
      </div>
      <Field label="Columns">
        <div className="space-y-1.5">
          {pairs.map((p, i) => (
            <div key={i} className="flex items-center gap-2">
              <Select className="flex-1" value={p.from} aria-label={`Column ${i + 1}`} onChange={(e) => setPair(i, { from: e.target.value })}>
                <option value="">{table.name} column…</option>
                {columns.map((c) => <option key={c.name} value={c.name}>{c.name}</option>)}
              </Select>
              <ArrowRight className="size-4 shrink-0 text-muted" />
              <Select className="flex-1" value={p.to} aria-label={`Referenced column ${i + 1}`} disabled={!refTable} onChange={(e) => setPair(i, { to: e.target.value })}>
                <option value="">{refTable || 'referenced'} column…</option>
                {refColumns.map((c) => <option key={c.name} value={c.name}>{c.name}</option>)}
              </Select>
              <IconButton label="Remove column" disabled={pairs.length === 1} onClick={() => setPairs((ps) => ps.filter((_, j) => j !== i))}><X className="size-3.5" /></IconButton>
            </div>
          ))}
          <button type="button" className="flex items-center gap-1 text-[12px] font-medium text-accent hover:underline" onClick={() => setPairs((ps) => [...ps, { from: '', to: '' }])}>
            <Plus className="size-3.5" /> Add a column (composite key)
          </button>
        </div>
      </Field>
      <div className="grid grid-cols-2 gap-3">
        <Field label="On delete"><Select value={onDelete} aria-label="On delete" onChange={(e) => setOnDelete(e.target.value)}>{ACTIONS.map((a) => <option key={a}>{a}</option>)}</Select></Field>
        <Field label="On update"><Select value={onUpdate} aria-label="On update" onChange={(e) => setOnUpdate(e.target.value)}>{ACTIONS.map((a) => <option key={a}>{a}</option>)}</Select></Field>
      </div>
    </AlterDialog>
  )
}

export function CheckDialog({ session, table, onClose, onDone }: { session: SessionInfo; table: TableRef; onClose: () => void; onDone: () => void }) {
  const [name, setName] = useState('')
  const [expression, setExpression] = useState('')
  const ops: AlterOp[] | null = expression.trim() ? [{ kind: 'add_check', check: { name: name.trim(), expression } }] : null
  return (
    <AlterDialog session={session} table={table} title="Add check" submitLabel="Add check" done="Check added" ops={ops} incomplete="Enter the condition rows must meet." onClose={onClose} onDone={onDone}>
      <Field label="Name"><Input value={name} placeholder="automatic" aria-label="Check name" onChange={(e) => setName(e.target.value)} /></Field>
      <Field label="Condition" hint="Every row must make this true, e.g. price >= 0 or end_date > start_date.">
        <Input autoFocus mono value={expression} placeholder="price >= 0" aria-label="Check condition" onChange={(e) => setExpression(e.target.value)} />
      </Field>
    </AlterDialog>
  )
}
