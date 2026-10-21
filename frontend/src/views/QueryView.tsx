import clsx from 'clsx'
import { AlertCircle, Bookmark, CheckCircle2, Copy, Download, FolderOpen, Lock, Pencil, Play, Save, Sparkles, Square, StepForward, Trash2, Undo2 } from 'lucide-react'
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { api, type TableRef } from '../api'
import { useConfirm } from '../components/Confirm'
import { Grid } from '../components/Grid'
import { SqlEditor, type SqlEditorHandle } from '../components/SqlEditor'
import { Button, DragHandle, IconButton, Modal, Select, errorParts, useMenu, useResizable, useToast } from '../components/ui'
import { HISTORY_EVENT, notify } from './SidebarPanels'
import { toCSV, toInserts, toJSON } from '../lib/exportClient'
import { saveTextAs } from '../lib/saveFile'
import { formatSQL } from '../lib/format'
import { cellAt, emptyEdits, pendingCount, setCell, toChanges, toggleDelete, type Edits } from '../lib/edits'
import { cellKind } from '../lib/cellKind'
import { sourceTable } from '../lib/sourceTable'
import type { Cell, Column, QueryResult, SessionInfo } from '../types'
import { fmt } from './DataView'

const LIMITS = [100, 1000, 10000, 0]
const LIMIT_KEY = 'gotable:queryLimit'
const savedLimit = () => { try { const v = localStorage.getItem(LIMIT_KEY); return v === null ? null : Number(v) } catch { return null } }

export function QueryView({ session, schema, active, initialSql, completions, onChange, onSchemaChanged, onSave, savedName, autoRun, onDirty }: {
  session: SessionInfo
  /** schema is the database this tab runs in. */
  schema: string
  /** onSave keeps the query under Queries (Ctrl+S). */
  onSave: (sql: string) => void
  /** savedName is set when the tab is a saved query. */
  savedName?: string
  /** autoRun runs the initial SQL once when the tab opens ("Run again" from history). */
  autoRun?: boolean
  active: boolean
  initialSql: string
  completions: Record<string, string[]>
  onChange: (sql: string) => void
  onSchemaChanged: (schema: string) => void
  /** onDirty reports edits to a result that aren't committed yet. */
  onDirty?: (dirty: boolean) => void
}) {
  const toast = useToast()
  const confirm = useConfirm()
  const menu = useMenu()
  const editor = useRef<SqlEditorHandle>(null)
  const fileInput = useRef<HTMLInputElement>(null)
  const [results, setResults] = useState<QueryResult[] | null>(null)
  const [tab, setTab] = useState(0) // 0 = messages, n = n-th result set
  const [running, setRunning] = useState<string | null>(null)
  const [elapsed, setElapsed] = useState(0)
  // The row limit is picked in the toolbar and remembered for every query tab.
  const [limit, setLimit] = useState(() => savedLimit() ?? (session.connection.limit_query_rows || 1000))
  const [editorH, dragEditor] = useResizable(260, 90, 900, 'y', false, 'gotable:editorH')
  const [runKey, setRunKey] = useState(0)

  // Typing never touches React state: the editor owns the text, and changes
  // are reported (debounced) for persistence only.
  const onChangeRef = useRef(onChange)
  onChangeRef.current = onChange
  const saveTimer = useRef<ReturnType<typeof setTimeout>>(undefined)
  const changed = useCallback((v: string) => {
    clearTimeout(saveTimer.current)
    saveTimer.current = setTimeout(() => onChangeRef.current(v), 400)
  }, [])
  useEffect(() => () => clearTimeout(saveTimer.current), [])
  useEffect(() => { if (active) editor.current?.focus() }, [active])

  useEffect(() => {
    if (!running) return
    const start = Date.now()
    const t = setInterval(() => setElapsed(Date.now() - start), 100)
    return () => clearInterval(t)
  }, [running])

  const run = useCallback(async (text: string) => {
    if (!text.trim() || running) return
    if (!await guardRef.current()) return
    const id = crypto.randomUUID()
    setRunning(id)
    setElapsed(0)
    try {
      const r = await api.execute(session.id, text, limit, id, schema)
      setResults(r.results)
      setRunKey((k) => k + 1)
      const sets = r.results.filter((x) => x.hasRows)
      const failed = r.results.some((x) => x.error)
      setTab(failed || !sets.length ? 0 : sets.length)
      if (/\b(create|drop|alter|rename|use|set\s+search_path)\b/i.test(text)) onSchemaChanged(r.schema)
      notify(HISTORY_EVENT)
    } catch (e) {
      const [title, body] = errorParts(e)
      setResults([{ statement: text, hasRows: false, rowsAffected: 0, ms: 0, truncated: false, error: { title, detail: body } }])
      setTab(0)
      notify(HISTORY_EVENT)
    } finally {
      setRunning(null)
    }
  }, [limit, onSchemaChanged, running, session.id, schema])

  // Ctrl+S saves the query (when this tab is visible).
  useEffect(() => {
    if (!active) return
    const onKey = (e: KeyboardEvent) => {
      if ((e.ctrlKey || e.metaKey) && !e.shiftKey && e.key.toLowerCase() === 's') { e.preventDefault(); onSave(editor.current?.value() ?? '') }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [active, onSave])

  // "Run again" from history runs the text once when the tab opens.
  const autoRan = useRef(false)
  useEffect(() => {
    if (autoRun && !autoRan.current && initialSql.trim()) { autoRan.current = true; run(initialSql) }
  }, [autoRun]) // eslint-disable-line react-hooks/exhaustive-deps

  const runCurrent = () => run(editor.current?.current() ?? '')
  const runAll = () => run(editor.current?.value() ?? '')
  const stop = () => { if (running) api.cancel(session.id, running) }
  const saveAs = async (name: string, kind: 'sql' | 'csv' | 'json', text: () => string) => {
    try {
      const t = await saveTextAs(name, kind, text())
      if (t) toast('ok', 'Saved', t.name)
    } catch (e) {
      toast('error', 'Could not save the file', String((e as Error).message ?? e))
    }
  }
  const beautify = () => {
    try {
      editor.current?.transform((text) => formatSQL(text, session.connection.driver))
    } catch (e) {
      toast('error', "Couldn't format this SQL", `Fix the syntax error first. ${(e as Error).message.split('\n')[0]}`)
    }
  }

  const sets = useMemo(() => (results ?? []).map((r, i) => ({ r, i })).filter((x) => x.r.hasRows), [results])
  const current = tab > 0 ? sets[tab - 1]?.r : undefined
  const failed = results?.find((r) => r.error)

  // A result of a plain SELECT from one table (with its primary key) can be
  // edited in place; changes are committed like in a table tab.
  const [editable, setEditable] = useState<{ table: TableRef; names: string[]; pk: number[]; cols: Column[] } | { reason: string } | null>(null)
  const [edits, setEdits] = useState<Edits>(emptyEdits)
  const [review, setReview] = useState<string[] | null>(null)
  const [committing, setCommitting] = useState(false)
  const target = editable && 'table' in editable ? editable : null
  const pending = target ? pendingCount(edits) : 0
  const rows = current?.rows ?? []
  useEffect(() => {
    setEdits(emptyEdits())
    setEditable(null)
    if (!current?.columns) return
    const src = sourceTable(current.statement)
    if (!src) { setEditable({ reason: 'Only the result of a plain SELECT from one table can be edited.' }); return }
    const table = { schema: src.schema || schema, name: src.name }
    const cols = current.columns
    let stale = false
    api.structure(session.id, table).then(({ columns }) => {
      if (stale) return
      // Result columns by table column (names may differ in case outside PostgreSQL).
      const find = (n: string) => columns.find((c) => c.name === n) ?? (session.connection.driver === 'postgres' ? undefined : columns.find((c) => c.name.toLowerCase() === n.toLowerCase()))
      const names = cols.map((n) => find(n)?.name ?? '')
      const keys = columns.filter((c) => c.primaryKey)
      if (names.some((n, i) => !n || names.indexOf(n) !== i)) setEditable({ reason: 'Some columns are computed or renamed, so they can’t be written back.' })
      else if (!keys.length) setEditable({ reason: `${table.name} has no primary key, so rows can’t be matched safely.` })
      else if (keys.some((k) => !names.includes(k.name))) setEditable({ reason: `Include the primary key (${keys.map((k) => k.name).join(', ')}) to edit these rows.` })
      else setEditable({ table, names, pk: keys.map((k) => names.indexOf(k.name)), cols: names.map((n) => columns.find((c) => c.name === n)!) })
    }, () => { if (!stale) setEditable({ reason: 'The table behind this result couldn’t be read.' }) })
    return () => { stale = true }
  }, [current, schema, session.id, session.connection.driver])

  const onDirtyRef = useRef(onDirty)
  onDirtyRef.current = onDirty
  useEffect(() => { onDirtyRef.current?.(pending > 0) }, [pending])

  // guard asks before something would throw away uncommitted edits.
  const guard = async () => {
    if (!pending) return true
    const r = await confirm({
      title: 'Unsaved changes in the result',
      message: <>You have <b>{pending}</b> change{pending === 1 ? '' : 's'} that {pending === 1 ? 'isn’t' : 'aren’t'} committed yet. Discard {pending === 1 ? 'it' : 'them'}?</>,
      confirmLabel: 'Discard changes', danger: true,
    })
    return r === 'confirm'
  }
  const guardRef = useRef(guard)
  guardRef.current = guard
  const showTab = async (n: number) => { if (n !== tab && await guard()) setTab(n) }

  const cell = useCallback((r: number, c: number) => cellAt(rows, edits, r, c), [rows, edits])
  const edit = (r: number, c: number, v: Cell) => setEdits((e) => setCell(rows, e, r, c, v))
  const deleteRows = (rs: number[]) => setEdits((e) => toggleDelete(rows, e, rs))
  const changes = () => toChanges(rows, edits, target!.names, target!.pk)
  const startReview = async () => {
    if (!pending) return
    try {
      setReview((await api.changes(session.id, target!.table, changes(), true, true)).statements)
    } catch (e) { toast('error', ...errorParts(e)) }
  }
  const commit = async () => {
    if (!current || !target) return
    setCommitting(true)
    try {
      const r = await api.changes(session.id, target.table, changes(), true, false)
      setReview(null)
      toast('ok', `Saved ${r.statements.length} change${r.statements.length === 1 ? '' : 's'}`, target.table.name)
      // Show the rows as they are now (defaults, triggers) by running the SELECT again.
      const again = await api.execute(session.id, current.statement, limit, crypto.randomUUID(), schema)
      const i = sets[tab - 1].i
      setResults((rs) => rs && rs.map((x, j) => (j === i ? again.results[0] ?? x : x)))
      setRunKey((k) => k + 1)
      notify(HISTORY_EVENT)
    } catch (e) {
      toast('error', ...errorParts(e))
    } finally {
      setCommitting(false)
    }
  }

  const exportResult = (e: React.MouseEvent) => {
    if (!current?.columns || !current.rows) return
    const cols = current.columns
    const rows = current.rows
    menu.open(e, [
      { label: 'Save as CSV…', onSelect: () => saveAs('result.csv', 'csv', () => toCSV(cols, rows)) },
      { label: 'Save as JSON…', onSelect: () => saveAs('result.json', 'json', () => toJSON(cols, rows)) },
      { label: 'Save as SQL INSERTs…', onSelect: () => saveAs('result.sql', 'sql', () => toInserts('result', cols, rows)) },
      'sep',
      { label: 'Copy as CSV', onSelect: () => { navigator.clipboard?.writeText(toCSV(cols, rows)); toast('ok', 'Copied') } },
    ])
  }

  return (
    <div className="flex h-full flex-col">
      <div className="flex h-11 shrink-0 items-center gap-1.5 border-b border-line px-2">
        <Button size="sm" variant="primary" icon={<Play className="size-3.5" />} onClick={runCurrent} disabled={!!running} kbd="Ctrl ↵">Run</Button>
        <Button size="sm" variant="ghost" icon={<StepForward className="size-3.5" />} onClick={runAll} disabled={!!running} title="Run all (Ctrl+Shift+Enter)">Run all</Button>
        <Button size="sm" variant="ghost" icon={<Sparkles className="size-3.5" />} onClick={beautify} title="Beautify the selection, or everything (Ctrl+I)">Format</Button>
        {running && <Button size="sm" variant="danger" icon={<Square className="size-3 fill-current" />} onClick={stop}>Stop · {(elapsed / 1000).toFixed(1)}s</Button>}
        <div className="flex-1" />
        <span className="text-[12px] text-muted">Limit</span>
        <Select aria-label="Row limit" className="w-28" value={limit} onChange={(e) => {
          const n = Number(e.target.value)
          setLimit(n)
          try { localStorage.setItem(LIMIT_KEY, String(n)) } catch { /* not remembered */ }
        }}>
          {[...new Set([...LIMITS, limit])].map((l) => <option key={l} value={l}>{l ? fmt(l) + ' rows' : 'No limit'}</option>)}
        </Select>
        <Button size="sm" variant="ghost" icon={<FolderOpen className="size-3.5" />} onClick={() => fileInput.current?.click()}>Open</Button>
        <Button size="sm" variant="ghost" icon={<Bookmark className="size-3.5" />} onClick={() => onSave(editor.current?.value() ?? '')}
          title={savedName ? `Update “${savedName}” (Ctrl+S)` : 'Save to Queries (Ctrl+S)'}>Save</Button>
        <IconButton label="Save as .sql file…" onClick={() => saveAs(`${savedName ?? 'query'}.sql`, 'sql', () => editor.current?.value() ?? '')}><Save className="size-3.5" /></IconButton>
        <input ref={fileInput} type="file" accept=".sql,.txt" hidden onChange={async (e) => {
          const f = e.target.files?.[0]
          if (f) editor.current?.setValue(await f.text())
          e.target.value = ''
        }} />
      </div>
      <div style={{ height: editorH }} className="shrink-0">
        <SqlEditor ref={editor} value={initialSql} onChange={changed} driver={session.connection.driver} tables={completions}
          onRunCurrent={runCurrent} onRunAll={runAll} onFormat={beautify} autoFocus={active} />
      </div>
      <DragHandle axis="y" onMouseDown={dragEditor} />
      <div className="flex min-h-0 flex-1 flex-col">
        <div className="flex h-9 shrink-0 items-center gap-1 border-b border-line bg-panel-2 px-2" role="tablist">
          <ResultTab active={tab === 0} onClick={() => showTab(0)}>
            {failed ? <AlertCircle className="size-3.5 text-err" /> : results ? <CheckCircle2 className="size-3.5 text-ok" /> : null}
            Messages
          </ResultTab>
          {sets.map((s, n) => (
            <ResultTab key={s.i} active={tab === n + 1} onClick={() => showTab(n + 1)}>
              Result {n + 1} <span className="text-faint">{fmt(s.r.rows?.length ?? 0)}</span>
            </ResultTab>
          ))}
          <div className="flex-1" />
          {current && (
            <>
              {editable && (target
                ? <span className="flex items-center gap-1 text-[12px] text-muted" data-testid="result-editable"><Pencil className="size-3" /> Editable · {target.table.name}</span>
                : <span className="flex items-center gap-1 text-[12px] text-faint" title={'reason' in editable ? editable.reason : undefined} data-testid="result-readonly"><Lock className="size-3" /> Read-only</span>)}
              <span className="text-[12px] text-muted">{fmt(current.rows?.length ?? 0)} rows · {current.ms.toFixed(1)} ms</span>
              {current.truncated && <span className="rounded bg-warn/15 px-1.5 py-0.5 text-[11px] font-medium text-warn">limited to {fmt(limit)}</span>}
              <Button size="sm" variant="ghost" icon={<Download className="size-3.5" />} onClick={exportResult}>Export</Button>
            </>
          )}
        </div>
        {pending > 0 && (
          <div className="flex h-10 shrink-0 items-center gap-2 border-b border-warn/30 bg-warn/10 px-3 text-[12.5px] animate-fade" role="status">
            <span className="size-2 rounded-full bg-warn" />
            <span><b>{pending}</b> unsaved change{pending === 1 ? '' : 's'} to {target!.table.name} — nothing is written until you commit.</span>
            <div className="flex-1" />
            <Button size="sm" variant="ghost" icon={<Undo2 className="size-3.5" />} onClick={() => setEdits(emptyEdits())}>Discard</Button>
            <Button size="sm" variant="primary" onClick={startReview}>Review & commit</Button>
          </div>
        )}
        <div className="min-h-0 flex-1">
          {current?.columns ? (
            <Grid key={`${runKey}-${tab}`} columns={current.columns.map((c, i) => {
              const col = target?.cols[i]
              return { name: c, type: col?.type ?? current.types?.[i], pk: target?.pk.includes(i), edit: col && cellKind(col.type, col.values, session.connection.driver), nullable: col?.nullable }
            })}
              rowCount={rows.length} cell={cell} readOnly={!target} onEdit={edit} onDeleteRows={deleteRows}
              onContextMenu={(e, r, c, rs) => menu.open(e, [
                { label: 'Copy value', icon: <Copy />, onSelect: () => navigator.clipboard?.writeText(cell(r, c).value ?? 'NULL') },
                ...(target ? [
                  'sep' as const,
                  { label: 'Set to NULL', disabled: cell(r, c).status === 'deleted', onSelect: () => edit(r, c, null) },
                  { label: 'Set to empty text', disabled: cell(r, c).status === 'deleted', onSelect: () => edit(r, c, '') },
                  { label: rs.length > 1 ? `Delete ${rs.length} rows` : 'Delete row', icon: <Trash2 />, kbd: 'Del', danger: true, onSelect: () => deleteRows(rs) },
                ] : []),
              ])} />
          ) : (
            <Messages results={results} running={!!running} />
          )}
        </div>
      </div>
      {menu.node}
      {review && target && (
        <Modal width={720} title="Review changes" subtitle={`${review.length} statement${review.length === 1 ? '' : 's'} will run in one transaction on ${target.table.name}.`}
          onClose={() => setReview(null)}
          footer={<>
            {session.connection.tag === 'production' && <span className="mr-auto text-[12px] font-medium text-err">⚠ This is PRODUCTION.</span>}
            <Button variant="ghost" onClick={() => setReview(null)}>Cancel</Button>
            <Button variant={session.connection.tag === 'production' ? 'danger' : 'primary'} loading={committing} onClick={commit} autoFocus>Commit</Button>
          </>}>
          <div className="h-[280px] overflow-hidden rounded-lg border border-line">
            <SqlEditor value={review.join('\n')} driver={session.connection.driver} readOnly />
          </div>
        </Modal>
      )}
    </div>
  )
}

function ResultTab({ active, onClick, children }: { active: boolean; onClick: () => void; children: React.ReactNode }) {
  return (
    <button role="tab" aria-selected={active} onClick={onClick}
      className={clsx('flex h-7 items-center gap-1.5 rounded-md px-2.5 text-[12px] font-medium', active ? 'bg-panel text-fg shadow-[0_0_0_1px_var(--line)]' : 'text-muted hover:text-fg')}>
      {children}
    </button>
  )
}

function Messages({ results, running }: { results: QueryResult[] | null; running: boolean }) {
  if (running) return <div className="p-4 text-[12.5px] text-muted">Running…</div>
  if (!results) return <div className="p-4 text-[12.5px] text-muted">Results appear here. Press <b>Ctrl ↵</b> to run the statement under the cursor.</div>
  return (
    <div className="h-full space-y-2 overflow-auto p-3" data-testid="messages">
      {results.map((r, i) => (
        <div key={i} className={clsx('rounded-lg border p-2.5', r.error ? 'border-err/30 bg-err/5' : 'border-line bg-panel')}>
          <div className="truncate font-mono text-[12px] text-fg-2">{r.statement.replace(/\s+/g, ' ')}</div>
          {r.error ? (
            <div className="mt-1.5 text-[12.5px]">
              <div className="font-semibold text-err">{r.error.title}</div>
              {r.error.hint && <div className="text-fg-2">{r.error.hint}</div>}
              <div className="mt-1 break-words font-mono text-[11.5px] text-muted">{r.error.detail}</div>
            </div>
          ) : (
            <div className="mt-1 text-[12px] text-muted">
              {r.hasRows ? `${fmt(r.rows?.length ?? 0)} row(s) returned` : `OK — ${fmt(r.rowsAffected)} row(s) affected`} · {r.ms.toFixed(1)} ms
              {r.truncated && ' · limited'}
            </div>
          )}
        </div>
      ))}
    </div>
  )
}
