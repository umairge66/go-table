// The operating system the window runs on, for examples shown in the UI.

export const isWindows = typeof navigator !== 'undefined' && /Windows/i.test(navigator.userAgent)

/** examplePath shows a path in the user's home folder in the platform's style. */
export const examplePath = (rel: string) => (isWindows ? `C:\\Users\\you\\${rel.replace(/\//g, '\\')}` : `/home/you/${rel}`)
