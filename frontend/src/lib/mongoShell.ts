// The query editor on MongoDB: shell commands instead of SQL. These helpers
// follow how the server reads them (internal/db/mongo_shell.go).

import type { CompletionContext, CompletionResult } from '@codemirror/autocomplete'
import type { SourceTable } from './sourceTable'

/** mongoColl writes how the shell reaches a collection. */
export const mongoColl = (name: string) => (/^[A-Za-z_][A-Za-z0-9_]*$/.test(name) ? `db.${name}` : `db.getCollection(${JSON.stringify(name)})`)

/**
 * walk goes through a script, skipping strings, comments and regular expressions,
 * and calls on(ch, i) for every other character.
 */
function walk(text: string, on: (ch: string, i: number, last: string) => void) {
  let last = ''
  for (let i = 0; i < text.length; i++) {
    const ch = text[i]
    if (ch === '"' || ch === "'" || ch === '`') {
      let j = i + 1
      while (j < text.length && text[j] !== ch && !(text[j] === '\n' && ch !== '`')) j += text[j] === '\\' ? 2 : 1
      i = j
      last = ch
      continue
    }
    if (ch === '/' && text[i + 1] === '/') { const nl = text.indexOf('\n', i); i = nl < 0 ? text.length : nl - 1; continue }
    if (ch === '/' && text[i + 1] === '*') { const end = text.indexOf('*/', i + 2); i = end < 0 ? text.length : end + 1; continue }
    if (ch === '/' && /^[:,([{]?$/.test(last)) {
      let j = i + 1
      let inClass = false
      while (j < text.length && text[j] !== '\n' && (text[j] !== '/' || inClass)) {
        if (text[j] === '\\') j++
        else if (text[j] === '[') inClass = true
        else if (text[j] === ']') inClass = false
        j++
      }
      i = j
      last = '/'
      continue
    }
    on(ch, i, last)
    if (!/\s/.test(ch)) last = ch
  }
}

/** mongoStatements finds each statement's range: a semicolon or a line end outside brackets ends one, unless the next line goes on with a dot. */
export function mongoStatements(text: string): [number, number][] {
  const bounds: [number, number][] = []
  let start = 0
  let depth = 0
  let seen = false
  walk(text, (ch, i, last) => {
    if (/[{[(]/.test(ch)) depth++
    else if (/[}\])]/.test(ch)) depth--
    else if (depth <= 0 && ch === ';') { bounds.push([start, i]); start = i + 1; seen = false; return }
    else if (depth <= 0 && ch === '\n' && seen && last !== '.' && !/^\s*\./.test(text.slice(i))) { bounds.push([start, i]); start = i + 1; seen = false; return }
    if (!/\s/.test(ch)) seen = true
  })
  bounds.push([start, text.length])
  return bounds
}

/** mongoStatementAt finds the statement around pos. */
export function mongoStatementAt(text: string, pos: number): string {
  const nonEmpty = mongoStatements(text).filter(([a, b]) => text.slice(a, b).replace(/\/\/[^\n]*|\/\*[\s\S]*?\*\//g, '').trim())
  const hit = nonEmpty.find(([a, b]) => pos >= a && pos <= b + 1) ?? nonEmpty.filter(([, b]) => b < pos).pop() ?? nonEmpty[0]
  return hit ? text.slice(hit[0], hit[1]).trim() : ''
}

/** closing returns the index of the bracket that closes the one at open, or -1. */
function closing(text: string, open: number): number {
  let depth = 0
  let found = -1
  walk(text.slice(open), (ch, i) => {
    if (found >= 0) return
    if (/[{[(]/.test(ch)) depth++
    else if (/[}\])]/.test(ch) && --depth === 0) found = open + i
  })
  return found
}

/**
 * mongoSourceTable names the collection a result comes from when it can be
 * edited in place: a plain find() of whole documents, picked and ordered by
 * sort, limit and skip. A projection could rename or compute fields.
 */
export function mongoSourceTable(statement: string): SourceTable | null {
  const sql = statement.trim().replace(/;\s*$/, '')
  const m = /^db\s*\.\s*(?:getCollection\(\s*(?:"([^"]+)"|'([^']+)')\s*\)|([A-Za-z_][\w.]*?))\s*\.\s*find\s*\(/.exec(sql)
  if (!m) return null
  const open = m[0].length - 1
  const close = closing(sql, open)
  if (close < 0) return null
  let commas = 0
  let depth = 0
  walk(sql.slice(open, close + 1), (ch) => {
    if (/[{[(]/.test(ch)) depth++
    else if (/[}\])]/.test(ch)) depth--
    else if (ch === ',' && depth === 1) commas++
  })
  if (commas > 0 && !/,\s*\)$/.test(sql.slice(open, close + 1))) return null
  let rest = sql.slice(close + 1)
  while (rest.trim()) {
    const c = /^\s*\.\s*(sort|limit|skip|toArray|pretty)\s*\(/.exec(rest)
    if (!c) return null
    const end = closing(rest, c[0].length - 1)
    if (end < 0) return null
    rest = rest.slice(end + 1)
  }
  return { schema: '', name: m[1] ?? m[2] ?? m[3] }
}

const COLLECTION_METHODS = ['find', 'findOne', 'aggregate', 'countDocuments', 'estimatedDocumentCount', 'distinct', 'insertOne', 'insertMany',
  'updateOne', 'updateMany', 'replaceOne', 'deleteOne', 'deleteMany', 'findOneAndUpdate', 'findOneAndReplace', 'findOneAndDelete',
  'createIndex', 'dropIndex', 'dropIndexes', 'getIndexes', 'renameCollection', 'stats', 'drop']
const CHAIN_METHODS = ['sort', 'limit', 'skip', 'project', 'hint', 'count', 'toArray']
const DB_METHODS = ['getCollection', 'getSiblingDB', 'createCollection', 'createView', 'getCollectionNames', 'getCollectionInfos', 'runCommand', 'adminCommand', 'stats', 'dropDatabase', 'serverStatus', 'version']
const OPERATORS = ['$eq', '$ne', '$gt', '$gte', '$lt', '$lte', '$in', '$nin', '$and', '$or', '$nor', '$not', '$exists', '$type', '$regex', '$options', '$expr', '$elemMatch', '$size', '$all',
  '$set', '$unset', '$inc', '$mul', '$rename', '$min', '$max', '$push', '$pull', '$addToSet', '$pop', '$currentDate', '$setOnInsert',
  '$match', '$group', '$project', '$sort', '$limit', '$skip', '$lookup', '$unwind', '$count', '$addFields', '$replaceRoot', '$facet', '$out', '$merge',
  '$sum', '$avg', '$first', '$last', '$push', '$cond', '$concat', '$toString']
const VALUES = ['ObjectId', 'ISODate', 'NumberLong', 'NumberInt', 'NumberDecimal', 'UUID', 'BinData', 'Timestamp', 'MinKey', 'MaxKey', 'true', 'false', 'null']

/** mongoCompletions completes collections, methods, operators and the fields of the collection in the statement. */
export const mongoCompletions = (tables: Record<string, string[]> | undefined) => (ctx: CompletionContext): CompletionResult | null => {
  const word = ctx.matchBefore(/[\w$.]*/)
  if (!word || (word.from === word.to && !ctx.explicit)) return null
  const opts = (names: string[], type: string) => names.map((label) => ({ label, type }))
  const after = (dot: number) => word.from + dot + 1
  const text = word.text
  if (text.startsWith('$')) return { from: word.from, options: opts([...new Set(OPERATORS)], 'keyword') }
  if (/^db\.\w*$/.test(text)) return { from: after(2), options: [...opts(Object.keys(tables ?? {}), 'class'), ...opts(DB_METHODS, 'function')] }
  if (/^db\.[\w.]+\.\w*$/.test(text)) return { from: after(text.lastIndexOf('.')), options: opts(COLLECTION_METHODS, 'function') }
  if (text.startsWith('.') || /\)\s*\.$/.test(ctx.state.sliceDoc(Math.max(0, word.from - 2), word.from + 1))) {
    return { from: word.from + text.lastIndexOf('.') + 1, options: opts(CHAIN_METHODS, 'function') }
  }
  // Inside a command: the fields of its collection.
  const before = ctx.state.sliceDoc(Math.max(0, word.from - 2000), word.from)
  const coll = [...before.matchAll(/db\.(?:getCollection\(["']([^"']+)["']\)|([A-Za-z_]\w*))/g)].pop()
  const fields = coll ? tables?.[coll[1] ?? coll[2]] ?? [] : []
  return { from: word.from, options: [...opts(fields, 'property'), ...opts(['db'], 'variable'), ...opts(VALUES, 'constant')] }
}

// ---- formatting

type Node = string | { open: string; close: string; items: Node[][] }

/** parseGroups reads a statement into text and bracketed groups split at commas. */
function parseGroups(text: string): Node[] {
  let i = 0
  const pair: Record<string, string> = { '{': '}', '[': ']', '(': ')' }
  const atoms = /^("(?:[^"\\]|\\.)*"|'(?:[^'\\]|\\.)*'|`(?:[^`\\]|\\.)*`)/
  const items = (close: string): Node[][] => {
    const out: Node[][] = [[]]
    let last = ''
    while (i < text.length) {
      const ch = text[i]
      if (close && ch === close) { i++; return out }
      if (/[}\])]/.test(ch)) throw new Error(`Unexpected ${ch}`)
      if (ch === ',') { i++; out.push([]); last = ','; continue }
      if (ch in pair) {
        i++
        const inner = items(pair[ch])
        if (inner[inner.length - 1].length === 0) inner.pop() // a trailing comma, or nothing
        out[out.length - 1].push({ open: ch, close: pair[ch], items: inner })
        last = ch
        continue
      }
      if (/\s/.test(ch)) {
        i++
        // Words stay apart: "new Date".
        if (/\w$/.test(String(out[out.length - 1].at(-1) ?? '')) && /[\w$]/.test(text[i] ?? '')) out[out.length - 1].push(' ')
        continue
      }
      const rest = text.slice(i)
      const atom = atoms.exec(rest)?.[0] ?? (ch === '/' && /^[:,([{]?$/.test(last) ? /^\/(?:\\.|\[(?:\\.|[^\]])*\]|[^/\\\n])+\/\w*/.exec(rest)?.[0] : undefined)
      const run = atom ?? /^[^\s,:{}[\]()"'`]+/.exec(rest)?.[0] ?? ch
      out[out.length - 1].push(run === ':' ? ': ' : run)
      i += run.length
      last = run.at(-1)!
    }
    if (close) throw new Error(`Missing ${close}`)
    return out
  }
  return items('')[0]
}

const flat = (nodes: Node[]): string => nodes.map((n) => {
  if (typeof n === 'string') return n
  const body = n.items.map(flat).join(', ')
  return n.open === '{' && body ? `{ ${body} }` : `${n.open}${body}${n.close}`
}).join('')

const WIDTH = 76

function render(nodes: Node[], indent: number): string {
  return nodes.map((n, idx) => {
    if (typeof n === 'string') return n
    const before = flat(nodes.slice(0, idx)).split('\n').pop()!.length
    const one = flat([n])
    if (indent + before + one.length <= WIDTH || n.items.length === 0) return one
    const pad = ' '.repeat(indent + 2)
    // A call with one document or array hugs it: find({ … }).
    if (n.open === '(' && n.items.length === 1 && n.items[0].length === 1 && typeof n.items[0][0] !== 'string') return `(${render(n.items[0], indent)})`
    return `${n.open}\n${n.items.map((it) => pad + render(it, indent + 2)).join(',\n')}\n${' '.repeat(indent)}${n.close}`
  }).join('')
}

/** breakChain puts .method() calls on lines of their own when a statement is long. */
function breakChain(nodes: Node[]): string {
  const calls: Node[][] = [[]]
  nodes.forEach((n, i) => {
    if (typeof n === 'string' && n.startsWith('.') && i > 0 && typeof nodes[i - 1] !== 'string') calls.push([])
    calls[calls.length - 1].push(n)
  })
  const one = render(nodes, 0)
  if (calls.length === 1 || (flat(nodes).length <= WIDTH && !one.includes('\n'))) return one
  return calls.map((c, i) => (i === 0 ? render(c, 0) : `  ${render(c, 2)}`)).join('\n')
}

/** formatMongo lays out shell commands. Statements with comments are left as they are. */
export function formatMongo(text: string): string {
  const out = mongoStatements(text).map(([a, b]) => text.slice(a, b).trim()).filter(Boolean).map((s) => {
    if (/\/\/|\/\*/.test(s.replace(/"(?:[^"\\]|\\.)*"|'(?:[^'\\]|\\.)*'/g, ''))) return s
    return breakChain(parseGroups(s)) + ';'
  }).join('\n')
  return text.endsWith('\n') ? out + '\n' : out
}
