import { useEffect, useState } from 'react'
import { api, type NewTable } from '../api'
import { Button, Field, Input, Modal, Select, errorParts, useToast } from '../components/ui'
import type { SessionInfo } from '../types'

type Kind = 'plain' | 'capped' | 'timeseries' | 'clustered'

const KINDS: [Kind, string, string][] = [
  ['plain', 'Regular', 'Documents of any shape; fields come with them.'],
  ['capped', 'Capped', 'A fixed size: when it is full, the oldest documents make room. Kept in insertion order, like a log.'],
  ['timeseries', 'Time series', 'Measurements over time, stored compactly by time and source (MongoDB 5.0+).'],
  ['clustered', 'Clustered', 'Stored in _id order, which makes range queries on _id fast (MongoDB 5.3+).'],
]

const UNITS: [string, number][] = [['KB', 1 << 10], ['MB', 1 << 20], ['GB', 1 << 30]]

/**
 * CreateCollectionDialog makes a MongoDB collection: a name is all a regular
 * one needs, as fields come with the documents; Compass's other kinds take a
 * few settings.
 */
export function CreateCollectionDialog({ session, schema, onClose, onCreated }: {
  session: SessionInfo
  schema: string
  onClose: () => void
  onCreated: (name: string) => void
}) {
  const toast = useToast()
  const [name, setName] = useState('')
  const [kind, setKind] = useState<Kind>('plain')
  const [size, setSize] = useState('16')
  const [unit, setUnit] = useState(1 << 20)
  const [maxDocs, setMaxDocs] = useState('')
  const [timeField, setTimeField] = useState('timestamp')
  const [metaField, setMetaField] = useState('')
  const [granularity, setGranularity] = useState('')
  const [expire, setExpire] = useState('')
  const [preview, setPreview] = useState<string[]>([])
  const [problem, setProblem] = useState('')
  const [busy, setBusy] = useState(false)
  const expireAfterSeconds = expire.trim() === '' ? undefined : Number(expire)
  const table: NewTable = {
    name: name.trim(), columns: [], comment: '',
    mongo: kind === 'capped' ? { capped: true, sizeBytes: Math.round(Number(size) * unit), maxDocs: Number(maxDocs) || undefined }
      : kind === 'timeseries' ? { timeField, metaField: metaField.trim() || undefined, granularity: granularity || undefined, expireAfterSeconds }
      : kind === 'clustered' ? { clustered: true, expireAfterSeconds }
      : undefined,
  }
  const key = JSON.stringify(table)
  useEffect(() => {
    if (!table.name) { setPreview([]); setProblem(''); return }
    let stale = false
    api.createTable(session.id, schema, table, true)
      .then((r) => { if (!stale) { setPreview(r.statements); setProblem('') } })
      .catch((e) => { if (!stale) { setPreview([]); setProblem(errorParts(e).filter(Boolean).join(' — ')) } })
    return () => { stale = true }
  }, [session.id, schema, key]) // eslint-disable-line react-hooks/exhaustive-deps
  const create = async () => {
    if (!table.name || busy || problem) return
    setBusy(true)
    try {
      await api.createTable(session.id, schema, table, false)
      toast('ok', 'Collection created', table.name)
      onCreated(table.name)
    } catch (e) {
      setProblem(errorParts(e).filter(Boolean).join(' — '))
      setBusy(false)
    }
  }
  return (
    <Modal width={560} title="New collection" subtitle={<>in <b className="font-medium text-fg-2">{schema}</b></>} onClose={onClose}
      footer={<><Button variant="ghost" onClick={onClose}>Cancel</Button><Button variant="primary" disabled={!table.name || busy || !!problem} onClick={create}>Create</Button></>}>
      <form className="space-y-3" aria-label="New collection" onSubmit={(e) => { e.preventDefault(); create() }}>
        <div className="grid grid-cols-[1fr_170px] gap-3">
          <Field label="Name"><Input autoFocus value={name} aria-label="Collection name" placeholder="orders" onChange={(e) => setName(e.target.value)} /></Field>
          <Field label="Kind">
            <Select value={kind} aria-label="Collection kind" onChange={(e) => setKind(e.target.value as Kind)}>
              {KINDS.map(([k, l]) => <option key={k} value={k}>{l}</option>)}
            </Select>
          </Field>
        </div>
        <div className="text-[12.5px] text-muted">{KINDS.find(([k]) => k === kind)![2]}</div>
        {kind === 'capped' && (
          <div className="grid grid-cols-[1fr_90px_1fr] gap-3">
            <Field label="Size"><Input inputMode="decimal" value={size} aria-label="Capped size" onChange={(e) => setSize(e.target.value.replace(/[^\d.]/g, ''))} /></Field>
            <Field label="Unit">
              <Select value={unit} aria-label="Size unit" onChange={(e) => setUnit(Number(e.target.value))}>{UNITS.map(([l, n]) => <option key={l} value={n}>{l}</option>)}</Select>
            </Field>
            <Field label="At most documents"><Input inputMode="numeric" value={maxDocs} placeholder="no limit" aria-label="Maximum documents" onChange={(e) => setMaxDocs(e.target.value.replace(/\D/g, ''))} /></Field>
          </div>
        )}
        {kind === 'timeseries' && (
          <div className="grid grid-cols-3 gap-3">
            <Field label="Time field" hint="A date in every document."><Input mono value={timeField} aria-label="Time field" onChange={(e) => setTimeField(e.target.value)} /></Field>
            <Field label="Meta field" hint="Which source, e.g. sensor."><Input mono value={metaField} placeholder="optional" aria-label="Meta field" onChange={(e) => setMetaField(e.target.value)} /></Field>
            <Field label="Granularity">
              <Select value={granularity} aria-label="Granularity" onChange={(e) => setGranularity(e.target.value)}>
                <option value="">automatic</option><option value="seconds">seconds</option><option value="minutes">minutes</option><option value="hours">hours</option>
              </Select>
            </Field>
          </div>
        )}
        {(kind === 'timeseries' || kind === 'clustered') && (
          <Field label="Delete documents after (seconds)" hint={kind === 'timeseries' ? 'Counted from the time field.' : 'Counted from _id, which must then be a date.'}>
            <Input inputMode="numeric" value={expire} placeholder="never" aria-label="Expire after seconds" onChange={(e) => setExpire(e.target.value.replace(/\D/g, ''))} />
          </Field>
        )}
        <div>
          <div className="mb-1 text-[11.5px] font-medium text-muted">Command that will run</div>
          <pre data-testid="create-preview" className={problem
            ? 'min-h-[44px] whitespace-pre-wrap rounded-lg border border-err/30 bg-err/5 px-3 py-2.5 font-mono text-[12px] text-err'
            : 'min-h-[44px] overflow-x-auto whitespace-pre-wrap rounded-lg border border-line bg-panel-2 px-3 py-2.5 font-mono text-[12px] text-fg-2'}>
            {problem || preview.join('\n') || 'Enter a name.'}
          </pre>
        </div>
        <button type="submit" hidden />
      </form>
    </Modal>
  )
}
