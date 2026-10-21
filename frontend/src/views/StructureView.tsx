import clsx from 'clsx'
import { ArrowDown, ArrowUp, ArrowUpToLine, Eraser, KeyRound, Link2, ListOrdered, MoreHorizontal, Pencil, Plus, Search, Trash2 } from 'lucide-react'
import { useEffect, useState, type ReactNode } from 'react'
import { api, type AlterOp, type ColumnDef, type TableRef } from '../api'
import { Button, Field, IconButton, Input, Select, Switch, useMenu } from '../components/ui'
import type { Column, ForeignRef, Index, SessionInfo, TableInfo } from '../types'
import { ColumnDialog } from './ColumnDialog'
import { AlterDialog, CheckDialog, ForeignKeyDialog, IndexDialog, useAlter } from './StructureDialogs'

type Dialog =
  | { kind: 'column'; editing?: Column }
  | { kind: 'index'; columns?: string[]; type?: 'index' | 'unique' | 'fulltext' }
  | { kind: 'renameIndex'; name: string }
  | { kind: 'foreignKey'; column?: string }
  | { kind: 'check' }

/** colDef restates a column for modify_column (moving it keeps everything else). */
const colDef = (c: Column, position = ''): ColumnDef => ({
  name: c.name, type: c.type, nullable: c.nullable, defaultKind: c.defaultKind ?? '', default: c.defaultText ?? '',
  comment: c.comment, position, collation: c.collation ?? '', onUpdateNow: !!c.onUpdateNow,
})

/**
 * StructureView shows a table's columns, indexes, foreign keys, checks and
 * settings, and changes them (like phpMyAdmin's Structure and Operations).
 */
export function StructureView({ session, table, readOnly, columns, indexes, foreignKeys, info, highlight, onChanged, onRenamed }: {
  session: SessionInfo
  table: TableRef
  /** readOnly: a view; only its columns are shown. */
  readOnly: boolean
  columns: Column[]
  indexes: Index[]
  foreignKeys: ForeignRef[]
  info: TableInfo | null
  /** highlight marks a just-added or changed column. */
  highlight?: string
  /** onChanged reloads the structure (highlighting a column by name). */
  onChanged: (column?: string) => void
  onRenamed: (name: string) => void
}) {
  const driver = session.connection.driver
  const mysql = driver === 'mysql' || driver === 'mariadb'
  const sqlite = driver === 'sqlite'
  const menu = useMenu()
  const [dialog, setDialog] = useState<Dialog | null>(null)
  const [checked, setChecked] = useState<string[]>([])
  const changed = (column?: string) => { setDialog(null); setChecked([]); onChanged(column) }
  const alter = useAlter(session, table, () => changed())
  useEffect(() => { setChecked((cs) => cs.filter((c) => columns.some((x) => x.name === c))) }, [columns])
  const pk = indexes.find((x) => x.primary)
  const inOrder = (names: string[]) => columns.map((c) => c.name).filter((n) => names.includes(n))
  const list = (names: string[]) => names.join(', ')

  const setPrimary = (names: string[]) => alter.run([{ kind: 'set_primary_key', columns: names }],
    pk ? `Replace the primary key (${list(pk.columns)}) with (${list(names)})` : `Add primary key (${list(names)})`, { done: 'Primary key changed' })
  const addIndex = (names: string[], type: 'index' | 'unique' | 'fulltext') => alter.run(
    [{ kind: 'add_index', index: { name: '', columns: names, unique: type === 'unique', kind: type === 'fulltext' ? 'fulltext' : '', method: '' } }],
    `Add ${type === 'index' ? 'an index' : type === 'unique' ? 'a unique index' : 'a fulltext index'} on ${list(names)}`, { done: 'Index added' })
  const dropColumns = (names: string[]) => alter.run(names.map((n): AlterOp => ({ kind: 'drop_column', column: n })),
    names.length === 1 ? `Drop column ${names[0]}` : `Drop ${names.length} columns`, { danger: true, done: names.length === 1 ? `Column “${names[0]}” dropped` : 'Columns dropped' })
  const move = (c: Column, position: string) => alter.run([{ kind: 'modify_column', column: c.name, def: colDef(c, position), autoIncrement: c.autoIncrement }],
    `Move column ${c.name}`, { done: 'Column moved' })

  const columnMenu = (e: React.MouseEvent, c: Column, i: number) => {
    const isText = /char|text|enum|set/i.test(c.type)
    menu.open(e, [
      { label: 'Edit…', icon: <Pencil />, onSelect: () => setDialog({ kind: 'column', editing: c }) },
      'sep',
      { label: pk ? 'Make it the primary key' : 'Primary key', icon: <KeyRound />, disabled: pk?.columns.length === 1 && pk.columns[0] === c.name, onSelect: () => setPrimary([c.name]) },
      ...(pk && !pk.columns.includes(c.name) ? [{ label: 'Add to the primary key', onSelect: () => setPrimary([...pk.columns, c.name]) }] : []),
      { label: 'Unique', onSelect: () => addIndex([c.name], 'unique') },
      { label: 'Index', icon: <ListOrdered />, onSelect: () => addIndex([c.name], 'index') },
      ...(mysql && isText ? [{ label: 'Fulltext', icon: <Search />, onSelect: () => addIndex([c.name], 'fulltext') }] : []),
      { label: 'Foreign key…', icon: <Link2 />, onSelect: () => setDialog({ kind: 'foreignKey', column: c.name }) },
      ...(mysql ? [
        'sep' as const,
        { label: 'Move to the top', icon: <ArrowUpToLine />, disabled: i === 0, onSelect: () => move(c, 'first') },
        { label: 'Move up', icon: <ArrowUp />, disabled: i === 0, onSelect: () => move(c, i >= 2 ? columns[i - 2].name : 'first') },
        { label: 'Move down', icon: <ArrowDown />, disabled: i === columns.length - 1, onSelect: () => move(c, columns[i + 1].name) },
      ] : []),
      'sep',
      { label: 'Drop column', icon: <Trash2 />, danger: true, disabled: columns.length === 1, onSelect: () => dropColumns([c.name]) },
    ])
  }

  const extra = (c: Column) => [c.autoIncrement && (mysql ? 'AUTO_INCREMENT' : sqlite ? 'rowid' : 'identity'), c.onUpdateNow && 'ON UPDATE CURRENT_TIMESTAMP'].filter(Boolean).join(' · ')
  const showCollation = columns.some((c) => c.collation)
  const allChecked = checked.length === columns.length && columns.length > 0

  return (
    <div className="h-full overflow-auto p-4" data-testid="structure">
      <Section title="Columns" count={columns.length} actions={!readOnly && (
        <Button size="sm" variant="primary" icon={<Plus className="size-3.5" />} onClick={() => setDialog({ kind: 'column' })}>Add column</Button>
      )}>
        {!readOnly && checked.length > 0 && (
          <div className="flex flex-wrap items-center gap-1.5 border-b border-line bg-accent-soft px-3 py-1.5 text-[12px] animate-fade" role="toolbar" aria-label="Selected columns">
            <span className="mr-1 font-medium text-accent">{checked.length} selected:</span>
            <Button size="sm" variant="ghost" icon={<KeyRound className="size-3.5" />} onClick={() => setPrimary(inOrder(checked))}>Primary key</Button>
            <Button size="sm" variant="ghost" onClick={() => addIndex(inOrder(checked), 'unique')}>Unique</Button>
            <Button size="sm" variant="ghost" onClick={() => addIndex(inOrder(checked), 'index')}>Index</Button>
            {mysql && <Button size="sm" variant="ghost" onClick={() => addIndex(inOrder(checked), 'fulltext')}>Fulltext</Button>}
            <Button size="sm" variant="ghost" icon={<Trash2 className="size-3.5" />} className="text-err" disabled={checked.length === columns.length} onClick={() => dropColumns(inOrder(checked))}>Drop</Button>
            <div className="flex-1" />
            <button className="text-[12px] text-muted hover:text-fg" onClick={() => setChecked([])}>Clear</button>
          </div>
        )}
        <table className="w-full text-[12.5px]">
          <thead className="bg-panel-2 text-left text-[11.5px] text-muted">
            <tr>
              {!readOnly && (
                <th className="w-8 border-b border-line pl-3">
                  <input type="checkbox" aria-label="Select all columns" className="accent-[var(--accent)]" checked={allChecked}
                    onChange={() => setChecked(allChecked ? [] : columns.map((c) => c.name))} />
                </th>
              )}
              {['#', 'Name', 'Type', ...(showCollation ? ['Collation'] : []), 'Null', 'Default', 'Extra', 'Comment'].map((h) => <th key={h} className="border-b border-line px-3 py-2 font-semibold">{h}</th>)}
              {!readOnly && <th className="w-24 border-b border-line" />}
            </tr>
          </thead>
          <tbody>
            {columns.map((c, i) => (
              <tr key={c.name} data-testid={`column-${c.name}`} onContextMenu={readOnly ? undefined : (e) => columnMenu(e, c, i)}
                className={clsx('group border-b border-line-2 last:border-0 hover:bg-hover', c.name === highlight && 'bg-inserted animate-fade', checked.includes(c.name) && 'bg-selected')}>
                {!readOnly && (
                  <td className="pl-3">
                    <input type="checkbox" aria-label={`Select ${c.name}`} className="accent-[var(--accent)]" checked={checked.includes(c.name)}
                      onChange={() => setChecked((cs) => (cs.includes(c.name) ? cs.filter((x) => x !== c.name) : [...cs, c.name]))} />
                  </td>
                )}
                <td className="w-10 px-3 py-1.5 text-faint">{i + 1}</td>
                <td className="px-3 py-1.5 font-medium"><span className="inline-flex items-center gap-1.5">{c.primaryKey && <KeyRound className="size-3 text-warn" aria-label="primary key" />}{c.name}</span></td>
                <td className="px-3 py-1.5 font-mono text-[12px] text-accent">{c.type}</td>
                {showCollation && <td className="px-3 py-1.5 font-mono text-[11.5px] text-muted">{c.collation}</td>}
                <td className="px-3 py-1.5">{c.nullable ? <span className="text-muted">yes</span> : <span className="font-medium">NOT NULL</span>}</td>
                <td className="px-3 py-1.5 font-mono text-[12px] text-muted">{c.default ?? <span className="text-faint">—</span>}</td>
                <td className="px-3 py-1.5 text-[11.5px] uppercase text-muted">{extra(c)}</td>
                <td className="px-3 py-1.5 text-muted">{c.comment}</td>
                {!readOnly && (
                  <td className="px-2 py-1 text-right">
                    <span className="inline-flex gap-0.5 opacity-60 group-hover:opacity-100">
                      <IconButton label={`Edit ${c.name}`} onClick={() => setDialog({ kind: 'column', editing: c })}><Pencil className="size-3.5" /></IconButton>
                      <IconButton label={`Drop ${c.name}`} disabled={columns.length === 1} onClick={() => dropColumns([c.name])}><Trash2 className="size-3.5" /></IconButton>
                      <IconButton label={`More for ${c.name}`} onClick={(e) => columnMenu(e, c, i)}><MoreHorizontal className="size-3.5" /></IconButton>
                    </span>
                  </td>
                )}
              </tr>
            ))}
          </tbody>
        </table>
      </Section>

      {!readOnly && <>
        <Section title="Indexes" count={indexes.length} actions={<Button size="sm" icon={<Plus className="size-3.5" />} onClick={() => setDialog({ kind: 'index' })}>Add index</Button>}>
          {indexes.length === 0 ? <Empty>No indexes. Add one to speed up searching and sorting on a column.</Empty> : (
            <Rows heads={['Name', 'Kind', 'Columns']}>
              {indexes.map((x) => (
                <tr key={x.name} data-testid={`index-${x.name}`} className="group border-b border-line-2 last:border-0 hover:bg-hover">
                  <td className="px-3 py-1.5 font-medium">{x.name}</td>
                  <td className="px-3 py-1.5"><IndexKind x={x} /></td>
                  <td className="px-3 py-1.5 font-mono text-[12px]">{x.columns.join(', ')}</td>
                  <td className="px-2 py-1 text-right">
                    <span className="inline-flex gap-0.5 opacity-60 group-hover:opacity-100">
                      <IconButton label={`Rename ${x.name}`} disabled={x.primary && (mysql || sqlite)} onClick={() => setDialog({ kind: 'renameIndex', name: x.name })}><Pencil className="size-3.5" /></IconButton>
                      <IconButton label={`Drop ${x.name}`} onClick={() => alter.run([{ kind: 'drop_index', name: x.name }], x.primary ? 'Drop the primary key' : `Drop index ${x.name}`, { danger: true, done: 'Index dropped' })}>
                        <Trash2 className="size-3.5" />
                      </IconButton>
                    </span>
                  </td>
                </tr>
              ))}
            </Rows>
          )}
        </Section>

        <Section title="Foreign keys" count={foreignKeys.length} actions={<Button size="sm" icon={<Plus className="size-3.5" />} onClick={() => setDialog({ kind: 'foreignKey' })}>Add foreign key</Button>}>
          {foreignKeys.length === 0 ? <Empty>No foreign keys. A foreign key makes a column point at rows of another table.</Empty> : (
            <Rows heads={['Name', 'Columns', 'References', 'On delete', 'On update']}>
              {foreignKeys.map((f) => (
                <tr key={f.name} data-testid={`fk-${f.name}`} className="group border-b border-line-2 last:border-0 hover:bg-hover">
                  <td className="px-3 py-1.5 font-medium">{sqlite ? <span className="text-muted">#{Number(f.name) + 1}</span> : f.name}</td>
                  <td className="px-3 py-1.5 font-mono text-[12px]">{f.from.join(', ')}</td>
                  <td className="px-3 py-1.5 font-mono text-[12px]">{f.table}({f.columns.join(', ')})</td>
                  <td className="px-3 py-1.5 text-[11.5px] text-muted">{f.onDelete}</td>
                  <td className="px-3 py-1.5 text-[11.5px] text-muted">{f.onUpdate}</td>
                  <td className="px-2 py-1 text-right">
                    <IconButton label={`Drop foreign key ${f.from.join(', ')}`} className="opacity-60 group-hover:opacity-100"
                      onClick={() => alter.run([{ kind: 'drop_foreign_key', name: f.name! }], `Drop the foreign key on ${f.from.join(', ')}`, { danger: true, done: 'Foreign key dropped' })}>
                      <Trash2 className="size-3.5" />
                    </IconButton>
                  </td>
                </tr>
              ))}
            </Rows>
          )}
        </Section>

        {!sqlite && (
          <Section title="Checks" count={info?.checks.length ?? 0} actions={<Button size="sm" icon={<Plus className="size-3.5" />} onClick={() => setDialog({ kind: 'check' })}>Add check</Button>}>
            {!info?.checks.length ? <Empty>No CHECK constraints. A check makes every row meet a condition, e.g. price {'>'}= 0.</Empty> : (
              <Rows heads={['Name', 'Condition']}>
                {info.checks.map((ck) => (
                  <tr key={ck.name} data-testid={`check-${ck.name}`} className="group border-b border-line-2 last:border-0 hover:bg-hover">
                    <td className="px-3 py-1.5 font-medium">{ck.name}</td>
                    <td className="px-3 py-1.5 font-mono text-[12px]">{ck.expression}</td>
                    <td className="px-2 py-1 text-right">
                      <IconButton label={`Drop check ${ck.name}`} className="opacity-60 group-hover:opacity-100"
                        onClick={() => alter.run([{ kind: 'drop_check', name: ck.name }], `Drop check ${ck.name}`, { danger: true, done: 'Check dropped' })}>
                        <Trash2 className="size-3.5" />
                      </IconButton>
                    </td>
                  </tr>
                ))}
              </Rows>
            )}
          </Section>
        )}

        <TableOperations session={session} table={table} info={info} run={alter.run} onRenamed={onRenamed} />
      </>}

      {readOnly && indexes.length > 0 && (
        <Section title="Indexes" count={indexes.length}>
          <Rows heads={['Name', 'Kind', 'Columns']} noActions>
            {indexes.map((x) => (
              <tr key={x.name} className="border-b border-line-2 last:border-0"><td className="px-3 py-1.5 font-medium">{x.name}</td><td className="px-3 py-1.5"><IndexKind x={x} /></td><td className="px-3 py-1.5 font-mono text-[12px]">{x.columns.join(', ')}</td></tr>
            ))}
          </Rows>
        </Section>
      )}

      {dialog?.kind === 'column' && (
        <ColumnDialog session={session} table={table} columns={columns} editing={dialog.editing} onClose={() => setDialog(null)} onDone={(name) => changed(name)} />
      )}
      {dialog?.kind === 'index' && (
        <IndexDialog session={session} table={table} columns={columns} initial={dialog.columns && { columns: dialog.columns, kind: dialog.type }} onClose={() => setDialog(null)} onDone={() => changed()} />
      )}
      {dialog?.kind === 'renameIndex' && <RenameIndexDialog session={session} table={table} name={dialog.name} onClose={() => setDialog(null)} onDone={() => changed()} />}
      {dialog?.kind === 'foreignKey' && (
        <ForeignKeyDialog session={session} table={table} columns={columns} initialColumn={dialog.column} onClose={() => setDialog(null)} onDone={() => changed()} />
      )}
      {dialog?.kind === 'check' && <CheckDialog session={session} table={table} onClose={() => setDialog(null)} onDone={() => changed()} />}
      {alter.node}
      {menu.node}
    </div>
  )
}

function Section({ title, count, actions, children }: { title: string; count: number; actions?: ReactNode; children: ReactNode }) {
  return (
    <section className="mb-6" aria-label={title}>
      <div className="mb-2 flex items-center">
        <h3 className="text-[12px] font-semibold uppercase tracking-wider text-muted">{title} <span className="font-normal text-faint">{count}</span></h3>
        <div className="flex-1" />
        {actions}
      </div>
      <div className="overflow-hidden rounded-xl border border-line">{children}</div>
    </section>
  )
}

function Rows({ heads, noActions, children }: { heads: string[]; noActions?: boolean; children: ReactNode }) {
  return (
    <table className="w-full text-[12.5px]">
      <thead className="bg-panel-2 text-left text-[11.5px] text-muted">
        <tr>{heads.map((h) => <th key={h} className="border-b border-line px-3 py-2 font-semibold">{h}</th>)}{!noActions && <th className="w-20 border-b border-line" />}</tr>
      </thead>
      <tbody>{children}</tbody>
    </table>
  )
}

const Empty = ({ children }: { children: ReactNode }) => <div className="px-3 py-3 text-[12.5px] text-muted">{children}</div>

function IndexKind({ x }: { x: Index }) {
  const t = (x.type ?? '').toLowerCase()
  const label = x.primary ? 'PRIMARY' : t === 'fulltext' ? 'FULLTEXT' : t === 'spatial' ? 'SPATIAL' : x.unique ? 'UNIQUE' : 'INDEX'
  return (
    <span className="inline-flex items-center gap-1.5">
      <span className={clsx('rounded px-1.5 py-0.5 text-[11px] font-semibold',
        x.primary ? 'bg-warn/15 text-warn' : x.unique ? 'bg-accent-soft text-accent' : 'bg-hover text-fg-2')}>{label}</span>
      {t && !['btree', 'fulltext', 'spatial'].includes(t) && <span className="font-mono text-[11px] uppercase text-muted">{t}</span>}
    </span>
  )
}

function RenameIndexDialog({ session, table, name, onClose, onDone }: { session: SessionInfo; table: TableRef; name: string; onClose: () => void; onDone: () => void }) {
  const [newName, setNewName] = useState(name)
  const ops: AlterOp[] | null = newName.trim() && newName.trim() !== name ? [{ kind: 'rename_index', name, newName: newName.trim() }] : null
  return (
    <AlterDialog session={session} table={table} title={`Rename index ${name}`} submitLabel="Rename" done="Index renamed" ops={ops} width={480}
      incomplete="Enter the new name." onClose={onClose} onDone={onDone}>
      <Field label="New name"><Input autoFocus value={newName} aria-label="New index name" onChange={(e) => setNewName(e.target.value)} /></Field>
    </AlterDialog>
  )
}

/** TableOperations: rename, comment, MySQL options and emptying the table (phpMyAdmin's Operations). */
function TableOperations({ session, table, info, run, onRenamed }: {
  session: SessionInfo; table: TableRef; info: TableInfo | null
  run: (ops: AlterOp[], title: string, opts?: { danger?: boolean; done?: string; after?: () => void }) => Promise<void>
  onRenamed: (name: string) => void
}) {
  const driver = session.connection.driver
  const mysql = driver === 'mysql' || driver === 'mariadb'
  const [name, setName] = useState(table.name)
  const [comment, setComment] = useState(info?.comment ?? '')
  const [engine, setEngine] = useState(info?.engine ?? '')
  const [collation, setCollation] = useState(info?.collation ?? '')
  const [autoInc, setAutoInc] = useState(info?.autoIncrement ? String(info.autoIncrement) : '')
  const [convert, setConvert] = useState(false)
  const [types, setTypes] = useState<{ engines: string[]; collations: string[] }>({ engines: [], collations: [] })
  useEffect(() => { setName(table.name) }, [table.name])
  useEffect(() => {
    setComment(info?.comment ?? ''); setEngine(info?.engine ?? ''); setCollation(info?.collation ?? '')
    setAutoInc(info?.autoIncrement ? String(info.autoIncrement) : '')
  }, [info])
  useEffect(() => { if (mysql) api.typeInfo(session.id, table.schema).then(setTypes).catch(() => {}) }, [mysql, session.id, table.schema])
  const optionsChanged = engine !== (info?.engine ?? '') || collation !== (info?.collation ?? '') || convert ||
    (autoInc !== '' && Number(autoInc) !== (info?.autoIncrement ?? 0))

  return (
    <section className="mb-6" aria-label="Table operations">
      <h3 className="mb-2 text-[12px] font-semibold uppercase tracking-wider text-muted">Table</h3>
      <div className="space-y-4 rounded-xl border border-line p-4">
        <div className="flex items-end gap-2">
          <Field label="Rename table" className="flex-1"><Input value={name} aria-label="New table name" onChange={(e) => setName(e.target.value)} /></Field>
          <Button disabled={!name.trim() || name.trim() === table.name}
            onClick={() => run([{ kind: 'rename_table', newName: name.trim() }], `Rename ${table.name} to ${name.trim()}`, { done: 'Table renamed', after: () => onRenamed(name.trim()) })}>Rename</Button>
        </div>
        {driver !== 'sqlite' && (
          <div className="flex items-end gap-2">
            <Field label="Table comment" className="flex-1"><Input value={comment} placeholder="optional" aria-label="Table comment" onChange={(e) => setComment(e.target.value)} /></Field>
            <Button disabled={comment === (info?.comment ?? '')} onClick={() => run([{ kind: 'table_comment', comment }], 'Change the table comment', { done: 'Comment saved' })}>Save</Button>
          </div>
        )}
        {mysql && (
          <div className="space-y-2">
            <div className="grid grid-cols-[1fr_1.4fr_1fr] gap-3">
              <Field label="Storage engine">
                <Select value={engine} aria-label="Storage engine" onChange={(e) => setEngine(e.target.value)}>
                  {[...new Set([engine, ...types.engines])].filter(Boolean).map((x) => <option key={x}>{x}</option>)}
                </Select>
              </Field>
              <Field label="Collation">
                <Input mono value={collation} list="table-collations" aria-label="Table collation" onChange={(e) => setCollation(e.target.value)} />
                <datalist id="table-collations">{types.collations.slice(0, 500).map((c) => <option key={c} value={c} />)}</datalist>
              </Field>
              <Field label="Next AUTO_INCREMENT"><Input inputMode="numeric" value={autoInc} placeholder="—" aria-label="Next AUTO_INCREMENT" onChange={(e) => setAutoInc(e.target.value.replace(/\D/g, ''))} /></Field>
            </div>
            <div className="flex items-center gap-3">
              <Switch checked={convert} onChange={setConvert} label={<span className="text-[12.5px]">Also convert every text column to this collation</span>} />
              <div className="flex-1" />
              <Button disabled={!optionsChanged}
                onClick={() => run([{ kind: 'table_options', options: { engine, collation, autoIncrement: Number(autoInc) || 0, convert } }], 'Change table options', { done: 'Table options saved' })}>
                Save options
              </Button>
            </div>
          </div>
        )}
        <div className="flex items-center gap-3 border-t border-line-2 pt-4">
          <div className="flex-1 text-[12.5px] text-muted">Delete every row but keep the table and its structure.</div>
          <Button variant="ghost" className="text-err" icon={<Eraser className="size-3.5" />}
            onClick={() => run([{ kind: 'truncate' }], `Empty ${table.name}`, { danger: true, done: 'Table emptied' })}>Empty table…</Button>
        </div>
      </div>
    </section>
  )
}
