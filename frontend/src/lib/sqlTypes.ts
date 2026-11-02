// Column types offered by the table designer, grouped like phpMyAdmin's
// type list, with what each one takes after its name.
import { forDriver, type Driver, type PerDriver } from '../types'

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

const CATALOG: PerDriver<TypeGroup[]> = {
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
  sqlserver: [
    { label: 'Numeric', types: [
      { name: 'int', hint: '4 bytes, ±2.1 billion', traits: ['int'] },
      { name: 'bigint', hint: '8 bytes', traits: ['int'] },
      { name: 'smallint', hint: '2 bytes', traits: ['int'] },
      { name: 'tinyint', hint: '0 to 255', traits: ['int'] },
      { name: 'decimal', hint: 'exact number, e.g. money', param: { kind: 'precision', placeholder: '18,2', initial: '18,2' } },
      { name: 'float', hint: 'approximate, 8 bytes' },
      { name: 'real', hint: 'approximate, 4 bytes' },
      { name: 'money', hint: 'currency amount' },
      { name: 'bit', hint: '0 / 1 (true / false)' },
    ] },
    { label: 'Text', types: [
      { name: 'nvarchar', hint: 'Unicode text up to the length (or max)', param: len('length or max', '255', true), traits: ['text'] },
      { name: 'varchar', hint: 'text in the collation\'s code page', param: len('length or max', '255', true), traits: ['text'] },
      { name: 'nchar', hint: 'fixed-length Unicode text', param: len('length', '1'), traits: ['text'] },
      { name: 'char', hint: 'fixed-length text', param: len('length', '1'), traits: ['text'] },
    ] },
    { label: 'Date and time', types: [
      { name: 'date', hint: 'calendar date' },
      { name: 'datetime2', hint: 'date and time', param: { kind: 'fsp' } },
      { name: 'datetimeoffset', hint: 'date and time with offset', param: { kind: 'fsp' } },
      { name: 'time', hint: 'time of day', param: { kind: 'fsp' } },
      { name: 'datetime', hint: 'older date and time (3 ms steps)' },
    ] },
    { label: 'Other', types: [
      { name: 'uniqueidentifier', hint: 'GUID' }, { name: 'varbinary', hint: 'bytes', param: len('length or max', 'max', true) },
      { name: 'xml', hint: 'XML document' }, { name: 'rowversion', hint: 'changes on every update' },
    ] },
  ],
  oracle: [
    { label: 'Numeric', types: [
      { name: 'NUMBER', hint: 'exact number, any size', param: { kind: 'precision', placeholder: '10,2' }, traits: ['int'] },
      { name: 'INTEGER', hint: 'whole number (NUMBER(38))', traits: ['int'] },
      { name: 'BINARY_DOUBLE', hint: 'approximate, 8 bytes' },
      { name: 'BINARY_FLOAT', hint: 'approximate, 4 bytes' },
      { name: 'FLOAT', hint: 'approximate number' },
    ] },
    { label: 'Text', types: [
      { name: 'VARCHAR2', hint: 'text up to the length', param: len('length', '255', true), traits: ['text'] },
      { name: 'NVARCHAR2', hint: 'Unicode text up to the length', param: len('length', '255', true), traits: ['text'] },
      { name: 'CHAR', hint: 'fixed-length text', param: len('length', '1'), traits: ['text'] },
      { name: 'CLOB', hint: 'text of any length' },
    ] },
    { label: 'Date and time', types: [
      { name: 'DATE', hint: 'date and time, to the second' },
      { name: 'TIMESTAMP', hint: 'date and time with fractions', param: { kind: 'fsp' } },
      { name: 'TIMESTAMP WITH TIME ZONE', hint: 'with its time zone' },
      { name: 'INTERVAL DAY TO SECOND', hint: 'a duration' },
    ] },
    { label: 'Other', types: [
      { name: 'BLOB', hint: 'bytes of any length' }, { name: 'RAW', hint: 'bytes up to the length', param: len('length', '16', true) },
      { name: 'BOOLEAN', hint: 'true / false (23ai)' }, { name: 'JSON', hint: 'JSON document (21c)' },
    ] },
  ],
  duckdb: [
    { label: 'Numeric', types: [
      { name: 'INTEGER', hint: '4 bytes', traits: ['int'] },
      { name: 'BIGINT', hint: '8 bytes', traits: ['int'] },
      { name: 'SMALLINT', hint: '2 bytes', traits: ['int'] },
      { name: 'HUGEINT', hint: '16 bytes', traits: ['int'] },
      { name: 'UBIGINT', hint: 'unsigned, 8 bytes', traits: ['int'] },
      { name: 'DECIMAL', hint: 'exact number', param: { kind: 'precision', placeholder: '18,3', initial: '18,3' } },
      { name: 'DOUBLE', hint: 'approximate, 8 bytes' },
      { name: 'FLOAT', hint: 'approximate, 4 bytes' },
    ] },
    { label: 'Text', types: [{ name: 'VARCHAR', hint: 'text of any length', traits: ['text'] }] },
    { label: 'Date and time', types: [
      { name: 'DATE', hint: 'calendar date' },
      { name: 'TIMESTAMP', hint: 'date and time' },
      { name: 'TIMESTAMPTZ', hint: 'date and time with time zone' },
      { name: 'TIME', hint: 'time of day' },
      { name: 'INTERVAL', hint: 'a duration' },
    ] },
    { label: 'Other', types: [
      { name: 'BOOLEAN', hint: 'true / false' }, { name: 'UUID', hint: 'UUID' }, { name: 'BLOB', hint: 'bytes' },
      { name: 'JSON', hint: 'JSON document' },
      { name: 'ENUM', hint: 'one of a list', param: { kind: 'values', placeholder: "'a','b','c'", initial: "'a','b'" }, traits: ['text'] },
    ] },
  ],
  clickhouse: [
    { label: 'Numeric', types: [
      { name: 'Int32', hint: '4 bytes', traits: ['int'] }, { name: 'Int64', hint: '8 bytes', traits: ['int'] },
      { name: 'UInt32', hint: 'unsigned, 4 bytes', traits: ['int'] }, { name: 'UInt64', hint: 'unsigned, 8 bytes', traits: ['int'] },
      { name: 'UInt8', hint: '0 to 255', traits: ['int'] }, { name: 'Int16', hint: '2 bytes', traits: ['int'] },
      { name: 'Decimal', hint: 'exact number', param: { kind: 'precision', placeholder: '18,4', initial: '18,4' } },
      { name: 'Float64', hint: 'approximate, 8 bytes' }, { name: 'Float32', hint: 'approximate, 4 bytes' },
    ] },
    { label: 'Text', types: [
      { name: 'String', hint: 'bytes of any length' },
      { name: 'LowCardinality(String)', hint: 'text with few distinct values (dictionary-encoded)' },
      { name: 'FixedString', hint: 'exactly N bytes', param: len('bytes', '16', true) },
    ] },
    { label: 'Date and time', types: [
      { name: 'Date', hint: 'calendar date (1970–2149)' },
      { name: 'Date32', hint: 'calendar date (1900–2299)' },
      { name: 'DateTime', hint: 'date and time, seconds' },
      { name: 'DateTime64', hint: 'date and time with sub-seconds', param: len('precision', '3', true) },
    ] },
    { label: 'Other', types: [
      { name: 'Bool', hint: 'true / false' }, { name: 'UUID', hint: 'UUID' }, { name: 'IPv4', hint: 'IPv4 address' }, { name: 'IPv6', hint: 'IPv6 address' },
      { name: 'Enum8', hint: 'one of a list', param: { kind: 'values', placeholder: "'a' = 1, 'b' = 2", initial: "'a' = 1, 'b' = 2" } },
      { name: 'Array(String)', hint: 'list of text' }, { name: 'Map(String, String)', hint: 'key/value pairs' },
    ] },
  ],
}

/** typeGroups lists the built-in types plus the database's own types. */
export function typeGroups(driver: Driver, custom: string[] = []): TypeGroup[] {
  const groups = forDriver(CATALOG, driver)
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
