import { AlertTriangle, Eye, Table2 } from 'lucide-react'
import { useEffect, useState } from 'react'
import { api, ApiError } from '../api'
import { Button, Input, Modal, Switch, errorParts, useToast } from '../components/ui'
import type { DbObject, SessionInfo } from '../types'

/** DeleteTablesDialog drops tables/views after showing exactly what will run. */
export function DeleteTablesDialog({ session, objects, onClose, onDeleted }: {
  session: SessionInfo
  objects: DbObject[]
  onClose: () => void
  onDeleted: (names: string[]) => void
}) {
  const toast = useToast()
  const [cascade, setCascade] = useState(false)
  const [sql, setSql] = useState<string[]>([])
  const [problem, setProblem] = useState<{ title: string; hint?: string; detail: string } | null>(null)
  const [typed, setTyped] = useState('')
  const [busy, setBusy] = useState(false)
  const driver = session.connection.driver
  const production = session.connection.tag === 'production'
  const tables = objects.filter((o) => o.kind === 'table').length
  const views = objects.length - tables
  const what = [tables && `${tables} table${tables === 1 ? '' : 's'}`, views && `${views} view${views === 1 ? '' : 's'}`].filter(Boolean).join(' and ')

  useEffect(() => {
    api.drop(session.id, objects, cascade, true).then((r) => setSql(r.statements)).catch(() => setSql([]))
  }, [session.id, objects, cascade])

  const run = async () => {
    setBusy(true)
    setProblem(null)
    try {
      await api.drop(session.id, objects, cascade, false)
      toast('ok', `Deleted ${what}`, objects.length <= 3 ? objects.map((o) => o.name).join(', ') : undefined)
      onDeleted(objects.map((o) => o.name))
    } catch (e) {
      const err = e as ApiError
      setProblem(err.title ? { title: err.title, hint: err.hint, detail: err.detail } : { title: errorParts(e)[0], detail: errorParts(e)[1] })
    } finally {
      setBusy(false)
    }
  }

  const needTyping = production || objects.length > 1
  const ok = !needTyping || typed.trim().toUpperCase() === 'DELETE'

  return (
    <Modal width={560} label={`Delete ${what}?`} title={<span className="flex items-center gap-2"><AlertTriangle className="size-4 text-err" />Delete {what}?</span>}
      subtitle="The tables and all their rows are removed permanently." onClose={onClose}
      footer={<>
        <Button variant="ghost" onClick={onClose}>Cancel</Button>
        <Button variant="danger" disabled={!ok} loading={busy} onClick={run}>Delete {what}</Button>
      </>}>
      <div className="space-y-4">
        <div className="max-h-40 overflow-y-auto rounded-xl border border-line p-1" aria-label="Tables to delete">
          {objects.map((o) => (
            <div key={o.name} className="flex h-7 items-center gap-2 px-2 text-[13px]">
              {o.kind === 'view' ? <Eye className="size-3.5 text-muted" /> : <Table2 className="size-3.5 text-muted" />}
              {o.name}
            </div>
          ))}
        </div>
        <div>
          <Switch checked={cascade} onChange={setCascade} label="Also drop objects that depend on them" />
          <div className="mt-1 pl-[42px] text-[12px] text-muted">
            {driver === 'postgres' ? 'Views and foreign keys of other tables that reference these are dropped too (CASCADE).'
              : 'Foreign keys from other tables won’t block the delete.'}
          </div>
        </div>
        <div>
          <div className="mb-1 text-[11.5px] font-medium text-muted">SQL that will run</div>
          <pre data-testid="drop-sql" className="max-h-32 overflow-auto whitespace-pre-wrap rounded-lg border border-line bg-panel-2 px-3 py-2.5 font-mono text-[12px] text-fg-2">{sql.join('\n')}</pre>
          <div className="mt-1 text-[12px] text-muted">
            {driver === 'mysql' || driver === 'mariadb' ? 'MySQL can’t undo a DROP: there is no way back without a backup.' : 'Runs in one transaction: either everything is deleted or nothing.'}
          </div>
        </div>
        {problem && (
          <div className="rounded-lg border border-err/30 bg-err/5 p-3 text-[12.5px]" role="alert">
            <div className="font-semibold text-err">{problem.title}</div>
            {problem.hint && <div className="text-fg-2">{problem.hint}</div>}
            <div className="mt-1 break-words font-mono text-[11.5px] text-muted">{problem.detail}</div>
          </div>
        )}
        {needTyping && (
          <div>
            <div className="mb-1 text-[12.5px] text-fg-2">
              {production ? <b className="text-err">This is PRODUCTION. </b> : null}Type <b>DELETE</b> to confirm.
            </div>
            <Input autoFocus value={typed} onChange={(e) => setTyped(e.target.value)} placeholder="DELETE" aria-label="Type DELETE to confirm"
              onKeyDown={(e) => e.key === 'Enter' && ok && run()} />
          </div>
        )}
      </div>
    </Modal>
  )
}
