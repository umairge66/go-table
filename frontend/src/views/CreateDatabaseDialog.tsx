import clsx from 'clsx'
import { AlertTriangle, FolderOpen } from 'lucide-react'
import { useEffect, useState } from 'react'
import { api, ApiError, type NewDatabase } from '../api'
import { Button, Field, Input, Modal, errorParts, useToast } from '../components/ui'
import { nativeDialogs } from '../lib/native'
import { examplePath } from '../lib/platform'
import { chooseSaveLocation } from '../lib/saveFile'
import type { Connection, SessionInfo } from '../types'

/**
 * CreateDatabaseDialog creates a database (or PostgreSQL schema). SQLite has
 * one database per file, so there it creates a new file and saves it as a
 * connection.
 */
export function CreateDatabaseDialog({ session, onClose, onCreated, onCreatedFile }: {
  session: SessionInfo
  onClose: () => void
  onCreated: (db: NewDatabase) => void
  onCreatedFile: (c: Connection) => void
}) {
  const toast = useToast()
  const conn = session.connection
  const driver = conn.driver
  const pg = driver === 'postgres'
  const sqlite = driver === 'sqlite'
  const [db, setDb] = useState<NewDatabase>({ name: '', kind: 'database', charset: driver === 'mysql' || driver === 'mariadb' ? 'utf8mb4' : '' })
  const [preview, setPreview] = useState('')
  const [problem, setProblem] = useState('')
  const [saving, setSaving] = useState(false)
  // SQLite: where the new file goes (a typed path, or a place picked in the
  // system dialog, which only the server knows by handle).
  const folder = (conn.path ?? '').replace(/[^/\\]*$/, '')
  const [path, setPath] = useState('')
  const [picked, setPicked] = useState<{ handle: string; name: string } | null>(null)
  const [native, setNative] = useState(false)
  useEffect(() => { nativeDialogs().then(setNative) }, [])
  const suggested = db.name.trim() ? `${folder}${db.name.trim().replace(/[^\w.-]+/g, '_')}.sqlite` : ''

  useEffect(() => {
    if (sqlite || !db.name.trim()) { setPreview(''); setProblem(''); return }
    const t = setTimeout(async () => {
      try {
        setPreview((await api.createDatabase(session.id, db, true)).statements.join('\n'))
        setProblem('')
      } catch (e) {
        setPreview('')
        setProblem((e as ApiError).detail ?? errorParts(e)[1])
      }
    }, 200)
    return () => clearTimeout(t)
  }, [db, session.id, sqlite])

  const create = async () => {
    setSaving(true)
    try {
      if (sqlite) {
        const c = await api.createSQLiteFile({ name: db.name.trim(), groupId: conn.group_id, tag: conn.tag,
          ...(picked ? { handle: picked.handle } : { path: path.trim() || suggested }) })
        toast('ok', `Database “${c.name}” created`, c.path)
        onCreatedFile(c)
      } else {
        await api.createDatabase(session.id, db, false)
        toast('ok', `${pg && db.kind === 'schema' ? 'Schema' : 'Database'} “${db.name.trim()}” created`)
        onCreated(db)
      }
    } catch (e) {
      setProblem(errorParts(e).filter(Boolean).join(' — '))
      setPicked(null)
    } finally {
      setSaving(false)
    }
  }

  const pick = async () => {
    const t = await chooseSaveLocation(`${db.name.trim() || 'database'}.sqlite`, 'sqlite')
    if (t && 'native' in t) setPicked({ handle: t.native, name: t.name })
  }

  const what = pg && db.kind === 'schema' ? 'schema' : 'database'
  const canCreate = db.name.trim() && (sqlite ? !!(picked || path.trim() || suggested) : !problem && !!preview)
  const production = conn.tag === 'production'

  return (
    <Modal width={560} title={sqlite ? 'New database file' : pg ? 'New database or schema' : 'New database'}
      subtitle={<>on <b className="font-medium text-fg-2">{conn.name}</b></>} onClose={onClose}
      footer={<>
        {production && !sqlite && <span className="mr-auto flex items-center gap-1.5 text-[12px] font-medium text-err"><AlertTriangle className="size-3.5" /> This is PRODUCTION.</span>}
        <Button variant="ghost" onClick={onClose}>Cancel</Button>
        <Button variant={production && !sqlite ? 'danger' : 'primary'} disabled={!canCreate} loading={saving} onClick={create}>
          {sqlite ? 'Create and open' : `Create ${what}`}
        </Button>
      </>}>
      <form className="space-y-3" aria-label="New database form" onSubmit={(e) => { e.preventDefault(); if (canCreate) create() }}>
        {pg && (
          <div className="flex rounded-lg border border-line bg-panel p-0.5" role="radiogroup" aria-label="What to create">
            {(['database', 'schema'] as const).map((k) => (
              <button type="button" key={k} role="radio" aria-checked={db.kind === k} onClick={() => setDb({ ...db, kind: k })}
                className={clsx('h-7 flex-1 rounded-md text-[12.5px] font-medium', db.kind === k ? 'bg-accent text-accent-fg' : 'text-muted hover:text-fg')}>
                {k === 'database' ? 'Database' : 'Schema in this database'}
              </button>
            ))}
          </div>
        )}
        <Field label="Name">
          <Input autoFocus value={db.name} placeholder={sqlite ? 'e.g. inventory' : 'e.g. analytics'} aria-label="Database name"
            onChange={(e) => { setDb({ ...db, name: e.target.value }); setPicked(null) }} />
        </Field>
        {(driver === 'mysql' || driver === 'mariadb') && (
          <Field label="Character set" hint="utf8mb4 stores any text, including emoji.">
            <Input mono value={db.charset} onChange={(e) => setDb({ ...db, charset: e.target.value })} aria-label="Character set" />
          </Field>
        )}
        {sqlite ? (
          <Field label="File" hint="SQLite keeps each database in its own file. It's saved as a new connection and opened.">
            {picked ? (
              <div className="flex h-8 items-center gap-2 rounded-lg border border-line bg-panel-2 px-2.5 text-[12.5px]">
                <span className="min-w-0 flex-1 truncate font-mono">{picked.name}</span>
                <button type="button" className="text-[12px] text-accent hover:underline" onClick={pick}>Change…</button>
              </div>
            ) : (
              <div className="flex gap-2">
                <Input mono className="flex-1" value={path} placeholder={suggested || examplePath('data/new.sqlite')} aria-label="Database file"
                  onChange={(e) => setPath(e.target.value)} />
                {native && <Button type="button" icon={<FolderOpen className="size-3.5" />} onClick={pick}>Choose…</Button>}
              </div>
            )}
          </Field>
        ) : (
          <div>
            <div className="mb-1 text-[11.5px] font-medium text-muted">SQL that will run</div>
            <pre data-testid="database-sql" className={clsx('min-h-[44px] overflow-x-auto whitespace-pre-wrap rounded-lg border px-3 py-2.5 font-mono text-[12px]',
              problem ? 'border-err/30 bg-err/5 text-err' : 'border-line bg-panel-2 text-fg-2')}>
              {problem || preview || `Enter a name to see the statement.`}
            </pre>
          </div>
        )}
        {sqlite && problem && <div className="rounded-lg border border-err/30 bg-err/5 px-3 py-2 text-[12px] text-err" role="alert">{problem}</div>}
      </form>
    </Modal>
  )
}
