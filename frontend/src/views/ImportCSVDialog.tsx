import clsx from 'clsx'
import { ArrowRight, KeyRound } from 'lucide-react'
import { useEffect, useMemo, useRef, useState } from 'react'
import { api, type TableRef } from '../api'
import { FilePicker, pickedName, type PickedFile } from '../components/FilePicker'
import { JobProgress, UploadProgress, useJob } from '../components/Progress'
import { Button, Field, Input, Modal, Select, Spinner, Switch, errorParts, useToast } from '../components/ui'
import type { Column, FileSource, SessionInfo } from '../types'
import { toSource } from './TransferDialog'

const DELIMS: [string, string][] = [[',', 'Comma ,'], [';', 'Semicolon ;'], ['\t', 'Tab'], ['|', 'Pipe |']]

/** normalize makes CSV headers and column names comparable: "First Name" ~ first_name. */
export const normalize = (s: string) => s.toLowerCase().replace(/[^a-z0-9]/g, '')

/** autoMap matches table columns to CSV header fields by name, else by position. */
export function autoMap(columns: string[], header: string[] | null): number[] {
  if (!header) return columns.map((_, i) => i)
  const idx = new Map(header.map((h, i) => [normalize(h), i] as const))
  return columns.map((c) => idx.get(normalize(c)) ?? -1)
}

/** ImportCSVDialog loads a CSV file into a table with a preview and column mapping. */
export function ImportCSVDialog({ session, table, columns, onClose, onImported }: {
  session: SessionInfo
  table: TableRef
  columns: Column[]
  onClose: () => void
  onImported: () => void
}) {
  const toast = useToast()
  const [file, setFile] = useState<PickedFile | null>(null)
  const [source, setSource] = useState<FileSource | null>(null)
  const [upload, setUpload] = useState<{ fraction: number; loaded: number } | null>(null)
  const [preview, setPreview] = useState<{ delimiter: string; rows: string[][] } | null>(null)
  const [delimiter, setDelimiter] = useState('')
  const [header, setHeader] = useState(true)
  const [mapping, setMapping] = useState<number[]>([])
  const [emptyAsNull, setEmptyAsNull] = useState(true)
  const [nullText, setNullText] = useState('NULL')
  const [truncate, setTruncate] = useState(false)
  const [jobId, setJobId] = useState<string | null>(null)
  const job = useJob(jobId)
  const abort = useRef<AbortController | null>(null)
  const reported = useRef(false)

  // Step 1: get the file into GoTable (upload, or a path), then preview it.
  useEffect(() => {
    if (!file) { setSource(null); setPreview(null); return }
    abort.current = new AbortController()
    let cancelled = false
    ;(async () => {
      try {
        if (file.kind === 'file') setUpload({ fraction: 0, loaded: 0 })
        const src = await toSource(file, (fraction, loaded) => setUpload({ fraction, loaded }), abort.current!.signal)
        if (cancelled) return
        setUpload(null)
        setSource(src)
      } catch (e) {
        setUpload(null)
        setFile(null)
        if ((e as Error).name !== 'AbortError') toast('error', ...errorParts(e))
      }
    })()
    return () => { cancelled = true; abort.current?.abort() }
  }, [file, toast])

  useEffect(() => {
    if (!source) return
    api.csvPreview(source, delimiter || undefined).then((p) => {
      setPreview(p)
      if (!delimiter) setDelimiter(p.delimiter)
    }).catch((e) => { toast('error', ...errorParts(e)); setFile(null) })
  }, [source, delimiter, toast])

  const csvHeader = preview && header ? preview.rows[0] ?? [] : null
  const fieldCount = Math.max(0, ...(preview?.rows.map((r) => r.length) ?? [0]))
  const names = useMemo(() => columns.map((c) => c.name), [columns])
  useEffect(() => { if (preview) setMapping(autoMap(names, csvHeader)) }, [preview, header]) // eslint-disable-line react-hooks/exhaustive-deps
  const fieldLabel = (i: number) => (csvHeader?.[i] ? `${csvHeader[i]}` : `Column ${i + 1}`)
  const sample = (i: number) => preview?.rows[header ? 1 : 0]?.[i] ?? ''
  const mapped = mapping.filter((m) => m >= 0).length
  const missingRequired = columns.filter((c, i) => !c.nullable && c.default === null && mapping[i] < 0)

  useEffect(() => {
    if (job && job.status !== 'running' && !reported.current) {
      reported.current = true
      if (job.status === 'done') {
        toast('ok', `Imported ${job.rows.toLocaleString()} rows`, table.name)
        onImported()
        onClose() // failures stay open so the error can be read
      }
    }
  }, [job, onImported, onClose, table.name, toast])

  const start = async () => {
    if (!source) return
    reported.current = false
    try {
      const j = await api.createJob(session.id, {
        kind: 'import-csv', source, table, delimiter, header, emptyAsNull, nullText, truncate,
        mapping: names.map((column, i) => ({ column, field: mapping[i] })),
      })
      setJobId(j.id)
    } catch (e) {
      toast('error', ...errorParts(e))
    }
  }

  const production = session.connection.tag === 'production'
  let body: React.ReactNode
  let footer: React.ReactNode = <Button variant="ghost" onClick={onClose}>Cancel</Button>
  if (job) {
    body = <JobProgress job={job} label={`Importing ${pickedName(file!)} into ${table.name}`} onCancel={() => api.cancelJob(job.id)} />
    footer = <Button variant="ghost" onClick={onClose}>{job.status === 'running' ? 'Hide (keeps running)' : 'Close'}</Button>
  } else if (upload && file?.kind === 'file') {
    body = <UploadProgress name={file.file.name} fraction={upload.fraction} loaded={upload.loaded} size={file.file.size} />
    footer = <Button variant="ghost" onClick={() => { abort.current?.abort(); setFile(null) }}>Cancel</Button>
  } else if (!file) {
    body = <FilePicker accept=".csv,.tsv,.txt" hint="CSV or TSV, UTF-8 — any size" value={null} onChange={setFile} />
  } else if (!preview) {
    body = <div className="flex h-32 items-center justify-center"><Spinner /></div>
  } else {
    body = (
      <div className="space-y-4">
        <FilePicker accept=".csv" hint="" value={file} onChange={(f) => { setFile(f); setDelimiter('') }} />
        <div className="grid grid-cols-3 items-end gap-3">
          <Field label="Delimiter">
            <Select value={delimiter} onChange={(e) => setDelimiter(e.target.value)} aria-label="CSV delimiter">
              {DELIMS.map(([d, l]) => <option key={l} value={d}>{l}</option>)}
            </Select>
          </Field>
          <div className="col-span-2 flex flex-wrap gap-x-5 gap-y-2 pb-1.5">
            <Switch checked={header} onChange={setHeader} label="First row is a header" />
            <Switch checked={emptyAsNull} onChange={setEmptyAsNull} label="Empty → NULL" />
          </div>
        </div>
        <div className="overflow-hidden rounded-xl border border-line">
          <div className="grid grid-cols-[1fr_24px_1fr_1fr] gap-2 bg-panel-2 px-3 py-2 text-[11.5px] font-semibold text-muted">
            <span>Table column</span><span /><span>From CSV</span><span>Sample</span>
          </div>
          <div className="max-h-60 overflow-y-auto" aria-label="Column mapping">
            {columns.map((c, i) => (
              <div key={c.name} className="grid grid-cols-[1fr_24px_1fr_1fr] items-center gap-2 border-t border-line-2 px-3 py-1.5 text-[12.5px]">
                <span className="flex min-w-0 items-center gap-1.5 font-medium">
                  {c.primaryKey && <KeyRound className="size-3 shrink-0 text-warn" />}
                  <span className="truncate">{c.name}</span>
                  <span className="truncate text-[11px] font-normal text-faint">{c.type.toLowerCase()}</span>
                </span>
                <ArrowRight className={clsx('size-3.5', mapping[i] >= 0 ? 'text-accent' : 'text-faint')} />
                <Select aria-label={`Source for ${c.name}`} value={mapping[i] ?? -1} onChange={(e) => setMapping((m) => m.map((x, j) => (j === i ? Number(e.target.value) : x)))}>
                  <option value={-1}>{c.default !== null ? '— default value —' : c.nullable ? '— leave empty (NULL) —' : '— skip —'}</option>
                  {Array.from({ length: fieldCount }, (_, f) => <option key={f} value={f}>{fieldLabel(f)}</option>)}
                </Select>
                <span className="truncate font-mono text-[11.5px] text-muted">{mapping[i] >= 0 ? sample(mapping[i]) : ''}</span>
              </div>
            ))}
          </div>
        </div>
        {missingRequired.length > 0 && (
          <div className="text-[12px] text-warn">Required column{missingRequired.length > 1 ? 's' : ''} without a source: {missingRequired.map((c) => c.name).join(', ')}. The import will fail unless you map {missingRequired.length > 1 ? 'them' : 'it'}.</div>
        )}
        <div className="grid grid-cols-2 items-end gap-3">
          <Field label="Text that means NULL"><Input mono value={nullText} onChange={(e) => setNullText(e.target.value)} placeholder="e.g. NULL or \N" /></Field>
          <div className="pb-1.5"><Switch checked={truncate} onChange={setTruncate} label={<span className={truncate ? 'font-medium text-err' : ''}>Delete existing rows first</span>} /></div>
        </div>
        {production && <div className="rounded-lg border border-err/30 bg-err/5 p-2.5 text-[12.5px] font-medium text-err">You are importing into PRODUCTION.</div>}
        <div className="text-[12px] text-muted">All rows are inserted in one transaction: if any row fails, nothing is imported.</div>
      </div>
    )
    footer = <>
      <Button variant="ghost" onClick={onClose}>Cancel</Button>
      <Button variant={production || truncate ? 'danger' : 'primary'} disabled={!mapped} onClick={start}>
        Import into {table.name}
      </Button>
    </>
  }

  return (
    <Modal width={700} title={`Import CSV into ${table.name}`} subtitle="Preview, map the columns, then import." onClose={onClose} footer={footer}>
      {body}
    </Modal>
  )
}
