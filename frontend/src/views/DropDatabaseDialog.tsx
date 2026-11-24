import { AlertTriangle, Database } from 'lucide-react'
import { useEffect, useState } from 'react'
import { api, ApiError } from '../api'
import { Button, Input, Modal, Select, Switch, errorParts, useToast } from '../components/ui'
import { family, type Driver, type SessionInfo } from '../types'

/** What can be deleted: a database, or a schema in one. */
export interface DropTarget {
  name: string
  kind: 'database' | 'schema'
  /** sid: the session that runs the command. */
  sid: string
  /** where: shown under the name, e.g. "in shop" for a schema. */
  where?: string
}

/** dropKind: what a listed database is to the server, or null where GoTable can't delete one (SQLite, Oracle). */
export function dropKind(driver: Driver, level: 'server' | 'listed'): 'database' | 'schema' | null {
  const f = family(driver)
  if (f === 'sqlite' || f === 'oracle') return null
  if (f === 'duckdb') return level === 'listed' ? 'schema' : null
  if (f === 'postgres' || f === 'sqlserver' || f === 'snowflake') return level === 'server' ? 'database' : 'schema'
  return level === 'listed' ? 'database' : null
}

/**
 * DropDatabaseDialog deletes a database or a schema after showing the exact
 * command, and only once its name is typed. With choices, the target is
 * picked from them first.
 */
export function DropDatabaseDialog({ session, target: initial, choices, before, onClose, onDeleted }: {
  session: SessionInfo
  target?: DropTarget
  choices?: DropTarget[]
  /** before runs first (closing what uses the database); false stops. */
  before?: (t: DropTarget) => Promise<boolean>
  onClose: () => void
  onDeleted: (t: DropTarget) => void
}) {
  const toast = useToast()
  const [target, setTarget] = useState<DropTarget | undefined>(initial ?? choices?.[0])
  const [cascade, setCascade] = useState(false)
  const [preview, setPreview] = useState('')
  const [problem, setProblem] = useState<{ title: string; hint?: string; detail: string } | null>(null)
  const [typed, setTyped] = useState('')
  const [busy, setBusy] = useState(false)
  const driver = session.connection.driver
  const production = session.connection.tag === 'production'
  // PostgreSQL and DuckDB can delete a schema with what is in it; SQL Server only an empty one.
  const canCascade = target?.kind === 'schema' && family(driver) !== 'sqlserver'
  const noun = target?.kind ?? 'database'

  useEffect(() => {
    setProblem(null)
    if (!target) { setPreview(''); return }
    api.dropDatabase(target.sid, { name: target.name, kind: target.kind, cascade: canCascade && cascade }, true)
      .then((r) => setPreview(r.statements.join('\n')))
      .catch((e) => { setPreview(''); setProblem({ title: errorParts(e)[0], detail: errorParts(e)[1] }) })
  }, [target, cascade, canCascade])

  const run = async () => {
    if (!target) return
    setBusy(true)
    setProblem(null)
    try {
      if (before && !(await before(target))) { setBusy(false); return }
      await api.dropDatabase(target.sid, { name: target.name, kind: target.kind, cascade: canCascade && cascade }, false)
      toast('ok', `Deleted ${noun} ${target.name}`)
      onDeleted(target)
    } catch (e) {
      const err = e as ApiError
      setProblem(err.title ? { title: err.title, hint: err.hint, detail: err.detail } : { title: errorParts(e)[0], detail: errorParts(e)[1] })
      setBusy(false)
    }
  }

  const ok = !!target && !!preview && typed.trim() === target.name

  return (
    <Modal width={560} label={`Delete ${noun}`} title={<span className="flex items-center gap-2"><AlertTriangle className="size-4 text-err" />Delete {noun}{target ? ` ${target.name}` : ''}?</span>}
      subtitle={target?.kind === 'schema' ? 'The schema and everything in it are removed permanently.' : 'The database, its tables and all their data are removed permanently.'}
      onClose={onClose}
      footer={<>
        <Button variant="ghost" onClick={onClose}>Cancel</Button>
        <Button variant="danger" disabled={!ok} loading={busy} onClick={run}>Delete {noun}</Button>
      </>}>
      <div className="space-y-4">
        {choices && choices.length > 0 ? (
          <Select aria-label="Database to delete" value={target ? `${target.kind}:${target.sid}:${target.name}` : ''}
            onChange={(e) => { setTarget(choices.find((c) => `${c.kind}:${c.sid}:${c.name}` === e.target.value)); setTyped('') }}>
            {choices.map((c) => <option key={`${c.kind}:${c.sid}:${c.name}`} value={`${c.kind}:${c.sid}:${c.name}`}>{c.name} — {c.kind}{c.where ? ` ${c.where}` : ''}</option>)}
          </Select>
        ) : target && (
          <div className="flex items-center gap-2 rounded-xl border border-line px-3 py-2 text-[13px]">
            <Database className="size-4 text-muted" />
            <span className="font-medium">{target.name}</span>
            <span className="text-muted">{target.kind}{target.where ? ` ${target.where}` : ''}</span>
          </div>
        )}
        {canCascade && (
          <Switch checked={cascade} onChange={setCascade} label={<span className="text-[13px]">Also delete everything in it <span className="text-muted">(CASCADE: without it, only an empty schema can go)</span></span>} />
        )}
        <div>
          <div className="mb-1 text-[11.5px] font-medium text-muted">Command that will run</div>
          <pre data-testid="drop-preview" className="min-h-[40px] whitespace-pre-wrap rounded-lg border border-line bg-panel-2 px-3 py-2.5 font-mono text-[12px] text-fg-2">{preview || '…'}</pre>
        </div>
        {problem && (
          <div className="rounded-lg border border-err/30 bg-err/5 p-3 text-[12.5px]" role="alert">
            <div className="font-semibold text-err">{problem.title}</div>
            {problem.hint && <div className="mt-0.5 text-fg-2">{problem.hint}</div>}
            {problem.detail && <div className="mt-1 break-words font-mono text-[11.5px] text-muted">{problem.detail}</div>}
          </div>
        )}
        {production && <div className="rounded-lg border border-err/30 bg-err/5 p-2.5 text-[12.5px] font-medium text-err">This is PRODUCTION.</div>}
        {target && (
          <label className="block text-[12.5px] text-fg-2">
            Type <b className="font-mono">{target.name}</b> to confirm
            <Input className="mt-1" mono value={typed} aria-label="Type the name to confirm" autoComplete="off" spellCheck={false}
              onChange={(e) => setTyped(e.target.value)} onKeyDown={(e) => e.key === 'Enter' && ok && run()} />
          </label>
        )}
      </div>
    </Modal>
  )
}
