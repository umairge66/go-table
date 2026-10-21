import clsx from 'clsx'
import { useEffect, useState } from 'react'
import { api, type AlterOp, type ColumnDef, type TableRef } from '../api'
import { AlterDialog } from './StructureDialogs'
import { TypeSelect } from '../components/TypeSelect'
import { Field, Input, Select, Switch } from '../components/ui'
import { findType, has, paramProblem, parseType, typeString, type TypeSpec } from '../lib/sqlTypes'
import type { Column, Driver, SessionInfo } from '../types'

export const TYPES: Record<Driver, string[]> = {
  postgres: ['text', 'varchar(255)', 'integer', 'bigint', 'numeric(10,2)', 'boolean', 'date', 'timestamptz', 'jsonb', 'uuid',
    'double precision', 'timestamp', 'bytea', 'text[]', 'smallint', 'real', 'time', 'inet'],
  mysql: ['varchar(255)', 'text', 'int', 'bigint', 'decimal(10,2)', 'tinyint(1)', 'date', 'datetime', 'json', 'timestamp',
    'double', 'char(36)', 'blob', "enum('a','b')", 'smallint', 'longtext'],
  mariadb: ['varchar(255)', 'text', 'int', 'bigint', 'decimal(10,2)', 'tinyint(1)', 'date', 'datetime', 'json', 'timestamp',
    'double', 'uuid', 'blob', "enum('a','b')", 'smallint', 'longtext'],
  sqlite: ['TEXT', 'INTEGER', 'REAL', 'NUMERIC', 'BLOB', 'BOOLEAN', 'DATETIME'],
}

export const EXPRESSIONS: Record<Driver, string[]> = {
  postgres: ['now()', 'CURRENT_DATE', 'gen_random_uuid()', "'{}'::jsonb"],
  mysql: ['CURRENT_TIMESTAMP', '(UUID())'],
  mariadb: ['CURRENT_TIMESTAMP', 'uuid()'],
  sqlite: [],
}

const DEFAULT_KINDS: { id: ColumnDef['defaultKind']; label: string }[] = [
  { id: '', label: 'None' }, { id: 'value', label: 'Value' }, { id: 'expression', label: 'Expression' }, { id: 'null', label: 'NULL' },
]

/** ColumnDialog adds a column, or edits one (editing) in place. */
export function ColumnDialog({ session, table, columns, editing, onClose, onDone }: {
  session: SessionInfo
  table: TableRef
  columns: Column[]
  editing?: Column
  onClose: () => void
  onDone: (name: string) => void
}) {
  const driver = session.connection.driver
  const mysql = driver === 'mysql' || driver === 'mariadb'
  const [def, setDef] = useState<ColumnDef>(() => editing
    ? { name: editing.name, type: editing.type, nullable: editing.nullable, defaultKind: editing.defaultKind ?? '', default: editing.defaultText ?? '',
        comment: editing.comment, position: '', collation: editing.collation ?? '', onUpdateNow: !!editing.onUpdateNow }
    : { name: '', type: '', nullable: true, defaultKind: '', default: '', comment: '', position: '', collation: '', onUpdateNow: false })
  // The type is picked from the database's list (see lib/sqlTypes).
  const [spec, setSpec] = useState<TypeSpec>(() => {
    const p = parseType(driver, editing?.type ?? TYPES[driver][0])
    // A bracket value the type list doesn't know (e.g. SQLite NUMERIC(10,2)): keep the type as written.
    return editing && !p.custom && p.param && !findType(driver, p.base)?.param ? { base: editing.type, param: '', unsigned: false, array: false, custom: true } : p
  })
  const [autoIncrement, setAutoIncrement] = useState(!!editing?.autoIncrement)
  const [collations, setCollations] = useState<string[]>([])
  useEffect(() => { api.typeInfo(session.id, table.schema).then((i) => setCollations(i.collations)).catch(() => {}) }, [session.id, table.schema])
  const t = spec.custom ? undefined : findType(driver, spec.base)
  const typeProblem = spec.custom ? (spec.base.trim() ? '' : 'type the column type') : paramProblem(t, spec.param)
  const full: ColumnDef = { ...def, name: def.name.trim(), type: typeString(spec), onUpdateNow: !!def.onUpdateNow && (has(t, 'time') || (spec.custom && !!editing?.onUpdateNow)) }
  const set = <K extends keyof ColumnDef>(k: K, v: ColumnDef[K]) => setDef((d) => ({ ...d, [k]: v }))
  const taken = columns.some((c) => c.name !== editing?.name && c.name.toLowerCase() === def.name.trim().toLowerCase())
  const others = columns.filter((c) => c.name !== editing?.name)
  // An edit only runs once something differs from how the dialog opened.
  const snapshot = JSON.stringify([full, autoIncrement])
  const [initial] = useState(snapshot)
  const unchanged = !!editing && snapshot === initial
  const ops: AlterOp[] | null = !def.name.trim() || typeProblem || taken || unchanged ? null
    : [editing ? { kind: 'modify_column', column: editing.name, def: full, autoIncrement: mysql && autoIncrement } : { kind: 'add_column', def: full }]

  return (
    <AlterDialog session={session} table={table} title={editing ? `Edit column ${editing.name}` : 'Add column'} submitLabel={editing ? 'Save column' : 'Add column'}
      done={editing ? `Column “${full.name}” changed` : `Column “${full.name}” added`} ops={ops}
      incomplete={unchanged ? 'No changes yet.' : !def.name.trim() ? 'Enter a name to see the statement.' : typeProblem || 'Choose another name.'}
      onClose={onClose} onDone={() => onDone(full.name)}>
        <div className="grid grid-cols-2 gap-3">
          <Field label="Name" hint={taken ? <span className="text-err">A column with this name already exists.</span> : undefined}>
            <Input autoFocus value={def.name} placeholder="e.g. email" onChange={(e) => set('name', e.target.value)} aria-label="Column name" />
          </Field>
          <Field label="Type">
            <div className="flex gap-1.5">
              <TypeSelect driver={driver} value={spec} onChange={setSpec} className="min-w-0 flex-1" />
              {t?.param && (
                <input className="h-8 w-24 rounded-md border border-line bg-panel px-2 font-mono text-[12.5px] focus:border-accent focus:outline-none"
                  aria-label="Length/Values" value={spec.param} placeholder={t.param.kind === 'fsp' ? '0–6' : t.param.placeholder}
                  onChange={(e) => setSpec({ ...spec, param: e.target.value })} />
              )}
            </div>
          </Field>
        </div>
        <div className="flex flex-wrap items-center gap-4">
          {mysql && has(t, 'unsigned') && <Switch checked={spec.unsigned} onChange={(v) => setSpec({ ...spec, unsigned: v })} label="UNSIGNED" />}
          {mysql && editing && (has(t, 'int') || spec.custom) && <Switch checked={autoIncrement} onChange={setAutoIncrement} label="AUTO_INCREMENT" />}
          {mysql && has(t, 'time') && <Switch checked={!!def.onUpdateNow} onChange={(v) => set('onUpdateNow', v)} label="On update: CURRENT_TIMESTAMP" />}
          {driver === 'postgres' && !spec.custom && <Switch checked={spec.array} onChange={(v) => setSpec({ ...spec, array: v })} label="Array ( [ ] )" />}
          {(has(t, 'text') || spec.custom) && (
            <label className="flex items-center gap-2 text-[12.5px] text-muted">Collation
              <input className="h-7 w-48 rounded-md border border-line bg-panel px-2 font-mono text-[12px] text-fg focus:border-accent focus:outline-none" list="add-column-collations"
                aria-label="Collation" value={def.collation ?? ''} placeholder="default" onChange={(e) => set('collation', e.target.value)} />
              <datalist id="add-column-collations">{collations.slice(0, 500).map((c) => <option key={c} value={c} />)}</datalist>
            </label>
          )}
        </div>
        <div className="grid grid-cols-2 gap-3 pt-1">
          <Field label="Default">
            <div className="flex rounded-lg border border-line bg-panel p-0.5" role="radiogroup" aria-label="Default kind">
              {DEFAULT_KINDS.map((k) => (
                <button type="button" key={k.id} role="radio" aria-checked={def.defaultKind === k.id} onClick={() => set('defaultKind', k.id)}
                  className={clsx('h-6 flex-1 rounded-md text-[12px] font-medium', def.defaultKind === k.id ? 'bg-accent text-accent-fg' : 'text-muted hover:text-fg')}>
                  {k.label}
                </button>
              ))}
            </div>
          </Field>
          <div className="flex items-end pb-1.5">
            <Switch checked={def.nullable} onChange={(v) => set('nullable', v)} label={<span>Allow NULL</span>} />
          </div>
        </div>
        {(def.defaultKind === 'value' || def.defaultKind === 'expression') && (
          <div className="animate-fade">
            <Input mono value={def.default} aria-label="Default value"
              placeholder={def.defaultKind === 'value' ? 'stored as text, e.g. pending or 0' : EXPRESSIONS[driver][0] ?? 'expression'}
              onChange={(e) => set('default', e.target.value)} />
            {def.defaultKind === 'expression' && EXPRESSIONS[driver].length > 0 && (
              <div className="mt-1.5 flex flex-wrap gap-1.5">
                {EXPRESSIONS[driver].map((x) => (
                  <button type="button" key={x} onClick={() => set('default', x)} className="h-6 rounded-full border border-line px-2 font-mono text-[11.5px] text-muted hover:bg-hover hover:text-fg">{x}</button>
                ))}
              </div>
            )}
          </div>
        )}

        <div className={clsx('grid gap-3', mysql ? 'grid-cols-2' : 'grid-cols-1')}>
          {driver !== 'sqlite' && (
            <Field label="Comment"><Input value={def.comment} placeholder="optional" onChange={(e) => set('comment', e.target.value)} aria-label="Column comment" /></Field>
          )}
          {mysql && (
            <Field label="Position">
              <Select value={def.position} onChange={(e) => set('position', e.target.value)} aria-label="Column position">
                <option value="">{editing ? 'Where it is' : 'At the end'}</option>
                <option value="first">First</option>
                {others.map((c) => <option key={c.name} value={c.name}>After {c.name}</option>)}
              </Select>
            </Field>
          )}
        </div>

    </AlterDialog>
  )
}
