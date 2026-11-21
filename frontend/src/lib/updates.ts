// GoTable's own updates, as the page sees them: what is installed, what
// GitHub has, and an update's progress. The server does the work
// (internal/server/update.go); this keeps one shared state for the notice
// and Settings → Updates.
import { useSyncExternalStore } from 'react'
import { api, type UpdateCheck, type UpdateInfo, type UpdateProgress } from '../api'
import { errorParts } from '../components/ui'
import { getSettings, updateSettings } from './settings'

export interface UpdateState {
  info?: UpdateInfo
  check?: UpdateCheck
  checking: boolean
  checkError?: string
  progress: UpdateProgress
  /** dismissed: "Later" hides the notice until the next launch. */
  dismissed: boolean
  restarting: boolean
}

let state: UpdateState = { checking: false, progress: { phase: 'idle', done: 0, total: 0 }, dismissed: false, restarting: false }
const listeners = new Set<() => void>()
const set = (patch: Partial<UpdateState>) => { state = { ...state, ...patch }; listeners.forEach((l) => l()) }
const subscribe = (l: () => void) => { listeners.add(l); return () => { listeners.delete(l) } }

export const useUpdates = () => useSyncExternalStore(subscribe, () => state)

const DAY = 24 * 60 * 60 * 1000

/** initUpdates reads what is installed and, once a day if allowed, checks GitHub (after the settings are loaded). */
export async function initUpdates() {
  try {
    set({ info: await api.updateInfo() })
  } catch { return }
  const s = getSettings().updates
  if (state.info?.how !== 'none' && s.auto && Date.now() - s.lastCheck > DAY) await checkForUpdates(false)
}

/** checkForUpdates asks GitHub; a check the user asked for reports errors, an automatic one stays quiet. */
export async function checkForUpdates(manual: boolean) {
  if (state.checking) return
  set({ checking: true, checkError: undefined })
  try {
    const check = await api.updateCheck()
    updateSettings({ updates: { ...getSettings().updates, lastCheck: Date.now() } })
    set({ check, checking: false, ...(manual ? { dismissed: false } : {}) })
  } catch (e) {
    set({ checking: false, checkError: manual ? errorParts(e).filter(Boolean).join(' — ') : undefined })
  }
}

let polling: ReturnType<typeof setInterval> | undefined

/** installUpdate downloads, checks and installs the version found, following its progress. */
export async function installUpdate() {
  try {
    set({ progress: await api.updateInstall(), dismissed: false })
  } catch (e) {
    const [title, detail] = errorParts(e)
    set({ progress: { phase: 'failed', done: 0, total: 0, error: { title, detail } } })
    return
  }
  clearInterval(polling)
  polling = setInterval(async () => {
    try {
      const p = await api.updateProgress()
      set({ progress: p })
      if (p.phase !== 'downloading' && p.phase !== 'installing') clearInterval(polling)
    } catch { /* keep trying */ }
  }, 400)
}

/** restartToUpdate starts the new version; this window closes. */
export async function restartToUpdate() {
  set({ restarting: true })
  try { await api.updateRestart() } catch (e) {
    const [title, detail] = errorParts(e)
    set({ restarting: false, progress: { ...state.progress, phase: 'failed', error: { title, detail } } })
  }
}

export const laterUpdate = () => set({ dismissed: true })

/** skipUpdate: no notice for this version (a newer one shows again). */
export function skipUpdate() {
  if (state.check) updateSettings({ updates: { ...getSettings().updates, skipped: state.check.latest } })
  set({ dismissed: true })
}

export const unskipUpdate = () => updateSettings({ updates: { ...getSettings().updates, skipped: '' } })

export const openRelease = () => api.updateOpenRelease().catch(() => {})

/** showNotice: whether the corner notice is up (an update found, or one under way). */
export function showNotice(s: UpdateState, skipped: string) {
  if (s.restarting || s.progress.phase !== 'idle') return true
  return !!s.check?.available && !s.dismissed && s.check.latest !== skipped
}

/** KIND_LABELS name how GoTable was installed. */
export const KIND_LABELS: Record<string, string> = {
  appimage: 'AppImage', tarball: 'Linux archive (.tar.gz)', deb: 'Debian/Ubuntu package (.deb)', rpm: 'Fedora/RHEL package (.rpm)',
  windows: 'Windows program', macos: 'macOS app', dev: 'development build',
}

export const mb = (n: number) => (n / (1024 * 1024)).toFixed(n < 10 * 1024 * 1024 ? 1 : 0)
export const plainVersion = (v?: string) => (v ?? '').replace(/^v/, '')
