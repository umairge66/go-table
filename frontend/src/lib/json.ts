// JSON that loses nothing: numbers keep their digits (a 64-bit integer is
// more than JavaScript's numbers hold), objects their order and all keys.

export type JNode =
  | { t: 'object'; e: [string, JNode][] }
  | { t: 'array'; v: JNode[] }
  | { t: 'string'; v: string }
  | { t: 'number'; raw: string }
  | { t: 'bool'; v: boolean }
  | { t: 'null' }

export type JPath = (string | number)[]

export class JSONError extends Error {
  pos: number
  line: number
  constructor(message: string, text: string, pos: number) {
    const before = text.slice(0, pos).split('\n')
    super(`Line ${before.length}, column ${before[before.length - 1].length + 1}: ${message}`)
    this.pos = pos
    this.line = before.length
  }
}

export function parseJSON(text: string): JNode {
  let i = 0
  const fail = (msg: string, at = i): never => { throw new JSONError(msg, text, at) }
  const space = () => { while (i < text.length && ' \t\r\n'.includes(text[i])) i++ }
  const str = (): string => {
    const start = i
    i++
    for (;;) {
      const ch = text[i]
      if (ch === undefined || ch === '\n') return fail('the text has no closing "', start)
      if (ch === '\\') i += 2
      else if (ch === '"') break
      else i++
    }
    i++
    try { return JSON.parse(text.slice(start, i)) as string } catch { return fail('this text has an escape JSON doesn’t know', start) }
  }
  const value = (depth: number): JNode => {
    space()
    if (depth > 500) fail('nested too deeply')
    const ch = text[i]
    if (ch === '{') {
      const e: [string, JNode][] = []
      const keys = new Set<string>()
      i++
      space()
      if (text[i] === '}') { i++; return { t: 'object', e } }
      for (;;) {
        space()
        if (text[i] !== '"') fail(text[i] === '}' ? 'a comma too many' : 'a field name in "quotes" goes here')
        const at = i
        const k = str()
        if (keys.has(k)) fail(`the field "${k}" is there twice`, at)
        keys.add(k)
        space()
        if (text[i] !== ':') fail('a : goes here')
        i++
        e.push([k, value(depth + 1)])
        space()
        if (text[i] === ',') { i++; continue }
        if (text[i] === '}') { i++; return { t: 'object', e } }
        fail(text[i] === undefined ? 'the object has no closing }' : 'a comma or } goes here')
      }
    }
    if (ch === '[') {
      const v: JNode[] = []
      i++
      space()
      if (text[i] === ']') { i++; return { t: 'array', v } }
      for (;;) {
        space()
        if (text[i] === ']') fail('a comma too many')
        v.push(value(depth + 1))
        space()
        if (text[i] === ',') { i++; continue }
        if (text[i] === ']') { i++; return { t: 'array', v } }
        fail(text[i] === undefined ? 'the list has no closing ]' : 'a comma or ] goes here')
      }
    }
    if (ch === '"') return { t: 'string', v: str() }
    const word = /^[A-Za-z0-9+\-.]+/.exec(text.slice(i, i + 400))?.[0] ?? ''
    const at = i
    i += word.length
    if (word === 'true' || word === 'false') return { t: 'bool', v: word === 'true' }
    if (word === 'null') return { t: 'null' }
    if (isNumber(word)) return { t: 'number', raw: word }
    return fail(ch === undefined ? 'a value is missing' : ch === "'" ? 'texts go in "double quotes"' : `${word || ch} isn’t JSON (texts go in "quotes")`, at)
  }
  const out = value(0)
  space()
  if (i < text.length) fail('there is text after the end')
  return out
}

export const isNumber = (s: string) => /^-?(0|[1-9]\d*)(\.\d+)?([eE][+-]?\d+)?$/.test(s)

export function compactJSON(n: JNode): string {
  switch (n.t) {
    case 'object': return '{' + n.e.map(([k, v]) => JSON.stringify(k) + ':' + compactJSON(v)).join(',') + '}'
    case 'array': return '[' + n.v.map(compactJSON).join(',') + ']'
    case 'string': return JSON.stringify(n.v)
    case 'number': return n.raw
    case 'bool': return String(n.v)
  }
  return 'null'
}

const isScalar = (n: JNode) => n.t !== 'object' && n.t !== 'array'

/** formatJSON indents, keeping what is short on one line. */
export function formatJSON(n: JNode, indent = ''): string {
  const inner = indent + '  '
  if (n.t === 'object') {
    if (!n.e.length) return '{}'
    // A value with a type of its own, such as {"$oid": "…"}, is one thing.
    if (n.e.length === 1 && n.e[0][0].startsWith('$') && isScalar(n.e[0][1])) return `{ ${JSON.stringify(n.e[0][0])}: ${formatJSON(n.e[0][1])} }`
    return '{\n' + n.e.map(([k, v]) => inner + JSON.stringify(k) + ': ' + formatJSON(v, inner)).join(',\n') + '\n' + indent + '}'
  }
  if (n.t === 'array') {
    if (!n.v.length) return '[]'
    if (n.v.every(isScalar)) {
      const line = '[' + n.v.map(compactJSON).join(', ') + ']'
      if (line.length <= 80) return line
    }
    return '[\n' + n.v.map((v) => inner + formatJSON(v, inner)).join(',\n') + '\n' + indent + ']'
  }
  return compactJSON(n)
}

/** pretty formats text that should be JSON (and leaves it alone if it isn't). */
export function pretty(text: string): string {
  try { return formatJSON(parseJSON(text)) } catch { return text }
}

export function getAt(n: JNode, path: JPath): JNode | undefined {
  let cur: JNode | undefined = n
  for (const p of path) {
    if (cur?.t === 'object') cur = cur.e.find(([k]) => k === p)?.[1]
    else if (cur?.t === 'array') cur = cur.v[p as number]
    else return undefined
  }
  return cur
}

/** updateAt returns a copy in which the node at path is fn's; undefined removes it. */
export function updateAt(n: JNode, path: JPath, fn: (n: JNode) => JNode | undefined): JNode {
  if (!path.length) return fn(n) ?? n
  const [p, ...rest] = path
  if (n.t === 'object') {
    return { t: 'object', e: n.e.flatMap(([k, v]): [string, JNode][] => {
      if (k !== p) return [[k, v]]
      if (rest.length) return [[k, updateAt(v, rest, fn)]]
      const next = fn(v)
      return next ? [[k, next]] : []
    }) }
  }
  if (n.t === 'array') {
    return { t: 'array', v: n.v.flatMap((v, i) => {
      if (i !== p) return [v]
      if (rest.length) return [updateAt(v, rest, fn)]
      const next = fn(v)
      return next ? [next] : []
    }) }
  }
  return n
}

export function renameAt(n: JNode, path: JPath, name: string): JNode {
  const parent = path.slice(0, -1)
  const key = path[path.length - 1]
  return updateAt(n, parent, (o) => (o.t === 'object' ? { t: 'object', e: o.e.map(([k, v]): [string, JNode] => [k === key ? name : k, v]) } : o))
}

// ---------------------------------------------------------------- types

/** JType: what a value is called, and how it is changed as text. */
export interface JType {
  label: string
  /** text: the value as it is shown and edited; undefined for containers. */
  text?: string
  /** read turns edited text back; throws with a reason. Missing: it can't be edited as text. */
  read?: (text: string) => JNode
  /** items: a container's children (a set's, inside its wrapper). */
  items?: { path: JPath; key: string; node: JNode }[]
  array?: boolean
}

const str = (v: string): JNode => ({ t: 'string', v })
const obj = (k: string, v: JNode): JNode => ({ t: 'object', e: [[k, v]] })

/** Types Extended JSON (MongoDB) writes as an object of one field. */
const EXTENDED: Record<string, { label: string; check?: RegExp; bad?: string }> = {
  $oid: { label: 'ObjectId', check: /^[0-9a-fA-F]{24}$/, bad: 'An ObjectId is 24 hexadecimal digits.' },
  $numberLong: { label: 'Long', check: /^-?\d+$/, bad: 'A Long is a whole number.' },
  $numberInt: { label: 'Int', check: /^-?\d+$/, bad: 'An Int is a whole number.' },
  $numberDouble: { label: 'Double', check: /^(-?Infinity|NaN|-?\d.*)$/, bad: 'A Double is a number.' },
  $numberDecimal: { label: 'Decimal', check: /^(-?Infinity|NaN|[+-]?(\d+\.?\d*|\.\d+)([eE][+-]?\d+)?)$/, bad: 'A Decimal is a number.' },
  $date: { label: 'Date' },
  $symbol: { label: 'Symbol' },
  $code: { label: 'Code' },
  $uuid: { label: 'UUID', check: /^[0-9a-fA-F]{8}(-[0-9a-fA-F]{4}){3}-[0-9a-fA-F]{12}$/, bad: 'A UUID is written 8-4-4-4-12 hexadecimal digits.' },
  $minKey: { label: 'MinKey' },
  $maxKey: { label: 'MaxKey' },
}

/** DynamoDB's types that JSON lacks. */
const SETS: Record<string, string> = { $ss: 'String set', $ns: 'Number set', $bs: 'Binary set' }

function base64Hex(b64: string) {
  try { return Array.from(atob(b64), (c) => c.charCodeAt(0).toString(16).padStart(2, '0')).join('') } catch { return '' }
}

export function typeOf(n: JNode): JType {
  switch (n.t) {
    case 'string': return { label: 'String', text: n.v, read: str }
    case 'number': return { label: 'Number', text: n.raw, read: (s) => { if (!isNumber(s.trim())) throw new Error('This isn’t a number. Change its type to store a text.'); return { t: 'number', raw: s.trim() } } }
    case 'bool': return { label: 'Boolean', text: String(n.v), read: (s) => ({ t: 'bool', v: s === 'true' }) }
    case 'null': return { label: 'Null', text: 'null' }
    case 'array': return { label: 'Array', array: true, items: n.v.map((node, i) => ({ path: [i], key: String(i), node })) }
  }
  if (n.e.length === 1) {
    const [k, v] = n.e[0]
    if (SETS[k] && v.t === 'array') return { label: SETS[k], array: true, items: v.v.map((node, i) => ({ path: [k, i], key: String(i), node })) }
    if (k === '$b' && v.t === 'string') return { label: 'Binary', text: '0x' + base64Hex(v.v) }
    const x = EXTENDED[k]
    if (x && k === '$date') {
      const long = v.t === 'object' && v.e[0]?.[0] === '$numberLong' && v.e[0][1].t === 'string' ? v.e[0][1].v : null
      if (v.t === 'string' || long !== null) {
        return { label: 'Date', text: v.t === 'string' ? v.v : long!, read: (s) => {
          const t = s.trim()
          if (/^-?\d+$/.test(t)) return obj('$date', obj('$numberLong', str(t)))
          if (!/^\d{4}-\d{2}-\d{2}([T ]\d{2}:\d{2}(:\d{2}(\.\d+)?)?(Z|[+-]\d{2}:?\d{2})?)?$/.test(t)) throw new Error('A date is written like 2026-01-31T10:20:30Z.')
          const full = t.length === 10 ? t + 'T00:00:00Z' : t.replace(' ', 'T').replace(/^(.{11}\d{2}:\d{2})(?!:)/, '$1:00')
          return obj('$date', str(/(Z|[+-]\d{2}:?\d{2})$/.test(full) ? full : full + 'Z'))
        } }
      }
    } else if (x && v.t === 'string') {
      return { label: x.label, text: v.v, read: (s) => { if (x.check && !x.check.test(s.trim())) throw new Error(x.bad); return obj(k, str(s.trim())) } }
    } else if (x && v.t === 'number') return { label: x.label, text: x.label }
    if (k === '$binary' && v.t === 'object') {
      const data = v.e.find(([f]) => f === 'base64')?.[1]
      const sub = v.e.find(([f]) => f === 'subType')?.[1]
      const hex = data?.t === 'string' ? base64Hex(data.v) : ''
      if (sub?.t === 'string' && (sub.v === '04' || sub.v === '03') && hex.length === 32) {
        return { label: 'UUID', text: [hex.slice(0, 8), hex.slice(8, 12), hex.slice(12, 16), hex.slice(16, 20), hex.slice(20)].join('-') }
      }
      return { label: 'Binary', text: '0x' + hex }
    }
    if (k === '$regularExpression' && v.t === 'object') {
      const part = (f: string) => { const p = v.e.find(([a]) => a === f)?.[1]; return p?.t === 'string' ? p.v : '' }
      return { label: 'Regex', text: `/${part('pattern')}/${part('options')}` }
    }
    if (k === '$timestamp' && v.t === 'object') return { label: 'Timestamp', text: compactJSON(v) }
  }
  return { label: 'Object', items: n.e.map(([k, node]) => ({ path: [k], key: k, node })) }
}

/** CONVERT: the types a value can be changed to, by name. */
export function conversions(dialect: 'mongo' | 'dynamo' | 'plain'): { label: string; make: (text: string) => JNode }[] {
  const num = (s: string): JNode => ({ t: 'number', raw: isNumber(s.trim()) ? s.trim() : '0' })
  const base: { label: string; make: (text: string) => JNode }[] = [
    { label: 'String', make: str },
    { label: 'Number', make: num },
    { label: 'Boolean', make: (s) => ({ t: 'bool', v: s === 'true' || s === '1' }) },
    { label: 'Null', make: () => ({ t: 'null' }) },
    { label: 'Object', make: () => ({ t: 'object', e: [] }) },
    { label: 'Array', make: () => ({ t: 'array', v: [] }) },
  ]
  if (dialect === 'mongo') {
    const date = (s: string) => { try { return typeOf(obj('$date', str('2026-01-01T00:00:00Z'))).read!(s) } catch { return obj('$date', str(new Date().toISOString())) } }
    base.push(
      { label: 'Date', make: date },
      { label: 'ObjectId', make: (s) => obj('$oid', str(/^[0-9a-fA-F]{24}$/.test(s) ? s : newObjectId())) },
      { label: 'Long', make: (s) => obj('$numberLong', str(/^-?\d+$/.test(s.trim()) ? s.trim() : '0')) },
      { label: 'Decimal', make: (s) => obj('$numberDecimal', str(isNumber(s.trim()) ? s.trim() : '0')) },
    )
  }
  if (dialect === 'dynamo') {
    base.push(
      { label: 'String set', make: (s) => obj('$ss', { t: 'array', v: [str(s || 'a')] }) },
      { label: 'Number set', make: (s) => obj('$ns', { t: 'array', v: [num(s)] }) },
    )
  }
  return base
}

function newObjectId() {
  const b = crypto.getRandomValues(new Uint8Array(8))
  return Math.floor(Date.now() / 1000).toString(16).padStart(8, '0') + Array.from(b, (x) => x.toString(16).padStart(2, '0')).join('')
}
