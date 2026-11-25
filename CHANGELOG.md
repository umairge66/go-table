# Changelog

What changed in each release of GoTable. Releases are tagged `vX.Y.Z`; the
release pages on GitHub also carry the full commit messages.

## Unreleased

### Added

- **JSON and tree views** of MongoDB, Elasticsearch, DynamoDB and Redis
  documents, to read and edit them whole: add, remove, rename and nest fields
  and change types. Only what changed is written, types and large numbers
  are kept, and a document changed by someone else meanwhile isn't overwritten.
- **ER diagram**: tables and foreign keys drawn and arranged; draw a key from
  column to column, and change columns, keys, indexes and tables from it.
  Export as SVG or Mermaid.
- **Processes**: what the server runs, live, with what blocks what; stop a
  query or close its connection.
- **Users & privileges**: make users, set passwords, lock them, and give and
  take roles and privileges without writing GRANT, one by one or several at once.
- **Streaming results**: rows show as the server sends them, *Run all* shows
  each result as its statement finishes, and *Stop* keeps the rows that had
  arrived. SQL databases only: on the others (MongoDB, Redis, Cassandra,
  DynamoDB, Elasticsearch, InfluxDB, BigQuery) rows come when the command is done.

### Changed

- **The row limit stops the query on the server** (PostgreSQL and compatible
  servers, MySQL 8, MariaDB, Percona Server, ClickHouse). Before, every remaining row was
  still downloaded: 1,000 of 20 million rows took 20 to 60 seconds and now take
  milliseconds. The session and an open transaction stay as they are. A
  `SELECT` that calls a function with side effects runs it only for the rows
  it returns; a query's own larger `LIMIT` wins on MySQL and MariaDB.

- **The connection page is redesigned.** Databases are in a searchable list
  beside the form (by name, or by the engine they are compatible with), so the
  form starts at the top. Test results and the buttons are always in view, and
  a saved connection can be duplicated or deleted from its header.
- The first screen starts a connection directly from a database.
- Notifications stand in the corner, just above what is at the bottom of the
  window, instead of floating at a fixed height.
- Databases without a logo show their initials instead of all the same icon.

## v0.13.0 — 2026-11-24

### Added

- **Snowflake**, tested on the service: exact values of every type, edits
  that can't change several rows by mistake, and no billed counts unless
  asked for. It makes GoTable about 15 MB larger.
- **BigQuery**, tested on its free tier: free browsing, counts and exports, what
  a query would read before it runs, and a limit that refuses larger ones.
  Rows aren't edited in the grid yet.
- **Cloudflare D1**, tested on the service: saves as one batch, exact large
  numbers, and no change that would make D1 delete rows of other tables.

### Changed

- **Turso** is tested on its cloud, so it is no longer marked untested. A
  wrong token is explained.

### Fixed

- *Open in SQL* quotes names that need it, and on Oracle names in lower case
  are quoted everywhere.

## v0.12.0 — 2026-11-24

### Added

- **Elasticsearch** (tested on 9.5) and **OpenSearch** (tested on 3): documents as rows,
  filters and sorts on the server, edits that keep each field's type, and an
  editor that takes requests as Kibana's Dev Tools does, SQL and ES|QL.
- **InfluxDB 3** (tested on Core 3.12): points as rows with nanosecond times,
  editing fields and adding points, SQL, and line protocol in the editor.
- **Greenplum** (tested on 6 and 7), with each table's distribution.
- **libSQL** servers (tested on sqld 0.24) and **Turso** (untested), over HTTP.

### Fixed

- DynamoDB on AWS couldn't be reached from the app: an empty host was saved
  as 127.0.0.1 (since v0.11.0). A connection saved meanwhile needs its
  Endpoint host cleared.
- Queries on Redis, Cassandra and DynamoDB always showed 0.0 ms.

## v0.11.1 — 2026-11-24

### Fixed

- The update notice closes: OK, Later, Close and × did nothing once an update
  was downloaded, installed or had failed. After Later, Settings → Updates
  still offers the restart.
- Settings → Panels applies to the connections already open, not only to the
  next one opened.
- The README's download button no longer depends on another site to load.

## v0.11.0 — 2026-11-23

### Added

- **DynamoDB**, tested on DynamoDB Local (not yet on AWS): tables, keys and
  indexes; items as rows, with a PartiQL condition run on the server; every
  attribute type shown and edited without losing it; a commit as one
  transaction (per 100 changes); PartiQL plus `CREATE TABLE`, `DROP TABLE`
  and `PUT` in the editor; export, import, CSV and emptying tables.

### Fixed

- The DocumentDB tile shows that it is untested (the word was cut off).
- Cassandra's message for a deleted row no longer suggests it detects
  changes to one.

## v0.10.0 — 2026-11-23

### Added

- **Cassandra and ScyllaDB.** Keyspaces, tables and materialized views, each
  column's role in the key, indexes, and the server's own DDL. Rows page by
  skipping (CQL has no OFFSET); filters and sorts work over up to 100,000
  rows, and a CQL filter runs on the server. Every CQL type is shown and
  edited (collections as CQL), and edits use lightweight transactions, so a row deleted meanwhile
  is reported (another edit of the same row is overwritten). *Corrected after
  release; tuples, user types, `duration`, `time`, `inet`, `varint`,
  `decimal` and materialized views haven't been tested.* The query editor runs CQL with `USE`;
  export writes CQL scripts; CSV import, adding and dropping columns and
  indexes, emptying tables and deleting keyspaces work too.

## v0.9.0 — 2026-11-23

### Added

- **Nine more servers**, all but DocumentDB tested in Docker (CI too;
  first published as ten):
  - **CrateDB** and **QuestDB** (PostgreSQL protocol), read through their own
    catalogs: columns, keys, DDL (`SHOW CREATE TABLE`), views. QuestDB pages
    with its `LIMIT low, high` and refuses row deletes with an explanation;
    CrateDB's grid edits are refreshed so they show at once.
  - **StarRocks** and **Apache Doris** (MySQL protocol, analytical): named in
    the top bar, case-insensitive search, edits and schema changes statement
    by statement, dumps without foreign-key settings.
  - **FerretDB** (MongoDB protocol on PostgreSQL), with text searches and
    Explain adapted to it, and the options it lacks left out of the UI;
    **Amazon DocumentDB** (TLS by default, retryable writes off; untested).
  - **Dragonfly**, **KeyDB** and **Garnet** (Redis protocol).
- Column types of CrateDB and QuestDB in the table designer.

## v0.8.0 — 2026-11-21

### Added

- **Redis and Valkey.** Connect with a host or a `redis://` / `rediss://`
  URL (TLS, ACL users and SSH are supported but untested). Numbered databases are listed;
  the sidebar shows all keys and the namespaces in them (`user:*`). A key
  list shows each key's type, TTL and value, and opens any key to its
  contents: hash fields, list items, set and sorted-set members, stream
  entries, strings and JSON. Keys, values, TTLs and a key's contents can be
  edited; the changes run as one watched `MULTI/EXEC`, so a commit is
  all or nothing. It doesn't notice a value changed since you loaded it
  (corrected after release). The query editor runs redis-cli style
  commands with completion and shows replies as tables. Export writes keys
  as commands with their expiry; Import script runs such a file.
- **DuckDB on Windows.** The Windows build now opens DuckDB files: it is
  built with cgo against DuckDB's Windows libraries (the `.exe` is bigger,
  about 120 MB). CI runs the DuckDB, SQLite, updater and export tests on a
  Windows machine.

### Changed

- The Windows `.exe`, `.zip` and installer are cross-built in a Fedora
  container, whose MinGW targets the UCRT as DuckDB's libraries need
  (`scripts/package.sh windows` uses `ucrt64-gcc-c++`, or `NO_DUCKDB=1`).

## v0.7.0 — 2026-11-21

### Added

- **A Windows installer** (`…-windows-amd64-setup.exe`): installs GoTable
  for the current user in `%LOCALAPPDATA%\Programs\GoTable` without an
  administrator, with a Start menu shortcut (a desktop one if ticked), an
  entry in Settings → Apps to uninstall it (keeping the saved connections),
  and *Start GoTable* at the end. The plain `.exe` and `.zip` stay, as the
  portable version.
- **Updates from GitHub.** Once a day GoTable checks for a newer release
  (Settings → Updates: on by default, *Check now*, or off) and offers it
  with its notes: *Update*, *Later* or *Skip this version*. The AppImage,
  the Linux `.tar.gz`, the Windows `.exe` and the macOS app replace
  themselves and restart; a `.deb` or `.rpm` is downloaded and opened with
  the system's installer. Releases are signed (`SHA256SUMS.sig`), and
  nothing whose signature or checksum doesn't match is installed. Copies
  from before this version must be updated by hand once.

- **Settings** (a gear on the home screen and in the workspace, the command
  palette, or Ctrl+,), saved in `settings.json` next to the connections:
  - theme: system, light or dark;
  - accent colour: eleven presets with their own dark-mode shades, or any
    colour; text on it stays readable;
  - the query editor's default text size;
  - keyboard shortcuts: every one can be given other keys, reset or removed,
    with clashes pointed out; keys the system needs (copy, undo…) are kept.
    Hints on buttons and in the command palette show the keys as set.
  - panels: whether the database rail, the sidebar and the row inspector
    start open, closed, or as they were last left (kept across restarts).
- Databases can be copied by name and deleted: right-click one in the
  rail (*Copy name*; on PostgreSQL and SQL Server the database's or the
  schema's), or *Delete a database…* in the Ctrl+D list and the command
  palette. The command is shown first, the name must be typed, and a schema
  can go with everything in it (CASCADE). The database GoTable is connected
  to, and the server's own (mysql, master, admin…), can't be deleted; on
  SQLite a database is a file and on Oracle a schema is a user, so neither
  is offered.
- Opening a table selects its first row and gives it the keyboard: arrow
  keys, Enter and typing work without clicking into the grid first (not
  while you are typing somewhere else, such as the table filter).
- The query editor's text can be made larger or smaller per tab: Ctrl+= /
  Ctrl+− / Ctrl+0, or the A− A+ buttons in its toolbar.
- The query editor highlights the whole statement that Run would run, not
  just the cursor's line, and follows the cursor to the next statement.
  Only a statement's own lines count: on a blank line (above, below or
  between statements) nothing is highlighted, and Run says so instead of
  running the statement below (as it used to).

### Fixed

- On Linux, the task bar showed a generic (Wayland) icon instead of
  GoTable's when it ran without being installed (the .tar.gz, the AppImage,
  a build from source): the shell finds an app's icon through its
  `.desktop` file, and there was none. GoTable now registers itself in
  `~/.local/share/applications` when no `gotable.desktop` exists, and gives
  way to the one a package installs (`GOTABLE_NO_DESKTOP_ENTRY=1` turns
  this off).
- The light or dark choice, and whether the sidebar, the database rail and
  the row inspector were shown, were forgotten when the desktop app
  restarted: the window's storage is tied to its address, which changes with
  every launch.

## v0.6.0 — 2026-11-20

MongoDB catches up with Compass, is tested on every supported version and
topology, and on Atlas.

### Added

- **Explain**: an Explain button and `.explain()` / `db.coll.explain()…` in the
  query editor draw the plan of a `find`, `aggregate`, `count` or `distinct`:
  its stages, the indexes used, documents and keys examined, time, per shard on
  a sharded cluster, with warnings for collection scans and in-memory sorts.
- **Index options**: a key per field (ascending, descending, text, 2dsphere,
  2d, hashed, or `$**` for every field), TTL, partial, sparse and hidden
  indexes; hide or unhide an index from the Structure tab, or with
  `hideIndex` / `unhideIndex`.
- **Validation rules**: view and edit a collection's validator (a
  `$jsonSchema` or a query), its level and its action.
- **New collection kinds**: capped, time series and clustered, with documents
  that expire.
- **Schema tab**: from a random sample, every field (nested ones and array
  elements too) with its types, how often it is there, its range and its most
  common values.
- **Aggregations tab**: build a pipeline stage by stage, each stage showing the
  documents it passes on; open it in the query editor to run, explain or save.
- **JSON import** into a collection: an array (as Compass exports) or one
  document a line (as `mongoexport` writes), in Extended JSON so types are
  kept; documents the server refuses can be skipped and are listed.
- Tests on MongoDB 4.4, 5.0, 6.0, 7.0 and 8.0, each standalone, as a replica
  set and as a sharded cluster, with TLS and X.509, over SSH, by
  `mongodb+srv://` with its own DNS server, and on Atlas
  (`GOTABLE_TEST_MONGOATLAS_URL`). CI runs 4.4 and 8.0 in every topology.

### Changed

- **JSON export** of a collection writes the documents themselves, in relaxed
  or canonical Extended JSON, instead of every value as text; it imports back
  unchanged.

### Fixed

- Atlas URLs with a database in them (`…mongodb.net/mydb?retryWrites=true`)
  failed to log in: the database was taken as where the user is defined,
  overriding Atlas's `authSource=admin`. `mongodb+srv://` now decides as the
  shell does: `authSource`, then the DNS TXT record, then the database.
- SSL mode "disable" was ignored for `mongodb+srv://`, which turns TLS on by
  itself.
- Timeouts set in a MongoDB connection's Options (`serverSelectionTimeoutMS`,
  `connectTimeoutMS`) were overridden by GoTable's own.
- Editing a nested document or array, or saving one unchanged along with
  another field, turned the longs inside it into ints. Nested numbers now keep
  their types unless the text names one (`NumberInt(…)`).
- An edit typed while a table's rows were reloading (just after a commit, a
  refresh or a page change) was silently lost. The grid now takes no edits
  until the rows are in. This affected every database.

## v0.5.0 — 2026-11-05

### Added

- MongoDB 4.4 and later: browse collections as tables, edit with types kept,
  a mongosh-style query editor, structure changes, CSV and JSON export, CSV
  import and whole-database scripts.

## v0.4.0 — 2026-11-02

### Added

- Oracle.

## v0.3.2 — 2026-11-02

### Added

- macOS: a native app for Apple silicon and Intel.

### Fixed

- Data grid: narrow tables fill the view; no stale scrollbar after closing the
  inspector.

## v0.3.1 — 2026-11-02

### Fixed

- Data grid: smooth scrolling and arrow keys on wide tables.

## v0.3.0 — 2026-11-02

### Added

- SQL Server, DuckDB, ClickHouse, CockroachDB, YugabyteDB, TiDB, SingleStore
  and more compatible databases.
- Row inspector: each field edited with its type's editor.
- Release notes show the full commit messages.

## v0.2.0 — 2026-10-21

### Added

- Structure editor, typed cell editors, editable query results and an SSH key
  picker.

## v0.1.1 — 2026-10-19

### Fixed

- The AppImage bundles WebKitGTK, so it runs on any Linux desktop.
- Releases carry the bare Windows `.exe` next to the `.zip`.

## v0.1.0 — 2026-10-19

First release: GoTable, a desktop database client.
