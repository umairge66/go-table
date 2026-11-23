import clsx from 'clsx'
import {
  ArrowDownUp, ChevronDown, Code2, Columns2, History, PanelLeft, Copy, Database, Download, Eye, FileCode2, Home as HomeIcon, LogOut, Moon, PanelRight, Plus,
  RefreshCw, Search, Server, Settings, Sun, Table2, Trash2, Upload, X,
} from 'lucide-react'
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { api } from '../api'
import { CommandPalette, type Command } from '../components/CommandPalette'
import { useConfirm } from '../components/Confirm'
import { JobsIndicator } from '../components/JobsIndicator'
import { Button, DragHandle, DriverBadge, IconButton, RefreshButton, Input, Kbd, TagPill, errorParts, useMenu, useResizable, useToast } from '../components/ui'
import { family, features, tagInfo, type Cell, type Connection, type DbObject, type Driver, type ForeignRef, type HistoryEntry, type SavedQuery, type SessionInfo, isCassandra, isMongo, isRedis } from '../types'
import { isPattern, keyLabel as redisLabel, quoteArg, unescapeKey } from '../lib/redisShell'
import { mongoColl } from '../lib/mongoShell'
import { getSettings, panelOpen, rememberPanel, useSettings } from '../lib/settings'
import { binding, hit, label as keyLabel } from '../lib/shortcuts'
import { keys } from '../lib/platform'
import { CreateCollectionDialog } from './CreateCollectionDialog'
import { DataView } from './DataView'
import { DeleteTablesDialog } from './DeleteTablesDialog'
import { subtitle } from './Home'
import { CreateDatabaseDialog } from './CreateDatabaseDialog'
import { DropDatabaseDialog, dropKind, type DropTarget } from './DropDatabaseDialog'
import { CreateTableView } from './CreateTableView'
import { QueryView } from './QueryView'
import { WindowControls, dragWindow, useOwnTitleBar } from '../components/WindowChrome'
import { HistoryPanel, NameDialog, QUERIES_EVENT, QueriesPanel, notify } from './SidebarPanels'
import { TransferDialog } from './TransferDialog'

/**
 * OpenDb is a database shown in the sidebar. Schemas of one connection
 * (PostgreSQL schemas, MySQL databases, attached SQLite files) share a
 * session; another PostgreSQL database gets its own (forked) session.
 */
interface OpenDb {
  key: string
  info: SessionInfo // the session it uses (id, connection, version, database)
  schema: string
  objects: DbObject[] | null // null while loading
  completions: Record<string, string[]>
  collapsed: boolean
}

export interface Tab {
  id: string
  kind: 'table' | 'query' | 'create-table'
  title: string
  dbKey: string
  object?: DbObject
  sql?: string
  dirty?: boolean
  /** savedId links a query tab to a saved query; savedSql is its stored text. */
  savedId?: string
  savedSql?: string
  /** modified: the text differs from the saved query. */
  modified?: boolean
  autoRun?: boolean
  /** focusFilter opens a table filtered to one row (a followed foreign key). */
  focusFilter?: { pairs: { column: string; value: Cell }[]; key: number }
  /** structure opens a table tab on its Structure view (after a rename). */
  structure?: boolean
}

type PersistedQuery = { sql: string; title?: string; savedId?: string; savedSql?: string }

const dbKeyOf = (sid: string, schema: string) => `${sid}|${schema}`
const queriesKey = (connId: string) => `gotable:queries:${connId}`
const openDbsKey = (connId: string) => `gotable:open-dbs:${connId}`

export function Workspace({ session, sessions, active, onHome, onSwitch, onDisconnect, theme, toggleTheme, onOpenConnection, onOpenSettings }: {
  session: SessionInfo
  sessions: SessionInfo[]
  active: boolean
  onHome: () => void
  onSwitch: (sid: string) => void
  onDisconnect: () => void
  theme: 'light' | 'dark'
  onOpenSettings: () => void
  toggleTheme: () => void
  /** onOpenConnection opens another saved connection (a new SQLite file). */
  onOpenConnection: (c: Connection) => void
}) {
  const toast = useToast()
  const confirm = useConfirm()
  const menu = useMenu()
  const ownTitleBar = useOwnTitleBar()
  const conn = session.connection
  // Key hints follow the shortcuts as set in Settings.
  const settings = useSettings()
  const keyHint = (id: string) => keyLabel(binding(id, settings.shortcuts))
  const withKeys = (text: string, id: string) => { const k = keyHint(id); return k ? `${text} (${keys(k.replace(/ /g, '+'))})` : text }
  // pg: databases on the server hold schemas (PostgreSQL, SQL Server); another
  // database opens as its own session.
  const pg = features(conn.driver).schemas && !features(conn.driver).file
  const fileDb = features(conn.driver).file
  const production = conn.tag === 'production'
  const mongo = isMongo(conn.driver)
  const redis = isRedis(conn.driver)
  const tableWord = mongo ? 'collection' : redis ? 'key' : 'table'
  const [creatingCollection, setCreatingCollection] = useState<OpenDb | null>(null)

  const mainSchema = session.schema || session.schemas[0] || ''
  const mainKey = dbKeyOf(session.id, mainSchema)
  const [dbs, setDbs] = useState<OpenDb[]>([{ key: mainKey, info: session, schema: mainSchema, objects: null, completions: {}, collapsed: false }])
  const [activeKey, setActiveKey] = useState(mainKey)
  const activeDb = dbs.find((d) => d.key === activeKey) ?? dbs[0]
  const dbsRef = useRef(dbs)
  dbsRef.current = dbs

  const [schemaList, setSchemaList] = useState<string[]>(session.schemas)
  const [serverDbs, setServerDbs] = useState<string[] | null>(null)
  const [dropping, setDropping] = useState<{ target?: DropTarget; choices?: DropTarget[] } | null>(null)
  const [filter, setFilter] = useState('')
  const [tabs, setTabs] = useState<Tab[]>(() => {
    try {
      const saved = JSON.parse(localStorage.getItem(queriesKey(conn.id)) ?? '[]') as (string | PersistedQuery)[]
      return saved.map((x) => (typeof x === 'string' ? { sql: x } : x)).filter((x) => x.sql.trim() || x.savedId)
        .map((x, i) => ({ id: `q${i}-${Date.now()}`, kind: 'query' as const, title: x.title ?? `Query ${i + 1}`, sql: x.sql, dbKey: mainKey,
          savedId: x.savedId, savedSql: x.savedSql, modified: x.savedId !== undefined && x.sql !== x.savedSql }))
    } catch { return [] }
  })
  const [activeTab, setActiveTab] = useState<string | null>(tabs[0]?.id ?? null)
  const lastTabByDb = useRef(new Map<string, string>())
  const [picker, setPicker] = useState<'all' | 'tables' | 'databases' | null>(null)
  // Panels start as Settings → Panels says (open, closed, or as last left).
  const [inspector, setInspector] = useState(() => panelOpen(getSettings(), 'inspector'))
  const toggleInspector = useCallback(() => setInspector((v) => { rememberPanel('inspector', !v); return !v }), [])
  const [transfer, setTransfer] = useState<{ tab: 'export' | 'import'; db: OpenDb; preselect?: string[] } | null>(null)
  // Sidebar multi-selection: names within one database (selDb).
  const [selected, setSelected] = useState<Set<string>>(new Set())
  const [selDb, setSelDb] = useState<string | null>(null)
  const [anchor, setAnchor] = useState<string | null>(null)
  const [deleting, setDeleting] = useState<{ db: OpenDb; objects: DbObject[] } | null>(null)
  const [sidebarW, sidebarDrag] = useResizable(240, 170, 480, 'x', false, 'gotable:sidebar')
  const [sideTab, setSideTab] = useState<'items' | 'queries' | 'history'>(() => (localStorage.getItem('gotable:side-tab') as 'items') ?? 'items')
  useEffect(() => localStorage.setItem('gotable:side-tab', sideTab), [sideTab])
  // Collapsible panels (remembered): the database rail and the table sidebar.
  const [railHidden, setRailHidden] = useState(() => !panelOpen(getSettings(), 'rail'))
  const [sidebarHidden, setSidebarHidden] = useState(() => !panelOpen(getSettings(), 'sidebar'))
  const toggleRail = useCallback(() => setRailHidden((v) => { rememberPanel('rail', v); return !v }), [])
  const toggleSidebar = useCallback(() => setSidebarHidden((v) => { rememberPanel('sidebar', v); return !v }), [])
  const [naming, setNaming] = useState<{ tabId: string; sql: string } | null>(null)
  const [creatingDb, setCreatingDb] = useState(false)
  const queryCounter = useRef(tabs.length)
  const filterRef = useRef<HTMLInputElement>(null)
  const asideRef = useRef<HTMLElement>(null)
  const navRef = useRef<HTMLElement>(null)
  // The sidebar's refresh icon spins for keyboard refreshes too.
  const [sidebarBusy, setSidebarBusy] = useState(false)

  const label = useCallback((d: Pick<OpenDb, 'info' | 'schema'>) => (pg ? `${d.info.database} › ${d.schema}` : d.schema || d.info.database), [pg])
  const patchDb = useCallback((key: string, patch: Partial<OpenDb>) => setDbs((ds) => ds.map((d) => (d.key === key ? { ...d, ...patch } : d))), [])

  const loadObjects = useCallback(async (key: string) => {
    const d = dbsRef.current.find((x) => x.key === key)
    if (!d) return
    try {
      const objects = await api.objects(d.info.id, d.schema)
      patchDb(key, { objects })
      api.completions(d.info.id, d.schema).then((r) => patchDb(key, { completions: r.tables })).catch(() => {})
    } catch (e) {
      patchDb(key, { objects: [] })
      toast('error', ...errorParts(e))
    }
  }, [patchDb, toast])

  useEffect(() => { loadObjects(mainKey) }, []) // eslint-disable-line react-hooks/exhaustive-deps

  // ---------------------------------------------------------------- tabs

  const queryText = useRef(new Map<string, string>())
  const persistQueries = useCallback((ts: Tab[]) => {
    const list: PersistedQuery[] = ts.filter((t) => t.kind === 'query')
      .map((t) => ({ sql: queryText.current.get(t.id) ?? t.sql ?? '', title: t.title, savedId: t.savedId, savedSql: t.savedSql }))
    localStorage.setItem(queriesKey(conn.id), JSON.stringify(list))
  }, [conn.id])
  useEffect(() => persistQueries(tabs), [tabs, persistQueries])

  const selectTab = useCallback((t: Tab | undefined) => {
    setActiveTab(t?.id ?? null)
    if (t) {
      lastTabByDb.current.set(t.dbKey, t.id)
      setActiveKey(t.dbKey)
    }
  }, [])

  const openTable = useCallback((db: OpenDb, o: DbObject, focusFilter?: Tab['focusFilter']) => {
    // Tables of another schema than the database's get their own id.
    const id = `t:${db.key}:${o.schema && o.schema !== db.schema ? `${o.schema}.` : ''}${o.name}`
    const tab: Tab = { id, kind: 'table', title: redis ? redisLabel(o.name) : o.name, object: o, dbKey: db.key, focusFilter }
    setTabs((ts) => (ts.some((t) => t.id === id) ? (focusFilter ? ts.map((t) => (t.id === id ? { ...t, focusFilter } : t)) : ts) : [...ts, tab]))
    selectTab(tab)
  }, [selectTab, redis])

  // openReference follows a foreign key: the referenced table, filtered to
  // the row, in the open database of that schema (or the current one).
  const openReference = (from: OpenDb, ref: ForeignRef, values: Cell[]) => {
    const db = dbs.find((d) => d.info.id === from.info.id && d.schema === ref.schema) ?? from
    const o = db.objects?.find((x) => x.schema === ref.schema && x.name === ref.table) ?? { schema: ref.schema, name: ref.table, kind: 'table' as const }
    openTable(db, o, { pairs: ref.columns.map((column, i) => ({ column, value: values[i] })), key: Date.now() })
  }

  // newTable opens a "New table" tab for designing a table in db.
  const newTable = useCallback((db: OpenDb) => {
    if (mongo) { setCreatingCollection(db); return } // nothing to design: fields come with the documents
    if (isCassandra(conn.driver)) {
      newQuery(`-- A Cassandra table's primary key (partition key, then clustering columns) is fixed when it is made.
CREATE TABLE ${quote('events', conn.driver)} (
  user_id uuid,
  at timestamp,
  kind text,
  payload map<text, text>,
  PRIMARY KEY ((user_id), at)
) WITH CLUSTERING ORDER BY (at DESC);
`, db.key)
      return
    }
    if (redis) { newQuery('# A key is made by the command that writes it:\nSET greeting "hello"\n# HSET user:1 name Alice  ·  RPUSH queue a b  ·  SADD tags x  ·  ZADD scores 10 alice\n', db.key); return }
    queryCounter.current += 1
    const tab: Tab = { id: `new:${db.key}:${queryCounter.current}`, kind: 'create-table', title: 'New table', dbKey: db.key }
    setTabs((ts) => [...ts, tab])
    selectTab(tab)
  }, [selectTab])

  const newQuery = useCallback((sql = '', dbKey?: string) => {
    queryCounter.current += 1
    const tab: Tab = { id: `q${queryCounter.current}-${Date.now()}`, kind: 'query', title: `Query ${queryCounter.current}`, sql, dbKey: dbKey ?? activeKey }
    setTabs((ts) => [...ts, tab])
    selectTab(tab)
  }, [activeKey, selectTab])

  const closeTabs = useCallback((ids: string[]) => {
    setTabs((ts) => {
      const next = ts.filter((t) => !ids.includes(t.id))
      setActiveTab((a) => {
        if (!a || !ids.includes(a)) return a
        const i = ts.findIndex((t) => t.id === a)
        return (next[i] ?? next[i - 1])?.id ?? null
      })
      return next
    })
  }, [])

  const closeTab = useCallback(async (id: string) => {
    const t = tabs.find((x) => x.id === id)
    if (t?.dirty) {
      const r = await confirm(t.kind === 'create-table'
        ? { title: 'Discard the new table?', message: 'The table hasn’t been created yet. Closing the tab discards its design.', confirmLabel: 'Discard & close', danger: true }
        : { title: `Close ${t.title}?`, message: 'It has changes that aren’t committed yet. Closing the tab discards them.', confirmLabel: 'Discard & close', danger: true })
      if (r !== 'confirm') return
    }
    closeTabs([id])
  }, [tabs, confirm, closeTabs])

  const updateTab = useCallback((id: string, patch: Partial<Tab>) => setTabs((ts) => {
    const t = ts.find((x) => x.id === id)
    if (!t || (Object.keys(patch) as (keyof Tab)[]).every((k) => t[k] === patch[k])) return ts
    return ts.map((x) => (x.id === id ? { ...x, ...patch } : x))
  }), [])

  // The active tab follows its database: picking a database shows its last
  // tab, or its empty state — never a table of another database.
  const activateDb = useCallback((key: string) => {
    setActiveKey(key)
    setActiveTab((cur) => {
      const curTab = tabs.find((t) => t.id === cur)
      if (curTab?.dbKey === key) return cur
      const last = lastTabByDb.current.get(key)
      return last && tabs.some((t) => t.id === last) ? last : null
    })
  }, [tabs])

  // ---------------------------------------------------------------- databases

  // openDb shows a database in the sidebar, or focuses it if it's open.
  // schema: a schema of `info`'s session (default: the active database's).
  // database: another PostgreSQL database (reuses an open one, else forks).
  const openDb = useCallback(async (opts: { schema?: string; database?: string; info?: SessionInfo }) => {
    const ds = dbsRef.current
    let info = opts.info ?? activeDb.info
    let schema = opts.schema ?? ''
    if (pg && opts.database && opts.database !== info.database) {
      const existing = ds.find((d) => d.info.database === opts.database)
      if (existing) { activateDb(existing.key); return }
      if (opts.database === session.database) {
        info = session
        schema = session.schema
      } else {
        try {
          info = await api.fork(session.id, opts.database)
        } catch (e) { toast('error', ...errorParts(e)); return }
        schema = info.schema
      }
    } else if (pg && opts.database && !opts.schema) {
      activateDb(activeDb.key) // the database that is already active
      return
    }
    const key = dbKeyOf(info.id, schema)
    if (ds.some((d) => d.key === key)) { activateDb(key); return }
    const db: OpenDb = { key, info, schema, objects: null, completions: {}, collapsed: false }
    dbsRef.current = [...dbsRef.current, db]
    setDbs((xs) => [...xs, db])
    activateDb(key)
    loadObjects(key)
  }, [activeDb.info, activeDb.key, pg, session, toast, activateDb, loadObjects])

  const closeDb = useCallback(async (key: string) => {
    const db = dbs.find((d) => d.key === key)
    if (!db || dbs.length === 1) return
    const its = tabs.filter((t) => t.dbKey === key)
    const dirty = its.filter((t) => t.dirty)
    if (dirty.length) {
      const r = await confirm({ title: `Close ${label(db)}?`, message: `${dirty.map((t) => t.title).join(', ')} ${dirty.length === 1 ? 'has' : 'have'} changes that aren’t committed. Closing the database discards them.`, confirmLabel: 'Discard & close', danger: true })
      if (r !== 'confirm') return
    }
    closeTabs(its.map((t) => t.id))
    const rest = dbs.filter((d) => d.key !== key)
    setDbs(rest)
    if (selDb === key) { setSelected(new Set()); setSelDb(null) }
    if (activeKey === key) activateDb(rest[Math.max(0, dbs.findIndex((d) => d.key === key) - 1)].key)
    // A forked session no other open database uses is closed.
    if (db.info.id !== session.id && !rest.some((d) => d.info.id === db.info.id)) api.closeSession(db.info.id).catch(() => {})
  }, [dbs, tabs, confirm, label, closeTabs, selDb, activeKey, activateDb, session.id])

  // ---------------------------------------------------------------- deleting databases

  const copy = (text: string) => {
    navigator.clipboard?.writeText(text).then(() => toast('ok', 'Copied', text), () => toast('error', 'Could not copy', text))
  }
  /** dropMatches: the open entries a deleted database or schema takes with it. */
  const dropMatches = (t: DropTarget, d: OpenDb) => (t.kind === 'database' && pg ? d.info.database === t.name : d.info.id === t.sid && d.schema === t.name)
  /** listedTarget / serverTarget: an open entry's schema (or database), and its PostgreSQL / SQL Server database. */
  const listedTarget = (d: OpenDb): DropTarget | null => {
    const kind = dropKind(conn.driver, 'listed')
    return kind && d.schema ? { name: d.schema, kind, sid: d.info.id, where: pg ? `in ${d.info.database}` : undefined } : null
  }
  const serverTarget = (name: string): DropTarget | null => (dropKind(conn.driver, 'server') ? { name, kind: 'database', sid: session.id } : null)

  /**
   * beforeDrop: what uses the database goes first. Tabs with uncommitted
   * edits ask. A whole database's open entries close now, as the server
   * won't drop a database GoTable is connected to; a schema's after.
   */
  const beforeDrop = async (t: DropTarget) => {
    const hit = dbsRef.current.filter((d) => dropMatches(t, d))
    const its = tabs.filter((tab) => hit.some((d) => d.key === tab.dbKey))
    const dirty = its.filter((tab) => tab.dirty)
    if (dirty.length) {
      const r = await confirm({ title: `Delete ${t.name}?`, message: `${dirty.map((x) => x.title).join(', ')} ${dirty.length === 1 ? 'has' : 'have'} changes that aren’t committed. They go with it.`, confirmLabel: 'Discard & delete', danger: true })
      if (r !== 'confirm') return false
    }
    closeTabs(its.map((x) => x.id))
    // Never leave the rail empty: open something else first.
    if (hit.length && hit.length === dbsRef.current.length) {
      const other = t.kind === 'database' && pg ? null : schemaList.find((x) => x !== t.name)
      if (other) await openDb({ schema: other })
    }
    if (t.kind === 'database' && pg) forgetDbs(hit.map((d) => d.key))
    return true
  }
  const afterDrop = (t: DropTarget) => {
    setDropping(null)
    forgetDbs(dbsRef.current.filter((d) => dropMatches(t, d)).map((d) => d.key))
    api.schemas(activeDb.info.id).then((r) => setSchemaList(r.schemas)).catch(() => {})
    if (pg) api.databases(session.id).then((r) => setServerDbs(r.databases)).catch(() => {})
  }
  /** forgetDbs takes entries off the rail (their tabs are closed already), closing forks nothing else uses. */
  const forgetDbs = (keys: string[]) => {
    const all = dbsRef.current
    const rest = all.filter((d) => !keys.includes(d.key))
    if (!keys.length || !rest.length) return
    dbsRef.current = rest
    setDbs(rest)
    if (selDb && keys.includes(selDb)) { setSelected(new Set()); setSelDb(null) }
    if (keys.includes(activeKey)) activateDb(rest[0].key)
    for (const d of all.filter((x) => keys.includes(x.key))) {
      if (d.info.id !== session.id && !rest.some((x) => x.info.id === d.info.id)) api.closeSession(d.info.id).catch(() => {})
    }
  }
  /** deleteChoices: every database and schema the picker lists that can go. */
  const deleteChoices = (): DropTarget[] => {
    const kind = dropKind(conn.driver, 'listed')
    const listed = kind ? schemaList.map((name): DropTarget => ({ name, kind, sid: activeDb.info.id, where: pg ? `in ${activeDb.info.database}` : undefined })) : []
    const server = pg && dropKind(conn.driver, 'server') ? (serverDbs ?? []).filter((n) => n !== session.database).map((n): DropTarget => ({ name: n, kind: 'database', sid: session.id })) : []
    return [...listed, ...server]
  }
  const canDrop = dropKind(conn.driver, 'listed') !== null

  // Remember which databases are open, and restore them next time. The
  // saved list is read once, before the effect below overwrites it.
  const [savedOpen] = useState<{ database: string; schema: string; active?: boolean }[]>(() => {
    try { return JSON.parse(localStorage.getItem(openDbsKey(conn.id)) ?? '[]') } catch { return [] }
  })
  useEffect(() => {
    const list = dbs.map((d) => ({ database: d.info.database, schema: d.schema, active: d.key === activeKey }))
    localStorage.setItem(openDbsKey(conn.id), JSON.stringify(list))
  }, [dbs, activeKey, conn.id])
  const restored = useRef(false)
  useEffect(() => {
    if (restored.current) return
    restored.current = true
    const saved = savedOpen
    ;(async () => {
      for (const s of saved) {
        if (s.database === session.database && s.schema === mainSchema) continue
        if (s.database === session.database && !session.schemas.includes(s.schema)) continue
        if (s.database === session.database) await openDb({ schema: s.schema, info: session }).catch(() => {})
        else await openDb({ database: s.database }).catch(() => {})
      }
      const act = saved.find((s) => s.active)
      if (act) {
        const d = dbsRef.current.find((x) => x.info.database === act.database && x.schema === act.schema)
        if (d) activateDb(d.key)
      }
    })()
  }, []) // eslint-disable-line react-hooks/exhaustive-deps

  // Opening the database picker refreshes the lists (schemas can be created
  // or attached at any time); PostgreSQL also lists the server's databases.
  const openDatabasePicker = useCallback(() => {
    setPicker('databases')
    api.schemas(activeDb.info.id).then((r) => setSchemaList(r.schemas)).catch(() => {})
    if (pg) api.databases(session.id).then((r) => setServerDbs(r.databases)).catch(() => setServerDbs([]))
  }, [activeDb.info.id, pg, session.id])

  // Disconnect also closes forked sessions.
  const disconnect = () => {
    for (const id of new Set(dbs.map((d) => d.info.id))) if (id !== session.id) api.closeSession(id).catch(() => {})
    onDisconnect()
  }

  const refresh = useCallback(() => {
    window.dispatchEvent(new CustomEvent('gotable:refresh', { detail: activeTab }))
    return loadObjects(activeKey)
  }, [activeKey, activeTab, loadObjects])

  // ---------------------------------------------------------------- keyboard

  useEffect(() => {
    if (!active) return
    const onKey = (e: KeyboardEvent) => {
      // Alt+1…9 switch between the open databases in the rail.
      if (e.altKey && !e.ctrlKey && !e.metaKey && /^Digit[1-9]$/.test(e.code)) {
        const d = dbsRef.current[Number(e.code.slice(5)) - 1]
        if (d) { e.preventDefault(); activateDb(d.key) }
        return
      }
      const mod = e.ctrlKey || e.metaKey
      // Ctrl+Tab / Ctrl+Shift+Tab (or Ctrl+PageDown / Ctrl+PageUp) walk
      // through the open tabs in tab-bar order, wrapping around.
      // WebKitGTK reports Shift+Tab as key "Unidentified" (GTK's ISO_Left_Tab);
      // the code is still "Tab".
      const tab = e.code === 'Tab' || e.key === 'Tab'
      if (mod && e.ctrlKey && (tab || e.key === 'PageDown' || e.key === 'PageUp')) {
        e.preventDefault()
        if (tabs.length < 2) return
        const back = e.key === 'PageUp' || (tab && e.shiftKey)
        const i = tabs.findIndex((t) => t.id === activeTab)
        selectTab(tabs[(i + (back ? -1 : 1) + tabs.length) % tabs.length])
        return
      }
      // Ctrl+1…9 go to a tab; everything else is a shortcut the settings can change.
      if (mod && /^[1-9]$/.test(e.key) && !e.shiftKey && !e.altKey) { const t = tabs[Number(e.key) - 1]; if (t) { e.preventDefault(); selectTab(t) } return }
      const act = (f: () => void) => { e.preventDefault(); f() }
      if (hit(e, 'ws.palette')) act(() => setPicker('all'))
      else if (hit(e, 'ws.tables')) act(() => setPicker('tables'))
      else if (hit(e, 'ws.databases')) act(openDatabasePicker)
      else if (hit(e, 'ws.newQuery')) act(() => newQuery())
      else if (hit(e, 'ws.rail')) act(toggleRail)
      else if (hit(e, 'ws.sidebar')) act(toggleSidebar)
      else if (hit(e, 'ws.closeTab')) act(() => { if (activeTab) closeTab(activeTab) })
      else if (hit(e, 'ws.filterTables')) act(() => filterRef.current?.focus())
      else if (hit(e, 'ws.inspector')) act(toggleInspector)
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [active, activeTab, closeTab, newQuery, tabs, openDatabasePicker, selectTab, activateDb, toggleRail, toggleSidebar])

  // Ctrl+R / F5 refresh what has focus: in the table sidebar just the table
  // list, elsewhere the open tab (and the list).
  useEffect(() => {
    if (!active) return
    const onRefresh = () => {
      if (asideRef.current?.contains(document.activeElement)) {
        setSidebarBusy(true)
        loadObjects(activeKey).finally(() => setSidebarBusy(false))
      } else refresh()
    }
    window.addEventListener('gotable:refresh-shortcut', onRefresh)
    return () => window.removeEventListener('gotable:refresh-shortcut', onRefresh)
  }, [active, refresh, loadObjects, activeKey])

  // ---------------------------------------------------------------- saved queries & history

  const dbLabelOf = useCallback((x: { database: string; schema: string }) => (pg ? `${x.database} › ${x.schema}` : x.schema || x.database), [pg])
  const findDbKey = (database: string, schema: string) => dbs.find((d) => d.info.database === database && d.schema === schema)?.key ?? activeKey

  // saveQueryTab updates the saved query of a tab, or asks for a name first.
  const saveQueryTab = useCallback(async (tabId: string, sql: string, name?: string) => {
    const t = tabs.find((x) => x.id === tabId)
    if (!t) return
    if (!t.savedId && !name) { setNaming({ tabId, sql }); return }
    const db = dbs.find((d) => d.key === t.dbKey) ?? activeDb
    try {
      const saved = await api.saveQuery({ id: t.savedId ?? crypto.randomUUID(), connectionId: conn.id, database: db.info.database,
        schema: db.schema, name: name ?? t.title, sql })
      updateTab(tabId, { title: saved.name, savedId: saved.id, savedSql: saved.sql, sql: saved.sql, modified: false })
      notify(QUERIES_EVENT)
      toast('ok', t.savedId ? 'Query updated' : 'Query saved', saved.name)
    } catch (e) { toast('error', ...errorParts(e)) }
  }, [tabs, dbs, activeDb, conn.id, updateTab, toast])

  const openSavedQuery = (q: SavedQuery) => {
    const existing = tabs.find((t) => t.savedId === q.id)
    if (existing) { selectTab(existing); return }
    queryCounter.current += 1
    const tab: Tab = { id: `q${queryCounter.current}-${Date.now()}`, kind: 'query', title: q.name, sql: q.sql, savedId: q.id, savedSql: q.sql, dbKey: findDbKey(q.database, q.schema) }
    setTabs((ts) => [...ts, tab])
    selectTab(tab)
  }

  const openHistory = (h: HistoryEntry, run: boolean) => {
    queryCounter.current += 1
    const tab: Tab = { id: `q${queryCounter.current}-${Date.now()}`, kind: 'query', title: `Query ${queryCounter.current}`, sql: h.sql, autoRun: run, dbKey: findDbKey(h.database, h.schema) }
    setTabs((ts) => [...ts, tab])
    selectTab(tab)
  }

  // ---------------------------------------------------------------- sidebar

  const q = filter.trim().toLowerCase()
  const shownOf = useCallback((d: OpenDb) => {
    const objs = (d.objects ?? []).filter((o) => !q || o.name.toLowerCase().includes(q))
    return [...objs.filter((o) => o.kind === 'table'), ...objs.filter((o) => o.kind === 'view')]
  }, [q])
  const selDbObj = dbs.find((d) => d.key === selDb)
  const selectedObjects = selDbObj ? shownOf(selDbObj).filter((o) => selected.has(o.name)) : []

  /** onItemClick: plain click opens; Ctrl/⌘ toggles; Shift selects a range (within one database). */
  const onItemClick = (e: React.MouseEvent, db: OpenDb, o: DbObject) => {
    const sameDb = selDb === db.key
    if (e.ctrlKey || e.metaKey) {
      setSelected((sel) => { const n = new Set(sameDb ? sel : []); if (n.has(o.name)) n.delete(o.name); else n.add(o.name); return n })
      setSelDb(db.key)
      setAnchor(o.name)
      return
    }
    if (e.shiftKey && anchor && sameDb) {
      const list = shownOf(db)
      const a = list.findIndex((x) => x.name === anchor)
      const b = list.findIndex((x) => x.name === o.name)
      if (a >= 0 && b >= 0) { setSelected(new Set(list.slice(Math.min(a, b), Math.max(a, b) + 1).map((x) => x.name))); return }
    }
    setSelected(new Set([o.name]))
    setSelDb(db.key)
    setAnchor(o.name)
    openTable(db, o)
  }

  // moveInSidebar focuses (and selects) the table `step` items away from the
  // focused one, in the order shown; Shift extends the selection.
  const moveInSidebar = (step: number | 'first' | 'last', extend = false) => {
    const items = [...(navRef.current?.querySelectorAll<HTMLButtonElement>('[data-item]') ?? [])]
    if (!items.length) return
    const cur = items.findIndex((el) => el === document.activeElement)
    const fallback = items.findIndex((el) => el.dataset.active === 'true')
    const from = cur >= 0 ? cur : fallback
    const i = step === 'first' ? 0 : step === 'last' ? items.length - 1
      : from < 0 ? (step > 0 ? 0 : items.length - 1) : Math.max(0, Math.min(items.length - 1, from + step))
    const el = items[i]
    el.focus()
    el.scrollIntoView({ block: 'nearest' })
    const name = el.dataset.item!
    const list = shownOf(activeDb)
    setSelDb(activeDb.key)
    if (extend && anchor && selDb === activeDb.key) {
      const a = list.findIndex((x) => x.name === anchor)
      const b = list.findIndex((x) => x.name === name)
      if (a >= 0 && b >= 0) { setSelected(new Set(list.slice(Math.min(a, b), Math.max(a, b) + 1).map((x) => x.name))); return }
    }
    setSelected(new Set([name]))
    setAnchor(name)
  }

  const onSidebarKey = (e: React.KeyboardEvent) => {
    const mod = e.ctrlKey || e.metaKey
    if (!mod && !e.altKey && (e.key === 'ArrowDown' || e.key === 'ArrowUp' || e.key === 'Home' || e.key === 'End')) {
      e.preventDefault() // walk the tables instead of scrolling
      moveInSidebar(e.key === 'ArrowDown' ? 1 : e.key === 'ArrowUp' ? -1 : e.key === 'Home' ? 'first' : 'last', e.shiftKey)
      return
    }
    if (mod && e.key.toLowerCase() === 'a') {
      // Select everything in the database being worked in.
      e.preventDefault()
      const db = activeDb
      setSelDb(db.key)
      setSelected(new Set(shownOf(db).map((o) => o.name)))
    } else if (e.key === 'Escape' && selected.size) {
      e.preventDefault()
      setSelected(new Set())
    } else if ((e.key === 'Delete' || e.key === 'Backspace') && selectedObjects.length && selDbObj) {
      e.preventDefault()
      setDeleting({ db: selDbObj, objects: selectedObjects })
    } else if (e.key === 'Enter' && selectedObjects.length === 1 && selDbObj) {
      openTable(selDbObj, selectedObjects[0])
    }
  }

  const afterDelete = (db: OpenDb, names: string[]) => {
    setDeleting(null)
    setSelected(new Set())
    closeTabs(names.map((n) => `t:${db.key}:${n}`))
    loadObjects(db.key)
  }

  const objectMenu = (e: React.MouseEvent, db: OpenDb, o: DbObject) => {
    if (selDb === db.key && selected.has(o.name) && selectedObjects.length > 1) {
      const n = selectedObjects.length
      menu.open(e, [
        { label: `Export ${n} tables…`, icon: <Download />, onSelect: () => setTransfer({ tab: 'export', db, preselect: selectedObjects.map((x) => x.name) }) },
        'sep',
        { label: `Delete ${n} tables…`, icon: <Trash2 />, kbd: 'Del', danger: true, onSelect: () => setDeleting({ db, objects: selectedObjects }) },
      ])
      return
    }
    setSelected(new Set([o.name]))
    setSelDb(db.key)
    setAnchor(o.name)
    menu.open(e, [
      { label: 'Open', icon: <Table2 />, onSelect: () => openTable(db, o) },
      { label: mongo ? 'New query: find…' : redis ? 'New query' : 'New query: SELECT…', icon: <Code2 />, onSelect: () => newQuery(redis ? (isPattern(o.name) ? `SCAN 0 MATCH ${quoteArg(o.name)} COUNT 1000` : `TYPE ${quoteArg(unescapeKey(o.name))}`) : mongo ? `${mongoColl(o.name)}.find({}).limit(100)` : `SELECT *\nFROM ${quote(o.name, conn.driver)}\nLIMIT 100;`, db.key) },
      { label: 'Copy name', icon: <Copy />, onSelect: () => navigator.clipboard?.writeText(redis && !isPattern(o.name) ? unescapeKey(o.name) : o.name) },
      'sep',
      { label: 'Export…', icon: <Download />, onSelect: () => setTransfer({ tab: 'export', db, preselect: [o.name] }) },
      'sep',
      { label: o.kind === 'view' ? 'Delete view…' : `Delete ${tableWord}…`, icon: <Trash2 />, kbd: 'Del', danger: true, onSelect: () => setDeleting({ db, objects: [o] }) },
    ])
  }

  const dbMenu = (e: React.MouseEvent, db: OpenDb) => menu.open(e, [
    { label: 'New query here', icon: <FileCode2 />, onSelect: () => newQuery('', db.key) },
    { label: `New ${tableWord}…`, icon: <Plus />, onSelect: () => newTable(db) },
    { label: 'Refresh', icon: <RefreshCw />, onSelect: () => loadObjects(db.key) },
    { label: 'Export database…', icon: <Download />, onSelect: () => setTransfer({ tab: 'export', db }) },
    { label: mongo || redis ? 'Import script…' : 'Import SQL file…', icon: <Upload />, onSelect: () => setTransfer({ tab: 'import', db }) },
    'sep',
    ...(pg
      ? [{ label: 'Copy database name', icon: <Copy />, onSelect: () => copy(db.info.database) }, ...(db.schema ? [{ label: 'Copy schema name', icon: <Copy />, onSelect: () => copy(db.schema) }] : [])]
      : [{ label: 'Copy name', icon: <Copy />, onSelect: () => copy(db.schema || db.info.database) }]),
    ...(dbs.length > 1 ? [{ label: 'Close database', icon: <X />, onSelect: () => closeDb(db.key) }] : []),
    ...(() => {
      const listed = listedTarget(db)
      const server = pg ? serverTarget(db.info.database) : null
      const items = []
      if (listed) items.push({ label: `Delete ${listed.kind} ${listed.name}…`, icon: <Trash2 />, danger: true, onSelect: () => setDropping({ target: listed }) })
      // The database the workspace is connected to can't be dropped from it.
      if (server) items.push({ label: db.info.database === session.database ? `Delete database ${server.name} (connected)` : `Delete database ${server.name}…`, icon: <Trash2 />, danger: true,
        disabled: db.info.database === session.database, onSelect: () => setDropping({ target: server }) })
      return items.length ? ['sep' as const, ...items] : []
    })(),
  ])

  // ---------------------------------------------------------------- pickers

  const objectCommands = (d: OpenDb, withDb: boolean): Command[] => (d.objects ?? []).map((o) => ({
    id: `open:${d.key}:${o.name}`, label: redis ? redisLabel(o.name) : o.name, icon: o.kind === 'view' ? <Eye /> : <Table2 />,
    hint: withDb && dbs.length > 1 ? label(d) : o.kind === 'view' ? 'View' : 'Table',
    active: activeTab === `t:${d.key}:${o.name}`, run: () => openTable(d, o),
  }))

  const commands: Command[] = useMemo(() => [
    ...objectCommands(activeDb, true),
    ...dbs.filter((d) => d.key !== activeDb.key).flatMap((d) => objectCommands(d, true)),
    { id: 'new-query', label: 'New query', hint: keyHint('ws.newQuery'), icon: <Code2 />, run: () => newQuery() },
    { id: 'new-table', label: `New ${tableWord}…`, icon: <Plus />, run: () => newTable(activeDb) },
    ...(!features(conn.driver).createDatabase ? [] : [{ id: 'new-database', label: pg ? 'New database or schema…' : family(conn.driver) === 'sqlite' ? 'New database file…' : fileDb ? 'New schema…' : 'New database…', icon: <Database />, run: () => setCreatingDb(true) }]),
    { id: 'saved-queries', label: 'Saved queries', icon: <FileCode2 />, run: () => { setSidebarHidden(false); setSideTab('queries') } },
    { id: 'history', label: 'Query history', icon: <History />, run: () => { setSidebarHidden(false); setSideTab('history') } },
    { id: 'toggle-sidebar', label: sidebarHidden ? 'Show sidebar' : 'Hide sidebar', hint: keyHint('ws.sidebar'), icon: <PanelLeft />, run: toggleSidebar },
    { id: 'toggle-rail', label: railHidden ? 'Show databases' : 'Hide databases', hint: keyHint('ws.rail'), icon: <Columns2 />, run: toggleRail },
    { id: 'go-table', label: 'Go to table…', hint: keyHint('ws.tables'), icon: <Table2 />, run: () => setPicker('tables') },
    { id: 'switch-db', label: 'Open database…', hint: keyHint('ws.databases'), icon: <Database />, run: openDatabasePicker },
    ...(canDrop ? [{ id: 'delete-db', label: pg ? 'Delete a database or schema…' : fileDb ? 'Delete a schema…' : 'Delete a database…', icon: <Trash2 />, run: async () => {
      // Fresh lists, as in the database picker.
      const schemas = await api.schemas(activeDb.info.id).then((r) => r.schemas).catch(() => schemaList)
      const servers = pg ? await api.databases(session.id).then((r) => r.databases).catch(() => serverDbs ?? []) : []
      setSchemaList(schemas)
      if (pg) setServerDbs(servers)
      const kind = dropKind(conn.driver, 'listed')!
      setDropping({ choices: [
        ...schemas.map((name): DropTarget => ({ name, kind, sid: activeDb.info.id, where: pg ? `in ${activeDb.info.database}` : undefined })),
        ...(pg && dropKind(conn.driver, 'server') ? servers.filter((n) => n !== session.database).map((n): DropTarget => ({ name: n, kind: 'database', sid: session.id })) : []),
      ] })
    } }] : []),
    { id: 'export-db', label: 'Export database…', icon: <Download />, run: () => setTransfer({ tab: 'export', db: activeDb }) },
    { id: 'import-sql', label: mongo || redis ? 'Import script…' : 'Import SQL file…', icon: <Upload />, run: () => setTransfer({ tab: 'import', db: activeDb }) },
    { id: 'refresh', label: 'Refresh tables', hint: keyHint('app.refresh'), icon: <RefreshCw />, run: refresh },
    { id: 'theme', label: theme === 'dark' ? 'Switch to light mode' : 'Switch to dark mode', hint: keyHint('app.theme'), icon: theme === 'dark' ? <Sun /> : <Moon />, run: toggleTheme },
    { id: 'settings', label: 'Settings…', hint: keyHint('app.settings'), icon: <Settings />, run: onOpenSettings },
    { id: 'inspector', label: inspector ? 'Hide row inspector' : 'Show row inspector', hint: keyHint('ws.inspector'), icon: <PanelRight />, run: toggleInspector },
    ...sessions.filter((s) => s.id !== session.id).map((s) => ({ id: 'conn:' + s.id, label: `Go to ${s.connection.name}`, icon: <Database />, run: () => onSwitch(s.id) })),
    { id: 'home', label: 'Connections', icon: <HomeIcon />, run: onHome },
    { id: 'disconnect', label: 'Disconnect', icon: <LogOut />, run: disconnect },
  ], [dbs, activeDb, activeTab, theme, inspector, sessions, session.id, refresh, sidebarHidden, railHidden, settings.shortcuts]) // eslint-disable-line react-hooks/exhaustive-deps

  const isOpen = (database: string, schema: string) => dbs.some((d) => d.info.database === database && d.schema === schema)
  const schemaGroup = pg ? `Schemas in ${activeDb.info.database}` : family(conn.driver) === 'sqlite' ? 'Attached databases' : fileDb ? 'Schemas' : 'Databases'
  const databaseCommands: Command[] = [
    ...schemaList.map((s) => ({ id: 'schema:' + s, label: s, group: schemaGroup, icon: <Database />,
      active: s === activeDb.schema, hint: s !== activeDb.schema && isOpen(activeDb.info.database, s) ? 'open' : pg ? 'schema' : undefined,
      run: () => openDb({ schema: s }) })),
    ...(pg ? (serverDbs ?? []).map((name) => ({ id: 'db:' + name, label: name, group: 'Databases on this server', icon: <Server />,
      active: name === activeDb.info.database, hint: name !== activeDb.info.database && dbs.some((d) => d.info.database === name) ? 'open' : undefined,
      run: () => openDb({ database: name }) })) : []),
    ...(!features(conn.driver).createDatabase ? [] : [{ id: 'create-database', label: pg ? 'Create database or schema…' : family(conn.driver) === 'sqlite' ? 'Create database file…' : fileDb ? 'Create schema…' : 'Create database…',
      group: 'Actions', icon: <Plus />, run: () => setCreatingDb(true) }]),
    ...(canDrop ? [{ id: 'delete-database', label: pg ? 'Delete a database or schema…' : fileDb ? 'Delete a schema…' : 'Delete a database…', group: 'Actions', icon: <Trash2 />,
      run: () => setDropping({ choices: deleteChoices() }) }] : []),
  ]

  const tag = tagInfo(conn.tag)
  const activeTabObj = tabs.find((t) => t.id === activeTab)

  return (
    <div className={clsx('flex h-full flex-col', !active && 'hidden')}>
      {/* Title bar */}
      <header onMouseDown={ownTitleBar ? dragWindow : undefined} className="relative flex h-11 shrink-0 select-none items-center gap-2 border-b border-line bg-panel px-2"
        style={conn.tag ? { boxShadow: `inset 0 3px 0 ${tag.color}` } : undefined}>
        <IconButton label="Connections" onClick={onHome}><HomeIcon className="size-4" /></IconButton>
        <IconButton label={withKeys(railHidden ? 'Show databases' : 'Hide databases', 'ws.rail')} active={!railHidden} onClick={toggleRail}><Columns2 className="size-4" /></IconButton>
        <IconButton label={withKeys(sidebarHidden ? 'Show sidebar' : 'Hide sidebar', 'ws.sidebar')} active={!sidebarHidden} onClick={toggleSidebar}><PanelLeft className="size-4" /></IconButton>
        <button
          className="flex h-8 items-center gap-2 rounded-lg px-2 hover:bg-hover"
          onClick={(e) => menu.open({ clientX: e.currentTarget.getBoundingClientRect().left, clientY: e.currentTarget.getBoundingClientRect().bottom + 4 }, [
            ...sessions.map((s) => ({ label: s.connection.name, icon: <Database />, onSelect: () => onSwitch(s.id) })),
            'sep', { label: 'All connections…', icon: <HomeIcon />, onSelect: onHome },
          ])}
        >
          <DriverBadge driver={conn.driver} size={24} />
          <span className="text-[13px] font-semibold">{conn.name}</span>
          <TagPill tag={conn.tag} />
          <ChevronDown className="size-3.5 text-muted" />
        </button>
        {/* The server version; its address and the database show on hover. */}
        <span data-testid="server-info" className="hidden min-w-0 max-w-[38%] items-center gap-1.5 text-[12px] text-muted lg:flex"
          title={`${activeDb.info.version}\n${subtitle(pg ? { ...conn, database: activeDb.info.database } : conn)}\nDatabase: ${label(activeDb)}${dbs.length > 1 ? ` (${dbs.length} open)` : ''}`}>
          <span className="size-1.5 shrink-0 rounded-full bg-ok" aria-hidden />
          <span className="truncate font-medium text-fg-2">{activeDb.info.version}</span>
        </span>
        <div className="flex-1" />
        <button onClick={() => setPicker('all')}
          className="flex h-8 w-[min(380px,30vw)] shrink-0 items-center gap-2 whitespace-nowrap rounded-lg border border-line bg-panel-2 px-2.5 text-[12.5px] text-faint hover:border-faint">
          <Search className="size-3.5 shrink-0" /> <span className="truncate">Jump to table or command…</span> <span className="ml-auto shrink-0"><Kbd>{keyHint('ws.palette')}</Kbd></span>
        </button>
        <div className="flex-1" />
        <JobsIndicator />
        <IconButton label="Import / Export database" onClick={() => setTransfer({ tab: 'export', db: activeDb })}><ArrowDownUp className="size-4" /></IconButton>
        <IconButton label={withKeys('New query', 'ws.newQuery')} onClick={() => newQuery()}><FileCode2 className="size-4" /></IconButton>
        <IconButton label={withKeys('Row inspector', 'ws.inspector')} active={inspector} onClick={toggleInspector}><PanelRight className="size-4" /></IconButton>
        <IconButton label={theme === 'dark' ? 'Light mode' : 'Dark mode'} onClick={toggleTheme}>{theme === 'dark' ? <Sun className="size-4" /> : <Moon className="size-4" />}</IconButton>
        <IconButton label={withKeys('Settings', 'app.settings')} onClick={onOpenSettings}><Settings className="size-4" /></IconButton>
        <IconButton label="Disconnect" onClick={disconnect}><LogOut className="size-4" /></IconButton>
        {ownTitleBar && <><span className="mx-1 h-5 w-px bg-line" /><WindowControls /></>}
      </header>
      {production && (
        <div className="flex h-7 shrink-0 items-center justify-center gap-2 bg-err text-[12px] font-medium text-white">
          You are connected to PRODUCTION — changes you commit are real.
        </div>
      )}

      <div className="flex min-h-0 flex-1">
        {/* Database rail: one tile per open database */}
        {!railHidden && <DbRail dbs={dbs} activeKey={activeKey} pg={pg} mainSid={session.id} onSelect={activateDb} onClose={closeDb}
          onMenu={dbMenu} onOpen={openDatabasePicker} />}

        {/* Table sidebar: the active database only */}
        {!sidebarHidden && <>
        <aside ref={asideRef} className="flex shrink-0 flex-col bg-sidebar" style={{ width: sidebarW }}>
          <div className="px-2.5 pt-2.5">
            <div className="flex rounded-lg border border-line bg-panel p-0.5" role="tablist" aria-label="Sidebar">
              {(['items', 'queries', 'history'] as const).map((k) => (
                <button key={k} role="tab" aria-selected={sideTab === k} onClick={() => setSideTab(k)}
                  className={clsx('h-6 flex-1 rounded-md text-[12px] font-medium', sideTab === k ? 'bg-accent text-accent-fg' : 'text-muted hover:text-fg')}>
                  {k === 'items' ? 'Items' : k === 'queries' ? 'Queries' : 'History'}
                </button>
              ))}
            </div>
          </div>
          {sideTab === 'queries' && <div className="flex min-h-0 flex-1 flex-col pt-2"><QueriesPanel connectionId={conn.id} dbLabel={dbLabelOf} onOpen={openSavedQuery} /></div>}
          {sideTab === 'history' && <div className="flex min-h-0 flex-1 flex-col pt-2"><HistoryPanel connectionId={conn.id} dbLabel={dbLabelOf} onOpen={openHistory} /></div>}
          {sideTab === 'items' && <>
          <div className="flex items-center gap-1 p-2.5">
            <Input ref={filterRef} className="min-w-0 flex-1" icon={<Search />} value={filter} placeholder={redis ? 'Filter keys' : 'Filter tables'} aria-label="Filter tables" onChange={(e) => setFilter(e.target.value)}
              onKeyDown={(e) => {
                const first = shownOf(activeDb)[0]
                if (e.key === 'Enter' && first) openTable(activeDb, first)
                if (e.key === 'Escape') setFilter('')
                if (e.key === 'ArrowDown') { e.preventDefault(); moveInSidebar('first') }
              }} />
            <IconButton label={`New ${tableWord}`} onClick={() => newTable(activeDb)}><Plus className="size-3.5" /></IconButton>
            <RefreshButton label="Refresh tables" iconClassName="size-3.5" busy={sidebarBusy || activeDb.objects === null} onRefresh={() => loadObjects(activeDb.key)} />
          </div>
          <nav ref={navRef} className="min-h-0 flex-1 overflow-y-auto px-1.5 pb-3 outline-none" aria-label="Tables" tabIndex={-1} onKeyDown={onSidebarKey}>
            {(() => {
              const d = activeDb
              const shown = shownOf(d)
              return (
                <>
                  {d.objects === null && <div className="space-y-1.5 p-2">{Array.from({ length: 6 }, (_, i) => <div key={i} className="h-4 animate-pulse rounded bg-hover" style={{ width: `${55 + ((i * 37) % 40)}%` }} />)}</div>}
                  <Section title={redis ? 'Keys' : 'Tables'} labelOf={redis ? redisLabel : undefined} items={shown.filter((o) => o.kind === 'table')} db={d} activeId={activeTab} selected={selDb === d.key ? selected : EMPTY} onClick={onItemClick} onMenu={objectMenu} />
                  <Section title="Views" items={shown.filter((o) => o.kind === 'view')} db={d} activeId={activeTab} selected={selDb === d.key ? selected : EMPTY} onClick={onItemClick} onMenu={objectMenu} />
                  {d.objects !== null && shown.length === 0 && (
                    <div className="px-3 py-6 text-center text-[12px] text-muted">
                      {q ? `Nothing matches “${filter}”.` : <>
                        This database has no tables yet.
                        <div className="mt-2.5 flex justify-center"><Button size="sm" icon={<Plus className="size-3.5" />} onClick={() => newTable(d)}>Create a {tableWord}</Button></div>
                      </>}
                    </div>
                  )}
                </>
              )
            })()}
          </nav>
          </>}
          {sideTab === 'items' && selectedObjects.length > 1 && selDbObj && (
            <div className="m-2 rounded-xl border border-line bg-panel p-2 shadow-pop animate-slide" role="toolbar" aria-label="Selected tables">
              <div className="mb-1.5 flex items-center gap-1">
                <span className="flex-1 truncate pl-1 text-[12px] font-semibold">{selectedObjects.length} selected{dbs.length > 1 ? ` in ${label(selDbObj)}` : ''}</span>
                <IconButton label="Clear selection (Esc)" onClick={() => setSelected(new Set())}><X className="size-3.5" /></IconButton>
              </div>
              <div className="grid grid-cols-2 gap-1.5">
                <Button size="sm" icon={<Download className="size-3.5" />}
                  onClick={() => setTransfer({ tab: 'export', db: selDbObj, preselect: selectedObjects.map((x) => x.name) })}>Export</Button>
                <Button size="sm" variant="danger" icon={<Trash2 className="size-3.5" />} onClick={() => setDeleting({ db: selDbObj, objects: selectedObjects })}>Delete</Button>
              </div>
            </div>
          )}
        </aside>
        <DragHandle onMouseDown={sidebarDrag} />
        </>}

        {/* Tabs + content */}
        <section className="flex min-w-0 flex-1 flex-col bg-panel">
          <div className="flex h-9 shrink-0 items-end gap-0.5 overflow-x-auto border-b border-line bg-panel-2 px-1.5" role="tablist" aria-label="Open tabs">
            {tabs.map((t) => {
              const tdb = dbs.find((d) => d.key === t.dbKey)
              return (
                <div
                  key={t.id}
                  role="tab"
                  aria-selected={t.id === activeTab}
                  title={tdb ? `${t.title} · ${label(tdb)}` : t.title}
                  onMouseDown={(e) => { if (e.button === 1) { e.preventDefault(); closeTab(t.id) } else selectTab(t) }}
                  className={clsx('group flex h-8 max-w-[260px] shrink-0 cursor-pointer items-center gap-1.5 rounded-t-lg border border-b-0 px-2.5 text-[12.5px]',
                    t.id === activeTab ? 'border-line bg-panel font-medium text-fg' : 'border-transparent text-muted hover:bg-hover hover:text-fg')}
                >
                  {t.kind === 'create-table' ? <Plus className="size-3.5 shrink-0 text-accent" /> : t.kind === 'query' ? <FileCode2 className="size-3.5 shrink-0 text-accent" /> : t.object?.kind === 'view' ? <Eye className="size-3.5 shrink-0" /> : <Table2 className="size-3.5 shrink-0" />}
                  <span className="truncate">{t.title}</span>
                  {dbs.length > 1 && tdb && <span className="truncate rounded bg-hover px-1 text-[10.5px] font-normal text-muted">{pg ? tdb.info.database === session.database ? tdb.schema : `${tdb.info.database}·${tdb.schema}` : tdb.schema}</span>}
                  {t.dirty && <span className="size-1.5 shrink-0 rounded-full bg-warn" title="Uncommitted changes" />}
                  {t.modified && <span className="size-1.5 shrink-0 rounded-full bg-accent" title="Edited since saved (Ctrl+S)" />}
                  <button aria-label={`Close ${t.title}`} onMouseDown={(e) => e.stopPropagation()} onClick={() => closeTab(t.id)}
                    className="ml-0.5 rounded p-0.5 opacity-0 hover:bg-active group-hover:opacity-100 aria-selected:opacity-100">
                    <X className="size-3" />
                  </button>
                </div>
              )
            })}
            <IconButton label="New query" className="mb-0.5 ml-1" onClick={() => newQuery()}><Plus className="size-3.5" /></IconButton>
          </div>
          <div className="relative min-h-0 flex-1">
            {!activeTabObj && (
              <div className="flex h-full flex-col items-center justify-center gap-4 text-center animate-fade" data-testid="db-empty">
                <div className="flex size-14 items-center justify-center rounded-2xl bg-accent-soft text-accent"><Database className="size-7" /></div>
                <div>
                  <div className="text-[15px] font-semibold">{label(activeDb)}</div>
                  <div className="mt-1 text-[12.5px] text-muted">{conn.name} · {activeDb.info.version}</div>
                </div>
                <div className="grid grid-cols-2 gap-x-6 gap-y-2 text-[12.5px] text-muted">
                  <span>Open a table</span><span className="text-right">click it on the left</span>
                  <span>Jump anywhere</span><span className="text-right"><Kbd>Ctrl K</Kbd></span>
                  <span>Go to table</span><span className="text-right"><Kbd>Ctrl P</Kbd></span>
                  <span>Open database</span><span className="text-right"><Kbd>Ctrl D</Kbd></span>
                  <span>New SQL query</span><span className="text-right"><Kbd>Ctrl T</Kbd></span>
                  <span>Run statement</span><span className="text-right"><Kbd>Ctrl ↵</Kbd></span>
                  <span>Format SQL</span><span className="text-right"><Kbd>Ctrl I</Kbd></span>
                  <span>Import / export</span><span className="text-right"><button className="text-accent hover:underline" onClick={() => setTransfer({ tab: 'import', db: activeDb })}>open</button></span>
                  <span>Commit changes</span><span className="text-right"><Kbd>Ctrl S</Kbd></span>
                </div>
              </div>
            )}
            {tabs.map((t) => {
              const tdb = dbs.find((d) => d.key === t.dbKey) ?? dbs[0]
              return (
                <div key={t.id} className={clsx('absolute inset-0', t.id !== activeTab && 'invisible')}>
                  {t.kind === 'create-table'
                    ? <CreateTableView session={tdb.info} schema={tdb.schema} label={label(tdb)} existing={(tdb.objects ?? []).map((o) => o.name)}
                        active={active && t.id === activeTab} onClose={() => closeTab(t.id)} onDirty={(d) => updateTab(t.id, { dirty: d })}
                        onCreated={async (name) => {
                          await loadObjects(tdb.key)
                          // The new table takes the designer's place in the tab bar.
                          const o: DbObject = { schema: tdb.schema, name, kind: 'table' }
                          const id = `t:${tdb.key}:${name}`
                          setTabs((ts) => ts.map((x) => (x.id === t.id ? { id, kind: 'table', title: name, object: o, dbKey: tdb.key } : x)))
                          setActiveTab(id)
                        }} />
                    : t.kind === 'table' && t.object
                    ? <DataView session={tdb.info} object={t.object} active={active && t.id === activeTab} tabId={t.id} inspector={inspector}
                        onDirty={(d) => updateTab(t.id, { dirty: d })} openQuery={(sql) => newQuery(sql, tdb.key)}
                        onOpenReference={(ref, vs) => openReference(tdb, ref, vs)} onOpenObject={(o) => openTable(tdb, o)} focusFilter={t.focusFilter} initialMode={t.structure ? 'structure' : undefined}
                        onStructureChanged={() => loadObjects(tdb.key)}
                        onRenamed={(name) => {
                          // The tab follows the table to its new name.
                          const id = `t:${tdb.key}:${name}`
                          setTabs((ts) => ts.map((x) => (x.id === t.id ? { ...x, id, title: name, object: { ...t.object!, name }, structure: true, dirty: false } : x)))
                          if (activeTab === t.id) setActiveTab(id)
                          loadObjects(tdb.key)
                        }} />
                    : <QueryView session={tdb.info} schema={tdb.schema} active={active && t.id === activeTab} initialSql={t.sql ?? ''} completions={tdb.completions}
                        onChange={(sql) => { queryText.current.set(t.id, sql); persistQueries(tabs); if (t.savedId) updateTab(t.id, { modified: sql !== t.savedSql }) }}
                        onSchemaChanged={() => loadObjects(tdb.key)}
                        onSave={(sql) => saveQueryTab(t.id, sql)} savedName={t.savedId ? t.title : undefined} autoRun={t.autoRun}
                        onDirty={(d) => updateTab(t.id, { dirty: d })} />}
                </div>
              )
            })}
          </div>
        </section>
      </div>


      {creatingCollection && (
        <CreateCollectionDialog session={creatingCollection.info} schema={creatingCollection.schema} onClose={() => setCreatingCollection(null)}
          onCreated={async (name) => {
            const d = creatingCollection
            setCreatingCollection(null)
            await loadObjects(d.key)
            openTable(d, { schema: d.schema, name, kind: 'table' })
          }} />
      )}
      {dropping && (
        <DropDatabaseDialog session={session} target={dropping.target} choices={dropping.choices}
          before={beforeDrop} onClose={() => setDropping(null)} onDeleted={afterDrop} />
      )}
      {creatingDb && (
        <CreateDatabaseDialog session={activeDb.info} onClose={() => setCreatingDb(false)}
          onCreatedFile={(c) => { setCreatingDb(false); onOpenConnection(c) }}
          onCreated={(d) => {
            setCreatingDb(false)
            const name = d.name.trim()
            openDb(pg && d.kind === 'database' ? { database: name } : { schema: name })
          }} />
      )}
      {naming && (
        <NameDialog title="Save query" confirmLabel="Save" initial={tabs.find((t) => t.id === naming.tabId)?.title ?? 'Query'}
          onClose={() => setNaming(null)} onSubmit={async (name) => { const n = naming; setNaming(null); await saveQueryTab(n.tabId, n.sql, name) }} />
      )}
      {deleting && (
        <DeleteTablesDialog session={deleting.db.info} objects={deleting.objects} onClose={() => setDeleting(null)}
          onDeleted={(names) => afterDelete(deleting.db, names)} />
      )}
      {transfer && (
        <TransferDialog session={transfer.db.info} schema={transfer.db.schema} objects={transfer.db.objects ?? []} initialTab={transfer.tab} preselect={transfer.preselect}
          onClose={() => setTransfer(null)} onImported={() => loadObjects(transfer.db.key)} />
      )}
      {picker === 'all' && <CommandPalette commands={commands} onClose={() => setPicker(null)} />}
      {picker === 'tables' && (
        <CommandPalette title={`Tables in ${label(activeDb)}`} placeholder={`Go to table in ${label(activeDb)}…`}
          emptyText={activeDb.objects?.length ? 'No table matches' : 'This database has no tables yet'}
          commands={objectCommands(activeDb, false)} onClose={() => setPicker(null)} />
      )}
      {picker === 'databases' && (
        <CommandPalette title="Open database" placeholder="Search databases…" emptyText="No database matches"
          loading={pg && serverDbs === null} commands={databaseCommands} onClose={() => setPicker(null)} />
      )}
      {menu.node}
    </div>
  )
}

const EMPTY = new Set<string>()

/**
 * DbRail is the narrow strip of open databases on the far left: click to
 * switch (Alt+1…9), × or right-click to close, + to open another (Ctrl+D).
 */
function DbRail({ dbs, activeKey, pg, mainSid, onSelect, onClose, onMenu, onOpen }: {
  dbs: OpenDb[]
  activeKey: string
  pg: boolean
  mainSid: string
  onSelect: (key: string) => void
  onClose: (key: string) => void
  onMenu: (e: React.MouseEvent, db: OpenDb) => void
  onOpen: () => void
}) {
  return (
    <nav className="flex w-[76px] shrink-0 flex-col items-center gap-1 overflow-y-auto border-r border-line bg-bg py-2" aria-label="Open databases">
      {dbs.map((d, i) => {
        const active = d.key === activeKey
        const name = pg ? d.schema : d.schema || d.info.database
        const full = pg ? `${d.info.database} › ${d.schema}` : name
        const forked = d.info.id !== mainSid
        return (
          <div key={d.key} className="group/tile relative w-[66px]">
            <button
              onClick={() => onSelect(d.key)}
              onContextMenu={(e) => { e.preventDefault(); onMenu(e, d) }}
              aria-label={`Database ${full}`}
              aria-pressed={active}
              title={`${full}${i < 9 ? `  (Alt+${i + 1})` : ''}`}
              className={clsx('flex w-full flex-col items-center gap-1 rounded-xl px-1 pb-1.5 pt-2 transition-colors',
                active ? 'bg-panel shadow-[0_0_0_1px_var(--line)]' : 'hover:bg-hover')}
            >
              <span className={clsx('flex size-9 items-center justify-center rounded-xl transition-colors',
                active ? 'bg-accent text-accent-fg' : 'bg-hover text-muted group-hover/tile:text-fg')}>
                {forked ? <Server className="size-[18px]" /> : <Database className="size-[18px]" />}
              </span>
              <span className={clsx('w-full truncate text-center text-[11px] leading-tight', active ? 'font-semibold text-fg' : 'text-fg-2')}>{name}</span>
              {pg && <span className="-mt-0.5 w-full truncate text-center text-[9.5px] leading-tight text-faint">{d.info.database}</span>}
            </button>
            {dbs.length > 1 && (
              <button aria-label={`Close ${full}`} onClick={() => onClose(d.key)}
                className="absolute -right-1 -top-1 hidden size-[18px] items-center justify-center rounded-full border border-line bg-panel text-muted shadow-sm hover:text-err group-hover/tile:flex">
                <X className="size-3" />
              </button>
            )}
          </div>
        )
      })}
      <button onClick={onOpen} aria-label="Open database" title="Open another database (Ctrl+D)"
        className="mt-1 flex size-9 items-center justify-center rounded-xl border border-dashed border-line text-muted hover:border-accent hover:text-accent">
        <Plus className="size-4" />
      </button>
    </nav>
  )
}

export function quote(name: string, driver: Driver) {
  if (/^[a-z_][a-z0-9_]*$/.test(name)) return name
  switch (family(driver)) {
    case 'mysql': return '`' + name.replace(/`/g, '``') + '`'
    case 'clickhouse': return '`' + name.replace(/[`\\]/g, '\\$&') + '`'
    case 'sqlserver': return '[' + name.replace(/]/g, ']]') + ']'
  }
  return '"' + name.replace(/"/g, '""') + '"'
}

function Section({ title, labelOf, items, db, activeId, selected, onClick, onMenu }: {
  title: string
  /** labelOf: how an item is named, when not by its name (Redis's "All keys"). */
  labelOf?: (name: string) => string
  items: DbObject[]
  db: OpenDb
  activeId: string | null
  selected: Set<string>
  onClick: (e: React.MouseEvent, db: OpenDb, o: DbObject) => void
  onMenu: (e: React.MouseEvent, db: OpenDb, o: DbObject) => void
}) {
  const [open, setOpen] = useState(true)
  if (!items.length) return null
  return (
    <div className="mt-0.5">
      <button className="flex h-6 w-full items-center gap-1 px-1.5 text-[10.5px] font-semibold uppercase tracking-wider text-muted hover:text-fg" onClick={() => setOpen(!open)}>
        <ChevronDown className={clsx('size-3 transition-transform', !open && '-rotate-90')} />{title}<span className="ml-auto font-normal text-faint">{items.length}</span>
      </button>
      {open && items.map((o) => {
        const active = activeId === `t:${db.key}:${o.name}`
        const isSel = selected.has(o.name) && selected.size > 1
        return (
          <button key={o.name} onClick={(e) => onClick(e, db, o)} onContextMenu={(e) => onMenu(e, db, o)} title={o.name}
            aria-selected={selected.has(o.name)} data-testid={`sidebar-${o.name}`} data-item={o.name} data-active={active}
            className={clsx('flex h-7 w-full scroll-my-1 select-none items-center gap-2 rounded-md px-2 text-left text-[13px] outline-none focus-visible:ring-2 focus-visible:ring-accent/60',
              isSel ? 'bg-selected font-medium text-fg' : active ? 'bg-active font-medium text-accent' : 'text-fg-2 hover:bg-hover')}>
            {o.kind === 'view' ? <Eye className="size-3.5 shrink-0 text-muted" /> : <Table2 className={clsx('size-3.5 shrink-0', active ? 'text-accent' : 'text-muted')} />}
            <span className="truncate">{labelOf ? labelOf(o.name) : o.name}</span>
          </button>
        )
      })}
    </div>
  )
}
