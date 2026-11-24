// The query editor on Elasticsearch and OpenSearch: requests as Kibana's Dev
// Tools takes them, SQL and ES|QL (internal/db/elastic_query.go cuts a script
// up the same way).

import type { CompletionContext, CompletionResult } from '@codemirror/autocomplete'
import { StreamLanguage } from '@codemirror/language'
import { pickStatement, trimRange } from './mongoShell'

const REQUEST = /^\s*(GET|POST|PUT|DELETE|HEAD|PATCH)\s+\S+[ \t]*/
const WORD = /^\s*[A-Za-z]/
const comment = (line: string) => /^\s*(#|\/\/)/.test(line)

/** Scan follows JSON's brackets and strings. */
class Scan {
  depth = 0
  str = false
  esc = false
  triple = false
  get open() { return this.depth > 0 || this.triple }
  feed(line: string) {
    for (let i = 0; i < line.length; i++) {
      const ch = line[i]
      if (this.triple) {
        if (line.startsWith('"""', i)) { this.triple = false; i += 2 }
      } else if (this.str) {
        if (this.esc) this.esc = false
        else if (ch === '\\') this.esc = true
        else if (ch === '"' || ch === '\n') this.str = false
      } else if (line.startsWith('"""', i)) { this.triple = true; i += 2 }
      else if (ch === '"') this.str = true
      else if (ch === '{' || ch === '[') this.depth++
      else if (ch === '}' || ch === ']') this.depth--
    }
  }
}

/** elasticStatements: the range of each request, SQL or ES|QL statement. */
export function elasticStatements(text: string): [number, number][] {
  const out: [number, number][] = []
  let start = -1
  let end = 0
  let request = false
  let js = new Scan()
  const flush = () => {
    if (start >= 0 && text.slice(start, end).trim()) out.push(trimRange(text, [start, end]))
    start = -1
  }
  for (let at = 0; at < text.length;) {
    const nl = text.indexOf('\n', at)
    const next = nl < 0 ? text.length : nl + 1
    const line = text.slice(at, next)
    const inside = start >= 0 && request && js.open
    const m = inside ? null : REQUEST.exec(line)
    if (m) {
      flush()
      start = at; end = next; request = true; js = new Scan()
      js.feed(line.slice(m[0].length))
    } else if (inside || (start >= 0 && request && !WORD.test(line))) {
      if (inside || !comment(line)) {
        js.feed(line)
        if (line.trim()) end = next
      }
    } else if (!line.trim() || comment(line) || line.trim().startsWith('--')) {
      if (start >= 0) end = next
    } else {
      if (request) flush()
      request = false
      if (start < 0) start = at
      end = next
      if (line.trim().endsWith(';')) flush()
    }
    at = next
  }
  flush()
  return out
}

export const elasticStatementRange = (text: string, pos: number) => pickStatement(text, elasticStatements(text), pos)

/** formatElastic indents the JSON bodies; bulk bodies (a line each) and SQL stay. */
export function formatElastic(text: string): string {
  let out = ''
  let pos = 0
  for (const [a, b] of elasticStatements(text)) {
    out += text.slice(pos, a)
    pos = b
    const s = text.slice(a, b)
    const m = REQUEST.exec(s)
    const body = m ? s.slice(m[0].length).trim() : ''
    if (!m || !body || body.includes('"""') || /\/_(bulk|msearch)\b/.test(m[0])) { out += s; continue }
    JSON.parse(body) // throws: not valid
    out += m[0].trim() + '\n' + indentJSON(body)
  }
  return out + text.slice(pos)
}

/** indentJSON re-indents without reading values, so long numbers keep their digits. */
export function indentJSON(src: string): string {
  let out = ''
  let depth = 0
  const pad = () => '\n' + '  '.repeat(depth)
  for (let i = 0; i < src.length; i++) {
    const ch = src[i]
    if (ch === '"') {
      let j = i + 1
      while (j < src.length && src[j] !== '"') j += src[j] === '\\' ? 2 : 1
      out += src.slice(i, j + 1)
      i = j
    } else if (/\s/.test(ch)) continue
    else if (ch === '{' || ch === '[') {
      let j = i + 1
      while (/\s/.test(src[j] ?? '')) j++
      if (src[j] === (ch === '{' ? '}' : ']')) { out += ch + src[j]; i = j } else { depth++; out += ch + pad() }
    } else if (ch === '}' || ch === ']') { depth--; out += pad() + ch }
    else if (ch === ',') out += ch + pad()
    else if (ch === ':') out += ': '
    else out += ch
  }
  return out
}

/** elasticLanguage colours methods, paths, JSON and the words of SQL and ES|QL. */
export const elasticLanguage = StreamLanguage.define<{ triple: boolean; path: boolean }>({
  name: 'elastic',
  startState: () => ({ triple: false, path: false }),
  token(stream, state) {
    if (state.triple) {
      if (stream.skipTo('"""')) { stream.match('"""'); state.triple = false } else stream.skipToEnd()
      return 'string'
    }
    if (stream.sol()) state.path = false
    if (stream.eatSpace()) return null
    if (stream.column() === stream.indentation() && (stream.match('#') || stream.match('//') || stream.match('--'))) { stream.skipToEnd(); return 'comment' }
    if (stream.column() === stream.indentation() && stream.match(/^(GET|POST|PUT|DELETE|HEAD|PATCH)\b/)) { state.path = true; return 'keyword' }
    if (state.path) { state.path = false; stream.eatWhile(/\S/); return 'typeName' }
    if (stream.match('"""')) { state.triple = true; return 'string' }
    const q = stream.peek()
    if (q === '"' || q === "'") {
      stream.next()
      let escaped = false
      for (let c = stream.next(); c != null; c = stream.next()) {
        if (c === q && !escaped) break
        escaped = !escaped && c === '\\'
      }
      return q === '"' && /^\s*:/.test(stream.string.slice(stream.pos)) ? 'propertyName' : 'string'
    }
    if (stream.match(/^-?\d+(\.\d+)?([eE][+-]?\d+)?/)) return 'number'
    if (stream.match(/^(true|false|null)\b/)) return 'bool'
    if (stream.match(/^(select|from|where|group|order|by|having|limit|and|or|not|like|rlike|in|is|as|show|describe|row|stats|eval|keep|drop|sort|dissect|grok|enrich|rename|asc|desc|with|distinct)\b/i)) return 'keyword'
    if (stream.eatWhile(/[\w.@]/)) return null
    stream.next()
    return 'punctuation'
  },
  languageData: { commentTokens: { line: '#' } },
})

const ENDPOINTS = ['_search', '_count', '_mapping', '_settings', '_doc/', '_create/', '_update/', '_bulk', '_refresh', '_delete_by_query', '_update_by_query', '_analyze', '_explain/', '_alias', '_stats']
const CLUSTER = ['_cat/indices', '_cat/aliases', '_cat/shards', '_cat/nodes', '_cat/health', '_cluster/health', '_cluster/settings', '_aliases', '_reindex', '_data_stream', '_index_template', '_nodes/stats', '_tasks']
const METHODS = ['GET', 'POST', 'PUT', 'DELETE', 'HEAD']
const DSL = ['query', 'bool', 'must', 'filter', 'should', 'must_not', 'term', 'terms', 'match', 'match_phrase', 'multi_match', 'range', 'exists', 'prefix', 'wildcard', 'ids', 'nested', 'query_string', 'sort', 'size', 'from', '_source', 'aggs', 'avg', 'sum', 'min', 'max', 'value_count', 'cardinality', 'date_histogram', 'histogram', 'top_hits', 'mappings', 'properties', 'settings', 'type', 'number_of_shards', 'number_of_replicas']

/** elasticCompletions: methods, paths, query words and the indices' fields. */
export const elasticCompletions = (tables: Record<string, string[]> = {}) => (ctx: CompletionContext): CompletionResult | null => {
  const line = ctx.state.doc.lineAt(ctx.pos)
  const before = line.text.slice(0, ctx.pos - line.from)
  const path = /^\s*(?:GET|POST|PUT|DELETE|HEAD|PATCH)\s+\/?([^\s?]*)$/.exec(before)
  if (path) {
    const [index, rest] = path[1].includes('/') && !path[1].startsWith('_') ? [path[1].slice(0, path[1].indexOf('/')), path[1].slice(path[1].indexOf('/') + 1)] : ['', path[1]]
    const options = index ? ENDPOINTS : [...Object.keys(tables), ...CLUSTER, ...ENDPOINTS.slice(0, 2)]
    return { from: ctx.pos - rest.length, options: options.map((label) => ({ label, type: label.startsWith('_') ? 'function' : 'class' })), validFor: /^[\w./-]*$/ }
  }
  const word = /[\w.@]*$/.exec(before)![0]
  if (!word && !ctx.explicit) return null
  const atStart = before.trim() === word
  const fields = [...new Set(Object.values(tables).flatMap((c) => (Array.isArray(c) ? c : [])))]
  return {
    from: ctx.pos - word.length,
    options: [...(atStart ? METHODS.map((label) => ({ label, type: 'keyword' })) : []),
      ...fields.map((label) => ({ label, type: 'property' })), ...DSL.map((label) => ({ label, type: 'keyword' })),
      ...Object.keys(tables).map((label) => ({ label, type: 'class' }))],
    validFor: /^[\w.@]*$/,
  }
}
