import clsx from 'clsx'
import { ListChecks, X } from 'lucide-react'
import { useEffect, useState } from 'react'
import { api } from '../api'
import type { Job } from '../types'
import { JobProgress, ProgressBar } from './Progress'
import { saveJobAs } from '../views/ExportDialog'
import { IconButton, Modal } from './ui'

/**
 * JobsIndicator shows running imports/exports in the status bar, so a
 * dialog can be closed while a long job continues. Clicking it lists every
 * job with its progress.
 */
export function JobsIndicator() {
  const [jobs, setJobs] = useState<Job[]>([])
  const [open, setOpen] = useState(false)
  // Polling the local API once a second is cheap and keeps every view in sync.
  useEffect(() => {
    let stop = false
    const tick = () => api.jobs().then((js) => { if (!stop) setJobs(js) }).catch(() => {})
    tick()
    const t = setInterval(tick, 1000)
    return () => { stop = true; clearInterval(t) }
  }, [])

  const running = jobs.filter((j) => j.status === 'running')
  if (!jobs.length) return null
  const first = running[0]
  const fraction = first && first.total > 0 ? first.done / first.total : null
  return (
    <>
      <button onClick={() => setOpen(true)} data-testid="jobs-indicator"
        title="Imports & exports"
        className={clsx('flex h-8 items-center gap-2 rounded-lg px-2 text-[12px] hover:bg-hover', running.length ? 'bg-accent-soft font-medium text-accent' : 'text-muted')}>
        <ListChecks className="size-3.5" />
        {running.length ? (
          <>
            <span className="max-w-48 truncate">{running.length === 1 ? first.title : `${running.length} tasks running`}</span>
            <span className="w-20"><ProgressBar fraction={fraction} /></span>
            {fraction !== null && <span className="tabular-nums">{Math.floor(fraction * 100)}%</span>}
          </>
        ) : <span>Tasks</span>}
      </button>
      {open && (
        <Modal width={620} title="Imports & exports" subtitle="Running tasks continue in the background." onClose={() => setOpen(false)}>
          <div className="space-y-3">
            {jobs.map((j) => (
              <div key={j.id} className="relative rounded-xl border border-line p-3">
                <JobProgress job={j} onCancel={() => api.cancelJob(j.id)} onDownload={() => saveJobAs(j.id, j.file ?? 'export', (j.file?.split('.').pop() ?? 'sql') as 'sql' | 'csv' | 'json' | 'zip')} />
                {j.status !== 'running' && (
                  <IconButton label="Remove from list" className="absolute right-2 top-2"
                    onClick={async () => { await api.cancelJob(j.id); setJobs((js) => js.filter((x) => x.id !== j.id)) }}>
                    <X className="size-3.5" />
                  </IconButton>
                )}
              </div>
            ))}
          </div>
        </Modal>
      )}
    </>
  )
}
