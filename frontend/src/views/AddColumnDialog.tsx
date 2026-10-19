import clsx from 'clsx'
import { AlertTriangle } from 'lucide-react'
import { useEffect, useState } from 'react'
import { api, ApiError, type ColumnDef, type TableRef } from '../api'
import { TypeSelect } from '../components/TypeSelect'
import { Button, Field, Input, Modal, Select, Switch, errorParts, useToast } from '../components/ui'
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

export function AddColumnDialog({ session, table, columns, onClose, onAdded }: {
  session: SessionInfo
  table: TableRef
  columns: Column[]
  onClose: () => void
  onAdded: (name: string) => void
}) {
  const toast = useToast()
  const driver = session.connection.driver
  const mysql = driver === 'mysql' || driver === 'mariadb'
  const [def, setDef] = useState<ColumnDef>({ name: '', type: '', nullable: true, defaultKind: '', default: '', comment: '', position: '', collation: '', onUpdateNow: false })
  // The type is picked from the database's list (see lib/sqlTypes).
  const [spec, setSpec] = useState<TypeSpec>(() => parseType(driver, TYPES[driver][0]))
  const [collations, setCollations] = useState<string[]>([])
  useEffect(() => { api.typeInfo(session.id, table.schema).then((i) => setCollations(i.collations)).catch(() => {}) }, [session.id, table.schema])
  const t = spec.custom ? undefined : findType(driver, spec.base)
  const typeProblem = spec.custom ? (spec.base.trim() ? '' : 'type the column type') : paramProblem(t, spec.param)
  const full: ColumnDef = { ...def, type: typeString(spec), onUpdateNow: !!def.onUpdateNow && has(t, 'time') }
  const [preview, setPreview] = useState<string[]>([])
  const [problem, setProblem] = useState<string>('')
  const [saving, setSaving] = useState(false)
  const set = <K extends keyof ColumnDef>(k: K, v: ColumnDef[K]) => setDef((d) => ({ ...d, [k]: v }))
  const taken = columns.some((c) => c.name.toLowerCase() === def.name.trim().toLowerCase())

  // Live preview from the server, so it is exactly what will run.
  useEffect(() => {
    if (!def.name.trim()) { setPreview([]); setProblem(''); return }
    const t = setTimeout(async () => {
      try {
        if (typeProblem) throw new ApiError({ title: typeProblem, detail: typeProblem }, 400)
        const r = await api.addColumn(session.id, table, full, true)
        setPreview(r.statements)
        setProblem('')
      } catch (e) {
        setPreview([])
        setProblem((e as ApiError).detail ?? errorParts(e)[1])
      }
    }, 200)
    return () => clearTimeout(t)
  }, [JSON.stringify(full), typeProblem, session.id, table]) // eslint-disable-line react-hooks/exhaustive-deps

  const add = async () => {
    setSaving(true)
    try {
      await api.addColumn(session.id, table, full, false)
      toast('ok', `Column “${def.name.trim()}” added`, table.name)
      onAdded(def.name.trim())
    } catch (e) {
      setProblem(errorParts(e).filter(Boolean).join(' — '))
    } finally {
      setSaving(false)
    }
  }

  const canAdd = def.name.trim() && !typeProblem && !problem && !taken && preview.length > 0
  const production = session.connection.tag === 'production'

  return (
    <Modal width={620} title="Add column" subtitle={<>to <b className="font-medium text-fg-2">{table.name}</b></>} onClose={onClose}
      footer={<>
        {production && <span className="mr-auto flex items-center gap-1.5 text-[12px] font-medium text-err"><AlertTriangle className="size-3.5" /> This is PRODUCTION.</span>}
        <Button variant="ghost" onClick={onClose}>Cancel</Button>
        <Button variant={production ? 'danger' : 'primary'} disabled={!canAdd} loading={saving} onClick={add}>Add column</Button>
      </>}>
      <form className="space-y-3" onSubmit={(e) => { e.preventDefault(); if (canAdd) add() }} aria-label="Add column form">
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
                <option value="">At the end</option>
                <option value="first">First</option>
                {columns.map((c) => <option key={c.name} value={c.name}>After {c.name}</option>)}
              </Select>
            </Field>
          )}
        </div>

        <div>
          <div className="mb-1 text-[11.5px] font-medium text-muted">SQL that will run</div>
          <pre data-testid="column-sql" className={clsx('min-h-[44px] overflow-x-auto whitespace-pre-wrap rounded-lg border px-3 py-2.5 font-mono text-[12px]',
            problem ? 'border-err/30 bg-err/5 text-err' : 'border-line bg-panel-2 text-fg-2')}>
            {problem || (preview.length ? preview.join('\n') : 'Enter a name to see the statement.')}
          </pre>
        </div>
      </form>
    </Modal>
  )
}
