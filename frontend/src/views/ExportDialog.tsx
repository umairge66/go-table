import clsx from 'clsx'
import { Braces, Database, FileSpreadsheet } from 'lucide-react'
import { useEffect, useRef, useState } from 'react'
import { api, type PageReq, type TableRef } from '../api'
import { JobProgress, useJob } from '../components/Progress'
import { Button, Field, Input, Modal, Select, Switch, errorParts, useToast } from '../components/ui'
import { chooseSaveLocation, saveJob, type SaveTarget } from '../lib/saveFile'
import type { SessionInfo } from '../types'

export type ExportSource = { table: TableRef; page: PageReq; columns: string[] }

const FORMATS = [
  { id: 'csv', label: 'CSV', icon: FileSpreadsheet, desc: 'Spreadsheets & imports' },
  { id: 'json', label: 'JSON', icon: Braces, desc: 'Array of objects' },
  { id: 'sql', label: 'SQL', icon: Database, desc: 'INSERT statements' },
] as const

/** ExportDialog exports one table (all filtered rows or the current page) with progress. */
export function ExportDialog({ session, title, source, onClose }: { session: SessionInfo; title: string; source: ExportSource; onClose: () => void }) {
  const toast = useToast()
  const [format, setFormat] = useState<'csv' | 'json' | 'sql'>('csv')
  const [allRows, setAllRows] = useState(true)
  const [header, setHeader] = useState(true)
  const [delimiter, setDelimiter] = useState(',')
  const [quoteAlways, setQuoteAlways] = useState(false)
  const [crlf, setCrlf] = useState(false)
  const [nullAsEmpty, setNullAsEmpty] = useState(false)
  const [batch, setBatch] = useState(100)
  const [ddl, setDdl] = useState(false)
  const [jobId, setJobId] = useState<string | null>(null)
  const job = useJob(jobId)
  const target = useRef<SaveTarget | null>(null)
  const saved = useRef(false)
  const [saving, setSaving] = useState(false)

  // When the export is ready, write it where the user chose.
  useEffect(() => {
    if (job?.status !== 'done' || saved.current || !target.current) return
    saved.current = true
    setSaving(true)
    saveJob(target.current, job.id)
      .then(() => {
        toast('ok', 'Export saved', target.current!.name)
        onClose() // success needs no further action; failures stay visible
      })
      .catch((e) => toast('error', 'Could not save the file', String(e?.message ?? e)))
      .finally(() => setSaving(false))
  }, [job, toast, onClose])

  const run = async () => {
    try {
      // Ask where to save first, like any desktop app.
      const t = await chooseSaveLocation(`${title}.${format}`, format)
      if (!t) return
      target.current = t
      saved.current = false
      const j = await api.createJob(session.id, {
        kind: 'export-table', format, allRows, ...source,
        csv: { header, delimiter, quoteAlways, crlf, nullAsEmpty, decimalComma: false },
        batchSize: batch, includeDDL: ddl,
      })
      setJobId(j.id)
    } catch (e) {
      toast('error', ...errorParts(e))
    }
  }

  return (
    <Modal title={`Export ${title}`} onClose={onClose}
      footer={job ? <Button variant="ghost" onClick={onClose}>{job.status === 'running' ? 'Hide (keeps running)' : 'Close'}</Button>
        : <><Button variant="ghost" onClick={onClose}>Cancel</Button><Button variant="primary" onClick={run}>Export {format.toUpperCase()}…</Button></>}>
      {job ? (
        <JobProgress job={job} label={saving ? `Saving ${target.current?.name}…` : undefined} onCancel={() => api.cancelJob(job.id)}
          onDownload={() => saveJobAs(job.id, job.file ?? `${title}.${format}`, format)} />
      ) : (
        <>
          <div className="grid grid-cols-3 gap-2" role="radiogroup" aria-label="Format">
            {FORMATS.map((f) => (
              <button key={f.id} role="radio" aria-checked={format === f.id} onClick={() => setFormat(f.id)}
                className={clsx('flex flex-col items-start gap-1 rounded-xl border p-3 text-left transition-all',
                  format === f.id ? 'border-accent ring-3 ring-accent/15' : 'border-line hover:border-faint')}>
                <f.icon className={clsx('size-5', format === f.id ? 'text-accent' : 'text-muted')} />
                <span className="text-[13px] font-semibold">{f.label}</span>
                <span className="text-[11.5px] text-muted">{f.desc}</span>
              </button>
            ))}
          </div>
          <div className="mt-4 flex gap-4 text-[12.5px]">
            {([[true, 'All rows matching the filter'], [false, 'Only the current page']] as const).map(([v, l]) => (
              <label key={String(v)} className="flex items-center gap-1.5">
                <input type="radio" checked={allRows === v} onChange={() => setAllRows(v)} className="accent-[var(--accent)]" />{l}
              </label>
            ))}
          </div>
          {format === 'csv' && (
            <div className="mt-4 grid grid-cols-2 gap-3">
              <Field label="Delimiter">
                <Select value={delimiter} onChange={(e) => setDelimiter(e.target.value)}>
                  <option value=",">Comma ,</option><option value=";">Semicolon ;</option><option value={'\t'}>Tab</option><option value="|">Pipe |</option>
                </Select>
              </Field>
              <Field label="Line breaks">
                <Select value={crlf ? 'crlf' : 'lf'} onChange={(e) => setCrlf(e.target.value === 'crlf')}><option value="lf">LF (Linux, macOS)</option><option value="crlf">CRLF (Windows, Excel)</option></Select>
              </Field>
              <Switch checked={header} onChange={setHeader} label="Column names in first row" />
              <Switch checked={quoteAlways} onChange={setQuoteAlways} label="Quote every value" />
              <Switch checked={nullAsEmpty} onChange={setNullAsEmpty} label="Write NULL as empty" />
            </div>
          )}
          {format === 'sql' && (
            <div className="mt-4 grid grid-cols-2 items-end gap-3">
              <Field label="Rows per INSERT"><Input inputMode="numeric" value={batch} onChange={(e) => setBatch(Number(e.target.value.replace(/\D/g, '')) || 1)} /></Field>
              <Switch checked={ddl} onChange={setDdl} label="Include CREATE TABLE" />
            </div>
          )}
        </>
      )}
    </Modal>
  )
}

/** saveJobAs asks for a location and saves a finished export there. */
export async function saveJobAs(jobId: string, name: string, kind: 'sql' | 'csv' | 'json' | 'zip') {
  const t = await chooseSaveLocation(name, kind)
  if (t) await saveJob(t, jobId)
  return t
}
