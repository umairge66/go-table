import type {
  ApiErrorBody, Column, Connection, DbObject, FileSource, Filter, Group, HistoryEntry, Index, Job, QueryResult, RowsResult, SavedQuery, Secrets, SessionInfo, Sort,
} from './types'

/** ApiError carries the server's plain-language explanation. */
export class ApiError extends Error {
  title: string
  hint?: string
  detail: string
  status: number
  constructor(body: ApiErrorBody, status: number) {
    super(body.title + (body.detail ? `: ${body.detail}` : ''))
    this.title = body.title
    this.hint = body.hint
    this.detail = body.detail
    this.status = status
  }
}

async function call<T>(method: string, path: string, body?: unknown, signal?: AbortSignal): Promise<T> {
  let res: Response
  try {
    res = await fetch(path, {
      method,
      headers: body === undefined ? undefined : { 'Content-Type': 'application/json' },
      body: body === undefined ? undefined : JSON.stringify(body),
      credentials: 'same-origin',
      signal,
    })
  } catch (e) {
    if ((e as Error).name === 'AbortError') throw e
    throw new ApiError({ title: 'GoTable is not responding', hint: 'The app may have been closed. Restart it.', detail: String(e) }, 0)
  }
  if (!res.ok) {
    let err: ApiErrorBody = { title: 'Something went wrong', detail: res.statusText }
    try {
      const j = await res.json()
      if (j?.error) err = j.error
    } catch {
      err.detail = await res.text().catch(() => res.statusText)
    }
    throw new ApiError(err, res.status)
  }
  return res.json() as Promise<T>
}

const q = (o: Record<string, string>) => new URLSearchParams(o).toString()

export interface TableRef { schema: string; name: string }

export interface PageReq {
  filters: Pick<Filter, 'column' | 'op' | 'value' | 'value2'>[]
  rawWhere: string
  sort: Sort[]
  limit: number
  offset: number
}

export interface ColumnDef {
  name: string
  type: string
  nullable: boolean
  defaultKind: '' | 'value' | 'expression' | 'null'
  default: string
  comment: string
  position: string // '' = end, 'first', or a column name (MySQL/MariaDB)
  collation?: string
  onUpdateNow?: boolean // MySQL/MariaDB DATETIME/TIMESTAMP
}

export interface NewColumn extends ColumnDef { primaryKey: boolean; autoIncrement: boolean; index?: '' | 'unique' | 'index' | 'fulltext' }
export interface NewTable { name: string; columns: NewColumn[]; comment: string; engine?: string; charset?: string; collation?: string; strict?: boolean }
export interface TypeInfo {
  customTypes: string[]; collations: string[]; charsets: string[]; engines: string[]
  defaultEngine: string; defaultCharset: string; defaultCollation: string; strict: boolean
}
export interface NewDatabase { name: string; kind: 'database' | 'schema'; charset: string }

export interface ChangeReq { kind: 'update' | 'insert' | 'delete'; key?: Record<string, string | null>; values?: Record<string, string | null> }

export const api = {
  connections: () => call<{ connections: Connection[]; groups: Group[] }>('GET', '/api/connections'),
  saveConnection: (c: Connection, secrets: Secrets) => call<Connection>('PUT', `/api/connections/${encodeURIComponent(c.id)}`, { connection: c, secrets }),
  deleteConnection: (id: string) => call('DELETE', `/api/connections/${encodeURIComponent(id)}`),
  duplicateConnection: (id: string) => call<Connection>('POST', `/api/connections/${encodeURIComponent(id)}/duplicate`),
  createGroup: (name: string) => call<Group>('POST', '/api/groups', { name }),
  updateGroup: (id: string, patch: Partial<Pick<Group, 'name' | 'expanded'>>) => call<Group>('PATCH', `/api/groups/${id}`, patch),
  deleteGroup: (id: string) => call('DELETE', `/api/groups/${id}`),
  test: (c: Connection, secrets: Secrets) => call<{ version: string; ms: number }>('POST', '/api/test', { connection: c, secrets }),
  parseURL: (url: string) => call<{ connection: Connection; password: string }>('POST', '/api/parse-url', { url }),

  queries: (connectionId: string) => call<SavedQuery[]>('GET', `/api/queries?${q({ connection: connectionId })}`),
  saveQuery: (sq: Omit<SavedQuery, 'updated'>) => call<SavedQuery>('PUT', `/api/queries/${encodeURIComponent(sq.id)}`, sq),
  deleteQuery: (id: string) => call('DELETE', `/api/queries/${encodeURIComponent(id)}`),
  history: (connectionId: string, text = '') => call<HistoryEntry[]>('GET', `/api/history?${q({ connection: connectionId, q: text })}`),
  clearHistory: (connectionId: string) => call('DELETE', `/api/history?${q({ connection: connectionId })}`),

  openSession: (connectionId: string) => call<SessionInfo>('POST', '/api/sessions', { connectionId }),
  closeSession: (sid: string) => call('DELETE', `/api/sessions/${sid}`),
  schemas: (sid: string) => call<{ schema: string; schemas: string[] }>('GET', `/api/sessions/${sid}/schemas`),
  databases: (sid: string) => call<{ database: string; databases: string[] }>('GET', `/api/sessions/${sid}/databases`),
  /** fork opens another database of the same server as a new session. */
  fork: (sid: string, database: string) => call<SessionInfo>('POST', `/api/sessions/${sid}/fork`, { database }),
  useSchema: (sid: string, schema: string) => call<{ schema: string }>('POST', `/api/sessions/${sid}/schema`, { schema }),
  objects: (sid: string, schema: string) => call<DbObject[]>('GET', `/api/sessions/${sid}/objects?${q({ schema })}`),
  structure: (sid: string, t: TableRef) => call<{ columns: Column[]; indexes: Index[] }>('GET', `/api/sessions/${sid}/structure?${q({ schema: t.schema, table: t.name })}`),
  definition: (sid: string, t: TableRef, kind: string) => call<{ ddl: string }>('GET', `/api/sessions/${sid}/definition?${q({ schema: t.schema, table: t.name, kind })}`),
  rows: (sid: string, table: TableRef, page: PageReq, columns: string[], signal?: AbortSignal) =>
    call<RowsResult>('POST', `/api/sessions/${sid}/rows`, { table, page, columns }, signal),
  changes: (sid: string, table: TableRef, changes: ChangeReq[], hasPK: boolean, dryRun: boolean) =>
    call<{ statements: string[]; ok?: boolean }>('POST', `/api/sessions/${sid}/changes`, { table, changes, hasPK, dryRun }),
  typeInfo: (sid: string, schema: string) => call<TypeInfo>('GET', `/api/sessions/${sid}/type-info?${q({ schema })}`),
  createTable: (sid: string, schema: string, table: NewTable, dryRun: boolean) =>
    call<{ statements: string[] }>('POST', `/api/sessions/${sid}/tables`, { schema, table, dryRun }),
  createDatabase: (sid: string, database: NewDatabase, dryRun: boolean) =>
    call<{ statements: string[] }>('POST', `/api/sessions/${sid}/databases`, { database, dryRun }),
  createSQLiteFile: (f: { name: string; path?: string; handle?: string; groupId?: string; tag?: string }) => call<Connection>('POST', '/api/sqlite-files', f),
  addColumn: (sid: string, table: TableRef, column: ColumnDef, dryRun: boolean) =>
    call<{ statements: string[]; ok?: boolean }>('POST', `/api/sessions/${sid}/columns`, { table, column, dryRun }),
  drop: (sid: string, objects: DbObject[], cascade: boolean, dryRun: boolean) =>
    call<{ statements: string[]; ok?: boolean }>('POST', `/api/sessions/${sid}/drop`, { objects, cascade, dryRun }),
  execute: (sid: string, sql: string, rowCap: number, queryId: string, schema?: string) =>
    call<{ results: QueryResult[]; schema: string }>('POST', `/api/sessions/${sid}/execute`, { sql, rowCap, queryId, schema }),
  cancel: (sid: string, queryId: string) => call('POST', `/api/sessions/${sid}/cancel`, { queryId }),
  completions: (sid: string, schema: string) => call<{ tables: Record<string, string[]> }>('GET', `/api/sessions/${sid}/completions?${q({ schema })}`),

  createJob: (sid: string, body: Record<string, unknown>) => call<Job>('POST', `/api/sessions/${sid}/jobs`, body),
  job: (id: string) => call<Job>('GET', `/api/jobs/${id}`),
  jobs: () => call<Job[]>('GET', '/api/jobs'),
  /** cancelJob stops a running job, or discards a finished one. */
  cancelJob: (id: string) => call('DELETE', `/api/jobs/${id}`),
  csvPreview: (source: FileSource, delimiter?: string) =>
    call<{ name: string; size: number; delimiter: string; rows: string[][] }>('POST', '/api/csv-preview', { ...source, delimiter }),

  /** upload streams a file to the server, reporting progress (0..1). */
  upload(file: File, onProgress: (fraction: number, loaded: number) => void, signal?: AbortSignal) {
    return new Promise<{ id: string; name: string; size: number }>((resolve, reject) => {
      const xhr = new XMLHttpRequest()
      xhr.open('POST', '/api/uploads')
      xhr.setRequestHeader('X-File-Name', file.name)
      xhr.upload.onprogress = (e) => onProgress(e.lengthComputable ? e.loaded / e.total : 0, e.loaded)
      xhr.onload = () => {
        let body: unknown = null
        try { body = JSON.parse(xhr.responseText) } catch { /* not JSON */ }
        if (xhr.status === 200) resolve(body as { id: string; name: string; size: number })
        else reject(new ApiError((body as { error?: ApiErrorBody })?.error ?? { title: 'Upload failed', detail: xhr.statusText }, xhr.status))
      }
      xhr.onerror = () => reject(new ApiError({ title: 'Upload failed', hint: 'Is GoTable still running?', detail: 'network error' }, 0))
      xhr.onabort = () => reject(new DOMException('Upload cancelled', 'AbortError'))
      signal?.addEventListener('abort', () => xhr.abort())
      xhr.send(file)
    })
  },

}
