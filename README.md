<p align="center"><img alt="" src="packaging/gotable.svg" width="112"></p>
<h1 align="center">GoTable</h1>
<p align="center">A fast, keyboard-friendly desktop client for SQL and NoSQL databases.<br>One native app for Linux, Windows and macOS, built with Go.</p>

<!-- Fixed badges are files in docs/badges (made with shields.io): GitHub's image
     proxy often times out on shields.io. Live ones come from GitHub and badgen.net. -->
<p align="center">
  <a href="https://github.com/umairge66/go-table/releases/latest"><img alt="Latest release" src="https://badgen.net/github/release/umairge66/go-table?color=5b5bd6"></a>
  <a href="https://github.com/umairge66/go-table/releases"><img alt="Downloads" src="https://badgen.net/github/assets-dl/umairge66/go-table?label=downloads&color=5b5bd6"></a>
  <a href="https://github.com/umairge66/go-table/actions/workflows/ci.yml"><img alt="Tests" src="https://github.com/umairge66/go-table/actions/workflows/ci.yml/badge.svg?branch=main"></a>
  <a href="https://github.com/umairge66/go-table/actions/workflows/build.yml"><img alt="Build" src="https://github.com/umairge66/go-table/actions/workflows/build.yml/badge.svg?branch=main"></a>
  <a href="LICENSE"><img alt="License: MIT" src="docs/badges/license-mit.svg"></a>
</p>

<p align="center">
  <a href="https://github.com/umairge66/go-table/releases/latest"><img alt="Download for Linux" src="docs/badges/download-for-linux.svg"></a>
  <a href="https://github.com/umairge66/go-table/releases/latest"><img alt="Download for Windows" src="docs/badges/download-for-windows.svg"></a>
  <a href="https://github.com/umairge66/go-table/releases/latest"><img alt="Download for macOS" src="docs/badges/download-for-macos.svg"></a>
</p>
<p align="center">
  <a href="#install">Which file do I need?</a> · <a href="#databases">Databases</a> · <a href="#build-and-run">Build from source</a> · <a href="CHANGELOG.md">Changelog</a>
</p>

<p align="center">
  <img alt="PostgreSQL" src="docs/badges/postgresql.svg">
  <img alt="MySQL" src="docs/badges/mysql.svg">
  <img alt="MariaDB" src="docs/badges/mariadb.svg">
  <img alt="SQLite" src="docs/badges/sqlite.svg">
  <img alt="SQL Server" src="docs/badges/sql-server.svg">
  <img alt="Oracle" src="docs/badges/oracle.svg">
  <img alt="DuckDB" src="docs/badges/duckdb.svg">
  <img alt="ClickHouse" src="docs/badges/clickhouse.svg">
  <img alt="MongoDB" src="docs/badges/mongodb.svg">
  <img alt="Redis" src="docs/badges/redis.svg">
  <img alt="Cassandra" src="docs/badges/cassandra.svg">
  <img alt="DynamoDB" src="docs/badges/dynamodb.svg">
</p>

A modern database client for PostgreSQL, MySQL, MariaDB, SQLite, SQL Server,
Oracle, DuckDB, ClickHouse, MongoDB, Redis, Cassandra, DynamoDB, Elasticsearch, InfluxDB, Snowflake and BigQuery, and the servers and cloud services compatible with
them (CockroachDB, YugabyteDB, TimescaleDB, Neon, Supabase, CrateDB, QuestDB,
TiDB, Percona Server, SingleStore, PlanetScale, StarRocks, Apache Doris,
FerretDB, Amazon DocumentDB, Valkey, Dragonfly, KeyDB, Garnet, ScyllaDB, Greenplum, libSQL, Turso, Cloudflare D1, OpenSearch). A Go backend with a web UI, shipped as one binary that opens in a native window
([Wails](https://wails.io): WebKitGTK on Linux, WebView2 on Windows, WebKit
on macOS).

![Data view](docs/screenshots/light/06-edit-filter.png)

| Connections | Query editor | New table | Dark mode |
|---|---|---|---|
| ![Connection form](docs/screenshots/light/03-form-tested.png) | ![Query editor](docs/screenshots/light/08-query.png) | ![Table designer](docs/screenshots/light/09-new-table.png) | ![Dark mode](docs/screenshots/dark/05-table.png) |

The layout: a connection list beside the connection form, a rail of open
databases, a table sidebar, tabs, a filter row above the grid, a row
inspector and a Data / Structure / Definition switch.

## Databases

| Database | Notes |
|---|---|
| PostgreSQL, MySQL, MariaDB, SQLite | everything below |
| Oracle (12c+; tested on 23ai Free) | pure-Go driver, no Oracle client needed. The Database field is the service name (e.g. `FREEPDB1`); schemas are users. DDL commits as it runs (no ON UPDATE for foreign keys); PL/SQL blocks in the editor end with a `/` line |
| SQL Server (2017+, Azure SQL) | schemas of the connected database; other databases open next to it. Comments are `MS_Description` properties |
| DuckDB | one file, like SQLite. Foreign keys and CHECKs only in CREATE TABLE; schema changes apply statement by statement |
| ClickHouse | native protocol. No transactions, foreign keys or auto-increment; rows change through synchronous mutations; tables get an engine (MergeTree by default) and a sort key |
| MongoDB (4.4+, Atlas; tested on 4.4–8.0) | a document database: see [below](#mongodb) |
| Redis, Valkey (tested on Redis 7, Valkey 8) | a key-value store: see [below](#redis) |
| Cassandra, ScyllaDB (tested on Cassandra 5, ScyllaDB 2026.3) | CQL: see [below](#cassandra) |
| DynamoDB (tested on DynamoDB Local, not on AWS) | items without fixed attributes: see [below](#dynamodb) |
| Elasticsearch (tested on 9.5), OpenSearch (tested on 3) | documents in indices: see [below](#elasticsearch) |
| InfluxDB 3 Core (tested on 3.12) | time series: see [below](#influxdb) |
| Snowflake (tested on the service) | a cloud warehouse: see [below](#snowflake) |
| BigQuery (tested on its free tier) | Google's warehouse, rows read-only in the grid: see [below](#bigquery) |
| Greenplum (tested on 6 and 7), libSQL (tested on sqld 0.24), Turso, Cloudflare D1 | PostgreSQL's and SQLite's SQL: see [below](#greenplum-libsql-and-turso), and [D1](#cloudflare-d1) |
| CockroachDB, YugabyteDB, TimescaleDB, Neon, Supabase | PostgreSQL protocol and SQL; the top bar names the product |
| TiDB, Percona Server, PlanetScale | MySQL protocol and SQL |
| SingleStore | MySQL protocol, without foreign keys, CHECKs, index renames or primary key changes (the key is the shard key); case-insensitive search |
| CrateDB (tested on 6.5) | PostgreSQL protocol, its own SQL and catalog: every column is indexed by CrateDB, so there are no indexes to manage; no foreign keys or transactions (edits run one by one). Grid edits are refreshed at once; other writes appear within a second, as CrateDB makes them visible |
| QuestDB (tested on 10.2) | PostgreSQL protocol, time series: one schema, no primary keys or constraints, and no `DELETE` (GoTable says so; drop a partition or the table instead). A table shows its designated timestamp, partitioning and WAL; `NULL <> 'x'` is true there |
| StarRocks (tested on its all-in-one image), Apache Doris (tested on 2.1) | MySQL protocol, analytical: the key model and distribution are set when a table is made; no foreign keys; edits and schema changes run statement by statement; case-insensitive search |
| FerretDB (tested on 2.7) | MongoDB protocol on PostgreSQL: as MongoDB, without views, hidden indexes, `$jsonSchema` validation or capped collections (the UI leaves those out). Searches match text fields only, and Explain shows PostgreSQL's plan |
| Amazon DocumentDB | MongoDB protocol, over TLS (give the cluster's CA file, `global-bundle.pem`); retryable writes are off, as DocumentDB needs. Not tested here: it needs an AWS cluster |
| Dragonfly, KeyDB, Garnet | Redis protocol, as Redis. Garnet has no streams |

Neon, Supabase, PlanetScale and DocumentDB default to TLS; pasting a
`postgres://`, `mysql://` or `mongodb://` URL with their host name picks them
(also CrateDB Cloud's `*.cratedb.net`), as do `cratedb://`, `questdb://`,
`starrocks://`, `doris://`, `dragonfly://`, `keydb://` and `garnet://` URLs.

### MongoDB

A collection opens like a table, with each top-level field as a column:

- **Columns** come from the collection's first 500 documents, and a page of
  rows adds any field they missed. A field's type is shown as MongoDB names
  it, or as `int|string` when documents differ. Nested documents and arrays
  are one cell of JSON; editing one keeps the types of the values inside it.
- **Editing** keeps types: text typed into a cell is read as the type the
  field holds, and text that doesn't fit is refused (spell out another type
  with `NumberLong("5")`, `ISODate("2024-03-15")`…). Review & commit shows
  the exact `updateOne` / `insertOne` / `deleteOne` commands. They run in one
  transaction on a replica set or sharded cluster; a standalone server has
  no transactions, so they run one by one and an error says what was applied.
- **Filters** compare with every type a value could be (`5` finds the number
  and the string); searches also find numbers, dates and ids. The raw filter
  takes a query document: `{ age: { $gt: 30 } }`.
- **The query editor** runs shell commands, one per line or separated by `;`:
  `db.people.find({ age: { $gt: 30 } }).sort({ name: 1 }).limit(10)`,
  `aggregate`, `countDocuments`, `distinct`, `insertOne/Many`,
  `updateOne/Many`, `replaceOne`, `deleteOne/Many`, `findOneAnd…`,
  `createIndex`, `dropIndex`, `getIndexes`, `renameCollection`, `drop`,
  `db.createCollection`, `db.createView`, `db.runCommand`, `show dbs`,
  `show collections` and `use`. Results of a plain `find()` can be edited in
  place. Only literal values are read: GoTable doesn't run JavaScript, so
  variables, functions and loops aren't available.
- **Explain**: the Explain button (or `.explain()` / `db.coll.explain()…`)
  draws the query plan of a `find`, `aggregate`, `count` or `distinct`: the
  stages, the indexes used, documents and keys examined, time, and warnings
  for collection scans and in-memory sorts, per shard on a sharded cluster.
- **Structure**: rename a field or remove it from every document, rename or
  empty the collection. Indexes take a key per field (ascending, descending,
  text, 2dsphere, 2d, hashed, or `$**` for every field) and can be TTL,
  partial, sparse or hidden; a hidden index can be shown to the planner
  again. **Validation** rules (a `$jsonSchema` or a query) are edited with
  their level and action. There are no foreign keys, CHECKs or column
  definitions.
- **New collection**: regular, capped (a fixed size), time series (5.0+) or
  clustered (5.3+), with documents that expire.
- **Schema**: from a random sample, every field (nested ones and array
  elements too) with its types, how often it is there, its range, and its
  most common values.
- **Aggregations**: build a pipeline stage by stage, each showing the first
  documents it passes on; `$out` and `$merge` aren't previewed. Open it in
  the query editor to run, explain or save it.
- **Export and import**: a table as CSV, or as JSON documents in relaxed or
  canonical Extended JSON (as Compass exports); a database as a script of
  shell commands (collections, indexes, views and documents, every value
  keeping its exact type) that imports into any database. Imports take JSON
  documents (an array, or one a line as `mongoexport` writes) keeping their
  types, optionally skipping the ones the server refuses, or CSV, whose own
  headers become new fields.
- **Connecting**: no user is needed on a server without authentication; a user
  is looked up in `admin` unless `authSource` says otherwise (with
  `mongodb+srv://`, as in the shell: `authSource`, then the DNS TXT record,
  then the database). `mongodb://` and `mongodb+srv://` URLs can be pasted; the Options field takes driver options
  such as `replicaSet=rs0`, and Host a list such as `a:27017,b:27017`. Over an
  SSH tunnel GoTable talks to that one member. A database appears with its first
  collection: run `use mydb`, then `db.createCollection("first")`.

### Redis

Keys are shown the way tables are (`redis://` and `rediss://` URLs paste in;
Valkey, KeyDB and Dragonfly speak the same protocol):

- **Databases** are Redis's numbered ones, `db0`, `db1`…; the Database
  field takes the number to start in.
- **The sidebar** lists *All keys* and the namespaces found in the keys
  (`user:*`, `session:*`). A pattern opens as a list of keys, with each one's
  type, TTL (seconds) and value — short strings whole, other types by size.
  The arrow on a key opens it: a hash's fields, a list's items, a set's
  members, a sorted set's members and scores, a stream's entries, a string or
  JSON value. Filters and search work on every column.
- **Editing**: rename a key, change a string, set or clear a TTL, add a
  string key, delete keys; in an opened key, change its fields, items,
  members, scores or entries. Review shows the exact commands (`RENAME`,
  `SET … KEEPTTL`, `HSET`, `LSET`, `ZADD`…). They run together in one
  `MULTI/EXEC`, watched, so it is all or nothing; a value changed since you
  loaded it is overwritten.
- **The query editor** runs commands as redis-cli takes them, one a line
  (`#` starts a comment): replies are tables (`HGETALL` as field/value,
  `ZRANGE … WITHSCORES` as member/score, `INFO` by section, `SCAN` with the
  next cursor). `MULTI…EXEC` and `SELECT` work across lines; `SUBSCRIBE` and
  `MONITOR`, which never return, are refused.
- **Export** writes the keys as commands with their expiry (`.redis`, for
  `redis-cli < file` or Import script); keys can also go to CSV or JSON.

### Cassandra

Keyspaces are the schemas; a table's primary key is its partition key, then
its clustering columns (the Structure tab says which). `cassandra://` and
`scylla://` URLs paste in; ScyllaDB speaks the same protocol.

- **Rows**: CQL has no OFFSET and filters or sorts on the server only by
  key, so a page is read by skipping rows, and the filter bar and column
  sorts work here over up to 100,000 rows. A CQL filter (WHERE) runs on the
  server, with ALLOW FILTERING added; put the partition key in it for big
  tables. Collections show as CQL (`{'a', 'b'}`, `[1, 2]`, `{'k': 1}`) and
  are edited the same way.
- **Editing** writes each type as CQL and uses lightweight transactions
  (`UPDATE … IF EXISTS`, `INSERT … IF NOT EXISTS`, `DELETE … IF EXISTS`),
  so a row deleted meanwhile is reported; another edit of
  the same row is overwritten. There are no
  transactions: each change commits on its own. Key columns can't change.
- **Structure**: columns can be added or dropped, indexes added or dropped,
  the comment set and the table emptied; the key and the column types are
  fixed. New tables are made with CREATE TABLE in the query editor, which
  New table opens with an example; keyspaces with CREATE KEYSPACE.
  Deleting a keyspace is DROP KEYSPACE.
- **The query editor** runs CQL (`USE` switches keyspace). Export writes the
  tables as CQL (`DESCRIBE`) and rows as INSERTs, for Import or
  `cqlsh -f`; CSV imports and exports work too.
- GoTable talks to the host it is given, which coordinates the cluster: the
  other nodes' addresses needn't be reachable (an SSH tunnel works).

### DynamoDB

A connection is a region and an AWS access key; without a key GoTable signs
in as the AWS CLI does (environment, `~/.aws`, SSO; `profile=name` in
Options). An endpoint host is for DynamoDB Local, which is never sent the
machine's AWS keys. URLs: `dynamodb:///us-east-1`,
`dynamodb://KEY:SECRET@localhost:8000/us-east-1`.

- **Reads on AWS are billed.** A table's columns come from its first 500
  items; a page is read by scanning up to it; the filter bar and sorts scan
  the table (up to 100,000 items). A PartiQL condition (WHERE) runs on the
  server: with the partition key in it, it reads only that. Tables over
  100,000 items show AWS's approximate count.
- **Types** are kept when edited; text that doesn't fit is refused. Lists
  and maps are JSON; sets and nested binary are `{"$ss": ["a"]}`,
  `{"$ns": [1]}`, `{"$bs": ["base64"]}`, `{"$b": "base64"}`. NULL removes
  the attribute. Keys can't change.
- **A commit** is one transaction per 100 changes. It fails if an item was
  deleted meanwhile or a new key exists; another edit of the same attribute
  is overwritten.
- **The query editor** runs PartiQL, and for what that can't say:
  `CREATE TABLE "t" ("id" S PARTITION KEY, "at" N SORT KEY)`,
  `CREATE TABLE "t" WITH {the AWS CLI's create-table JSON}`, `DROP TABLE`
  and `PUT "t" {item}`. Exports use these, so GoTable's Import reads them.
  Exports include keys, indexes and capacity, but not TTL, streams, tags or
  backups. Indexes are changed with the AWS console or CLI.

### Elasticsearch

Elasticsearch (tested on 9.5) and OpenSearch (tested on 3) over HTTP or HTTPS, with a user
and password or an API key (User empty, the encoded key as the password).
`elasticsearch://`, `opensearch://` and `…+https://` URLs paste in.

- **Rows** are documents: `_id`, then the mapping's top-level fields; objects
  and lists are cells of JSON. Aliases and data streams are read-only.
- **Filters and sorts** run on the server. Whole-value comparisons, parts of
  words and sorting use keyword fields (or a text field's keyword sub-field,
  which by default leaves out text over 256 characters); text without one is
  searched by words and can't be sorted. The query field takes Lucene
  (`status:active AND age:>30`) or a JSON query. Rows past the 10,000th are
  read by scrolling to them.
- **Editing** keeps each field's type; a list or object is replaced as a
  whole. Each document is written by itself (there are no transactions), and
  one changed between reading it for the save and writing it is refused; an
  edit made since you loaded the table is overwritten.
- **The query editor** takes requests as Kibana's Dev Tools does (`GET
  /index/_search` and a JSON body, NDJSON for `_bulk`), SQL, and on
  Elasticsearch ES|QL (`FROM index | …`). Hits, `_cat` answers and
  aggregations show as tables.
- **Export** writes indices (settings, mappings, aliases) and documents as
  requests, which Import runs; CSV goes both ways. Mappings are changed with
  requests. Data streams aren't in the export.

### InfluxDB

InfluxDB 3 Core (tested on 3.12; Enterprise has the same API but wasn't tried) with a
token. InfluxDB 1 and 2, and InfluxDB's cloud, aren't supported.
`influxdb://TOKEN@host:8181/database` URLs paste in.

- **Rows** are points, the newest first. The key is the tags and the time,
  which has nanoseconds.
- **Editing** writes line protocol: fields can be changed and points added,
  all of a save or none of it. InfluxDB can't delete rows, set a field back
  to NULL, or change a point's tags or time. Two people editing the same
  field: the last save wins.
- **The query editor** runs SQL, and for what InfluxDB's SQL can't say:
  `WRITE` on a line of its own, then line protocol up to an empty line;
  `CREATE TABLE "t" ("site" TAG, "temp" FLOAT64)`, `DROP TABLE`,
  `CREATE DATABASE`, `DROP DATABASE`. Exports use these.
- CSV imports need a column for the time.

### Greenplum, libSQL and Turso

- **Greenplum** (tested on 6 and 7) is PostgreSQL spread over segments: a
  table's `DISTRIBUTED BY` shows, and is in its definition and exports. A
  unique index must contain the distribution key; foreign keys are kept but
  not enforced. Without a sort, rows come in no fixed order, so page by a
  sorted column.
- **libSQL** servers (tested on sqld 0.24) run SQLite's SQL over HTTP, with
  a token if the server asks for one. They have no ATTACH, temporary tables
  or VACUUM, and end a transaction left idle for some seconds: GoTable then
  says so instead of going on outside it. `libsql://host?authToken=…` URLs
  paste in (`&tls=0` for plain HTTP).
- **Turso** uses the same protocol over TLS (tested on its cloud).

### Cloudflare D1

SQLite's SQL through Cloudflare's API (tested on the service): an account
ID, an API token with the permission Account → D1 → Edit, and a database by
name or ID. `d1://ACCOUNT_ID:API_TOKEN@cloudflare/database` URLs paste in.

- **A save** is one batch, all of it or none; a row deleted meanwhile stops
  it. A structure change, a CSV import and an import as one transaction are
  a batch too, up to what D1 takes in one request.
- **Foreign keys** are always enforced. Dropping a table deletes the rows
  that reference it `ON DELETE CASCADE`, so GoTable refuses to rebuild a
  table that others reference (a change of type, NULL, default or key), or
  to delete it without them.
- **Whole numbers past 2^53** are rounded by Cloudflare's API. Tables and
  exports are read so that they are exact; in the query editor such a number
  is marked with ≈ (`CAST(x AS TEXT)` shows it).
- There is no BEGIN, ATTACH, temporary table or VACUUM, and a statement
  takes at most 100,000 bytes. Reading counts towards the account's limits.

### Snowflake

Tested on the service, signing in with a key pair. Signing in with a
password, an access token or the browser (single sign-on), and hybrid,
Iceberg and external tables, are not tested. The account is written `ORGANIZATION-ACCOUNT`;
`snowflake://user:password@account/database?warehouse=…&role=…` URLs paste
in.

- **What is billed**: reading rows runs the warehouse. Lists of databases,
  tables, columns and keys, definitions and a table's own row count don't.
  A view or a filtered table is counted only when you press *Count*.
- **Keys aren't enforced** by Snowflake, so an edit that would change more
  than one row is undone. It accepts a foreign key with `ON DELETE CASCADE`
  and silently doesn't create it: GoTable refuses those. Tables are dropped
  with `RESTRICT`, as `DROP TABLE` alone also drops other tables' keys.
- **Values** are exact: 38 digits, nanoseconds, JSON, binary, geography and
  vectors can be read, edited and exported, whatever the account's date
  formats are.
- **Names** written without quotes are stored in upper case; names typed in
  GoTable's dialogs are kept as they are.
- Schema changes commit one by one, there are no indexes, and a column's
  default can only be removed. In the editor, a scripting block goes between
  `$$` (`EXECUTE IMMEDIATE $$ BEGIN … END $$`), and PUT and GET, which copy
  files, aren't run.
- Snowflake's client would send telemetry: GoTable keeps it from being sent, and
  from loading a native library.

### BigQuery

Tested on a project of the free tier, with a service account's key (a JSON
file). Without one, GoTable uses the sign-in of `gcloud auth
application-default login` (not tested). Datasets are the schemas, projects
the databases. `bigquery://project/dataset?credentials_path=/path/key.json`
URLs paste in.

- **Rows can't be edited in the grid** yet. INSERT, UPDATE and DELETE run
  from the query editor, in a project with billing (not tested: the free
  tier refuses them).
- **What is billed**: a query is billed for the columns it reads, whatever
  its LIMIT, and 10 MB at least if it reads a table. Browsing a table without
  filters or sorting, counting it, its structure, definition and exports are
  free. A filter, a sort or a view is a query.
- **A limit for each query** (10 GB unless the connection says otherwise):
  BigQuery refuses a larger one, and nothing is billed. The editor shows
  what its text would read before it runs.
- **Scripts** with variables, transactions or temporary tables run as one,
  with a result for each statement. A view's query must name the dataset of
  its tables.
- **CSV** is imported by a load job: free, and all or nothing.
- **Exports** are SQL with one INSERT for many rows. Their rows were read
  back exactly, but the INSERTs themselves need billing and haven't been run.
- **Foreign keys**: dropping a table would drop other tables' keys to it, so
  GoTable refuses while such a key is in the same dataset.

## Features

- **Connections**: list with search, groups, environment colours (production
  windows get a red bar and a warning before commit), driver tiles, inline
  *Test*, paste a `postgres://…` URL, SSL modes and certificates, SSH tunnels
  (password, key, encrypted key). Passwords live in the system keyring and
  are never sent to the UI.
- **Data grid**: virtualised, keyboard driven (arrows, Enter/F2 to edit,
  typing replaces, Delete marks rows, Ctrl+C copies), resizable columns,
  click-to-sort, NULL shown as a pill, edited / new / deleted cells colour
  coded, right-aligned numbers, row inspector, context menu (copy value / JSON
  / INSERT, filter by value, set NULL, duplicate, delete).
- **Filtering**: quick search across all columns, a filter row per condition
  (17 operators) and an optional raw `WHERE`.
- **Safe editing**: changes stay pending until *Review & commit* (Ctrl+S)
  shows the exact SQL; it runs in one transaction and rolls back on error.
  Rows changed by someone else are detected.
- **SQL editor**: CodeMirror with highlighting, table/column autocomplete,
  Ctrl+Enter runs the statement under the cursor or the selection,
  Ctrl+Shift+Enter runs all, Format / Ctrl+I beautifies the selection or the
  whole query in the connection's dialect (one undo reverts it), Stop cancels
  on the server, row limit, multiple
  result tabs, messages with plain-language errors, open/save `.sql`; query
  tabs are restored on the next start.
- **Sidebar**: Ctrl+A selects all listed tables (respecting the filter),
  Ctrl/⌘+click and Shift+click select several; the selection can be exported
  or deleted together. Delete shows the exact DROP SQL, runs in one
  transaction where the database allows it, orders related tables so foreign
  keys don't get in the way, can CASCADE to dependent objects, and asks for
  DELETE to be typed for several tables or on production.
- **Structure**: columns and indexes, *Add column* with type suggestions per
  database, default (value, expression or NULL), NULL toggle, comment, MySQL
  position, and a live preview of the exact `ALTER TABLE` before it runs.
- **Import & export** with live progress (percent, MB or rows, speed, time
  left; cancel anytime; jobs keep running in the background and are listed in
  the status bar):
  - *Database*: export selected tables and views as a portable SQL dump
    (structure and/or data, optional DROP, foreign keys added after the data,
    sequences/auto-increment restored, binary data kept) or as a ZIP of CSVs;
    import any SQL script or dump — `pg_dump` plain format including
    `COPY … FROM stdin`, `mysqldump` (with `/*!…*/` and `DELIMITER`), SQLite
    `.dump` — either all-or-nothing in one transaction or skipping failed
    statements (reported with line numbers).
  - *Table*: export CSV / JSON / SQL (all filtered rows or a page); import CSV
    with delimiter detection, a preview, column mapping by name, NULL rules and
    an optional "delete existing rows first", in one transaction.
  - *Query results*: CSV / JSON / INSERT export in the browser.
  - **Large files**: everything streams — files are never held in memory.
    Measured on this machine: a 1 GB SQL file (5.4 M rows) imports into SQLite
    in 26 s and PostgreSQL in 86 s, a 914 MB `pg_dump` in 28 s, and a 1 GB
    dump exports in 24 s, all with under 30 MB of memory. Very large files
    can be imported by path to skip the upload copy.
- **Several databases at once**: **Ctrl+D** (or *Open database* in the
  sidebar) opens another database next to the current one — schemas of the
  same database, MySQL databases, attached SQLite files, or another
  PostgreSQL database on the server (opened on its own connection). Open
  databases sit in a narrow rail on the far left (click or **Alt+1…9** to
  switch, × to close); the table sidebar shows only the active database,
  which also decides what **Ctrl+P** (tables of the current database),
  new queries and import/export act on. Tabs show their database, query tabs
  always run in the database they were opened in, and the open databases
  are remembered per connection. **Ctrl+K** searches everything.
  Right-click a database in the rail to copy its name or delete it (also
  *Delete a database…* in the Ctrl+D list): the DROP command is shown and
  the name must be typed first.
- **Everywhere**: command palette (Ctrl+K), toasts, light/dark (Ctrl+Shift+L),
  several connections open at once.
- **Settings** (the gear, or Ctrl+,): theme (system, light or dark), the
  accent colour (eleven presets, each with its own dark-mode shade, or any
  colour), whether the database rail, the sidebar and the row inspector
  start open, closed or as last left, the query editor's default text size,
  and every keyboard
  shortcut listed in this README (record new keys, reset, or remove them;
  clashes are pointed out). They are saved in `settings.json` next to the
  connections. A query tab can zoom its own text: Ctrl+= / Ctrl+− / Ctrl+0
  or the A− A+ buttons. The shortcuts here are the defaults.
- **Feels like a desktop app**: a native window; exports and saved results
  use the system *Save As* dialog and Go writes the file directly; imports
  use the system *Open* dialog and read the file in place (no copy, any
  size); unsaved edits are guarded
  by in-app dialogs (*Review & commit… / Discard / Cancel*), never browser
  alerts; F5 / Ctrl+R refresh data instead of reloading; browser shortcuts,
  the browser context menu and file-drop navigation are disabled.
- **Saved queries and history.** The sidebar has three tabs: *Items* (tables),
  *Queries* and *History*. **Ctrl+S** in a query tab saves it under a name
  (later Ctrl+S updates it; a dot on the tab means unsaved edits). History
  records every query you run and every change you commit from the grid,
  with time, duration, rows and errors; click an entry to open it again, or
  right-click → *Run again*. Both are stored next to `connections.json`
  (`queries.json`, `history.jsonl`, last 1,000 entries per connection).
- **Foreign keys** (single- and multi-column). Foreign key columns show a
  link icon in the header; hover or select a cell and click its arrow (or
  right-click → *Open …*) to open the referenced table filtered to that row.
  Double-click (or Enter) opens a searchable list of the referenced rows,
  showing the key plus a readable column such as `name`; picking one sets
  every column of the key. *Set NULL* and *Edit as text* are at the bottom,
  and typing into the cell still edits it as text.
- **One instance.** Starting GoTable while it's already running brings its
  window to the front instead of starting a second one.
- **Creating tables and databases.** *New table* (the **+** next to the
  sidebar's refresh, right-click a database in the rail, or Ctrl+K) opens a
  designer tab modelled on phpMyAdmin: per column a name, a type from the
  database's own grouped list (Numeric, Date and time, String, JSON,
  Spatial… plus PostgreSQL enums/domains/extension types), Length/Values,
  default, NULL, index (PRIMARY — several make a composite key — UNIQUE,
  INDEX, FULLTEXT on MySQL/MariaDB) and auto-increment; more options per
  column (collation from the server's list, UNSIGNED, ON UPDATE
  CURRENT_TIMESTAMP, PostgreSQL arrays, comment, reorder); table options
  (engine, character set and collation on MySQL/MariaDB, STRICT on SQLite);
  *Add N columns*. The exact SQL is shown live and problems are explained
  before anything runs; **Ctrl+Enter** creates the table. *Add column* uses
  the same type list. *Create database…* in the Ctrl+D list makes a
  database (PostgreSQL also a schema; MySQL/MariaDB with a character set)
  and opens it; on SQLite it creates a new database file, saves it as a
  connection and opens it.
- **Switching tabs.** **Ctrl+Tab** / **Ctrl+Shift+Tab** (or **Ctrl+PageDown**
  / **Ctrl+PageUp**) go to the next / previous open tab, wrapping around;
  **Ctrl+1…9** jump to a tab, **Ctrl+W** closes it.
- **Collapsible panels.** **Ctrl+B** hides/shows the table sidebar,
  **Ctrl+Shift+B** the database rail (also the two buttons top-left).

## Install

Download the file for your system from the [latest release](https://github.com/umairge66/go-table/releases/latest):

| System | File | Install |
|---|---|---|
| Ubuntu 22.04+, Debian 12+ | `gotable_<version>_amd64.deb` | `sudo apt install ./gotable_*_amd64.deb` |
| Fedora 36+ | `gotable-<version>-1.x86_64.rpm` | `sudo dnf install ./gotable-*.x86_64.rpm` |
| Any Linux | `GoTable-<version>-x86_64.AppImage` | `chmod +x GoTable-*.AppImage`, then run it — self-contained (WebKitGTK included) |
| Windows 10 / 11 | `gotable-<version>-windows-amd64-setup.exe` | run it: installs GoTable for your account (no administrator needed), with a Start menu shortcut and an entry in Settings → Apps to uninstall it |
| Windows, portable | `gotable-<version>-windows-amd64.exe` (or the `.zip`) | no setup needed: double-click the `.exe` to start GoTable (unzip the `.zip` first). Connections and settings are kept in `%AppData%\gotable` |
| macOS 11+ (Apple silicon and Intel) | `GoTable-<version>-macos-universal.dmg` (or the `.zip`) | open it and drag GoTable to Applications |

The `.deb` and `.rpm` pull in WebKitGTK and add GoTable to the app menu.

The Mac app isn't signed with an Apple Developer ID, so macOS blocks it the
first time: open it, then choose **Open Anyway** in System Settings → Privacy
& Security (or run `xattr -dr com.apple.quarantine /Applications/GoTable.app`).
`gotable -version` prints the installed version.

## Supported platforms

| Platform | Window | Status |
|---|---|---|
| **Linux**, Wayland session — GNOME, KDE Plasma, Sway, Hyprland… on Fedora, Ubuntu 22.04+, Debian 12+, Arch, … | native (WebKitGTK 4.1), GoTable's own title bar | tested on Fedora 44 / KDE Plasma; other distros and desktops use the same libraries and are expected to work |
| **Windows** 10 / 11 | native (WebView2, built into Windows), system title bar | builds and passes the checks; not yet tried on real Windows |
| **macOS** 11+, Apple silicon and Intel | native (WKWebView), system title bar and menu bar | built by CI; not yet tried on a real Mac |
| Linux on **X11** | open GoTable in a browser (`-no-window`) or the Chrome app window (`make build-browser`) | the native window needs Wayland |

On Linux the native window needs a Wayland session (the default on current
GNOME and KDE). In an X11 session GoTable says so and exits; use
`gotable -no-window` there.

## Build and run

You need **Go 1.26+** and **Node 20.19+** (or 22.12+), plus, for the native
window, the WebView libraries of your system.

### Linux

```sh
# Build tools and WebKitGTK 4.1 headers
sudo dnf install gcc webkit2gtk4.1-devel                    # Fedora
sudo apt install build-essential libwebkit2gtk-4.1-dev      # Ubuntu 22.04+, Debian 12+
sudo pacman -S base-devel webkit2gtk-4.1                    # Arch

make build          # builds the web UI, then bin/gotable
bin/gotable         # opens GoTable
```

To run a built `gotable` on another machine, only the WebKitGTK runtime is
needed: `webkit2gtk4.1` (Fedora), `libwebkit2gtk-4.1-0` (Ubuntu/Debian),
`webkit2gtk-4.1` (Arch).

### Windows

WebView2 is part of Windows 10 and 11, so only Go and Node are needed. In
PowerShell:

```powershell
cd frontend; npm ci; npm run build; cd ..
go build -tags desktop,production -ldflags "-s -w -H windowsgui" -o bin\gotable.exe .\cmd\gotable
.\bin\gotable.exe
```

That build has no cgo, so it can't open DuckDB files. With DuckDB, the
release `.exe` is cross-compiled from Linux with a MinGW that targets the
UCRT, which is how DuckDB's Windows libraries are built: Fedora's
`ucrt64-gcc-c++` (Debian's and Ubuntu's MinGW use the older msvcrt and fail
to link). `make build-windows` uses it when it's installed, and builds
without DuckDB otherwise. CI builds the tests the same way and runs them on
Windows.
On Windows GoTable keeps the system title bar; shortcuts, the native
Save/Open dialogs and saved passwords (Credential Manager) work as on Linux.

### macOS

With Xcode's command line tools (`xcode-select --install`), Go and Node:

```sh
make package-macos   # dist/GoTable-…-macos-universal.dmg and .zip (Apple silicon + Intel)
```

`scripts/package.sh macos` builds each architecture with cgo (WKWebView,
DuckDB), joins them with `lipo`, makes `GoTable.app` and signs it ad hoc.

### Any system, in a browser

```sh
make build-browser            # no C compiler or WebKitGTK needed
bin/gotable                   # opens a Chrome / Chromium / Edge app window
bin/gotable -no-window        # prints a URL to open in any browser
```

`GOTABLE_BROWSER=chromium bin/gotable` picks the browser.

The server listens on 127.0.0.1 only and requires a random per-launch token
(exchanged for an HttpOnly cookie); it also checks Host and Origin headers.
Connections are saved in `~/.config/gotable/connections.json` (on Windows
`%AppData%\gotable\connections.json`).

For UI development: `bin/gotable -no-window -addr 127.0.0.1:7777`, then
`GOTABLE_TOKEN=<token> npm run dev` in `frontend/`.

## Tests

```sh
make test       # Go unit + SQLite + SSH + HTTP API tests, and UI unit tests
make test-dbs   # also PostgreSQL 15, MySQL 8.4 and MariaDB 11 in Docker
ENGINES=all scripts/test-dbs.sh   # every server: + SQL Server, Oracle, ClickHouse, MongoDB, Redis, Valkey, CockroachDB,
                                  # YugabyteDB, TimescaleDB, TiDB, Percona, SingleStore, CrateDB, QuestDB,
                                  # StarRocks, Doris, FerretDB, Dragonfly, KeyDB, Garnet, Cassandra, ScyllaDB, DynamoDB Local,
                                  # Greenplum, libSQL, Elasticsearch, OpenSearch, InfluxDB
                                  # (the analytical ones are big images)
ENGINES="mongodb mongors mongoshard mongotls" MONGODB_IMAGE=mongo:4.4 scripts/test-dbs.sh -run Mongo ./internal/...
                                  # MongoDB standalone, replica set, sharded cluster, TLS + X.509
scripts/test-mongo-srv.sh         # mongodb+srv:// with SRV/TXT records from a DNS container
GOTABLE_TEST_MONGOATLAS_URL='mongodb+srv://user:pw@cluster0.….mongodb.net' go test -run TestMongoDB ./internal/db/
                                  # the same MongoDB tests on Atlas (uses, then drops, gt_test and gt_copy)
make e2e        # Playwright drives the real binary in headless Chrome
GOTABLE_E2E_PG=postgres://user:pw@host/db make e2e   # adds a Postgres scenario
GOTABLE_E2E_COMPAT="sqlserver://… clickhouse://… duckdb:///tmp/x.duckdb" make e2e  # one UI scenario per URL
GOTABLE_E2E_MONGODB=mongodb://user:pw@host/gt_e2e?authSource=admin make e2e  # adds the MongoDB scenario (it drops that database)
```

The screenshots in `docs/screenshots/` are taken by
`frontend/e2e/screenshots.spec.ts` from a PostgreSQL database loaded with
`scripts/screenshot-demo.sql`:

```sh
cd frontend && GOTABLE_SCREENSHOTS=../docs/screenshots \
  GOTABLE_SCREENSHOT_PG=postgres://postgres:pw@127.0.0.1:5432/shop npx playwright test e2e/screenshots.spec.ts
```


## Continuous integration and releases

GitHub Actions (`.github/workflows/`):

- **CI** — on every push and pull request, as parallel jobs: TypeScript
  and unit tests; `go vet`; the Go tests against real PostgreSQL 15, MySQL
  8.4, MariaDB 11, SQL Server 2022, ClickHouse, MongoDB 7 and Oracle 23ai (service containers) and
  DuckDB/SQLite files; MongoDB 4.4 and 8.0 as a standalone server, a replica
  set, a sharded cluster, with TLS and by `mongodb+srv://`; and the end-to-end tests in Chrome, including the
  PostgreSQL scenario (`scripts/e2e-demo.sql`), MongoDB's, and one per SQL
  Server, ClickHouse, MongoDB and DuckDB. Each job keeps its own Go build cache.
- **Build** — on every push to `main`: the `.deb`, `.rpm`, AppImage,
  `.tar.gz`, Windows `.zip` and the macOS `.dmg` (built on a Mac runner),
  downloadable from the run's artifacts. A
  newer push cancels the older build.

Changes to Markdown files or the licence alone run neither.
  Linux builds run on Ubuntu 22.04, so one binary covers Ubuntu 22.04+,
  Debian 12+ and Fedora 36+.

To publish a release, push a version tag; the same files are attached to a
GitHub release whose notes are the full commit messages since the previous
tag (`scripts/release-notes.sh`):

```sh
git tag v0.1.0
git push origin v0.1.0
```

The packaging is `scripts/package.sh` (`make package-linux` /
`make package-windows`), so it runs the same way locally.

### Updates

GoTable keeps itself up to date from these releases (`internal/update`).
Once a day (Settings → Updates; never for a development build) it asks
GitHub for the latest release; nothing about the user or their databases is
sent. A newer one shows a notice with its notes, and *Update* installs it:

| Installed as | Update |
|---|---|
| AppImage, `.tar.gz`, Windows `.exe` | replaced in place, then *Restart now* |
| macOS app | `GoTable.app` replaced in place, then *Restart now* |
| `.deb` / `.rpm` | downloaded and opened with the system's installer |

Nothing is installed unless it matches `SHA256SUMS` and that file's
Ed25519 signature checks out with the key built into GoTable
(`internal/update/key.go`). The release job signs with the private key in
the **`GOTABLE_RELEASE_KEY`** secret (`cmd/release-sign`) and stops if the
secret is missing or doesn't match. Keep a copy of that key: without it,
copies already installed can't be sent signed updates. A new key pair:
`go run ./cmd/release-sign -genkey key.txt` (then the printed public key
goes into `key.go`, and copies with the old key take no more updates).

## Layout

| Path | Responsibility |
|---|---|
| `internal/model` | connections, groups, JSON store, keyring, URL parsing |
| `internal/db` | dialects, SQL builders, streaming statement scanner, dump/import, engines, sessions |
| `internal/sshtunnel` | local port forwarding over SSH |
| `internal/export` | CSV / SQL / JSON writers |
| `internal/server` | local JSON API, auth, background jobs, uploads, plain-language errors |
| `internal/webui` | embeds the built UI |
| `internal/update` | GoTable's own updates: GitHub releases, signatures, installing |
| `internal/desktopentry` | registers a portable Linux copy with the desktop (its icon) |
| `cmd/release-sign` | signs a release's files (`SHA256SUMS`, `SHA256SUMS.sig`) |
| `frontend/` | React + TypeScript + Tailwind UI, Vitest and Playwright tests |
| `cmd/gotable` | starts the server and the window (`window_wails.go`; `window_browser.go` with `-tags browser`) |
| `packaging/` | app icon, desktop entry, `.deb`/`.rpm` definition (nfpm) |
| `scripts/` | release packaging, database test containers, end-to-end demo data |
| `.github/workflows/` | CI (tests) and Build (packages, releases) |

## Not implemented yet

Other engines (IBM Db2, …), ER
diagrams, Excel import and user management. PostgreSQL custom-format (`pg_dump -Fc`) archives need
`pg_restore`; plain SQL dumps are supported.

## License

[MIT](LICENSE).

## Trademarks

PostgreSQL, MariaDB and SQLite logos are trademarks of their respective
owners. They are shown only to identify the database engine a connection
uses; no endorsement is implied. The SVG artwork comes from
[Simple Icons](https://simpleicons.org) (CC0-1.0); see
`frontend/src/lib/driverLogos.ts` for the source and each owner's guidelines.
MySQL is a trademark of Oracle; GoTable shows a generic icon for it rather
than the MySQL logo. MongoDB is a trademark of MongoDB, Inc., and is likewise
shown with a generic icon.
