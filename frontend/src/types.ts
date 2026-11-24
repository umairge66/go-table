export type Driver = 'postgres' | 'mysql' | 'mariadb' | 'sqlite' | 'sqlserver' | 'oracle' | 'duckdb' | 'clickhouse' | 'mongodb' | 'redis' | 'cassandra' | 'dynamodb' | 'elasticsearch' | 'influxdb' | 'snowflake' | 'bigquery'
  | 'cockroachdb' | 'yugabytedb' | 'timescaledb' | 'neon' | 'supabase' | 'greenplum' | 'libsql' | 'turso' | 'd1' | 'opensearch'
  | 'tidb' | 'percona' | 'singlestore' | 'planetscale' | 'starrocks' | 'doris' | 'cratedb' | 'questdb'
  | 'ferretdb' | 'documentdb' | 'valkey' | 'dragonfly' | 'keydb' | 'garnet' | 'scylladb'
/** Family: the engine whose protocol and SQL dialect a driver uses. */
export type Family = 'postgres' | 'mysql' | 'sqlite' | 'sqlserver' | 'oracle' | 'duckdb' | 'clickhouse' | 'mongodb' | 'redis' | 'cassandra' | 'dynamodb' | 'elasticsearch' | 'influxdb' | 'snowflake' | 'bigquery'

/** family maps compatible servers and services to the engine they follow. */
export const family = (d: Driver): Family => driverInfo(d).family
/** isMongo: a document database, with collections and shell commands instead of tables and SQL. */
export const isMongo = (d: Driver) => family(d) === 'mongodb'
/** isRedis: a key-value store: keys (by pattern) instead of tables, commands instead of SQL. */
export const isRedis = (d: Driver) => family(d) === 'redis'
/** mongoExtras: MongoDB features FerretDB doesn't have (hidden indexes, validation rules). */
export const mongoExtras = (d: Driver) => isMongo(d) && d !== 'ferretdb'
/** isCassandra: CQL, keyspaces, and a primary key fixed when the table is made. */
export const isCassandra = (d: Driver) => family(d) === 'cassandra'
/** isDynamo: an AWS web service: PartiQL, items without fixed attributes, a region and AWS keys. */
export const isDynamo = (d: Driver) => family(d) === 'dynamodb'
/** isElastic: documents in indices: requests (and SQL) instead of tables and SQL statements. */
export const isElastic = (d: Driver) => family(d) === 'elasticsearch'
/** isInflux: time series: SQL reads, writes are line protocol; a point's key is its tags and time. */
export const isInflux = (d: Driver) => family(d) === 'influxdb'
/** fixedStructure: columns and indexes can't be changed from the structure view. */
export const fixedStructure = (d: Driver) => isDynamo(d) || isElastic(d) || isInflux(d)
/** noInserts: rows don't fit SQL INSERT statements. */
export const noInserts = (d: Driver) => usesCommands(d) || isCassandra(d) || isDynamo(d) || isInflux(d)
/** isLibSQL: SQLite's SQL on a server, reached over HTTP with a token. */
export const isLibSQL = (d: Driver) => d === 'libsql' || d === 'turso'
/** isD1: SQLite's SQL through Cloudflare's API: an account, a database and a token, no host. */
export const isD1 = (d: Driver) => d === 'd1'
/** isSnowflake: a cloud warehouse found by its account; queries run on a warehouse and are billed. */
export const isSnowflake = (d: Driver) => d === 'snowflake'
/** isBigQuery: Google's warehouse: a project and a credentials file; queries are billed by what they read. */
export const isBigQuery = (d: Driver) => d === 'bigquery'
/** rowEdits: rows can be edited in the grid. */
export const rowEdits = (d: Driver) => !isBigQuery(d)
/** hostless: a service found without a host name (its own endpoint, unless one is given). */
export const hostless = (d: Driver) => isDynamo(d) || isD1(d) || isSnowflake(d) || isBigQuery(d)
/** usesCommands: the query editor takes the server's own commands, not SQL. */
export const usesCommands = (d: Driver) => isMongo(d) || isRedis(d) || isElastic(d)

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
  // Fields live in documents; a database appears with its first collection. A
  // commit is one transaction only on a replica set (the server decides).
  mongodb: { ...BASE, transactions: false, foreignKeys: false, checks: false, renameIndex: false, primaryKey: false, autoIncrement: false,
    comments: false, collations: false, createDatabase: false, importTransaction: false, onUpdate: false },
  // Keys, not tables: a key list or a key's contents is what a "table" shows.
  redis: { ...BASE, transactions: true, foreignKeys: false, checks: false, renameIndex: false, primaryKey: false, uniqueIndexes: false,
    autoIncrement: false, comments: false, collations: false, createDatabase: false, importTransaction: false, onUpdate: false },
  // Keyspaces are the schemas; no transactions, constraints or key changes.
  cassandra: { ...BASE, transactions: false, foreignKeys: false, checks: false, renameIndex: false, primaryKey: false, uniqueIndexes: false,
    autoIncrement: false, cascade: false, schemas: true, collations: false, createDatabase: false, importTransaction: false, onUpdate: false },
  // Keys are fixed when a table is made; a commit is one transaction.
  dynamodb: { ...BASE, foreignKeys: false, checks: false, renameIndex: false, primaryKey: false, uniqueIndexes: false, autoIncrement: false,
    comments: false, collations: false, createDatabase: false, importTransaction: false, onUpdate: false },
  // Each document is written by itself; mappings change through requests.
  elasticsearch: { ...BASE, transactions: false, foreignKeys: false, checks: false, renameIndex: false, primaryKey: false, uniqueIndexes: false, autoIncrement: false,
    comments: false, collations: false, createDatabase: false, importTransaction: false, onUpdate: false },
  // Points are added and their fields replaced; nothing else changes.
  influxdb: { ...BASE, foreignKeys: false, checks: false, renameIndex: false, primaryKey: false, uniqueIndexes: false, autoIncrement: false,
    comments: false, collations: false, createDatabase: false, importTransaction: false, onUpdate: false },
  // Keys are kept but not enforced; DDL commits by itself.
  snowflake: { ...BASE, schemas: true, cascade: true, checks: false, collations: false, importTransaction: false, onUpdate: false },
  // Keys are kept but not enforced; rows are changed in the query editor.
  bigquery: { ...BASE, transactions: false, schemas: true, checks: false, renameIndex: false, uniqueIndexes: false, autoIncrement: false,
    collations: false, createDatabase: false, importTransaction: false, onUpdate: false },
}
/** DRIVER_FEATURES: where a server differs from its family's engine. */
const DRIVER_FEATURES: Partial<Record<Driver, Partial<Features> & { tableOptions?: boolean }>> = {
  // A distributed database: no foreign keys or CHECKs; the primary key is
  // the shard key, and table options are fixed at creation.
  singlestore: { foreignKeys: false, checks: false, renameIndex: false, primaryKey: false, tableOptions: false },
  // A server, not a file: no ATTACH, and databases are made by its own tools.
  libsql: { file: false, createDatabase: false },
  turso: { file: false, createDatabase: false },
  // Keys are always enforced, and a transaction is one batch.
  d1: { file: false, createDatabase: false },
  // Every column is indexed by CrateDB itself; no foreign keys or transactions.
  cratedb: { transactions: false, importTransaction: false, foreignKeys: false, renameIndex: false, uniqueIndexes: false, primaryKey: false,
    autoIncrement: false, comments: false, collations: false, cascade: false, createDatabase: false, onUpdate: false, indexMethods: [] },
  // One schema; no primary keys, constraints, defaults or DELETE.
  questdb: { transactions: false, importTransaction: false, foreignKeys: false, checks: false, renameIndex: false, uniqueIndexes: false,
    primaryKey: false, autoIncrement: false, comments: false, collations: false, cascade: false, schemas: false, createDatabase: false,
    onUpdate: false, indexMethods: [] },
  // Analytical: the table's key model and distribution are fixed when it is made.
  starrocks: { transactions: false, importTransaction: false, foreignKeys: false, checks: false, renameIndex: false, uniqueIndexes: false,
    primaryKey: false, autoIncrement: false, collations: false, onUpdate: false, tableOptions: false },
  doris: { transactions: false, importTransaction: false, foreignKeys: false, checks: false, renameIndex: false, uniqueIndexes: false,
    primaryKey: false, autoIncrement: false, collations: false, onUpdate: false, tableOptions: false },
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
  /** Snowflake: the warehouse and role (empty: the user's defaults), and how to sign in. */
  warehouse?: string
  role?: string
  auth?: '' | 'key' | 'token' | 'browser'
  key_path?: string
  ssl_mode?: string
  ssl_key_path?: string
  ssl_cert_path?: string
  ssl_ca_path?: string
  /** srv: look the hosts up in DNS (mongodb+srv://, e.g. MongoDB Atlas). */
  srv?: boolean
  /** options: driver options as a query string (MongoDB), e.g. authSource=admin&replicaSet=rs0. */
  options?: string
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
  /** MongoDB: each column's key (1, -1, text, 2dsphere…), the options in words, and whether the planner ignores it. */
  orders?: string[]
  options?: string
  hidden?: boolean
}

export interface Check { name: string; expression: string }

/** TableInfo: the comment, MySQL/MariaDB options and CHECK constraints of a table. */
export interface TableInfo {
  comment: string; engine: string; collation: string; autoIncrement: number; checks: Check[]
  /** MongoDB: the validator (relaxed Extended JSON), its level and action, and the collection's options in words. */
  validator?: string; validationLevel?: string; validationAction?: string; options?: string
}

export type Cell = string | null

export interface RowsResult { columns: string[]; types: string[]; rows: Cell[][]; total: number; /** more: total is only what was read: counting would be billed. */ more?: boolean; ms: number }

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
  /** plan: a MongoDB explain, summarized (see internal/db/mongo_explain.go). */
  plan?: QueryPlan
}

export interface PlanStage {
  stage: string
  index?: string
  keyPattern?: string
  direction?: string
  filter?: string
  shard?: string
  nReturned?: number
  docsExamined?: number
  keysExamined?: number
  ms?: number
  children?: PlanStage[]
}

export interface QueryPlan {
  verbosity: string
  root: PlanStage
  nReturned?: number
  docsExamined?: number
  keysExamined?: number
  ms?: number
  indexes: string[]
  collScan: boolean
  inMemorySort: boolean
  rejectedPlans: number
}

/**
 * DRIVERS in display order: the engines, then the servers and cloud services
 * compatible with one of them. color is the badge background for engines
 * without a logo (see lib/driverLogos); user is the usual administrator
 * account (the User field's placeholder); hosted services require TLS.
 */
export const DRIVERS: { id: Driver; label: string; family: Family; port: number; color: string; user: string; hosted?: boolean; host?: string; database?: string
  /** note: shown in the connection form when the server is chosen. */
  note?: string }[] = [
  { id: 'postgres', label: 'PostgreSQL', family: 'postgres', port: 5432, color: '#336791', user: 'postgres' },
  { id: 'mysql', label: 'MySQL', family: 'mysql', port: 3306, color: '#00758f', user: 'root' },
  { id: 'mariadb', label: 'MariaDB', family: 'mysql', port: 3306, color: '#b4644a', user: 'root' },
  { id: 'sqlite', label: 'SQLite', family: 'sqlite', port: 0, color: '#3b82c4', user: '' },
  { id: 'sqlserver', label: 'SQL Server', family: 'sqlserver', port: 1433, color: '#a91d22', user: 'sa' },
  { id: 'oracle', label: 'Oracle', family: 'oracle', port: 1521, color: '#c74634', user: 'system', database: 'service name, e.g. FREEPDB1' },
  { id: 'duckdb', label: 'DuckDB', family: 'duckdb', port: 0, color: '#8a6d00', user: '' },
  { id: 'clickhouse', label: 'ClickHouse', family: 'clickhouse', port: 9000, color: '#9a8a00', user: 'default' },
  { id: 'mongodb', label: 'MongoDB', family: 'mongodb', port: 27017, color: '#00684a', user: 'optional' },
  { id: 'redis', label: 'Redis', family: 'redis', port: 6379, color: '#d82c20', user: 'optional', database: 'number, e.g. 0' },
  { id: 'cassandra', label: 'Cassandra', family: 'cassandra', port: 9042, color: '#1287b1', user: 'optional', database: 'keyspace, optional' },
  { id: 'dynamodb', label: 'DynamoDB', family: 'dynamodb', port: 8000, color: '#4053d6', user: 'empty: your AWS profile', host: 'empty: AWS', database: 'us-east-1' },
  { id: 'elasticsearch', label: 'Elasticsearch', family: 'elasticsearch', port: 9200, color: '#00788a', user: 'empty: API key' },
  { id: 'influxdb', label: 'InfluxDB 3', family: 'influxdb', port: 8181, color: '#7a2bbf', user: '', database: 'empty: the first one' },
  { id: 'snowflake', label: 'Snowflake', family: 'snowflake', port: 443, color: '#1a7fa8', user: 'user name', hosted: true, host: 'ORGANIZATION-ACCOUNT', database: 'optional' },
  { id: 'bigquery', label: 'BigQuery', family: 'bigquery', port: 443, color: '#3367d6', user: '', hosted: true, database: "empty: the credentials' project" },
  { id: 'cockroachdb', label: 'CockroachDB', family: 'postgres', port: 26257, color: '#6933ff', user: 'root' },
  { id: 'yugabytedb', label: 'YugabyteDB', family: 'postgres', port: 5433, color: '#c4471f', user: 'yugabyte' },
  { id: 'timescaledb', label: 'TimescaleDB', family: 'postgres', port: 5432, color: '#a3610b', user: 'postgres' },
  { id: 'neon', label: 'Neon', family: 'postgres', port: 5432, color: '#047857', user: 'neondb_owner', hosted: true, host: 'ep-…-pooler.region.aws.neon.tech' },
  { id: 'supabase', label: 'Supabase', family: 'postgres', port: 5432, color: '#1f7a4f', user: 'postgres', hosted: true, host: 'db.<project>.supabase.co' },
  { id: 'greenplum', label: 'Greenplum', family: 'postgres', port: 5432, color: '#2f7d32', user: 'gpadmin', database: 'postgres' },
  { id: 'tidb', label: 'TiDB', family: 'mysql', port: 4000, color: '#c0130a', user: 'root' },
  { id: 'percona', label: 'Percona Server', family: 'mysql', port: 3306, color: '#b5341a', user: 'root' },
  { id: 'singlestore', label: 'SingleStore', family: 'mysql', port: 3306, color: '#7a1fa2', user: 'root' },
  { id: 'planetscale', label: 'PlanetScale', family: 'mysql', port: 3306, color: '#1f2328', user: '', hosted: true, host: 'aws.connect.psdb.cloud' },
  { id: 'starrocks', label: 'StarRocks', family: 'mysql', port: 9030, color: '#b8432b', user: 'root' },
  { id: 'doris', label: 'Apache Doris', family: 'mysql', port: 9030, color: '#3a6fd8', user: 'root' },
  { id: 'cratedb', label: 'CrateDB', family: 'postgres', port: 5432, color: '#0f9b8e', user: 'crate', database: 'schema, e.g. doc' },
  { id: 'questdb', label: 'QuestDB', family: 'postgres', port: 8812, color: '#a2285a', user: 'admin', database: 'qdb' },
  { id: 'ferretdb', label: 'FerretDB', family: 'mongodb', port: 27017, color: '#3c7a2e', user: 'optional' },
  { id: 'documentdb', label: 'DocumentDB (untested)', family: 'mongodb', port: 27017, color: '#2e5c8a', user: 'optional', hosted: true, host: '<cluster>.docdb.amazonaws.com',
    note: "Untested: DocumentDB runs only on AWS, so GoTable hasn't been tried against it. It connects as MongoDB, over TLS (give the cluster's CA file, global-bundle.pem), with retryable writes off. A cluster is reached from inside its VPC: use an SSH tunnel through an EC2 machine. Please report what doesn't work." },
  { id: 'valkey', label: 'Valkey', family: 'redis', port: 6379, color: '#4f46b8', user: 'optional', database: 'number, e.g. 0' },
  { id: 'dragonfly', label: 'Dragonfly', family: 'redis', port: 6379, color: '#7c3aed', user: 'optional', database: 'number, e.g. 0' },
  { id: 'keydb', label: 'KeyDB', family: 'redis', port: 6379, color: '#c2410c', user: 'optional', database: 'number, e.g. 0' },
  { id: 'garnet', label: 'Garnet', family: 'redis', port: 6379, color: '#9f1239', user: 'optional', database: 'number, e.g. 0' },
  { id: 'scylladb', label: 'ScyllaDB', family: 'cassandra', port: 9042, color: '#53389e', user: 'optional', database: 'keyspace, optional' },
  { id: 'opensearch', label: 'OpenSearch', family: 'elasticsearch', port: 9200, color: '#005eb8', user: 'optional' },
  { id: 'libsql', label: 'libSQL', family: 'sqlite', port: 8080, color: '#1f7a6b', user: '' },
  { id: 'turso', label: 'Turso', family: 'sqlite', port: 443, color: '#0f766e', user: '', hosted: true, host: '<database>-<organization>.turso.io' },
  { id: 'd1', label: 'Cloudflare D1', family: 'sqlite', port: 443, color: '#c2570c', user: '32 letters and digits', hosted: true, database: 'name or ID' },
]

/** ENGINES come first in the picker; the rest are compatible with one of them. */
export const ENGINES: Driver[] = ['postgres', 'mysql', 'mariadb', 'sqlite', 'sqlserver', 'oracle', 'duckdb', 'clickhouse', 'mongodb', 'redis', 'cassandra', 'dynamodb', 'elasticsearch', 'influxdb', 'snowflake', 'bigquery']

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
  mongodb: ['disable', 'require', 'skip-verify'],
  redis: ['disable', 'require', 'skip-verify'],
  cassandra: ['disable', 'require', 'skip-verify'],
  dynamodb: ['disable', 'require', 'skip-verify'],
  elasticsearch: ['disable', 'require', 'skip-verify'],
  influxdb: ['disable', 'require', 'skip-verify'],
  snowflake: ['require'],
  bigquery: ['require'],
  neon: ['require', 'verify-full', 'verify-ca'],
  supabase: ['require', 'verify-full', 'verify-ca'],
  planetscale: ['required', 'verify_identity', 'verify_ca'],
  documentdb: ['require', 'skip-verify'],
  libsql: ['disable', 'require', 'skip-verify'],
  turso: ['require'],
  d1: ['require'],
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
