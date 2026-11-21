// The app's preferences: theme, accent colour, editor text size and
// keyboard shortcuts. They are saved by the server in settings.json (the
// window's own storage forgets them: its address changes with each launch);
// a copy in localStorage paints the first frame in the right colours.
import { useSyncExternalStore } from 'react'

export type ThemeChoice = 'system' | 'light' | 'dark'

/** The workspace's panels that can be shown or hidden. */
export type Panel = 'rail' | 'sidebar' | 'inspector'
export const PANELS: Panel[] = ['rail', 'sidebar', 'inspector']
/** PanelChoice: how a panel starts in a new workspace: open, closed, or as it was last left. */
export type PanelChoice = 'open' | 'closed' | 'last'

export interface Settings {
  theme: ThemeChoice
  /** accent: a preset's id, or a colour as #rrggbb. */
  accent: string
  /** editorFontSize: the query editor's text size in pixels. */
  editorFontSize: number
  /** shortcuts: the user's own bindings by shortcut id ('' switches one off). */
  shortcuts: Record<string, string>
  /** panels: how each panel starts when a connection opens. */
  panels: Record<Panel, PanelChoice>
  /** lastPanels: whether each panel was last left open, for 'last'. */
  lastPanels: Record<Panel, boolean>
  /** updates: checking GitHub for a newer GoTable (auto: once a day), a version the user skipped, and when it last checked (ms). */
  updates: { auto: boolean; skipped: string; lastCheck: number }
}

export const DEFAULTS: Settings = {
  theme: 'system', accent: 'indigo', editorFontSize: 13, shortcuts: {},
  panels: { rail: 'last', sidebar: 'last', inspector: 'last' },
  lastPanels: { rail: true, sidebar: true, inspector: true },
  updates: { auto: true, skipped: '', lastCheck: 0 },
}
export const FONT_SIZES = { min: 9, max: 32 }

/** ACCENTS: each preset's colour in the light theme and in the dark one. */
export const ACCENTS: { id: string; label: string; light: string; dark: string }[] = [
  { id: 'indigo', label: 'Indigo', light: '#5b5bd6', dark: '#8b8cff' },
  { id: 'blue', label: 'Blue', light: '#2563eb', dark: '#6ea8ff' },
  { id: 'sky', label: 'Sky', light: '#0284c7', dark: '#4fc3f7' },
  { id: 'teal', label: 'Teal', light: '#0f8a7e', dark: '#3fd0c0' },
  { id: 'green', label: 'Green', light: '#1e8a4c', dark: '#4ade80' },
  { id: 'amber', label: 'Amber', light: '#b26a00', dark: '#fbbf24' },
  { id: 'orange', label: 'Orange', light: '#d4520f', dark: '#fb923c' },
  { id: 'rose', label: 'Rose', light: '#d1294f', dark: '#fb7192' },
  { id: 'pink', label: 'Pink', light: '#c2338f', dark: '#f472c6' },
  { id: 'violet', label: 'Violet', light: '#7c3aed', dark: '#b18cff' },
  { id: 'slate', label: 'Graphite', light: '#475569', dark: '#a3b1c6' },
]

const CACHE = 'gotable:settings'
const HEX = /^#[0-9a-f]{6}$/i

/** normalize keeps what is valid in saved settings and fills in the rest. */
export function normalize(raw: unknown): Settings {
  const r = (raw && typeof raw === 'object' ? raw : {}) as Partial<Settings>
  const theme: ThemeChoice = r.theme === 'light' || r.theme === 'dark' || r.theme === 'system' ? r.theme : DEFAULTS.theme
  const accent = typeof r.accent === 'string' && (ACCENTS.some((a) => a.id === r.accent) || HEX.test(r.accent)) ? r.accent : DEFAULTS.accent
  const size = Number(r.editorFontSize)
  const editorFontSize = Number.isFinite(size) ? Math.round(Math.min(FONT_SIZES.max, Math.max(FONT_SIZES.min, size))) : DEFAULTS.editorFontSize
  const shortcuts: Record<string, string> = {}
  if (r.shortcuts && typeof r.shortcuts === 'object') {
    for (const [k, v] of Object.entries(r.shortcuts)) if (typeof v === 'string') shortcuts[k] = v
  }
  const panels = { ...DEFAULTS.panels }
  const lastPanels = { ...DEFAULTS.lastPanels }
  for (const p of PANELS) {
    const c = (r.panels as Record<string, unknown> | undefined)?.[p]
    if (c === 'open' || c === 'closed' || c === 'last') panels[p] = c
    const l = (r.lastPanels as Record<string, unknown> | undefined)?.[p]
    if (typeof l === 'boolean') lastPanels[p] = l
  }
  const u = (r.updates ?? {}) as Partial<Settings['updates']>
  const updates = {
    auto: typeof u.auto === 'boolean' ? u.auto : DEFAULTS.updates.auto,
    skipped: typeof u.skipped === 'string' ? u.skipped : '',
    lastCheck: Number.isFinite(u.lastCheck) ? Number(u.lastCheck) : 0,
  }
  return { theme, accent, editorFontSize, shortcuts, panels, lastPanels, updates }
}

/** panelOpen: whether a panel starts open in a new workspace. */
export const panelOpen = (s: Settings, p: Panel) => (s.panels[p] === 'last' ? s.lastPanels[p] : s.panels[p] === 'open')

/** rememberPanel keeps whether a panel was left open, for "as I left it". */
export function rememberPanel(p: Panel, open: boolean) {
  if (current.lastPanels[p] !== open) updateSettings({ lastPanels: { ...current.lastPanels, [p]: open } })
}

function cached(): Settings {
  try {
    const raw = localStorage.getItem(CACHE)
    if (raw) return normalize(JSON.parse(raw))
    // Before settings, the theme was kept on its own.
    const t = localStorage.getItem('gotable:theme')
    if (t === 'light' || t === 'dark') return { ...DEFAULTS, theme: t }
  } catch { /* storage unavailable */ }
  return DEFAULTS
}

let current: Settings = cached()
const listeners = new Set<() => void>()
const emit = () => listeners.forEach((l) => l())

export const getSettings = () => current

let saveTimer: ReturnType<typeof setTimeout> | undefined
let saving: Promise<unknown> = Promise.resolve()

/** updateSettings changes some settings now and saves them a moment later. */
export function updateSettings(patch: Partial<Settings>) {
  current = normalize({ ...current, ...patch })
  try { localStorage.setItem(CACHE, JSON.stringify(current)) } catch { /* not cached */ }
  emit()
  clearTimeout(saveTimer)
  saveTimer = setTimeout(() => { saving = save(current) }, 250)
}

async function save(s: Settings) {
  try {
    await fetch('/api/settings', { method: 'PUT', credentials: 'same-origin', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(s) })
  } catch { /* kept in this window; saved with the next change */ }
}

/** flushSettings saves a pending change right away (tests, closing the dialog). */
export function flushSettings() {
  if (saveTimer) { clearTimeout(saveTimer); saveTimer = undefined; saving = save(current) }
  return saving
}

/** loadSettings reads the saved settings at startup. A theme picked before settings existed comes along. */
export async function loadSettings() {
  try {
    const res = await fetch('/api/settings', { credentials: 'same-origin' })
    if (!res.ok) return
    const saved = await res.json()
    const empty = !saved || typeof saved !== 'object' || Object.keys(saved).length === 0
    if (empty) {
      if (JSON.stringify(current) !== JSON.stringify(DEFAULTS)) updateSettings({}) // keep what this window had
      return
    }
    current = normalize(saved)
    try { localStorage.setItem(CACHE, JSON.stringify(current)) } catch { /* not cached */ }
    emit()
  } catch { /* the cached copy stays */ }
}

const subscribe = (l: () => void) => { listeners.add(l); return () => { listeners.delete(l) } }

/** useSettings re-renders when the settings change. */
export const useSettings = () => useSyncExternalStore(subscribe, getSettings)

// ---------------------------------------------------------------- colours

const rgb = (hex: string) => [1, 3, 5].map((i) => parseInt(hex.slice(i, i + 2), 16))
const toHex = (c: number[]) => '#' + c.map((v) => Math.round(v).toString(16).padStart(2, '0')).join('')
const mixHex = (a: string, b: string, t: number) => toHex(rgb(a).map((v, i) => v + (rgb(b)[i] - v) * t))

/** luminance: WCAG relative luminance, 0 (black) to 1 (white). */
export function luminance(hex: string) {
  const [r, g, b] = rgb(hex).map((v) => { const c = v / 255; return c <= 0.03928 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4 })
  return 0.2126 * r + 0.7152 * g + 0.0722 * b
}

/** accentColors: the colours for an accent in each theme, and the text that reads on them. */
export function accentColors(accent: string) {
  const preset = ACCENTS.find((a) => a.id === accent)
  // A custom colour is the light theme's; the dark theme gets it lightened.
  const light = preset?.light ?? (HEX.test(accent) ? accent.toLowerCase() : ACCENTS[0].light)
  const dark = preset?.dark ?? mixHex(light, '#ffffff', luminance(light) < 0.25 ? 0.35 : 0.1)
  // White text on a light theme's button unless the colour is itself light
  // (yellow, a pale custom one): mid tones read better in white than the
  // contrast ratio alone says. The dark theme's shades are bright, so there
  // the stronger contrast wins, as with the default's dark text.
  const strongest = (c: string) => ((luminance(c) + 0.05) / 0.05 > 1.05 / (luminance(c) + 0.05) ? '#111116' : '#ffffff')
  return { light, dark, lightFg: luminance(light) > 0.4 ? '#111116' : '#ffffff', darkFg: strongest(dark) }
}

/** resolvedTheme: the theme in use, the system's when the choice is "system". */
export const resolvedTheme = (s: Settings, systemDark: boolean): 'light' | 'dark' => (s.theme === 'system' ? (systemDark ? 'dark' : 'light') : s.theme)

/** applyAppearance sets the theme, the accent colours and the editor's text size on the page. */
export function applyAppearance(s: Settings, theme: 'light' | 'dark') {
  const root = document.documentElement
  root.dataset.theme = theme
  if (s.accent === DEFAULTS.accent) {
    // The stylesheet's own colours, tuned by hand.
    delete root.dataset.accent
    for (const v of ['--accent-l', '--accent-d', '--accent-fg-l', '--accent-fg-d']) root.style.removeProperty(v)
  } else {
    const c = accentColors(s.accent)
    root.dataset.accent = s.accent
    root.style.setProperty('--accent-l', c.light)
    root.style.setProperty('--accent-d', c.dark)
    root.style.setProperty('--accent-fg-l', c.lightFg)
    root.style.setProperty('--accent-fg-d', c.darkFg)
  }
  root.style.setProperty('--editor-font-size', `${s.editorFontSize}px`)
}
