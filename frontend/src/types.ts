export type Driver = 'postgres' | 'mysql' | 'mariadb' | 'sqlite' | 'sqlserver' | 'oracle' | 'duckdb' | 'clickhouse'
  | 'cockroachdb' | 'yugabytedb' | 'timescaledb' | 'neon' | 'supabase'
  | 'tidb' | 'percona' | 'singlestore' | 'planetscale'
/** Family: the engine whose protocol and SQL dialect a driver uses. */
export type Family = 'postgres' | 'mysql' | 'sqlite' | 'sqlserver' | 'oracle' | 'duckdb' | 'clickhouse'

/** family maps compatible servers and services to the engine they follow. */
export const family = (d: Driver): Family => driverInfo(d).family

/** PerDriver: a value for each family, optionally overridden for one driver. */
export type PerDriver<T> = Record<Family, T> & Partial<Record<Driver, T>>
export const forDriver = <T,>(m: PerDriver<T>, d: Driver): T => m[d] ?? m[family(d)]

/** Features: what a family's database can do, so the UI offers only that. */
export interface Features {
  /** Row edits and imports commit (or roll back) as one transaction. */
  transactions: boolean
  foreignKeys: boolean
  /** CHECK constraints can be added to an existing table. */
  checks: boolean
  renameIndex: boolean
  /** The primary key can be changed after the table is created. */
  primaryKey: boolean
  uniqueIndexes: boolean
  autoIncrement: boolean
  /** DROP … CASCADE drops dependent views and keys too. */
  cascade: boolean
  /** Databases hold schemas (else a database is the schema level). */
  schemas: boolean
  /** Files instead of a server. */
  file: boolean
  comments: boolean
  collations: boolean
  /** Index methods/types offered when adding an index. */
  indexMethods: string[]
  /** New databases or schemas can be made from the app. */
  createDatabase: boolean
  /** A SQL import can run as one transaction (undone on error). */
  importTransaction: boolean
  /** Foreign keys take ON UPDATE actions. */
  onUpdate: boolean
}

const BASE: Features = { transactions: true, foreignKeys: true, checks: true, renameIndex: true, primaryKey: true, uniqueIndexes: true,
  autoIncrement: true, cascade: false, schemas: false, file: false, comments: true, collations: true, indexMethods: [],
  createDatabase: true, importTransaction: true, onUpdate: true }

export const FEATURES: Record<Family, Features> = {
  postgres: { ...BASE, cascade: true, schemas: true, indexMethods: ['btree', 'hash', 'gin', 'gist', 'brin', 'spgist'] },
  mysql: { ...BASE, transactions: true, indexMethods: ['btree', 'hash'] },
  sqlite: { ...BASE, checks: false, primaryKey: true, file: true, comments: false },
  sqlserver: { ...BASE, schemas: true },
  // A schema is a user; DDL commits by itself; foreign keys have no ON UPDATE.
  oracle: { ...BASE, cascade: true, schemas: true, collations: false, createDatabase: false, importTransaction: false, onUpdate: false },
  duckdb: { ...BASE, foreignKeys: false, checks: false, renameIndex: false, primaryKey: false, schemas: true, file: true },
  clickhouse: { ...BASE, transactions: false, importTransaction: false, foreignKeys: false, renameIndex: false, primaryKey: false, uniqueIndexes: false,
    autoIncrement: false, collations: false, indexMethods: ['minmax', 'set(100)', 'bloom_filter', 'ngrambf_v1(3, 256, 2, 0)', 'tokenbf_v1(256, 2, 0)'] },
}
/** DRIVER_FEATURES: where a server differs from its family's engine. */
const DRIVER_FEATURES: Partial<Record<Driver, Partial<Features> & { tableOptions?: boolean }>> = {
  // A distributed database: no foreign keys or CHECKs; the primary key is
  // the shard key, and table options are fixed at creation.
  singlestore: { foreignKeys: false, checks: false, renameIndex: false, primaryKey: false, tableOptions: false },
}
export const features = (d: Driver): Features => ({ ...FEATURES[family(d)], ...DRIVER_FEATURES[d] })
/** tableOptions: storage engine, collation and AUTO_INCREMENT can change (MySQL family). */
export const tableOptions = (d: Driver) => family(d) === 'mysql' && DRIVER_FEATURES[d]?.tableOptions !== false

export type Tag = '' | 'local' | 'development' | 'testing' | 'staging' | 'production'

export interface Connection {
  id: string
  name: string
  group_id?: string
  color?: string
  tag?: Tag
  driver: Driver
  host?: string
  port?: number
  user?: string
  database?: string
  path?: string
  ssl_mode?: string
  ssl_key_path?: string
  ssl_cert_path?: string
  ssl_ca_path?: string
  use_ssh_tunnel?: boolean
  ssh_host?: string
  ssh_port?: number
  ssh_user?: string
  ssh_key_path?: string
  rows_per_page?: number
  limit_query_rows?: number
  selected_schema?: string
  saved?: { password: boolean; sshPassword: boolean; sshPassphrase: boolean }
}

export interface Group { id: string; name: string; expanded: boolean }

export interface Secrets { password?: string | null; sshPassword?: string | null; sshPassphrase?: string | null }

export interface ApiErrorBody { title: string; hint?: string; detail: string }

export interface SessionInfo {
  id: string
  connection: Connection
  version: string
  /** database is the PostgreSQL database, MySQL default database or SQLite file. */
  database: string
  schema: string
  schemas: string[]
}

export interface DbObject { schema: string; name: string; kind: 'table' | 'view' }

export interface Column {
  name: string
  type: string
  nullable: boolean
  default: string | null
  primaryKey: boolean
  comment: string
  /** values are the choices of an ENUM or SET column. */
  values?: string[]
  /** references is set for a single-column foreign key. */
  references?: ForeignRef
  /** collation is set when the column has its own (MySQL/MariaDB) or a non-default one (PostgreSQL). */
  collation?: string
  autoIncrement?: boolean
  /** onUpdateNow: ON UPDATE CURRENT_TIMESTAMP (MySQL/MariaDB). */
  onUpdateNow?: boolean
  /** defaultKind/defaultText: the default as the column editor takes it (a literal value or an SQL expression). */
  defaultKind?: '' | 'value' | 'expression' | 'null'
  defaultText?: string
}

/** ForeignRef is a foreign key: `from` columns match `columns` (same order) of schema.table. */
export interface ForeignRef { name?: string; schema: string; table: string; from: string[]; columns: string[]; onDelete?: string; onUpdate?: string }

export interface Index {
  name: string; columns: string[]; unique: boolean; primary: boolean
  /** type is the method or kind: btree, hash, gin, fulltext, spatial… */
  type?: string
  /** constraint: made by a PRIMARY KEY or UNIQUE constraint. */
  constraint?: boolean
}

export interface Check { name: string; expression: string }

/** TableInfo: the comment, MySQL/MariaDB options and CHECK constraints of a table. */
export interface TableInfo { comment: string; engine: string; collation: string; autoIncrement: number; checks: Check[] }

export type Cell = string | null

export interface RowsResult { columns: string[]; types: string[]; rows: Cell[][]; total: number; ms: number }

export type FilterOp =
  | '=' | '<>' | '<' | '>' | '<=' | '>=' | 'IN' | 'NOT IN' | 'IS NULL' | 'IS NOT NULL' | 'BETWEEN'
  | 'LIKE' | 'NOT LIKE' | 'Contains' | 'Not contains' | 'Begins with' | 'Ends with'

export interface Filter { id: string; column: string; op: FilterOp; value: string; value2: string; enabled: boolean }

export interface Sort { column: string; dir: 'asc' | 'desc' }

export interface QueryResult {
  statement: string
  columns?: string[]
  types?: string[]
  rows?: Cell[][]
  hasRows: boolean
  rowsAffected: number
  ms: number
  truncated: boolean
  error?: ApiErrorBody
}

/**
 * DRIVERS in display order: the engines, then the servers and cloud services
 * compatible with one of them. color is the badge background for engines
 * without a logo (see lib/driverLogos); user is the usual administrator
 * account (the User field's placeholder); hosted services require TLS.
 */
export const DRIVERS: { id: Driver; label: string; family: Family; port: number; color: string; user: string; hosted?: boolean; host?: string; database?: string }[] = [
  { id: 'postgres', label: 'PostgreSQL', family: 'postgres', port: 5432, color: '#336791', user: 'postgres' },
  { id: 'mysql', label: 'MySQL', family: 'mysql', port: 3306, color: '#00758f', user: 'root' },
  { id: 'mariadb', label: 'MariaDB', family: 'mysql', port: 3306, color: '#b4644a', user: 'root' },
  { id: 'sqlite', label: 'SQLite', family: 'sqlite', port: 0, color: '#3b82c4', user: '' },
  { id: 'sqlserver', label: 'SQL Server', family: 'sqlserver', port: 1433, color: '#a91d22', user: 'sa' },
  { id: 'oracle', label: 'Oracle', family: 'oracle', port: 1521, color: '#c74634', user: 'system', database: 'service name, e.g. FREEPDB1' },
  { id: 'duckdb', label: 'DuckDB', family: 'duckdb', port: 0, color: '#8a6d00', user: '' },
  { id: 'clickhouse', label: 'ClickHouse', family: 'clickhouse', port: 9000, color: '#9a8a00', user: 'default' },
  { id: 'cockroachdb', label: 'CockroachDB', family: 'postgres', port: 26257, color: '#6933ff', user: 'root' },
  { id: 'yugabytedb', label: 'YugabyteDB', family: 'postgres', port: 5433, color: '#c4471f', user: 'yugabyte' },
  { id: 'timescaledb', label: 'TimescaleDB', family: 'postgres', port: 5432, color: '#a3610b', user: 'postgres' },
  { id: 'neon', label: 'Neon', family: 'postgres', port: 5432, color: '#047857', user: 'neondb_owner', hosted: true, host: 'ep-…-pooler.region.aws.neon.tech' },
  { id: 'supabase', label: 'Supabase', family: 'postgres', port: 5432, color: '#1f7a4f', user: 'postgres', hosted: true, host: 'db.<project>.supabase.co' },
  { id: 'tidb', label: 'TiDB', family: 'mysql', port: 4000, color: '#c0130a', user: 'root' },
  { id: 'percona', label: 'Percona Server', family: 'mysql', port: 3306, color: '#b5341a', user: 'root' },
  { id: 'singlestore', label: 'SingleStore', family: 'mysql', port: 3306, color: '#7a1fa2', user: 'root' },
  { id: 'planetscale', label: 'PlanetScale', family: 'mysql', port: 3306, color: '#1f2328', user: '', hosted: true, host: 'aws.connect.psdb.cloud' },
]

/** ENGINES come first in the picker; the rest are compatible with one of them. */
export const ENGINES: Driver[] = ['postgres', 'mysql', 'mariadb', 'sqlite', 'sqlserver', 'oracle', 'duckdb', 'clickhouse']

export const driverInfo = (d: Driver) => DRIVERS.find((x) => x.id === d) ?? DRIVERS[0]

export const TAGS: { id: Tag; label: string; color: string }[] = [
  { id: '', label: 'None', color: '#8b8b9c' },
  { id: 'local', label: 'Local', color: '#7a7a8c' },
  { id: 'development', label: 'Development', color: '#1f9d6a' },
  { id: 'testing', label: 'Testing', color: '#3b82c4' },
  { id: 'staging', label: 'Staging', color: '#c97c10' },
  { id: 'production', label: 'Production', color: '#dc3e42' },
]

export const tagInfo = (t?: Tag) => TAGS.find((x) => x.id === (t ?? '')) ?? TAGS[0]

/** SSL_MODES: the first is the default. Hosted services only take TLS. */
export const SSL_MODES: PerDriver<string[]> = {
  postgres: ['prefer', 'disable', 'allow', 'require', 'verify-ca', 'verify-full'],
  mysql: ['preferred', 'disabled', 'required', 'verify_ca', 'verify_identity'],
  sqlite: [],
  duckdb: [],
  /** optional: only the login is encrypted (and a self-signed certificate is accepted); trust: encrypted, any certificate. */
  sqlserver: ['optional', 'disable', 'required', 'trust', 'strict'],
  clickhouse: ['disable', 'require', 'skip-verify'],
  oracle: ['disable', 'require', 'skip-verify'],
  neon: ['require', 'verify-full', 'verify-ca'],
  supabase: ['require', 'verify-full', 'verify-ca'],
  planetscale: ['required', 'verify_identity', 'verify_ca'],
}

export const FILTER_OPS: FilterOp[] = ['=', '<>', '<', '>', '<=', '>=', 'IN', 'NOT IN', 'IS NULL', 'IS NOT NULL', 'BETWEEN',
  'LIKE', 'NOT LIKE', 'Contains', 'Not contains', 'Begins with', 'Ends with']

export const opNeedsValue = (op: FilterOp) => op !== 'IS NULL' && op !== 'IS NOT NULL'

export interface ImportErrorItem { line: number; statement: string; message: string }

export interface Job {
  id: string
  kind: 'export-table' | 'export-database' | 'import-sql' | 'import-csv'
  title: string
  status: 'running' | 'done' | 'failed' | 'cancelled'
  phase: string
  done: number
  total: number
  unit: 'bytes' | 'rows'
  rows: number
  statements: number
  failed: number
  errors: ImportErrorItem[]
  error?: ApiErrorBody
  file?: string
  size: number
  started: string
  finished?: string
}

export interface FileSource { uploadId?: string; path?: string }

export interface SavedQuery {
  id: string
  connectionId: string
  database: string
  schema: string
  name: string
  sql: string
  updated: string
}

export interface HistoryEntry {
  id: string
  connectionId: string
  database: string
  schema: string
  kind: 'query' | 'commit'
  sql: string
  at: string
  ms: number
  rows: number
  error?: string
}
