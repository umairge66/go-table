import clsx from 'clsx'
import { ArrowDown, ArrowUp, Code2, Plus, Trash2, TriangleAlert } from 'lucide-react'
import { useEffect, useRef, useState } from 'react'
import { api, type TableRef } from '../api'
import { SqlEditor } from '../components/SqlEditor'
import { Button, IconButton, Select, Spinner, Switch } from '../components/ui'
import { mongoColl } from '../lib/mongoShell'
import { STAGES, WRITES, aggregateCommand, previewCommand, template, type Stage } from '../lib/pipeline'
import type { QueryResult, SessionInfo } from '../types'
import { fmt } from './DataView'

const PREVIEW = 10
const storeKey = (t: TableRef) => `gotable:pipeline:${t.schema}.${t.name}`
const load = (t: TableRef): Stage[] => {
  try { return JSON.parse(localStorage.getItem(storeKey(t)) ?? '[]') } catch { return [] }
}

/**
 * PipelineView builds an aggregation stage by stage, as Compass's
 * Aggregations tab does: each stage shows the documents that leave it.
 */
export function PipelineView({ session, table, openQuery }: { session: SessionInfo; table: TableRef; openQuery: (text: string) => void }) {
  const [stages, setStages] = useState<Stage[]>(() => load(table))
  useEffect(() => {
    try { localStorage.setItem(storeKey(table), JSON.stringify(stages)) } catch { /* not remembered */ }
  }, [stages, table])
  const add = (at: number) => setStages((ss) => [...ss.slice(0, at), { id: crypto.randomUUID(), op: '$match', body: template('$match'), enabled: true }, ...ss.slice(at)])
  const update = (id: string, patch: Partial<Stage>) => setStages((ss) => ss.map((s) => (s.id === id ? { ...s, ...patch } : s)))
  const move = (i: number, by: number) => setStages((ss) => {
    const next = [...ss]
    ;[next[i], next[i + by]] = [next[i + by], next[i]]
    return next
  })

  return (
    <div className="flex h-full flex-col" data-testid="pipeline">
      <div className="flex h-11 shrink-0 items-center gap-2 border-b border-line px-3 text-[12.5px]">
        <span className="text-muted">{stages.length ? `${stages.filter((s) => s.enabled).length} of ${stages.length} stages on · each shows up to ${PREVIEW} documents it passes on` : 'Build an aggregation stage by stage.'}</span>
        <div className="flex-1" />
        {stages.length > 0 && <Button size="sm" variant="ghost" className="text-err" icon={<Trash2 className="size-3.5" />} onClick={() => setStages([])}>Clear</Button>}
        <Button size="sm" variant="ghost" icon={<Code2 className="size-3.5" />} disabled={!stages.length} onClick={() => openQuery(aggregateCommand(table.name, stages))}
          title="Open the whole pipeline as a command, to run, explain or save it">Open in query</Button>
        <Button size="sm" variant="primary" icon={<Plus className="size-3.5" />} onClick={() => add(stages.length)}>Add stage</Button>
      </div>
      <div className="min-h-0 flex-1 overflow-auto p-4">
        <Source session={session} table={table} />
        {stages.map((s, i) => (
          <div key={s.id}>
            <Connector onAdd={() => add(i)} />
            <StageCard session={session} table={table} stages={stages} index={i}
              onChange={(patch) => update(s.id, patch)} onRemove={() => setStages((ss) => ss.filter((x) => x.id !== s.id))}
              onMove={(by) => move(i, by)} />
          </div>
        ))}
        <Connector onAdd={() => add(stages.length)} last />
      </div>
    </div>
  )
}

function Connector({ onAdd, last }: { onAdd: () => void; last?: boolean }) {
  return (
    <div className="group flex h-8 items-center pl-6">
      <div className="h-full border-l-2 border-line" />
      <button onClick={onAdd} aria-label={last ? 'Add a stage at the end' : 'Add a stage here'}
        className={clsx('ml-2 flex items-center gap-1 rounded-md px-1.5 py-0.5 text-[11.5px] font-medium text-accent hover:bg-accent-soft', !last && 'opacity-0 group-hover:opacity-100 focus:opacity-100')}>
        <Plus className="size-3" /> {last ? 'Add stage' : 'Insert a stage'}
      </button>
    </div>
  )
}

/** Source: what goes into the first stage. */
function Source({ session, table }: { session: SessionInfo; table: TableRef }) {
  const [total, setTotal] = useState<number | null>(null)
  useEffect(() => {
    api.execute(session.id, `${mongoColl(table.name)}.estimatedDocumentCount()`, 1, crypto.randomUUID(), table.schema)
      .then((r) => setTotal(Number(r.results[0]?.rows?.[0]?.[0] ?? NaN)))
      .catch(() => setTotal(null))
  }, [session.id, table])
  return (
    <div className="inline-flex items-center gap-2 rounded-lg border border-line bg-panel-2 px-3 py-2 text-[12.5px]">
      <span className="font-mono font-semibold">{table.name}</span>
      {total !== null && !Number.isNaN(total) && <span className="text-muted">about {fmt(total)} documents</span>}
    </div>
  )
}

function StageCard({ session, table, stages, index, onChange, onRemove, onMove }: {
  session: SessionInfo; table: TableRef; stages: Stage[]; index: number
  onChange: (patch: Partial<Stage>) => void; onRemove: () => void; onMove: (by: number) => void
}) {
  const s = stages[index]
  const writes = WRITES.includes(s.op)
  const command = previewCommand(table.name, stages, index, PREVIEW)
  const [result, setResult] = useState<QueryResult | null>(null)
  const [busy, setBusy] = useState(false)
  const seq = useRef(0)
  // Preview the documents after this stage, a moment after typing stops.
  useEffect(() => {
    if (!command) { setResult(null); return }
    const my = ++seq.current
    const t = setTimeout(async () => {
      setBusy(true)
      try {
        const r = await api.execute(session.id, command, PREVIEW, crypto.randomUUID(), table.schema)
        if (my === seq.current) setResult(r.results[r.results.length - 1] ?? null)
      } catch (e) {
        if (my === seq.current) setResult({ statement: command, hasRows: false, rowsAffected: 0, ms: 0, truncated: false, error: { title: 'Could not run the preview', detail: String((e as Error).message ?? e) } })
      } finally {
        if (my === seq.current) setBusy(false)
      }
    }, 450)
    return () => clearTimeout(t)
  }, [command, session.id, table.schema])

  const changeOp = (op: string) => {
    // A template replaces the body unless it was edited.
    const untouched = s.body.trim() === template(s.op).trim()
    onChange({ op, ...(untouched ? { body: template(op) } : {}) })
  }
  const rows = result?.rows ?? []
  return (
    <div className={clsx('overflow-hidden rounded-xl border', s.enabled ? 'border-line' : 'border-dashed border-line opacity-60')} data-testid={`stage-${index + 1}`}>
      <div className="flex items-center gap-2 border-b border-line bg-panel-2 px-3 py-1.5">
        <span className="text-[11.5px] font-semibold text-muted">Stage {index + 1}</span>
        <Select className="w-40" aria-label={`Stage ${index + 1} operator`} value={s.op} onChange={(e) => changeOp(e.target.value)}>
          {STAGES.map(([op]) => <option key={op} value={op}>{op}</option>)}
        </Select>
        <Switch checked={s.enabled} onChange={(v) => onChange({ enabled: v })} label={<span className="text-[12px] text-muted">On</span>} />
        <div className="flex-1" />
        {busy && <Spinner className="size-3.5" />}
        <IconButton label={`Move stage ${index + 1} up`} disabled={index === 0} onClick={() => onMove(-1)}><ArrowUp className="size-3.5" /></IconButton>
        <IconButton label={`Move stage ${index + 1} down`} disabled={index === stages.length - 1} onClick={() => onMove(1)}><ArrowDown className="size-3.5" /></IconButton>
        <IconButton label={`Remove stage ${index + 1}`} onClick={onRemove}><Trash2 className="size-3.5" /></IconButton>
      </div>
      <div className="grid grid-cols-[minmax(260px,1fr)_minmax(300px,1.3fr)]">
        <div className="h-[180px] border-r border-line" data-testid={`stage-${index + 1}-editor`}>
          <SqlEditor key={s.id + s.op} value={s.body} driver={session.connection.driver} onChange={(body) => onChange({ body })} />
        </div>
        <div className="h-[180px] overflow-auto bg-panel-2 p-2" data-testid={`stage-${index + 1}-output`}>
          {!s.enabled ? <Note>This stage is off: documents pass it unchanged.</Note>
            : writes || !command ? <Note icon><b>{s.op}</b> writes documents, so it isn&apos;t previewed. Open in query to run the pipeline.</Note>
            : result?.error ? <div className={clsx(busy && 'opacity-50', 'rounded-lg')}><div className="rounded-lg border border-err/30 bg-err/5 p-2 text-[12px] text-err"><div className="font-semibold">{result.error.title}</div><div className="mt-0.5 break-words font-mono text-[11.5px]">{result.error.detail}</div></div></div>
            : !result ? <Note>…</Note>
            : rows.length === 0 ? <Note>No documents leave this stage.</Note>
            : (
              <>
                <div className="mb-1.5 text-[11.5px] text-muted">{rows.length === PREVIEW ? `First ${PREVIEW} documents` : `${rows.length} document${rows.length === 1 ? '' : 's'}`}</div>
                <div className={clsx('space-y-1.5', busy && 'opacity-50')}>
                  {rows.map((row, r) => <DocCard key={r} text={row[result.columns!.indexOf('doc')] ?? ''} />)}
                </div>
              </>
            )}
        </div>
      </div>
    </div>
  )
}

/** DocCard shows one document, field by field, from its relaxed Extended JSON. */
function DocCard({ text }: { text: string }) {
  let doc: Record<string, unknown> | null = null
  try { doc = JSON.parse(text) } catch { /* shown as text */ }
  return (
    <div className="rounded-md border border-line bg-panel px-2 py-1 font-mono text-[11.5px] leading-[1.5]">
      {doc && typeof doc === 'object' ? Object.entries(doc).map(([k, v]) => (
        <div key={k} className="truncate"><span className="text-muted">{k}: </span><Value v={v} /></div>
      )) : text}
    </div>
  )
}

function Value({ v }: { v: unknown }) {
  if (v === null) return <span className="text-faint">null</span>
  if (typeof v === 'string') return <span className="text-accent">{JSON.stringify(v)}</span>
  if (typeof v === 'object') {
    // Extended JSON's wrappers read as the shell writes them.
    const o = v as Record<string, unknown>
    if ('$oid' in o) return <>ObjectId(&quot;{String(o.$oid)}&quot;)</>
    if ('$date' in o) return <>ISODate(&quot;{typeof o.$date === 'string' ? o.$date : JSON.stringify(o.$date)}&quot;)</>
    if ('$numberLong' in o) return <>{String(o.$numberLong)}</>
    if ('$numberDecimal' in o) return <>{String(o.$numberDecimal)}</>
    return <>{JSON.stringify(v)}</>
  }
  return <>{String(v)}</>
}

const Note = ({ children, icon }: { children: React.ReactNode; icon?: boolean }) => (
  <div className="flex items-start gap-1.5 p-1 text-[12px] text-muted">{icon && <TriangleAlert className="mt-0.5 size-3.5 shrink-0 text-warn" />}<span>{children}</span></div>
)
