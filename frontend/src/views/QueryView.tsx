import clsx from 'clsx'
import { AlertCircle, Bookmark, CheckCircle2, Download, FolderOpen, Play, Save, Sparkles, Square, StepForward } from 'lucide-react'
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { api } from '../api'
import { Grid } from '../components/Grid'
import { SqlEditor, type SqlEditorHandle } from '../components/SqlEditor'
import { Button, DragHandle, IconButton, Select, errorParts, useMenu, useResizable, useToast } from '../components/ui'
import { HISTORY_EVENT, notify } from './SidebarPanels'
import { toCSV, toInserts, toJSON } from '../lib/exportClient'
import { saveTextAs } from '../lib/saveFile'
import { formatSQL } from '../lib/format'
import type { QueryResult, SessionInfo } from '../types'
import { fmt } from './DataView'

const LIMITS = [100, 1000, 10000, 0]

export function QueryView({ session, schema, active, initialSql, completions, onChange, onSchemaChanged, onSave, savedName, autoRun }: {
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
}) {
  const toast = useToast()
  const menu = useMenu()
  const editor = useRef<SqlEditorHandle>(null)
  const fileInput = useRef<HTMLInputElement>(null)
  const [results, setResults] = useState<QueryResult[] | null>(null)
  const [tab, setTab] = useState(0) // 0 = messages, n = n-th result set
  const [running, setRunning] = useState<string | null>(null)
  const [elapsed, setElapsed] = useState(0)
  const [limit, setLimit] = useState(session.connection.limit_query_rows || 1000)
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
        <Select aria-label="Row limit" className="w-28" value={limit} onChange={(e) => setLimit(Number(e.target.value))}>
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
          <ResultTab active={tab === 0} onClick={() => setTab(0)}>
            {failed ? <AlertCircle className="size-3.5 text-err" /> : results ? <CheckCircle2 className="size-3.5 text-ok" /> : null}
            Messages
          </ResultTab>
          {sets.map((s, n) => (
            <ResultTab key={s.i} active={tab === n + 1} onClick={() => setTab(n + 1)}>
              Result {n + 1} <span className="text-faint">{fmt(s.r.rows?.length ?? 0)}</span>
            </ResultTab>
          ))}
          <div className="flex-1" />
          {current && (
            <>
              <span className="text-[12px] text-muted">{fmt(current.rows?.length ?? 0)} rows · {current.ms.toFixed(1)} ms</span>
              {current.truncated && <span className="rounded bg-warn/15 px-1.5 py-0.5 text-[11px] font-medium text-warn">limited to {fmt(limit)}</span>}
              <Button size="sm" variant="ghost" icon={<Download className="size-3.5" />} onClick={exportResult}>Export</Button>
            </>
          )}
        </div>
        <div className="min-h-0 flex-1">
          {current?.columns ? (
            <Grid key={`${runKey}-${tab}`} columns={current.columns.map((c, i) => ({ name: c, type: current.types?.[i] }))}
              rowCount={current.rows?.length ?? 0} cell={(r, c) => ({ value: current.rows![r][c], status: 'clean' })} readOnly
              onContextMenu={(e, r, c) => menu.open(e, [{ label: 'Copy value', onSelect: () => navigator.clipboard?.writeText(current.rows![r][c] ?? 'NULL') }])} />
          ) : (
            <Messages results={results} running={!!running} />
          )}
        </div>
      </div>
      {menu.node}
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
