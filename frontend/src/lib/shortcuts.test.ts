import { expect, test } from 'vitest'
import { codemirrorKey, comboOf, conflicts, label, matches, parse } from './shortcuts'
import { DEFAULTS, accentColors, normalize, panelOpen } from './settings'

const key = (k: string, mods: { ctrl?: boolean; meta?: boolean; shift?: boolean; alt?: boolean } = {}) =>
  ({ key: k, ctrlKey: !!mods.ctrl, metaKey: !!mods.meta, shiftKey: !!mods.shift, altKey: !!mods.alt }) as KeyboardEvent

test('a key press is named the same way on every platform', () => {
  expect(comboOf(key('Enter', { ctrl: true, shift: true }))).toBe('ctrl+shift+enter')
  expect(comboOf(key('s', { meta: true }))).toBe('ctrl+s') // ⌘ is Ctrl
  expect(comboOf(key('K', { ctrl: true, shift: true }))).toBe('ctrl+shift+k')
  expect(comboOf(key(' ', { alt: true }))).toBe('alt+space')
  expect(comboOf(key('Shift', { shift: true }))).toBeNull()
  expect(comboOf(key('Control', { ctrl: true }))).toBeNull()
})

test('combos read as the app shows keys, and as CodeMirror names them', () => {
  expect(label('ctrl+shift+enter')).toBe('Ctrl Shift ↵')
  expect(label('ctrl+,')).toBe('Ctrl ,')
  expect(label('ctrl++')).toBe('Ctrl +')
  expect(label('alt+arrowup')).toBe('Alt ↑')
  expect(label('')).toBe('')
  expect(parse('ctrl+shift++')).toEqual({ mods: ['ctrl', 'shift'], key: '+' })
  expect(codemirrorKey('ctrl+shift+enter')).toBe('Mod-Shift-Enter')
  expect(codemirrorKey('ctrl+/')).toBe('Mod-/')
  expect(codemirrorKey('ctrl++')).toBe('Mod-+')
  expect(codemirrorKey('alt+f2')).toBe('Alt-F2')
})

test('the user’s bindings replace the defaults; aliases go with the default', () => {
  expect(matches(key('Enter', { ctrl: true }), 'editor.runCurrent', {})).toBe(true)
  expect(matches(key('e', { ctrl: true }), 'editor.runCurrent', { 'editor.runCurrent': 'ctrl+e' })).toBe(true)
  expect(matches(key('Enter', { ctrl: true }), 'editor.runCurrent', { 'editor.runCurrent': 'ctrl+e' })).toBe(false)
  expect(matches(key('Enter', { ctrl: true }), 'editor.runCurrent', { 'editor.runCurrent': '' })).toBe(false) // switched off
  expect(matches(key('+', { ctrl: true }), 'editor.zoomIn', {})).toBe(true)
  expect(matches(key('+', { ctrl: true }), 'editor.zoomIn', { 'editor.zoomIn': 'ctrl+shift+u' })).toBe(false)
})

test('a clash is reported only where both shortcuts can work', () => {
  // Ctrl+S saves a query and commits a table: never in the same place.
  expect(conflicts('query.save', 'ctrl+s', {}).map((s) => s.id)).toEqual([])
  // Ctrl+K is the palette everywhere in a workspace.
  expect(conflicts('editor.format', 'ctrl+k', {}).map((s) => s.id)).toEqual(['ws.palette'])
  expect(conflicts('query.save', 'ctrl+enter', {}).map((s) => s.id)).toEqual(['editor.runCurrent'])
  // Moving the palette away removes the clash.
  expect(conflicts('editor.format', 'ctrl+k', { 'ws.palette': 'ctrl+shift+p' })).toEqual([])
})

test('saved settings are checked, and the accent text stays readable', () => {
  expect(normalize({ theme: 'neon', accent: 'nope', editorFontSize: 99, shortcuts: { a: 'ctrl+e', b: 5 } }))
    .toMatchObject({ theme: 'system', accent: 'indigo', editorFontSize: 32, shortcuts: { a: 'ctrl+e' } })
  expect(normalize({ accent: '#FFCC00', editorFontSize: '15' })).toMatchObject({ accent: '#FFCC00', editorFontSize: 15 })
  expect(accentColors('#ffcc00').lightFg).toBe('#111116') // yellow takes dark text
  expect(accentColors('#1e3a8a').lightFg).toBe('#ffffff')
  expect(accentColors('teal').lightFg).toBe('#ffffff') // a mid tone: white reads better
  expect(accentColors('amber').darkFg).toBe('#111116') // dark mode's bright shades take dark text
  expect(accentColors('teal')).toMatchObject({ light: '#0f8a7e', dark: '#3fd0c0' })
  // A custom colour gets a lighter shade for the dark theme.
  const c = accentColors('#1e3a8a')
  expect(c.dark).not.toBe(c.light)
})

test('panels start open, closed, or as they were last left', () => {
  // By default, as last left: open until closed once.
  const d = normalize({})
  expect(d.panels).toEqual(DEFAULTS.panels)
  expect(panelOpen(d, 'sidebar')).toBe(true)
  const s = normalize({ panels: { rail: 'closed', sidebar: 'open', inspector: 'sideways' }, lastPanels: { inspector: false, rail: 'yes' } })
  expect(s.panels).toEqual({ rail: 'closed', sidebar: 'open', inspector: 'last' })
  expect(panelOpen(s, 'rail')).toBe(false)
  expect(panelOpen(s, 'sidebar')).toBe(true)
  expect(panelOpen(s, 'inspector')).toBe(false) // last left closed
  expect(s.lastPanels.rail).toBe(true) // not a boolean: the default
})

test('update settings: checked once a day by default, skipped version kept', () => {
  expect(normalize({}).updates).toEqual({ auto: true, skipped: '', lastCheck: 0 })
  expect(normalize({ updates: { auto: false, skipped: 'v0.7.0', lastCheck: 123 } }).updates).toEqual({ auto: false, skipped: 'v0.7.0', lastCheck: 123 })
  expect(normalize({ updates: { auto: 'yes', skipped: 7, lastCheck: 'x' } }).updates).toEqual({ auto: true, skipped: '', lastCheck: 0 })
})
