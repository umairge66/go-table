# Changelog

What changed in each release of GoTable. Releases are tagged `vX.Y.Z`; the
release pages on GitHub also carry the full commit messages.

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
