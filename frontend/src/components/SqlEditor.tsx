import { autocompletion, closeBrackets, closeBracketsKeymap, completionKeymap } from '@codemirror/autocomplete'
import { defaultKeymap, history, historyKeymap, indentWithTab, toggleComment } from '@codemirror/commands'
import { MSSQL, MySQL, PLSQL, PostgreSQL, SQLDialect, SQLite, sql } from '@codemirror/lang-sql'
import { javascript, javascriptLanguage } from '@codemirror/lang-javascript'
import { HighlightStyle, bracketMatching, indentOnInput, syntaxHighlighting } from '@codemirror/language'
import { highlightSelectionMatches, searchKeymap } from '@codemirror/search'
import { Compartment, EditorState, Prec, RangeSetBuilder, StateField } from '@codemirror/state'
import { Decoration, EditorView, GutterMarker, drawSelection, gutterLineClass, highlightActiveLineGutter, keymap, lineNumbers, placeholder } from '@codemirror/view'
import { tags as t } from '@lezer/highlight'
import { forwardRef, useEffect, useImperativeHandle, useRef } from 'react'
import { mongoCompletions, mongoStatementAt, mongoStatementRange, pickStatement, trimRange } from '../lib/mongoShell'
import { elasticCompletions, elasticLanguage, elasticStatementRange } from '../lib/elasticConsole'
import { influxStatementRange } from '../lib/influx'
import { forDriver, isBigQuery, isElastic, isInflux, isMongo, isRedis, isSnowflake, type Driver, type PerDriver } from '../types'
import { redisCompletions, redisLanguage, redisStatementAt, redisStatementRange } from '../lib/redisShell'
import { useSettings } from '../lib/settings'
import { codemirrorKey, combosFor, label } from '../lib/shortcuts'

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

// DuckDB writes SQL like PostgreSQL; ClickHouse quotes names with backticks like MySQL.
const Snowflake = SQLDialect.define({ ...PostgreSQL.spec, keywords: PostgreSQL.spec.keywords + ' warehouse undrop qualify ilike rlike regexp pivot unpivot flatten lateral sample tablesample at before clone swap stage pipe stream task dynamic transient copy put list describe show use minus autoincrement cluster comment variant',
  types: 'number decimal numeric int integer bigint smallint tinyint byteint float double real decfloat varchar char string text binary varbinary boolean date datetime time timestamp timestamp_ltz timestamp_ntz timestamp_tz variant object array map geography geometry vector',
  slashComments: true, backslashEscapes: true, doubleDollarQuotedStrings: true, caseInsensitiveIdentifiers: true })
const BigQuery = SQLDialect.define({ ...MySQL.spec, keywords: PostgreSQL.spec.keywords + ' struct unnest qualify except replace partition cluster options description declare exception safe_cast offset ordinal enforced pivot unpivot tablesample system ml export load data clone snapshot temp',
  types: 'int64 float64 numeric bignumeric bool string bytes date datetime time timestamp json geography interval array struct range', hashComments: true, slashComments: false, backslashEscapes: true, doubleQuotedStrings: true, identifierQuotes: '`' })
const dialects: PerDriver<SQLDialect> = { bigquery: BigQuery, snowflake: Snowflake, postgres: PostgreSQL, mysql: MySQL, mariadb: MySQL, sqlite: SQLite, sqlserver: MSSQL, oracle: PLSQL, duckdb: PostgreSQL, clickhouse: MySQL, mongodb: PostgreSQL, redis: PostgreSQL, cassandra: PostgreSQL, dynamodb: PostgreSQL, elasticsearch: PostgreSQL, influxdb: PostgreSQL }

/** language: MongoDB's commands are written in the JavaScript of its shell; Redis's are command lines. */
const language = (driver: Driver, tables?: Record<string, string[]>) => isMongo(driver)
  ? [javascript(), javascriptLanguage.data.of({ autocomplete: mongoCompletions(tables) })]
  : isRedis(driver) ? [redisLanguage, redisLanguage.data.of({ autocomplete: redisCompletions })]
  : isElastic(driver) ? [elasticLanguage, elasticLanguage.data.of({ autocomplete: elasticCompletions(tables) })]
  : sql({ dialect: forDriver(dialects, driver), schema: tables && cleanSchema(tables), upperCaseKeywords: true })

/** cleanSchema drops tables without a column list: CodeMirror's SQL
 * completion fails on them, and takes the whole editor down. */
const cleanSchema = (tables: Record<string, string[]>) =>
  Object.fromEntries(Object.entries(tables).filter(([, cols]) => Array.isArray(cols)))

/** rangeAt: the statement the cursor is at, the way the driver's commands are written. */
const rangeAt = (driver: Driver, text: string, pos: number, withSemicolon = false) =>
  isMongo(driver) ? mongoStatementRange(text, pos) : isRedis(driver) ? redisStatementRange(text, pos) : isElastic(driver) ? elasticStatementRange(text, pos) : isInflux(driver) ? influxStatementRange(text, pos, (t, p) => statementRange(t, p, withSemicolon)) : statementRange(text, pos, withSemicolon, isSnowflake(driver) ? 'snowflake' : isBigQuery(driver) ? 'bigquery' : undefined)

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
  const r = statementRange(text, pos)
  return r ? text.slice(r[0], r[1]) : ''
}

/**
 * statementRange: where the statement that runs for the cursor at pos starts
 * and ends, without surrounding space; null when the cursor isn't at one
 * (see pickStatement).
 */
export function statementRange(text: string, pos: number, withSemicolon = false, style?: 'snowflake' | 'bigquery'): [number, number] | null {
  const snowflake = style === 'snowflake', bigquery = style === 'bigquery'
  const bounds: [number, number][] = []
  let start = 0
  let q: string | null = null
  for (let i = 0; i < text.length; i++) {
    const ch = text[i]
    if (q) {
      if ((snowflake && q === "'" || bigquery && q !== '`') && ch === '\\') { i++; continue }
      if (q.length === 3 ? text.startsWith(q, i) : ch === q) { i += q.length - 1; q = null }
      continue
    }
    if (bigquery && (text.startsWith("'''", i) || text.startsWith('"""', i))) { q = text.slice(i, i + 3); i += 2 }
    else if (ch === "'" || ch === '"' || ch === '`') q = ch
    else if (ch === '-' && text[i + 1] === '-' || snowflake && ch === '/' && text[i + 1] === '/' || bigquery && ch === '#') { const nl = text.indexOf('\n', i); i = nl < 0 ? text.length : nl }
    else if (ch === '/' && text[i + 1] === '*') { const end = text.indexOf('*/', i + 2); i = end < 0 ? text.length : end + 1 }
    else if (ch === '$' && /^\$[A-Za-z_]*\$/.test(text.slice(i))) {
      const tag = /^\$[A-Za-z_]*\$/.exec(text.slice(i))![0]
      const end = text.indexOf(tag, i + tag.length)
      i = end < 0 ? text.length : end + tag.length - 1
    } else if (ch === ';') { bounds.push([start, i]); start = i + 1 }
  }
  bounds.push([start, text.length])
  const nonEmpty = bounds.filter(([a, b]) => text.slice(a, b).trim())
  const hit = pickStatement(text, nonEmpty.map((r) => trimRange(text, r)), pos)
  // Run leaves out the closing ;, the highlight takes it in.
  return hit && withSemicolon && text[hit[1]] === ';' ? [hit[0], hit[1] + 1] : hit
}

/** Statements in very long scripts aren't tracked as the cursor moves: scanning would lag. */
const HIGHLIGHT_LIMIT = 300_000

/**
 * statementHighlight marks every line of the statement that Run would run
 * for the cursor: a tinted block with an accent bar, and accent line numbers.
 * With a selection, the selection is what runs, so nothing is marked.
 */
function statementHighlight(driver: () => Driver) {
  const line = Decoration.line({ class: 'cm-statement-highlight' })
  const marker = new (class extends GutterMarker { elementClass = 'cm-statement-gutter' })()
  const compute = (state: EditorState) => {
    const sel = state.selection.main
    if (!sel.empty || state.doc.length === 0 || state.doc.length > HIGHLIGHT_LIMIT) return null
    const text = state.doc.toString()
    const r = rangeAt(driver(), text, sel.head, true)
    if (!r || r[0] >= r[1]) return null
    return { first: state.doc.lineAt(r[0]).number, last: state.doc.lineAt(r[1]).number }
  }
  const field = StateField.define<{ first: number; last: number } | null>({
    create: compute,
    update: (v, tr) => (tr.docChanged || tr.selection ? compute(tr.state) : v),
  })
  const lines = (state: EditorState, build: (from: number) => void) => {
    const r = state.field(field)
    if (!r) return
    for (let n = r.first; n <= r.last; n++) build(state.doc.line(n).from)
  }
  return [
    field,
    EditorView.decorations.compute([field], (state) => {
      const b = new RangeSetBuilder<Decoration>()
      lines(state, (from) => b.add(from, from, line))
      return b.finish()
    }),
    gutterLineClass.compute([field], (state) => {
      const b = new RangeSetBuilder<GutterMarker>()
      lines(state, (from) => b.add(from, from, marker))
      return b.finish()
    }),
  ]
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
  /** fontSize: this editor's text size; the default from Settings otherwise. */
  fontSize?: number
  autoFocus?: boolean
}>(function SqlEditor(p, ref) {
  const host = useRef<HTMLDivElement>(null)
  const view = useRef<EditorView | null>(null)
  const lang = useRef(new Compartment())
  const keys = useRef(new Compartment())
  const shortcuts = useSettings().shortcuts
  const cb = useRef(p)
  cb.current = p

  useEffect(() => {
    const v = new EditorView({
      parent: host.current!,
      state: EditorState.create({
        doc: p.value,
        extensions: [
          lineNumbers(), highlightActiveLineGutter(), history(), drawSelection(), indentOnInput(), bracketMatching(),
          closeBrackets(), autocompletion({ icons: false }), highlightSelectionMatches(),
          // The statement that runs, rather than just the cursor's line.
          p.readOnly ? [] : statementHighlight(() => cb.current.driver),
          syntaxHighlighting(highlight),
          lang.current.of(language(p.driver, p.tables)),
          EditorState.readOnly.of(!!p.readOnly),
          EditorView.editable.of(!p.readOnly),
          keys.current.of(editorKeys(shortcuts, p.driver, cb)),
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
    view.current?.dispatch({ effects: lang.current.reconfigure(language(p.driver, p.tables)) })
  }, [p.driver, p.tables])

  // Shortcuts changed in Settings apply to open editors at once.
  useEffect(() => {
    view.current?.dispatch({ effects: keys.current.reconfigure(editorKeys(shortcuts, p.driver, cb)) })
  }, [shortcuts, p.driver])

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
      if (from !== to) return text.slice(from, to)
      if (isRedis(cb.current.driver)) return redisStatementAt(text, from)
      return isMongo(cb.current.driver) ? mongoStatementAt(text, from) : statementAt(text, from)
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

  return <div ref={host} className="h-full overflow-hidden" data-testid="sql-editor"
    style={p.fontSize ? { '--editor-font-size': `${p.fontSize}px` } as React.CSSProperties : undefined} />
})

type Callbacks = { current: { onRunCurrent?: () => void; onRunAll?: () => void; onFormat?: () => void } }

/**
 * editorKeys: the editor's own shortcuts, from the settings, and the hint
 * shown in an empty editor. They come first so that the defaults (Ctrl+I,
 * Ctrl+/) don't take them.
 */
function editorKeys(overrides: Record<string, string>, driver: Driver, cb: Callbacks) {
  const bind = (id: string, run: (v: EditorView) => boolean) => combosFor(id, overrides).map((c) => ({ key: codemirrorKey(c), run }))
  const call = (f: (() => void) | undefined) => () => { f?.(); return true }
  const hint = (id: string, what: string) => { const k = label(combosFor(id, overrides)[0] ?? ''); return k ? `${k.replace(/ /g, '+')} ${what}` : '' }
  const tips = [hint('editor.runCurrent', 'runs the statement under the cursor'), hint('editor.runAll', 'runs everything'), hint('editor.format', 'formats')].filter(Boolean)
  return [
    placeholder((isMongo(driver) ? 'db.collection.find({ … })' : isRedis(driver) ? 'GET key   ·   HGETALL key   ·   one command a line' : 'Write SQL…') + (tips.length ? '   ' + tips.join(' · ') : '')),
    Prec.highest(keymap.of([
      ...bind('editor.runCurrent', () => call(cb.current.onRunCurrent)()),
      ...bind('editor.runAll', () => call(cb.current.onRunAll)()),
      ...bind('editor.comment', toggleComment),
      ...bind('editor.format', () => call(cb.current.onFormat)()),
    ])),
  ]
}
