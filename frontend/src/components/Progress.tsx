import clsx from 'clsx'
import { AlertCircle, CheckCircle2, CircleSlash, Download, Loader2 } from 'lucide-react'
import { useEffect, useRef, useState } from 'react'
import { api } from '../api'
import type { Job } from '../types'
import { Button } from './ui'

export function fmtBytes(n: number): string {
  if (n < 1024) return `${n} B`
  const units = ['KB', 'MB', 'GB', 'TB']
  let v = n / 1024
  let i = 0
  while (v >= 1024 && i < units.length - 1) { v /= 1024; i++ }
  return `${v < 10 ? v.toFixed(1) : Math.round(v)} ${units[i]}`
}

export function fmtDuration(ms: number): string {
  const s = Math.max(0, Math.round(ms / 1000))
  if (s < 60) return `${s}s`
  const m = Math.floor(s / 60)
  if (m < 60) return `${m}m ${String(s % 60).padStart(2, '0')}s`
  return `${Math.floor(m / 60)}h ${String(m % 60).padStart(2, '0')}m`
}

const fmtNum = (n: number) => n.toLocaleString('en-US')

/** ProgressBar draws a determinate bar, or an animated stripe when fraction is null. */
export function ProgressBar({ fraction, tone = 'accent' }: { fraction: number | null; tone?: 'accent' | 'ok' | 'err' | 'muted' }) {
  return (
    <div className="relative h-2 w-full overflow-hidden rounded-full bg-hover" role="progressbar"
      aria-valuemin={0} aria-valuemax={100} aria-valuenow={fraction === null ? undefined : Math.round(fraction * 100)}>
      {fraction === null ? (
        <div className="absolute inset-y-0 w-1/3 animate-[indeterminate_1.2s_ease-in-out_infinite] rounded-full bg-accent" />
      ) : (
        <div className={clsx('h-full rounded-full transition-[width] duration-300',
          tone === 'ok' ? 'bg-ok' : tone === 'err' ? 'bg-err' : tone === 'muted' ? 'bg-faint' : 'bg-accent')}
          style={{ width: `${Math.min(100, fraction * 100)}%` }} />
      )}
    </div>
  )
}

/** useJob polls a job until it finishes. */
export function useJob(id: string | null) {
  const [job, setJob] = useState<Job | null>(null)
  useEffect(() => {
    if (!id) { setJob(null); return }
    let stop = false
    let timer: ReturnType<typeof setTimeout>
    const tick = async () => {
      try {
        const j = await api.job(id)
        if (stop) return
        setJob(j)
        if (j.status === 'running') timer = setTimeout(tick, 350)
      } catch {
        if (!stop) timer = setTimeout(tick, 1000)
      }
    }
    tick()
    return () => { stop = true; clearTimeout(timer) }
  }, [id])
  return job
}

/** useRate estimates speed (units/s) from recent samples. */
function useRate(done: number, running: boolean) {
  const samples = useRef<[number, number][]>([])
  const now = Date.now()
  if (running) {
    const s = samples.current
    if (!s.length || s[s.length - 1][1] !== done) s.push([now, done])
    while (s.length > 2 && now - s[0][0] > 5000) s.shift()
  }
  const s = samples.current
  if (s.length < 2) return 0
  const [t0, d0] = s[0]
  const [t1, d1] = s[s.length - 1]
  return t1 > t0 ? ((d1 - d0) * 1000) / (t1 - t0) : 0
}

/** JobProgress shows a running or finished import/export. */
export function JobProgress({ job, onCancel, onDownload, label }: {
  job: Job
  onCancel?: () => void
  onDownload?: () => void
  label?: string
}) {
  const running = job.status === 'running'
  const rate = useRate(job.done, running)
  const [, force] = useState(0)
  useEffect(() => {
    if (!running) return
    const t = setInterval(() => force((x) => x + 1), 1000)
    return () => clearInterval(t)
  }, [running])
  const elapsed = (job.finished ? Date.parse(job.finished) : Date.now()) - Date.parse(job.started)
  const fraction = job.total > 0 ? job.done / job.total : running ? null : 1
  const eta = running && rate > 0 && job.total > 0 ? ((job.total - job.done) / rate) * 1000 : 0
  const amount = (n: number) => (job.unit === 'bytes' ? fmtBytes(n) : `${fmtNum(n)} rows`)
  const speed = job.unit === 'bytes' ? `${fmtBytes(rate)}/s` : `${fmtNum(Math.round(rate))} rows/s`

  return (
    <div className="space-y-3" data-testid="job-progress" data-status={job.status}>
      <div className="flex items-center gap-2.5">
        {running && <Loader2 className="size-4 animate-spin text-accent" />}
        {job.status === 'done' && <CheckCircle2 className="size-4 text-ok" />}
        {job.status === 'failed' && <AlertCircle className="size-4 text-err" />}
        {job.status === 'cancelled' && <CircleSlash className="size-4 text-muted" />}
        <div className="min-w-0 flex-1">
          <div className="truncate text-[13px] font-medium">{label ?? job.title}</div>
          <div className="truncate text-[12px] text-muted">
            {job.status === 'done' ? 'Finished' : job.status === 'cancelled' ? 'Cancelled — nothing more was written' : job.status === 'failed' ? 'Stopped' : job.phase}
          </div>
        </div>
        <span className="text-[18px] font-semibold tabular-nums">{fraction === null ? '' : `${Math.floor(fraction * 100)}%`}</span>
      </div>
      <ProgressBar fraction={fraction} tone={job.status === 'done' ? 'ok' : job.status === 'failed' ? 'err' : job.status === 'cancelled' ? 'muted' : 'accent'} />
      <div className="flex flex-wrap gap-x-4 gap-y-1 text-[12px] text-muted tabular-nums">
        <span>{job.total > 0 ? `${amount(job.done)} of ${amount(job.total)}` : amount(job.done)}</span>
        {running && rate > 0 && <span>{speed}</span>}
        {running && eta > 0 && <span>about {fmtDuration(eta)} left</span>}
        <span>{fmtDuration(elapsed)} elapsed</span>
        {job.unit === 'bytes' && job.rows > 0 && <span>{fmtNum(job.rows)} rows</span>}
        {job.statements > 0 && <span>{fmtNum(job.statements)} statements</span>}
        {job.failed > 0 && <span className="font-medium text-err">{fmtNum(job.failed)} failed</span>}
        {job.status === 'done' && job.size > 0 && <span>{fmtBytes(job.size)} file</span>}
      </div>
      {job.error && (
        <div className="rounded-lg border border-err/30 bg-err/5 p-3 text-[12.5px]">
          <div className="font-semibold text-err">{job.error.title}</div>
          {job.error.hint && <div className="text-fg-2">{job.error.hint}</div>}
          <div className="mt-1 break-words font-mono text-[11.5px] text-muted">{job.error.detail}</div>
          {job.kind === 'import-sql' && <div className="mt-1.5 text-[12px] text-fg-2">Everything this import ran in its transaction was rolled back.</div>}
        </div>
      )}
      {job.errors.length > 0 && !(job.errors.length === 1 && job.error) && (
        <details className="rounded-lg border border-line" open={job.status !== 'running'}>
          <summary className="px-3 py-2 text-[12.5px] font-medium">{fmtNum(job.failed)} statement{job.failed === 1 ? '' : 's'} failed{job.failed > job.errors.length ? ` (first ${job.errors.length} shown)` : ''}</summary>
          <div className="max-h-48 space-y-2 overflow-auto px-3 pb-3" data-testid="job-errors">
            {job.errors.map((e, i) => (
              <div key={i} className="text-[12px]">
                <div><span className="font-semibold">Line {fmtNum(e.line)}:</span> <span className="text-err">{e.message}</span></div>
                <div className="truncate font-mono text-[11px] text-muted">{e.statement}</div>
              </div>
            ))}
          </div>
        </details>
      )}
      <div className="flex justify-end gap-2">
        {running && onCancel && <Button size="sm" variant="ghost" onClick={onCancel}>Cancel</Button>}
        {job.status === 'done' && job.file && onDownload && <Button size="sm" icon={<Download className="size-3.5" />} onClick={onDownload}>Save a copy…</Button>}
      </div>
    </div>
  )
}

/** UploadProgress shows a file being sent to GoTable. */
export function UploadProgress({ name, fraction, loaded, size }: { name: string; fraction: number; loaded: number; size: number }) {
  return (
    <div className="space-y-3" data-testid="upload-progress">
      <div className="flex items-center gap-2.5">
        <Loader2 className="size-4 animate-spin text-accent" />
        <div className="min-w-0 flex-1">
          <div className="truncate text-[13px] font-medium">Reading {name}</div>
          <div className="text-[12px] text-muted">Step 1 of 2 · copying the file into GoTable</div>
        </div>
        <span className="text-[18px] font-semibold tabular-nums">{Math.floor(fraction * 100)}%</span>
      </div>
      <ProgressBar fraction={fraction} />
      <div className="text-[12px] text-muted tabular-nums">{fmtBytes(loaded)} of {fmtBytes(size)}</div>
    </div>
  )
}
