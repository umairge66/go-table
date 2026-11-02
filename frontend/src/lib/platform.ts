// The operating system the window runs on, for examples shown in the UI.

export const isWindows = typeof navigator !== 'undefined' && /Windows/i.test(navigator.userAgent)
export const isMac = typeof navigator !== 'undefined' && /Mac OS X|Macintosh/i.test(navigator.userAgent)

/**
 * keys writes a shortcut the platform's way: "Ctrl+Shift+I" and "Ctrl S"
 * stay on Linux and Windows, and read ⌘⇧I and ⌘S on a Mac (the app
 * accepts ⌘ wherever it accepts Ctrl). Ctrl+Tab stays Ctrl there too.
 */
export const keys = (s: string, mac = isMac) => (mac ? macKeys(s) : s)

export function macKeys(s: string) {
  return s
    .replace(/\bCtrl(\s*\+\s*|\s)(?=Tab\b|PgUp|PgDn|Page)/g, '⌃')
    .replace(/\bCtrl(\s*\+\s*|\s)?/g, '⌘')
    .replace(/\bShift(\s*\+\s*|\s)?/g, '⇧')
    .replace(/\bAlt(\s*\+\s*|\s)?/g, '⌥')
}

/** examplePath shows a path in the user's home folder in the platform's style. */
export const examplePath = (rel: string) => (isWindows ? `C:\\Users\\you\\${rel.replace(/\//g, '\\')}` : `/home/you/${rel}`)
