// Column types offered by the table designer, grouped like phpMyAdmin's
// type list, with what each one takes after its name.
import type { Driver } from '../types'

/** Param says what goes in brackets after the type name. */
export type Param =
  | { kind: 'length'; placeholder: string; required?: boolean; initial?: string }
  | { kind: 'precision'; placeholder: string; initial?: string } // p,s
  | { kind: 'values'; placeholder: string; initial?: string } // 'a','b'
  | { kind: 'fsp' } // fractional seconds, 0–6

export interface SqlType {
  name: string
  hint: string
  param?: Param
  /** int: can auto-increment and be UNSIGNED; text: takes a collation; time: ON UPDATE. */
  traits?: ('int' | 'unsigned' | 'text' | 'time')[]
}

export interface TypeGroup { label: string; types: SqlType[] }

const len = (placeholder: string, initial?: string, required = false): Param => ({ kind: 'length', placeholder, initial, required })

const MYSQL_NUMERIC: SqlType[] = [
  { name: 'TINYINT', hint: '1 byte, -128 to 127', traits: ['int', 'unsigned'] },
  { name: 'SMALLINT', hint: '2 bytes, ±32 thousand', traits: ['int', 'unsigned'] },
  { name: 'MEDIUMINT', hint: '3 bytes, ±8 million', traits: ['int', 'unsigned'] },
  { name: 'INT', hint: '4 bytes, ±2.1 billion', traits: ['int', 'unsigned'] },
  { name: 'BIGINT', hint: '8 bytes, ±9.2 quintillion', traits: ['int', 'unsigned'] },
  { name: 'DECIMAL', hint: 'exact number, e.g. money', param: { kind: 'precision', placeholder: '10,2', initial: '10,2' }, traits: ['unsigned'] },
  { name: 'FLOAT', hint: 'approximate, 4 bytes', traits: ['unsigned'] },
  { name: 'DOUBLE', hint: 'approximate, 8 bytes', traits: ['unsigned'] },
  { name: 'BIT', hint: 'bit field', param: len('1–64', '1') },
  { name: 'BOOLEAN', hint: 'alias for TINYINT(1)' },
]
const MYSQL_TIME: SqlType[] = [
  { name: 'DATE', hint: 'YYYY-MM-DD' },
  { name: 'DATETIME', hint: 'date and time', param: { kind: 'fsp' }, traits: ['time'] },
  { name: 'TIMESTAMP', hint: 'date and time, stored in UTC', param: { kind: 'fsp' }, traits: ['time'] },
  { name: 'TIME', hint: 'hh:mm:ss', param: { kind: 'fsp' } },
  { name: 'YEAR', hint: 'four-digit year' },
]
const MYSQL_STRING: SqlType[] = [
  { name: 'VARCHAR', hint: 'variable text up to the length', param: len('length', '255', true), traits: ['text'] },
  { name: 'CHAR', hint: 'fixed-length text', param: len('length', '1'), traits: ['text'] },
  { name: 'TEXT', hint: 'up to 64 KB of text', traits: ['text'] },
  { name: 'TINYTEXT', hint: 'up to 255 bytes of text', traits: ['text'] },
  { name: 'MEDIUMTEXT', hint: 'up to 16 MB of text', traits: ['text'] },
  { name: 'LONGTEXT', hint: 'up to 4 GB of text', traits: ['text'] },
  { name: 'BINARY', hint: 'fixed-length bytes', param: len('length', '1') },
  { name: 'VARBINARY', hint: 'variable bytes', param: len('length', '255', true) },
  { name: 'BLOB', hint: 'up to 64 KB of bytes' },
  { name: 'TINYBLOB', hint: 'up to 255 bytes' },
  { name: 'MEDIUMBLOB', hint: 'up to 16 MB of bytes' },
  { name: 'LONGBLOB', hint: 'up to 4 GB of bytes' },
  { name: 'ENUM', hint: 'one of a list', param: { kind: 'values', placeholder: "'a','b','c'", initial: "'a','b'" }, traits: ['text'] },
  { name: 'SET', hint: 'any of a list', param: { kind: 'values', placeholder: "'a','b','c'", initial: "'a','b'" }, traits: ['text'] },
]
const MYSQL_SPATIAL: SqlType[] = ['GEOMETRY', 'POINT', 'LINESTRING', 'POLYGON', 'MULTIPOINT', 'MULTILINESTRING', 'MULTIPOLYGON', 'GEOMETRYCOLLECTION']
  .map((name) => ({ name, hint: 'spatial' }))

const CATALOG: Record<Driver, TypeGroup[]> = {
  mysql: [
    { label: 'Numeric', types: MYSQL_NUMERIC },
    { label: 'Date and time', types: MYSQL_TIME },
    { label: 'String', types: MYSQL_STRING },
    { label: 'JSON', types: [{ name: 'JSON', hint: 'validated JSON document' }] },
    { label: 'Spatial', types: MYSQL_SPATIAL },
  ],
  mariadb: [
    { label: 'Numeric', types: MYSQL_NUMERIC },
    { label: 'Date and time', types: MYSQL_TIME },
    { label: 'String', types: MYSQL_STRING },
    { label: 'Other', types: [
      { name: 'UUID', hint: 'UUID (MariaDB 10.7+)' }, { name: 'INET4', hint: 'IPv4 address (10.10+)' }, { name: 'INET6', hint: 'IPv6 address' },
      { name: 'JSON', hint: 'JSON text (checked)' },
    ] },
    { label: 'Spatial', types: MYSQL_SPATIAL },
  ],
  postgres: [
    { label: 'Numeric', types: [
      { name: 'smallint', hint: '2 bytes', traits: ['int'] },
      { name: 'integer', hint: '4 bytes, ±2.1 billion', traits: ['int'] },
      { name: 'bigint', hint: '8 bytes', traits: ['int'] },
      { name: 'numeric', hint: 'exact, e.g. money', param: { kind: 'precision', placeholder: '10,2', initial: '10,2' } },
      { name: 'real', hint: 'approximate, 4 bytes' },
      { name: 'double precision', hint: 'approximate, 8 bytes' },
      { name: 'money', hint: 'currency amount' },
    ] },
    { label: 'Text', types: [
      { name: 'text', hint: 'any length', traits: ['text'] },
      { name: 'varchar', hint: 'text with a maximum length', param: len('length', '255'), traits: ['text'] },
      { name: 'char', hint: 'fixed-length text', param: len('length', '1'), traits: ['text'] },
    ] },
    { label: 'Date and time', types: [
      { name: 'date', hint: 'calendar date' },
      { name: 'timestamptz', hint: 'date and time with time zone', param: { kind: 'fsp' } },
      { name: 'timestamp', hint: 'date and time, no time zone', param: { kind: 'fsp' } },
      { name: 'time', hint: 'time of day', param: { kind: 'fsp' } },
      { name: 'interval', hint: 'a duration' },
    ] },
    { label: 'Boolean', types: [{ name: 'boolean', hint: 'true / false' }] },
    { label: 'JSON', types: [{ name: 'jsonb', hint: 'binary JSON, indexable' }, { name: 'json', hint: 'JSON text' }] },
    { label: 'Other', types: [
      { name: 'uuid', hint: 'UUID' }, { name: 'bytea', hint: 'binary data' }, { name: 'inet', hint: 'IP address' },
      { name: 'cidr', hint: 'IP network' }, { name: 'macaddr', hint: 'MAC address' }, { name: 'point', hint: 'x,y point' },
      { name: 'xml', hint: 'XML document' }, { name: 'tsvector', hint: 'full-text search document' },
    ] },
  ],
  sqlite: [
    { label: 'Storage classes', types: [
      { name: 'INTEGER', hint: 'whole numbers', traits: ['int'] },
      { name: 'REAL', hint: 'floating point' },
      { name: 'TEXT', hint: 'text', traits: ['text'] },
      { name: 'BLOB', hint: 'bytes, stored as given' },
      { name: 'NUMERIC', hint: 'numbers, kept exact when possible' },
      { name: 'ANY', hint: 'anything (STRICT tables)' },
    ] },
    { label: 'Common names (stored by affinity)', types: [
      { name: 'VARCHAR', hint: 'stored as TEXT', param: len('length', '255'), traits: ['text'] },
      { name: 'BOOLEAN', hint: 'stored as 0 / 1' },
      { name: 'DATE', hint: 'stored as text or number' },
      { name: 'DATETIME', hint: 'stored as text or number' },
      { name: 'DECIMAL', hint: 'stored as NUMERIC', param: { kind: 'precision', placeholder: '10,2' } },
      { name: 'BIGINT', hint: 'stored as INTEGER' },
      { name: 'DOUBLE', hint: 'stored as REAL' },
    ] },
  ],
}

/** typeGroups lists the built-in types plus the database's own types. */
export function typeGroups(driver: Driver, custom: string[] = []): TypeGroup[] {
  const groups = CATALOG[driver]
  return custom.length ? [...groups, { label: 'Defined in this database', types: custom.map((name) => ({ name, hint: 'custom type', traits: ['text'] })) }] : groups
}

export function findType(driver: Driver, name: string, custom: string[] = []): SqlType | undefined {
  const n = name.toLowerCase()
  for (const g of typeGroups(driver, custom)) for (const t of g.types) if (t.name.toLowerCase() === n) return t
  return undefined
}

export const has = (t: SqlType | undefined, trait: NonNullable<SqlType['traits']>[number]) => !!t?.traits?.includes(trait)

/** TypeSpec is a column type as the designer edits it. */
export interface TypeSpec {
  base: string // a catalog name, or the full text of a custom type
  param: string // what goes in brackets
  unsigned: boolean
  array: boolean // PostgreSQL []
  custom: boolean // base is free text
}

export function typeString(s: TypeSpec): string {
  if (s.custom) return s.base.trim()
  let t = s.base
  if (s.param.trim()) t += `(${s.param.trim()})`
  if (s.unsigned) t += ' UNSIGNED'
  if (s.array) t += '[]'
  return t
}

/** parseType turns a type string back into a spec (for prefilled rows). */
export function parseType(driver: Driver, type: string, custom: string[] = []): TypeSpec {
  const m = /^\s*([A-Za-z][A-Za-z0-9_ ]*?)\s*(?:\((.*)\))?\s*(unsigned)?\s*(\[\])?\s*$/i.exec(type)
  const base = m && findType(driver, m[1], custom)
  if (!m || !base) return { base: type, param: '', unsigned: false, array: false, custom: true }
  return { base: base.name, param: m[2] ?? '', unsigned: !!m[3], array: !!m[4], custom: false }
}

/** paramProblem explains a missing or malformed bracket value, or ''. */
export function paramProblem(t: SqlType | undefined, param: string): string {
  const p = t?.param
  const v = param.trim()
  if (!p) return ''
  if (!v) return p.kind === 'length' && p.required ? `${t!.name} needs a length` : p.kind === 'values' ? `${t!.name} needs a list of values` : ''
  if (p.kind === 'length' && !/^\d+$/.test(v)) return 'length is a whole number'
  if (p.kind === 'precision' && !/^\d+(\s*,\s*\d+)?$/.test(v)) return 'precision looks like 10 or 10,2'
  if (p.kind === 'fsp' && !/^[0-6]$/.test(v)) return 'fractional seconds is 0–6'
  if (p.kind === 'values' && !/^'([^']|'')*'(\s*,\s*'([^']|'')*')*$/.test(v)) return "values look like 'a','b'"
  return ''
}

/** groupOf names the group a type is listed under ('' for unknown types). */
export function groupOf(driver: Driver, name: string, custom: string[] = []): string {
  const n = name.toLowerCase()
  return typeGroups(driver, custom).find((g) => g.types.some((t) => t.name.toLowerCase() === n))?.label ?? ''
}

/** category sorts a type group into a colour family for the designer. */
export function category(group: string): 'number' | 'text' | 'time' | 'json' | 'bool' | 'other' {
  const g = group.toLowerCase()
  if (g.startsWith('numeric') || g === 'storage classes') return 'number'
  if (g.startsWith('string') || g.startsWith('text')) return 'text'
  if (g.startsWith('date')) return 'time'
  if (g === 'json') return 'json'
  if (g === 'boolean') return 'bool'
  return 'other'
}
