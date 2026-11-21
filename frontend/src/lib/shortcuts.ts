import { getSettings, useSettings } from './settings'

// Every keyboard shortcut that can be changed, in one place. A binding is a
// combo such as "ctrl+shift+enter" (Ctrl stands for ⌘ on a Mac too); the
// user's own bindings are kept in the settings and override the defaults.

/** Where a shortcut works: two shortcuts can share keys only in places that never overlap. */
export type Scope = 'global' | 'home' | 'workspace' | 'table' | 'query' | 'editor' | 'create'

export interface ShortcutDef {
  id: string
  label: string
  group: string
  scope: Scope
  default: string
  /** aliases also work while the default is in use (Ctrl++ for Ctrl+=). */
  aliases?: string[]
}

export const SHORTCUTS: ShortcutDef[] = [
  { id: 'app.settings', label: 'Open settings', group: 'General', scope: 'global', default: 'ctrl+,' },
  { id: 'app.theme', label: 'Switch between light and dark', group: 'General', scope: 'global', default: 'ctrl+shift+l' },
  { id: 'app.refresh', label: 'Refresh (F5 also works)', group: 'General', scope: 'global', default: 'ctrl+r' },
  { id: 'home.new', label: 'New connection', group: 'General', scope: 'home', default: 'ctrl+n' },

  { id: 'ws.palette', label: 'Jump to table or command', group: 'Workspace', scope: 'workspace', default: 'ctrl+k' },
  { id: 'ws.tables', label: 'Open a table', group: 'Workspace', scope: 'workspace', default: 'ctrl+p' },
  { id: 'ws.databases', label: 'Open a database', group: 'Workspace', scope: 'workspace', default: 'ctrl+d' },
  { id: 'ws.newQuery', label: 'New query tab', group: 'Workspace', scope: 'workspace', default: 'ctrl+t' },
  { id: 'ws.closeTab', label: 'Close the tab', group: 'Workspace', scope: 'workspace', default: 'ctrl+w' },
  { id: 'ws.sidebar', label: 'Show or hide the sidebar', group: 'Workspace', scope: 'workspace', default: 'ctrl+b' },
  { id: 'ws.rail', label: 'Show or hide the database rail', group: 'Workspace', scope: 'workspace', default: 'ctrl+shift+b' },
  { id: 'ws.filterTables', label: 'Filter the table list', group: 'Workspace', scope: 'workspace', default: 'ctrl+shift+f' },
  { id: 'ws.inspector', label: 'Show or hide the row inspector', group: 'Workspace', scope: 'workspace', default: 'ctrl+shift+i' },

  { id: 'table.commit', label: 'Review & commit changes', group: 'Table', scope: 'table', default: 'ctrl+s' },
  { id: 'table.search', label: 'Search the rows', group: 'Table', scope: 'table', default: 'ctrl+f' },
  { id: 'table.applyFilter', label: 'Apply the filters', group: 'Table', scope: 'table', default: 'ctrl+enter' },
  { id: 'create.create', label: 'Create the table', group: 'Table', scope: 'create', default: 'ctrl+enter' },

  { id: 'query.save', label: 'Save the query', group: 'Query editor', scope: 'query', default: 'ctrl+s' },
  { id: 'editor.runCurrent', label: 'Run the statement under the cursor', group: 'Query editor', scope: 'editor', default: 'ctrl+enter' },
  { id: 'editor.runAll', label: 'Run everything', group: 'Query editor', scope: 'editor', default: 'ctrl+shift+enter' },
  { id: 'editor.format', label: 'Format', group: 'Query editor', scope: 'editor', default: 'ctrl+i' },
  { id: 'editor.comment', label: 'Comment or uncomment lines', group: 'Query editor', scope: 'editor', default: 'ctrl+/' },
  { id: 'editor.zoomIn', label: 'Larger text', group: 'Query editor', scope: 'query', default: 'ctrl+=', aliases: ['ctrl++', 'ctrl+shift++'] },
  { id: 'editor.zoomOut', label: 'Smaller text', group: 'Query editor', scope: 'query', default: 'ctrl+-', aliases: ['ctrl+shift+_'] },
  { id: 'editor.zoomReset', label: 'Default text size', group: 'Query editor', scope: 'query', default: 'ctrl+0' },
]

/** FIXED: shortcuts that can't be changed, listed so they aren't a surprise. */
export const FIXED: [string, string][] = [
  ['Ctrl 1 … 9', 'Go to tab 1 to 9'],
  ['Ctrl Tab / Ctrl Shift Tab', 'Next or previous tab'],
  ['F5', 'Refresh'],
  ['Ctrl C / Ctrl V / Ctrl X', 'Copy, paste, cut'],
  ['Ctrl Z / Ctrl Shift Z', 'Undo, redo (in the editor)'],
  ['Ctrl A', 'Select all'],
  ['Ctrl F', 'Find (in the editor)'],
  ['Esc', 'Close a dialog or menu, cancel an edit'],
]

/** RESERVED: keys the system or every text field already uses. */
export const RESERVED = ['ctrl+c', 'ctrl+v', 'ctrl+x', 'ctrl+z', 'ctrl+shift+z', 'ctrl+y', 'ctrl+a', 'ctrl+tab', 'ctrl+shift+tab', 'f5', 'escape',
  'ctrl+1', 'ctrl+2', 'ctrl+3', 'ctrl+4', 'ctrl+5', 'ctrl+6', 'ctrl+7', 'ctrl+8', 'ctrl+9']

export const def = (id: string) => SHORTCUTS.find((s) => s.id === id)

const MODIFIERS = new Set(['control', 'shift', 'alt', 'meta', 'os', 'altgraph'])

/** comboOf names a key press: "ctrl+shift+enter". Ctrl and ⌘ are one. Null for a lone modifier. */
export function comboOf(e: Pick<KeyboardEvent, 'key' | 'ctrlKey' | 'metaKey' | 'altKey' | 'shiftKey'>): string | null {
  const key = e.key === ' ' ? 'space' : e.key.toLowerCase()
  if (MODIFIERS.has(key) || key === 'unidentified' || key === 'dead') return null
  const parts: string[] = []
  if (e.ctrlKey || e.metaKey) parts.push('ctrl')
  if (e.altKey) parts.push('alt')
  if (e.shiftKey) parts.push('shift')
  parts.push(key)
  return parts.join('+')
}

/** binding: the keys for a shortcut given the user's overrides; '' when it is switched off. */
export const binding = (id: string, overrides: Record<string, string>) => overrides[id] ?? def(id)?.default ?? ''

/** combosFor: every combo that triggers a shortcut (its binding, plus aliases while that is the default). */
export function combosFor(id: string, overrides: Record<string, string>): string[] {
  const d = def(id)
  const b = binding(id, overrides)
  if (!b) return []
  return d && b === d.default && d.aliases ? [b, ...d.aliases] : [b]
}

/** matches says whether a key press triggers a shortcut. */
export function matches(e: KeyboardEvent, id: string, overrides: Record<string, string>): boolean {
  const c = comboOf(e)
  return !!c && combosFor(id, overrides).includes(c)
}

const KEY_NAMES: Record<string, string> = {
  enter: '↵', escape: 'Esc', arrowup: '↑', arrowdown: '↓', arrowleft: '←', arrowright: '→', space: 'Space',
  backspace: '⌫', delete: 'Del', pageup: 'PgUp', pagedown: 'PgDn', tab: 'Tab', home: 'Home', end: 'End',
}

/** parse splits a combo into its modifiers and its key; "ctrl++" is Ctrl and "+". */
export function parse(combo: string): { mods: string[]; key: string } {
  if (combo.endsWith('++')) return { mods: combo.slice(0, -2).split('+').filter(Boolean), key: '+' }
  const parts = combo.split('+')
  return { mods: parts.slice(0, -1), key: parts[parts.length - 1] }
}

/** label writes a combo the way the app shows keys: "Ctrl Shift ↵" (Kbd turns it into ⌘⇧↵ on a Mac). */
export function label(combo: string): string {
  if (!combo) return ''
  const { mods, key } = parse(combo)
  const names = mods.map((m) => (m === 'ctrl' ? 'Ctrl' : m === 'alt' ? 'Alt' : 'Shift'))
  const k = KEY_NAMES[key] ?? (key.length === 1 ? key.toUpperCase() : key[0].toUpperCase() + key.slice(1))
  return [...names, k].join(' ')
}

/** codemirrorKey writes a combo as CodeMirror's keymap does: "Mod-Shift-Enter". */
export function codemirrorKey(combo: string): string {
  const { mods, key } = parse(combo)
  const named: Record<string, string> = { enter: 'Enter', space: 'Space', escape: 'Escape', tab: 'Tab', backspace: 'Backspace', delete: 'Delete',
    arrowup: 'ArrowUp', arrowdown: 'ArrowDown', arrowleft: 'ArrowLeft', arrowright: 'ArrowRight', pageup: 'PageUp', pagedown: 'PageDown', home: 'Home', end: 'End' }
  const k = named[key] ?? (/^f\d{1,2}$/.test(key) ? key.toUpperCase() : key)
  return [...mods.map((m) => (m === 'ctrl' ? 'Mod' : m === 'alt' ? 'Alt' : 'Shift')), k].join('-')
}

/** hit: whether a key press triggers a shortcut, with the current settings. */
export const hit = (e: KeyboardEvent, id: string) => matches(e, id, getSettings().shortcuts)

/** useShortcutLabel: a shortcut's keys as shown in hints, following the settings. */
export function useShortcutLabel(id: string) {
  return label(binding(id, useSettings().shortcuts))
}

/** overlap: whether two scopes can be active at once, so the same keys would clash. */
export function overlap(a: Scope, b: Scope): boolean {
  if (a === b || a === 'global' || b === 'global') return true
  const inWorkspace: Scope[] = ['table', 'query', 'editor', 'create']
  if ((a === 'workspace' && inWorkspace.includes(b)) || (b === 'workspace' && inWorkspace.includes(a))) return true
  const pair = [a, b].sort().join()
  return pair === 'editor,query'
}

/** conflicts: the other shortcuts that the given keys would clash with. */
export function conflicts(id: string, combo: string, overrides: Record<string, string>): ShortcutDef[] {
  const me = def(id)
  if (!me || !combo) return []
  return SHORTCUTS.filter((s) => s.id !== id && overlap(s.scope, me.scope) && combosFor(s.id, overrides).includes(combo))
}
