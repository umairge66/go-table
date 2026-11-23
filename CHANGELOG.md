# Changelog

What changed in each release of GoTable. Releases are tagged `vX.Y.Z`; the
release pages on GitHub also carry the full commit messages.

## v0.9.0 — 2026-11-23

### Added

- **Ten more servers**, each tested in Docker (CI too):
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
  URL (with TLS, an ACL user, or over SSH). Numbered databases are listed;
  the sidebar shows all keys and the namespaces in them (`user:*`). A key
  list shows each key's type, TTL and value, and opens any key to its
  contents: hash fields, list items, set and sorted-set members, stream
  entries, strings and JSON. Keys, values, TTLs and a key's contents can be
  edited; the changes run as one watched `MULTI/EXEC`, so nothing is saved
  over a key that changed meanwhile. The query editor runs redis-cli style
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
