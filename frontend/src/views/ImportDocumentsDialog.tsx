import { useEffect, useRef, useState } from 'react'
import { api, type TableRef } from '../api'
import { FilePicker, pickedName, type PickedFile } from '../components/FilePicker'
import { JobProgress, UploadProgress, useJob } from '../components/Progress'
import { Button, Modal, Switch, errorParts, useToast } from '../components/ui'
import type { Column, FileSource, SessionInfo } from '../types'
import { ImportCSVDialog } from './ImportCSVDialog'
import { toSource } from './TransferDialog'

const isCSV = (f: PickedFile) => /\.(csv|tsv|txt)$/i.test(pickedName(f))

/**
 * ImportDocumentsDialog is MongoDB's import: a JSON file of documents (an
 * array, as Compass exports, or one a line, as mongoexport writes) goes in
 * as it is; a CSV file goes to the CSV dialog to map its columns.
 */
export function ImportDocumentsDialog({ session, table, columns, onClose, onImported }: {
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
  const [truncate, setTruncate] = useState(false)
  const [skipErrors, setSkipErrors] = useState(false)
  const [jobId, setJobId] = useState<string | null>(null)
  const job = useJob(jobId)
  const abort = useRef<AbortController | null>(null)
  const reported = useRef(false)
  const json = file && !isCSV(file)

  useEffect(() => {
    if (!file || !json) { setSource(null); return }
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
  }, [file, json, toast])

  useEffect(() => {
    if (job && job.status !== 'running' && !reported.current) {
      reported.current = true
      if (job.status === 'done') {
        onImported()
        if (job.failed > 0) return // stays open: the refused documents are listed
        toast('ok', `Imported ${job.rows.toLocaleString()} documents`, table.name)
        onClose()
      }
    }
  }, [job, onImported, onClose, table.name, toast])

  if (file && !json) return <ImportCSVDialog session={session} table={table} columns={columns} initialFile={file} onClose={onClose} onImported={onImported} />

  const start = async () => {
    if (!source) return
    reported.current = false
    try {
      const j = await api.createJob(session.id, { kind: 'import-json', source, table, truncate, continueOnError: skipErrors })
      setJobId(j.id)
    } catch (e) {
      toast('error', ...errorParts(e))
    }
  }

  const production = session.connection.tag === 'production'
  let body: React.ReactNode
  let footer: React.ReactNode = <Button variant="ghost" onClick={onClose}>Cancel</Button>
  if (job) {
    body = (
      <div className="space-y-3">
        <JobProgress job={job} label={`Importing ${pickedName(file!)} into ${table.name}`} onCancel={() => api.cancelJob(job.id)} />
        {job.status === 'done' && job.failed > 0 && (
          <div className="rounded-lg border border-warn/30 bg-warn/10 p-2.5 text-[12.5px]" data-testid="import-skipped">
            <div className="font-medium">{job.rows.toLocaleString()} imported, {job.failed.toLocaleString()} refused and skipped:</div>
            <ul className="mt-1 space-y-0.5 font-mono text-[11.5px] text-fg-2">
              {job.errors.map((e, i) => <li key={i}>{e.statement}: {e.message}</li>)}
            </ul>
          </div>
        )}
      </div>
    )
    footer = <Button variant="ghost" onClick={onClose}>{job.status === 'running' ? 'Hide (keeps running)' : 'Close'}</Button>
  } else if (upload && file?.kind === 'file') {
    body = <UploadProgress name={file.file.name} fraction={upload.fraction} loaded={upload.loaded} size={file.file.size} />
    footer = <Button variant="ghost" onClick={() => { abort.current?.abort(); setFile(null) }}>Cancel</Button>
  } else if (!file) {
    body = <FilePicker accept=".json,.jsonl,.ndjson,.csv,.tsv" hint="JSON documents (an array, or one a line) or CSV — any size" value={null} onChange={setFile} />
  } else {
    body = (
      <div className="space-y-4">
        <FilePicker accept=".json,.jsonl,.ndjson" hint="" value={file} onChange={setFile} />
        <div className="text-[12.5px] text-muted">
          Each document goes in as it is. Extended JSON keeps its types: <code className="font-mono">{'{"$oid": …}'}</code>, <code className="font-mono">{'{"$date": …}'}</code>, <code className="font-mono">{'{"$numberLong": …}'}</code>. Documents without an <code className="font-mono">_id</code> get one.
        </div>
        <div className="flex flex-col gap-2">
          <Switch checked={skipErrors} onChange={setSkipErrors} label={<span className="text-[12.5px]">Skip documents the server refuses <span className="text-muted">(a duplicate _id, a validation rule) and go on</span></span>} />
          <Switch checked={truncate} onChange={setTruncate} label={<span className={truncate ? 'text-[12.5px] font-medium text-err' : 'text-[12.5px]'}>Delete existing documents first</span>} />
        </div>
        {production && <div className="rounded-lg border border-err/30 bg-err/5 p-2.5 text-[12.5px] font-medium text-err">You are importing into PRODUCTION.</div>}
        <div className="text-[12px] text-muted">{skipErrors ? 'Refused documents are listed when it finishes.' : 'The import stops at the first refused document; the ones before it stay imported.'}</div>
      </div>
    )
    footer = <>
      <Button variant="ghost" onClick={onClose}>Cancel</Button>
      <Button variant={production || truncate ? 'danger' : 'primary'} disabled={!source} onClick={start}>Import into {table.name}</Button>
    </>
  }

  return (
    <Modal width={620} title={`Import into ${table.name}`} subtitle="JSON documents or a CSV file." onClose={onClose} footer={footer}>
      {body}
    </Modal>
  )
}
