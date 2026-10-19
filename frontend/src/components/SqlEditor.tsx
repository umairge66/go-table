import { autocompletion, closeBrackets, closeBracketsKeymap, completionKeymap } from '@codemirror/autocomplete'
import { defaultKeymap, history, historyKeymap, indentWithTab, toggleComment } from '@codemirror/commands'
import { MySQL, PostgreSQL, SQLite, sql, type SQLDialect } from '@codemirror/lang-sql'
import { HighlightStyle, bracketMatching, indentOnInput, syntaxHighlighting } from '@codemirror/language'
import { highlightSelectionMatches, searchKeymap } from '@codemirror/search'
import { Compartment, EditorState, Prec } from '@codemirror/state'
import { EditorView, drawSelection, highlightActiveLine, highlightActiveLineGutter, keymap, lineNumbers, placeholder } from '@codemirror/view'
import { tags as t } from '@lezer/highlight'
import { forwardRef, useEffect, useImperativeHandle, useRef } from 'react'
import type { Driver } from '../types'

const highlight = HighlightStyle.define([
  { tag: [t.keyword, t.operatorKeyword, t.modifier], color: 'var(--accent)', fontWeight: '600' },
  { tag: [t.string, t.special(t.string)], color: 'var(--ok)' },
  { tag: [t.number, t.bool, t.null], color: 'var(--warn)' },
  { tag: [t.comment, t.lineComment, t.blockComment], color: 'var(--faint)', fontStyle: 'italic' },
  { tag: [t.typeName, t.standard(t.name)], color: '#c2410c' },
  { tag: [t.function(t.variableName), t.function(t.name)], color: '#0e7490' },
  { tag: [t.punctuation, t.operator], color: 'var(--muted)' },
  { tag: t.special(t.name), color: 'var(--err)' },
])

const dialects: Record<Driver, SQLDialect> = { postgres: PostgreSQL, mysql: MySQL, mariadb: MySQL, sqlite: SQLite }

export interface SqlEditorHandle {
  /** current returns the selection, or the statement under the cursor. */
  current: () => string
  /** value returns the whole document. */
  value: () => string
  focus: () => void
  setValue: (v: string) => void
  /** transform rewrites the selection (or the whole document) in one undoable step. */
  transform: (fn: (text: string) => string) => void
}

/** statementAt finds the ;-separated statement around pos (quotes and comments aware). */
export function statementAt(text: string, pos: number): string {
  const bounds: [number, number][] = []
  let start = 0
  let q: string | null = null
  for (let i = 0; i < text.length; i++) {
    const ch = text[i]
    if (q) {
      if (ch === q) q = null
      continue
    }
    if (ch === "'" || ch === '"' || ch === '`') q = ch
    else if (ch === '-' && text[i + 1] === '-') { const nl = text.indexOf('\n', i); i = nl < 0 ? text.length : nl }
    else if (ch === '/' && text[i + 1] === '*') { const end = text.indexOf('*/', i + 2); i = end < 0 ? text.length : end + 1 }
    else if (ch === '$' && /^\$[A-Za-z_]*\$/.test(text.slice(i))) {
      const tag = /^\$[A-Za-z_]*\$/.exec(text.slice(i))![0]
      const end = text.indexOf(tag, i + tag.length)
      i = end < 0 ? text.length : end + tag.length - 1
    } else if (ch === ';') { bounds.push([start, i]); start = i + 1 }
  }
  bounds.push([start, text.length])
  const nonEmpty = bounds.filter(([a, b]) => text.slice(a, b).trim())
  const hit = nonEmpty.find(([a, b]) => pos >= a && pos <= b + 1) ?? nonEmpty.filter(([, b]) => b < pos).pop() ?? nonEmpty[0]
  return hit ? text.slice(hit[0], hit[1]).trim() : ''
}

export const SqlEditor = forwardRef<SqlEditorHandle, {
  value: string
  onChange?: (v: string) => void
  driver: Driver
  tables?: Record<string, string[]>
  readOnly?: boolean
  onRunCurrent?: () => void
  onRunAll?: () => void
  onFormat?: () => void
  autoFocus?: boolean
}>(function SqlEditor(p, ref) {
  const host = useRef<HTMLDivElement>(null)
  const view = useRef<EditorView | null>(null)
  const lang = useRef(new Compartment())
  const cb = useRef(p)
  cb.current = p

  useEffect(() => {
    const run = (all: boolean) => () => { (all ? cb.current.onRunAll : cb.current.onRunCurrent)?.(); return true }
    const v = new EditorView({
      parent: host.current!,
      state: EditorState.create({
        doc: p.value,
        extensions: [
          lineNumbers(), highlightActiveLineGutter(), history(), drawSelection(), indentOnInput(), bracketMatching(),
          closeBrackets(), autocompletion({ icons: false }), highlightActiveLine(), highlightSelectionMatches(),
          syntaxHighlighting(highlight),
          lang.current.of(sql({ dialect: dialects[p.driver], schema: p.tables, upperCaseKeywords: true })),
          placeholder('Write SQL…   Ctrl+↵ runs the statement under the cursor · Ctrl+Shift+↵ runs everything · Ctrl+I formats'),
          EditorState.readOnly.of(!!p.readOnly),
          EditorView.editable.of(!p.readOnly),
          Prec.highest(keymap.of([
            { key: 'Mod-Enter', run: run(false) },
            { key: 'Mod-Shift-Enter', run: run(true) },
            { key: 'Mod-/', run: toggleComment },
            { key: 'Mod-i', run: () => { cb.current.onFormat?.(); return true } },
          ])),
          keymap.of([...closeBracketsKeymap, ...defaultKeymap, ...searchKeymap, ...historyKeymap, ...completionKeymap, indentWithTab]),
          EditorView.updateListener.of((u) => { if (u.docChanged) cb.current.onChange?.(u.state.doc.toString()) }),
        ],
      }),
    })
    view.current = v
    if (p.autoFocus) v.focus()
    return () => v.destroy()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  useEffect(() => {
    view.current?.dispatch({ effects: lang.current.reconfigure(sql({ dialect: dialects[p.driver], schema: p.tables, upperCaseKeywords: true })) })
  }, [p.driver, p.tables])

  useEffect(() => {
    const v = view.current
    if (v && p.readOnly && v.state.doc.toString() !== p.value) {
      v.dispatch({ changes: { from: 0, to: v.state.doc.length, insert: p.value } })
    }
  }, [p.value, p.readOnly])

  useImperativeHandle(ref, () => ({
    current: () => {
      const v = view.current!
      const { from, to } = v.state.selection.main
      const text = v.state.doc.toString()
      return from !== to ? text.slice(from, to) : statementAt(text, from)
    },
    value: () => view.current?.state.doc.toString() ?? '',
    focus: () => view.current?.focus(),
    transform: (fn: (text: string) => string) => {
      const v = view.current!
      const sel = v.state.selection.main
      const [from, to] = sel.empty ? [0, v.state.doc.length] : [sel.from, sel.to]
      const out = fn(v.state.doc.sliceString(from, to))
      v.dispatch({ changes: { from, to, insert: out }, selection: { anchor: from + out.length }, scrollIntoView: true, userEvent: 'input.format' })
      v.focus()
    },
    setValue: (s: string) => {
      const v = view.current!
      v.dispatch({ changes: { from: 0, to: v.state.doc.length, insert: s } })
    },
  }))

  return <div ref={host} className="h-full overflow-hidden" data-testid="sql-editor" />
})
