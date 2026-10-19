// Makes the app window behave like a desktop application rather than a web
// page: browser shortcuts, the browser context menu and file-drop navigation
// are switched off.

/** Browser shortcuts with no meaning in GoTable (checked after the app had its turn). */
const BROWSER_KEYS = new Set([
  'ctrl+s', 'ctrl+p', 'ctrl+f', 'ctrl+g', 'ctrl+shift+g', 'ctrl+h', 'ctrl+j', 'ctrl+d', 'ctrl+l', 'ctrl+e', 'ctrl+k',
  'ctrl+n', 'ctrl+t', 'ctrl+o', 'ctrl+u', 'ctrl+r', 'ctrl+shift+r', 'ctrl+shift+n', 'ctrl+shift+t', 'ctrl+shift+o',
  'ctrl+shift+b', 'ctrl+shift+delete', 'ctrl+shift+i', 'ctrl+shift+j', 'ctrl+shift+c',
  'f1', 'f3', 'f5', 'ctrl+f5', 'shift+f5', 'f6', 'f7', 'f12', 'alt+arrowleft', 'alt+arrowright', 'alt+home',
  'browserback', 'browserforward', 'browserrefresh',
])

export function comboOf(e: KeyboardEvent) {
  const parts: string[] = []
  if (e.ctrlKey || e.metaKey) parts.push('ctrl')
  if (e.altKey) parts.push('alt')
  if (e.shiftKey) parts.push('shift')
  parts.push(e.key.toLowerCase())
  return parts.join('+')
}

const isTextTarget = (t: EventTarget | null) =>
  t instanceof HTMLElement && (t.isContentEditable || t.closest('input, textarea, .cm-editor') !== null)

let installed = false

export function installNativeFeel() {
  if (installed) return
  installed = true

  // Bubble phase on window: components (CodeMirror, dialogs, grid) handle
  // their shortcuts first; whatever is left must not reach the browser.
  window.addEventListener('keydown', (e) => {
    const combo = comboOf(e)
    // F5 / Ctrl+R refresh the data in the app instead of reloading the page.
    if (combo === 'f5' || combo === 'ctrl+r' || combo === 'ctrl+shift+r' || combo === 'ctrl+f5' || combo === 'shift+f5') {
      e.preventDefault()
      window.dispatchEvent(new CustomEvent('gotable:refresh-shortcut'))
      return
    }
    if (!e.defaultPrevented && BROWSER_KEYS.has(combo)) e.preventDefault()
  })

  // The browser menu (Back, Reload, Inspect…) is not part of the app; text
  // fields keep it for copy and paste.
  window.addEventListener('contextmenu', (e) => {
    if (!isTextTarget(e.target)) e.preventDefault()
  })

  // A file dropped outside a drop zone would make the window navigate to it.
  window.addEventListener('dragover', (e) => e.preventDefault())
  window.addEventListener('drop', (e) => e.preventDefault())

  // Middle-click and Ctrl+click on links would open browser tabs.
  window.addEventListener('auxclick', (e) => { if (e.button === 1) e.preventDefault() })

  // Closing the window never asks: like closing the app, uncommitted table
  // edits are discarded. (No beforeunload handler on purpose.)
}
