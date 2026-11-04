import { useEffect, useState } from 'react'
import { api } from '../api'
import { Button, Field, Input, Modal, errorParts, useToast } from '../components/ui'
import type { SessionInfo } from '../types'

/** CreateCollectionDialog makes a MongoDB collection: a name is all it needs, as fields come with the documents. */
export function CreateCollectionDialog({ session, schema, onClose, onCreated }: {
  session: SessionInfo
  schema: string
  onClose: () => void
  onCreated: (name: string) => void
}) {
  const toast = useToast()
  const [name, setName] = useState('')
  const [preview, setPreview] = useState<string[]>([])
  const [busy, setBusy] = useState(false)
  const table = { name: name.trim(), columns: [], comment: '' }
  useEffect(() => {
    if (!table.name) { setPreview([]); return }
    let stale = false
    api.createTable(session.id, schema, table, true).then((r) => { if (!stale) setPreview(r.statements) }).catch(() => { if (!stale) setPreview([]) })
    return () => { stale = true }
  }, [session.id, schema, table.name]) // eslint-disable-line react-hooks/exhaustive-deps
  const create = async () => {
    if (!table.name || busy) return
    setBusy(true)
    try {
      await api.createTable(session.id, schema, table, false)
      toast('ok', 'Collection created', table.name)
      onCreated(table.name)
    } catch (e) {
      toast('error', ...errorParts(e))
      setBusy(false)
    }
  }
  return (
    <Modal width={480} title="New collection" subtitle={<>in <b className="font-medium text-fg-2">{schema}</b></>} onClose={onClose}
      footer={<><Button variant="ghost" onClick={onClose}>Cancel</Button><Button variant="primary" disabled={!table.name || busy} onClick={create}>Create</Button></>}>
      <form className="space-y-3" aria-label="New collection" onSubmit={(e) => { e.preventDefault(); create() }}>
        <Field label="Name"><Input autoFocus value={name} aria-label="Collection name" placeholder="orders" onChange={(e) => setName(e.target.value)} /></Field>
        <div>
          <div className="mb-1 text-[11.5px] font-medium text-muted">Command that will run</div>
          <pre className="min-h-[44px] overflow-x-auto whitespace-pre-wrap rounded-lg border border-line bg-panel-2 px-3 py-2.5 font-mono text-[12px] text-fg-2">{preview.join('\n') || 'Enter a name.'}</pre>
        </div>
      </form>
    </Modal>
  )
}
