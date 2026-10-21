// Which table a query result comes from, when it can be edited in place:
// a plain SELECT of columns (or *) from one table, with no joins,
// grouping, DISTINCT, unions or computed columns.

export interface SourceTable {
  /** schema is empty when the query doesn't name one. */
  schema: string
  name: string
}

const IDENT = String.raw`(?:"(?:[^"]|"")+"|\`(?:[^\`]|\`\`)+\`|\[[^\]]+\]|[A-Za-z_][\w$]*)`
// FROM [schema.]table [[AS] alias]
const FROM = new RegExp(String.raw`^(${IDENT})(?:\s*\.\s*(${IDENT}))?(?:\s+(?:as\s+)?${IDENT})?$`, 'i')
const COLUMN = new RegExp(String.raw`^(?:${IDENT}\s*\.\s*)?(?:\*|${IDENT})$`)
// Words that make a result something other than rows of one table.
const NOT_SIMPLE = /\b(join|group\s+by|having|distinct|window|over|into)\b/i
const SET_OP = /\b(union|intersect|except)\b/i

/** unquote strips "…", `…` or […] quoting from an identifier. */
export const unquote = (id: string) => {
  const q = id[0]
  if (q === '"') return id.slice(1, -1).replace(/""/g, '"')
  if (q === '`') return id.slice(1, -1).replace(/``/g, '`')
  if (q === '[') return id.slice(1, -1)
  return id
}

/** stripComments removes -- and /* *\/ comments outside string literals. */
function stripComments(sql: string) {
  return sql.replace(/('(?:[^']|'')*')|--[^\n]*|\/\*[\s\S]*?\*\//g, (_, str) => str ?? ' ')
}

export function sourceTable(statement: string): SourceTable | null {
  const sql = stripComments(statement).trim().replace(/;\s*$/, '')
  const m = /^select\s+([\s\S]+?)\s+from\s+([\s\S]+)$/i.exec(sql)
  if (!m || SET_OP.test(sql)) return null
  const [, list, rest] = m
  // Only the select list and FROM clause are checked for structure; the
  // WHERE / ORDER BY / LIMIT that follow just pick rows.
  const tail = /\s(where|order\s+by|limit|offset|fetch|for\s+update)\b/i.exec(rest)
  const from = (tail ? rest.slice(0, tail.index) : rest).trim()
  if (NOT_SIMPLE.test(list + ' ' + from) || /\(/.test(list) || /[(,]/.test(from)) return null
  if (!list.split(',').every((c) => COLUMN.test(c.trim()))) return null
  const t = FROM.exec(from)
  if (!t) return null
  return t[2] ? { schema: unquote(t[1]), name: unquote(t[2]) } : { schema: '', name: unquote(t[1]) }
}
