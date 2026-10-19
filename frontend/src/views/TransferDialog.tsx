import clsx from 'clsx'
import { Database, Eye, FileArchive, FileCode2, Search, Table2 } from 'lucide-react'
import { useEffect, useRef, useState } from 'react'
import { api } from '../api'
import { FilePicker, pickedName, type PickedFile } from '../components/FilePicker'
import { JobProgress, UploadProgress, useJob } from '../components/Progress'
import { Button, Input, Modal, Switch, errorParts, useToast } from '../components/ui'
import { chooseSaveLocation, saveJob, timestamp, type SaveTarget } from '../lib/saveFile'
import type { DbObject, FileSource, SessionInfo } from '../types'
import { saveJobAs } from './ExportDialog'

type Tab = 'export' | 'import'

/** TransferDialog exports or imports a whole database (schema). */
export function TransferDialog({ session, schema, objects, initialTab = 'export', preselect, onClose, onImported }: {
  session: SessionInfo
  schema: string
  objects: DbObject[]
  initialTab?: Tab
  /** preselect limits the initial export selection to these names. */
  preselect?: string[]
  onClose: () => void
  onImported: () => void
}) {
  const [tab, setTab] = useState<Tab>(initialTab)
  const [busy, setBusy] = useState(false)
  return (
    <Modal width={640} title={tab === 'export' ? 'Export database' : 'Import into database'}
      subtitle={<>{session.connection.name} · schema <b className="font-medium text-fg-2">{schema}</b></>} onClose={onClose}>
      {!busy && (
        <div className="mb-4 flex rounded-lg border border-line bg-panel p-0.5" role="tablist" aria-label="Direction">
          {(['export', 'import'] as const).map((t) => (
            <button key={t} role="tab" aria-selected={tab === t} onClick={() => setTab(t)}
              className={clsx('h-7 flex-1 rounded-md text-[12.5px] font-medium', tab === t ? 'bg-accent text-accent-fg' : 'text-muted hover:text-fg')}>
              {t === 'export' ? 'Export' : 'Import'}
            </button>
          ))}
        </div>
      )}
      {tab === 'export'
        ? <ExportDatabase session={session} schema={schema} objects={objects} preselect={preselect} onBusy={setBusy} />
        : <ImportSQL session={session} schema={schema} onBusy={setBusy} onImported={onImported} />}
    </Modal>
  )
}

function ExportDatabase({ session, schema, objects, preselect, onBusy }: { session: SessionInfo; schema: string; objects: DbObject[]; preselect?: string[]; onBusy: (b: boolean) => void }) {
  const toast = useToast()
  const [selected, setSelected] = useState<Set<string>>(() => new Set(preselect ?? objects.map((o) => o.name)))
  const [filter, setFilter] = useState('')
  const [format, setFormat] = useState<'sql' | 'csv-zip'>('sql')
  const [structure, setStructure] = useState(true)
  const [data, setData] = useState(true)
  const [drop, setDrop] = useState(false)
  const [jobId, setJobId] = useState<string | null>(null)
  const job = useJob(jobId)
  const target = useRef<SaveTarget | null>(null)
  const saved = useRef(false)
  const [savedTo, setSavedTo] = useState<string | null>(null)

  // The direction tabs come back as soon as the task stops.
  useEffect(() => { if (job && job.status !== 'running') onBusy(false) }, [job, onBusy])
  // When the export is ready, write it where the user chose.
  useEffect(() => {
    if (job?.status !== 'done' || saved.current || !target.current) return
    saved.current = true
    saveJob(target.current, job.id)
      .then(() => { setSavedTo(target.current!.name); toast('ok', 'Export saved', target.current!.name) })
      .catch((e) => toast('error', 'Could not save the file', String(e?.message ?? e)))
  }, [job, toast])

  const shown = objects.filter((o) => !filter || o.name.toLowerCase().includes(filter.toLowerCase()))
  const chosen = objects.filter((o) => selected.has(o.name) && (format === 'sql' || o.kind === 'table'))
  const toggle = (name: string) => setSelected((s) => { const n = new Set(s); if (n.has(name)) n.delete(name); else n.add(name); return n })

  const start = async () => {
    try {
      // Ask where to save first, like any desktop app.
      const ext = format === 'sql' ? 'sql' : 'zip'
      const t = await chooseSaveLocation(`${session.connection.name.replace(/[^\w.-]+/g, '_')}-${timestamp()}.${ext}`, ext)
      if (!t) return
      target.current = t
      saved.current = false
      setSavedTo(null)
      onBusy(true)
      const j = await api.createJob(session.id, {
        kind: 'export-database', schema, format, objects: chosen, structure, data, dropFirst: drop, batchSize: 200, csv: { header: true, delimiter: ',' },
      })
      setJobId(j.id)
    } catch (e) {
      onBusy(false)
      toast('error', ...errorParts(e))
    }
  }

  if (job) {
    return (
      <div className="space-y-4">
        <JobProgress job={job} label={savedTo ? `Saved to ${savedTo}` : undefined} onCancel={() => api.cancelJob(job.id)}
          onDownload={() => saveJobAs(job.id, target.current?.name ?? job.file ?? 'export', format === 'sql' ? 'sql' : 'zip')} />
        {job.status !== 'running' && (
          <div className="flex justify-end"><Button size="sm" onClick={() => { setJobId(null); onBusy(false) }}>Export again</Button></div>
        )}
      </div>
    )
  }

  return (
    <div className="space-y-4">
      <div className="grid grid-cols-2 gap-2" role="radiogroup" aria-label="Export format">
        {([['sql', FileCode2, 'SQL dump', 'Tables, data and views — load it into any database of this kind'],
          ['csv-zip', FileArchive, 'CSV files (ZIP)', 'One spreadsheet-friendly CSV per table']] as const).map(([id, Icon, label, desc]) => (
          <button key={id} role="radio" aria-checked={format === id} onClick={() => setFormat(id)}
            className={clsx('flex gap-3 rounded-xl border p-3 text-left transition-all', format === id ? 'border-accent ring-3 ring-accent/15' : 'border-line hover:border-faint')}>
            <Icon className={clsx('mt-0.5 size-5 shrink-0', format === id ? 'text-accent' : 'text-muted')} />
            <span><span className="block text-[13px] font-semibold">{label}</span><span className="block text-[11.5px] text-muted">{desc}</span></span>
          </button>
        ))}
      </div>
      {format === 'sql' && (
        <div className="flex flex-wrap gap-x-5 gap-y-2">
          <Switch checked={structure} onChange={setStructure} label="Structure" />
          <Switch checked={data} onChange={setData} label="Data" />
          <Switch checked={drop} onChange={setDrop} label="DROP existing tables first" />
        </div>
      )}
      <div className="rounded-xl border border-line">
        <div className="flex items-center gap-2 border-b border-line-2 p-2">
          <Input icon={<Search />} className="flex-1" value={filter} placeholder="Filter tables" onChange={(e) => setFilter(e.target.value)} aria-label="Filter export tables" />
          <button className="text-[12px] font-medium text-accent hover:underline" onClick={() => setSelected(new Set(objects.map((o) => o.name)))}>All</button>
          <button className="text-[12px] font-medium text-accent hover:underline" onClick={() => setSelected(new Set())}>None</button>
        </div>
        <div className="max-h-56 overflow-y-auto p-1" aria-label="Tables to export">
          {shown.map((o) => {
            const disabled = format === 'csv-zip' && o.kind === 'view'
            return (
              <label key={o.name} className={clsx('flex h-8 items-center gap-2.5 rounded-md px-2 text-[13px] hover:bg-hover', disabled && 'opacity-40')}>
                <input type="checkbox" className="accent-[var(--accent)]" disabled={disabled} checked={selected.has(o.name) && !disabled} onChange={() => toggle(o.name)} />
                {o.kind === 'view' ? <Eye className="size-3.5 text-muted" /> : <Table2 className="size-3.5 text-muted" />}
                {o.name}
              </label>
            )
          })}
          {objects.length === 0 && <div className="p-4 text-center text-[12.5px] text-muted">This schema has no tables.</div>}
        </div>
      </div>
      <div className="flex items-center justify-between">
        <span className="text-[12px] text-muted">{chosen.length} of {objects.length} selected</span>
        <Button variant="primary" icon={<Database className="size-3.5" />} disabled={!chosen.length || (format === 'sql' && !structure && !data)} onClick={start}>
          Export {chosen.length} {chosen.length === 1 ? 'table' : 'tables'}…
        </Button>
      </div>
    </div>
  )
}

/** startUpload sends a picked file (or passes a path) and returns the job source. */
export async function toSource(p: PickedFile, onProgress: (f: number, loaded: number) => void, signal: AbortSignal): Promise<FileSource> {
  if (p.kind === 'path') return { path: p.path }
  const up = await api.upload(p.file, onProgress, signal)
  return { uploadId: up.id }
}

function ImportSQL({ session, schema, onBusy, onImported }: { session: SessionInfo; schema: string; onBusy: (b: boolean) => void; onImported: () => void }) {
  const toast = useToast()
  const mysql = session.connection.driver === 'mysql' || session.connection.driver === 'mariadb'
  const [file, setFile] = useState<PickedFile | null>(null)
  const [mode, setMode] = useState<'transaction' | 'continue'>(mysql ? 'continue' : 'transaction')
  const [upload, setUpload] = useState<{ fraction: number; loaded: number } | null>(null)
  const [jobId, setJobId] = useState<string | null>(null)
  const job = useJob(jobId)
  const abort = useRef<AbortController | null>(null)
  const reported = useRef(false)

  useEffect(() => {
    if (job && job.status !== 'running' && !reported.current) {
      reported.current = true
      onBusy(false)
      onImported()
      if (job.status === 'done') toast('ok', 'Import finished', `${job.statements.toLocaleString()} statements${job.failed ? `, ${job.failed} failed` : ''}`)
    }
  }, [job, onBusy, onImported, toast])

  const start = async () => {
    if (!file) return
    onBusy(true)
    reported.current = false
    abort.current = new AbortController()
    try {
      if (file.kind === 'file') setUpload({ fraction: 0, loaded: 0 })
      const source = await toSource(file, (fraction, loaded) => setUpload({ fraction, loaded }), abort.current.signal)
      setUpload(null)
      const j = await api.createJob(session.id, { kind: 'import-sql', schema, source, transaction: mode === 'transaction', continueOnError: mode === 'continue' })
      setJobId(j.id)
    } catch (e) {
      setUpload(null)
      onBusy(false)
      if ((e as Error).name !== 'AbortError') toast('error', ...errorParts(e))
    }
  }

  if (upload && file?.kind === 'file') {
    return (
      <div className="space-y-4">
        <UploadProgress name={file.file.name} fraction={upload.fraction} loaded={upload.loaded} size={file.file.size} />
        <div className="flex justify-end"><Button size="sm" variant="ghost" onClick={() => abort.current?.abort()}>Cancel</Button></div>
      </div>
    )
  }
  if (job) {
    return (
      <div className="space-y-4">
        <JobProgress job={job} label={`Importing ${pickedName(file!)}`} onCancel={() => api.cancelJob(job.id)} />
        {job.status !== 'running' && (
          <div className="flex justify-end"><Button size="sm" onClick={() => { setJobId(null); setFile(null); onBusy(false) }}>Import another file</Button></div>
        )}
      </div>
    )
  }
  return (
    <div className="space-y-4">
      <FilePicker accept=".sql,.txt,.dump" hint="SQL scripts and dumps (pg_dump plain format, mysqldump, sqlite .dump) — any size" value={file} onChange={setFile} />
      <div className="space-y-2" role="radiogroup" aria-label="If a statement fails">
        <div className="text-[11.5px] font-medium text-muted">If a statement fails</div>
        {([['transaction', 'Stop and undo everything', mysql
          ? 'Runs in one transaction. Note: MySQL commits CREATE/ALTER/DROP immediately, so those can’t be undone.'
          : 'Runs in one transaction: the database is unchanged unless the whole file succeeds.'],
          ['continue', 'Skip it and keep going', 'Failed statements are listed with their line numbers at the end.']] as const).map(([id, label, desc]) => (
          <label key={id} className={clsx('flex cursor-pointer gap-3 rounded-xl border p-3', mode === id ? 'border-accent bg-accent-soft/40' : 'border-line hover:bg-hover')}>
            <input type="radio" className="mt-0.5 accent-[var(--accent)]" checked={mode === id} onChange={() => setMode(id)} />
            <span><span className="block text-[13px] font-medium">{label}</span><span className="block text-[12px] text-muted">{desc}</span></span>
          </label>
        ))}
      </div>
      {session.connection.tag === 'production' && (
        <div className="rounded-lg border border-err/30 bg-err/5 p-2.5 text-[12.5px] font-medium text-err">You are importing into PRODUCTION.</div>
      )}
      <div className="flex justify-end">
        <Button variant={session.connection.tag === 'production' ? 'danger' : 'primary'} disabled={!file} onClick={start}>Start import</Button>
      </div>
    </div>
  )
}
