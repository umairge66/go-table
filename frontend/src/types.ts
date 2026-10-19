export type Driver = 'postgres' | 'mysql' | 'mariadb' | 'sqlite'
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
  /** references is set for a single-column foreign key. */
  references?: ForeignRef
}

/** ForeignRef is a foreign key: `from` columns match `columns` (same order) of schema.table. */
export interface ForeignRef { schema: string; table: string; from: string[]; columns: string[] }

export interface Index { name: string; columns: string[]; unique: boolean; primary: boolean }

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

/** color is the badge background for engines without a logo (see lib/driverLogos). */
export const DRIVERS: { id: Driver; label: string; port: number; color: string }[] = [
  { id: 'postgres', label: 'PostgreSQL', port: 5432, color: '#336791' },
  { id: 'mysql', label: 'MySQL', port: 3306, color: '#00758f' },
  { id: 'mariadb', label: 'MariaDB', port: 3306, color: '#b4644a' },
  { id: 'sqlite', label: 'SQLite', port: 0, color: '#3b82c4' },
]

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

export const SSL_MODES: Record<Driver, string[]> = {
  postgres: ['prefer', 'disable', 'allow', 'require', 'verify-ca', 'verify-full'],
  mysql: ['preferred', 'disabled', 'required', 'verify_ca', 'verify_identity'],
  mariadb: ['preferred', 'disabled', 'required', 'verify_ca', 'verify_identity'],
  sqlite: [],
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
