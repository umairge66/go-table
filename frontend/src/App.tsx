import { useCallback, useEffect, useState } from 'react'
import { api } from './api'
import { ConfirmProvider } from './components/Confirm'
import { ResizeEdges, useOwnTitleBar } from './components/WindowChrome'
import { Spinner, ToastProvider, errorParts, useToast } from './components/ui'
import type { Connection, Group, SessionInfo } from './types'
import { Home } from './views/Home'
import { Workspace } from './views/Workspace'

type Theme = 'light' | 'dark'

function initialTheme(): Theme {
  const saved = localStorage.getItem('gotable:theme')
  if (saved === 'light' || saved === 'dark') return saved
  return window.matchMedia?.('(prefers-color-scheme: dark)').matches ? 'dark' : 'light'
}

function Shell() {
  const toast = useToast()
  const [theme, setTheme] = useState<Theme>(initialTheme)
  const [connections, setConnections] = useState<Connection[] | null>(null)
  const [groups, setGroups] = useState<Group[]>([])
  const [sessions, setSessions] = useState<SessionInfo[]>([])
  const [view, setView] = useState<string>('home') // 'home' or a session id
  const [connecting, setConnecting] = useState<string | null>(null)
  const ownTitleBar = useOwnTitleBar()

  useEffect(() => {
    document.documentElement.dataset.theme = theme
    // In the desktop window, GTK draws <select> lists and file dialogs; tell
    // it the theme so they match (a no-op in a browser).
    fetch('/api/native/theme', { method: 'POST', credentials: 'same-origin', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ dark: theme === 'dark' }) })
      .catch(() => {})
  }, [theme])
  const toggleTheme = useCallback(() => setTheme((t) => {
    const next = t === 'dark' ? 'light' : 'dark'
    localStorage.setItem('gotable:theme', next)
    return next
  }), [])

  const reload = useCallback(async () => {
    try {
      const r = await api.connections()
      setConnections(r.connections)
      setGroups(r.groups)
    } catch (e) {
      toast('error', ...errorParts(e))
      setConnections((c) => c ?? [])
    }
  }, [toast])
  useEffect(() => { reload() }, [reload])

  const connect = useCallback(async (c: Connection) => {
    const open = sessions.find((s) => s.connection.id === c.id)
    if (open) { setView(open.id); return }
    setConnecting(c.id)
    try {
      const s = await api.openSession(c.id)
      setSessions((ss) => [...ss, s])
      setView(s.id)
    } catch (e) {
      toast('error', ...errorParts(e))
    } finally {
      setConnecting(null)
    }
  }, [sessions, toast])

  const disconnect = useCallback(async (sid: string) => {
    await api.closeSession(sid).catch(() => {})
    setSessions((ss) => ss.filter((s) => s.id !== sid))
    setView('home')
  }, [])

  // Ctrl+Shift+L toggles the theme everywhere.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.ctrlKey || e.metaKey) && e.shiftKey && e.key.toLowerCase() === 'l') { e.preventDefault(); toggleTheme() }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [toggleTheme])

  if (!connections) return <div className="flex h-full items-center justify-center"><Spinner className="size-6" /></div>

  return (
    <>
      {ownTitleBar && <ResizeEdges />}
      {view === 'home' && (
        <Home connections={connections} groups={groups} reload={reload} onConnect={connect} connecting={connecting} theme={theme} toggleTheme={toggleTheme} />
      )}
      {sessions.map((s) => (
        <Workspace key={s.id} session={s} sessions={sessions} active={view === s.id} onHome={() => setView('home')} onSwitch={setView}
          onDisconnect={() => disconnect(s.id)} theme={theme} toggleTheme={toggleTheme}
          onOpenConnection={(c) => { reload(); connect(c) }} />
      ))}
    </>
  )
}

export default function App() {
  return <ToastProvider><ConfirmProvider><Shell /></ConfirmProvider></ToastProvider>
}
