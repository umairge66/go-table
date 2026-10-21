package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"gotable/internal/db"
	"gotable/internal/model"
)

func (s *Server) routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/connections", s.listConnections)
	mux.HandleFunc("PUT /api/connections/{id}", s.saveConnection)
	mux.HandleFunc("DELETE /api/connections/{id}", s.deleteConnection)
	mux.HandleFunc("POST /api/connections/{id}/duplicate", s.duplicateConnection)
	mux.HandleFunc("POST /api/groups", s.createGroup)
	mux.HandleFunc("PATCH /api/groups/{id}", s.updateGroup)
	mux.HandleFunc("DELETE /api/groups/{id}", s.deleteGroup)
	mux.HandleFunc("POST /api/test", s.testConnection)
	mux.HandleFunc("POST /api/parse-url", s.parseURL)

	mux.HandleFunc("GET /api/native", s.nativeInfo)
	mux.HandleFunc("POST /api/native/theme", s.nativeTheme)
	mux.HandleFunc("POST /api/native/window", s.nativeWindow)
	mux.HandleFunc("POST /api/dialog/save", s.dialogSave)
	mux.HandleFunc("POST /api/dialog/open", s.dialogOpen)
	mux.HandleFunc("POST /api/ssh-key/check", s.checkSSHKey)
	mux.HandleFunc("POST /api/jobs/{id}/save", s.saveJob)
	mux.HandleFunc("POST /api/files", s.saveBody)
	mux.HandleFunc("GET /api/queries", s.listQueries)
	mux.HandleFunc("PUT /api/queries/{id}", s.saveQuery)
	mux.HandleFunc("DELETE /api/queries/{id}", s.deleteQuery)
	mux.HandleFunc("GET /api/history", s.listHistory)
	mux.HandleFunc("DELETE /api/history", s.clearHistory)

	mux.HandleFunc("POST /api/sessions", s.openSession)
	mux.HandleFunc("DELETE /api/sessions/{sid}", s.closeSession)
	mux.HandleFunc("POST /api/sessions/{sid}/schema", s.useSchema)
	mux.HandleFunc("GET /api/sessions/{sid}/schemas", s.schemas)
	mux.HandleFunc("GET /api/sessions/{sid}/databases", s.databases)
	mux.HandleFunc("POST /api/sessions/{sid}/fork", s.forkSession)
	mux.HandleFunc("GET /api/sessions/{sid}/objects", s.objects)
	mux.HandleFunc("GET /api/sessions/{sid}/structure", s.structure)
	mux.HandleFunc("GET /api/sessions/{sid}/definition", s.definition)
	mux.HandleFunc("POST /api/sessions/{sid}/rows", s.rows)
	mux.HandleFunc("POST /api/sessions/{sid}/changes", s.changes)
	mux.HandleFunc("POST /api/sessions/{sid}/columns", s.addColumn)
	mux.HandleFunc("POST /api/sessions/{sid}/alter", s.alterTable)
	mux.HandleFunc("POST /api/sessions/{sid}/tables", s.createTable)
	mux.HandleFunc("GET /api/sessions/{sid}/type-info", s.typeInfo)
	mux.HandleFunc("POST /api/sessions/{sid}/databases", s.createDatabase)
	mux.HandleFunc("POST /api/sqlite-files", s.createSQLiteFile)
	mux.HandleFunc("POST /api/sessions/{sid}/drop", s.drop)
	mux.HandleFunc("POST /api/sessions/{sid}/execute", s.execute)
	mux.HandleFunc("POST /api/sessions/{sid}/cancel", s.cancel)
	mux.HandleFunc("GET /api/sessions/{sid}/completions", s.completions)
	mux.HandleFunc("POST /api/sessions/{sid}/jobs", s.createJob)
	mux.HandleFunc("POST /api/uploads", s.handleUpload)
	mux.HandleFunc("POST /api/csv-preview", s.csvPreview)
	mux.HandleFunc("GET /api/jobs", s.listJobs)
	mux.HandleFunc("GET /api/jobs/{id}", s.getJob)
	mux.HandleFunc("DELETE /api/jobs/{id}", s.deleteJob)
	mux.HandleFunc("GET /api/jobs/{id}/download", s.downloadJob)
}

// ---- connections ----

type secretFlags struct {
	Password      bool `json:"password"`
	SSHPassword   bool `json:"sshPassword"`
	SSHPassphrase bool `json:"sshPassphrase"`
}

type connectionDTO struct {
	*model.Connection
	Saved secretFlags `json:"saved"`
}

func (s *Server) dto(c *model.Connection) connectionDTO {
	cred, _ := db.LoadCredentials(s.secrets, c.ID)
	return connectionDTO{c, secretFlags{cred.Password != "", cred.SSHPassword != "", cred.SSHPassphrase != ""}}
}

func (s *Server) listConnections(w http.ResponseWriter, r *http.Request) {
	conns := make([]connectionDTO, 0, len(s.store.Connections))
	for _, c := range s.store.Connections {
		conns = append(conns, s.dto(c))
	}
	groups := s.store.Groups
	if groups == nil {
		groups = []*model.Group{}
	}
	writeJSON(w, 200, map[string]any{"connections": conns, "groups": groups})
}

// secretsIn carries passwords from the form. A nil field keeps the stored
// value; an empty string clears it.
type secretsIn struct {
	Password      *string `json:"password"`
	SSHPassword   *string `json:"sshPassword"`
	SSHPassphrase *string `json:"sshPassphrase"`
}

func (s *Server) credentials(id string, in secretsIn) (db.Credentials, error) {
	cred, err := db.LoadCredentials(s.secrets, id)
	if err != nil {
		return cred, err
	}
	if in.Password != nil {
		cred.Password = *in.Password
	}
	if in.SSHPassword != nil {
		cred.SSHPassword = *in.SSHPassword
	}
	if in.SSHPassphrase != nil {
		cred.SSHPassphrase = *in.SSHPassphrase
	}
	return cred, nil
}

type connectionIn struct {
	Connection *model.Connection `json:"connection"`
	Secrets    secretsIn         `json:"secrets"`
}

func normalize(c *model.Connection) {
	c.Name = strings.TrimSpace(c.Name)
	if c.Driver.IsFileBased() {
		c.Host, c.Port, c.User, c.DBName, c.SSLMode, c.UseSSHTunnel = "", 0, "", "", "", false
	} else {
		c.Path = ""
		if c.Host == "" {
			c.Host = "127.0.0.1"
		}
	}
	if c.Name == "" {
		switch {
		case c.Driver.IsFileBased() && c.Path != "":
			base := c.Path[strings.LastIndexAny(c.Path, `/\`)+1:]
			if i := strings.LastIndex(base, "."); i > 0 {
				base = base[:i]
			}
			c.Name = base
		case c.DBName != "":
			c.Name = c.DBName + " @ " + c.Host
		default:
			c.Name = c.Host
		}
	}
}

func (s *Server) saveConnection(w http.ResponseWriter, r *http.Request) {
	var in connectionIn
	if err := decode(r, &in); err != nil || in.Connection == nil {
		writeErr(w, 400, fmt.Errorf("invalid request: %v", err))
		return
	}
	c := in.Connection
	c.ID = r.PathValue("id")
	normalize(c)
	if err := c.Validate(); err != nil {
		writeErr(w, 400, err)
		return
	}
	cred, err := s.credentials(c.ID, in.Secrets)
	if err == nil {
		err = storeCredentials(s.secrets, c.ID, cred)
	}
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	s.store.Upsert(c)
	if err := s.store.Save(); err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, s.dto(c))
}

func storeCredentials(sec model.Secrets, id string, c db.Credentials) error {
	return errors.Join(sec.Set(id, model.SecretDBPassword, c.Password),
		sec.Set(id, model.SecretSSHPassword, c.SSHPassword),
		sec.Set(id, model.SecretSSHPassphrase, c.SSHPassphrase))
}

func (s *Server) deleteConnection(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.store.Delete(id)
	s.secrets.Clear(id)
	if err := s.store.Save(); err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) duplicateConnection(w http.ResponseWriter, r *http.Request) {
	c := s.store.Connection(r.PathValue("id"))
	if c == nil {
		writeErr(w, 404, errNotFound)
		return
	}
	cp := c.Clone()
	if cred, err := db.LoadCredentials(s.secrets, c.ID); err == nil {
		storeCredentials(s.secrets, cp.ID, cred)
	}
	s.store.Upsert(cp)
	if err := s.store.Save(); err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, s.dto(cp))
}

func (s *Server) createGroup(w http.ResponseWriter, r *http.Request) {
	var in struct{ Name string }
	if err := decode(r, &in); err != nil || strings.TrimSpace(in.Name) == "" {
		writeErr(w, 400, errors.New("group name is required"))
		return
	}
	g := s.store.AddGroup(strings.TrimSpace(in.Name))
	if err := s.store.Save(); err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, g)
}

func (s *Server) updateGroup(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name     *string `json:"name"`
		Expanded *bool   `json:"expanded"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, 400, err)
		return
	}
	for _, g := range s.store.Groups {
		if g.ID == r.PathValue("id") {
			if in.Name != nil && strings.TrimSpace(*in.Name) != "" {
				g.Name = strings.TrimSpace(*in.Name)
			}
			if in.Expanded != nil {
				g.Expanded = *in.Expanded
			}
			if err := s.store.Save(); err != nil {
				writeErr(w, 500, err)
				return
			}
			writeJSON(w, 200, g)
			return
		}
	}
	writeErr(w, 404, errNotFound)
}

func (s *Server) deleteGroup(w http.ResponseWriter, r *http.Request) {
	s.store.DeleteGroup(r.PathValue("id"))
	if err := s.store.Save(); err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) testConnection(w http.ResponseWriter, r *http.Request) {
	var in connectionIn
	if err := decode(r, &in); err != nil || in.Connection == nil {
		writeErr(w, 400, fmt.Errorf("invalid request: %v", err))
		return
	}
	c := in.Connection
	normalize(c)
	if err := c.Validate(); err != nil {
		writeErr(w, 400, err)
		return
	}
	cred, err := s.credentials(c.ID, in.Secrets)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	start := time.Now()
	version, err := db.Test(ctx, c, cred)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 200, map[string]any{"version": version, "ms": time.Since(start).Milliseconds()})
}

func (s *Server) parseURL(w http.ResponseWriter, r *http.Request) {
	var in struct{ URL string }
	if err := decode(r, &in); err != nil {
		writeErr(w, 400, err)
		return
	}
	c, pw, err := model.ParseURL(in.URL)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 200, map[string]any{"connection": c, "password": pw})
}

// ---- sessions ----

func (s *Server) openSession(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ConnectionID string `json:"connectionId"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, 400, err)
		return
	}
	stored := s.store.Connection(in.ConnectionID)
	if stored == nil {
		writeErr(w, 404, errors.New("connection not found"))
		return
	}
	cfg := *stored
	cred, err := db.LoadCredentials(s.secrets, cfg.ID)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	conn, err := db.Open(ctx, &cfg, cred)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	version, _ := conn.ServerVersion(ctx)
	schemas, err := conn.Schemas(ctx)
	if err != nil {
		conn.Close()
		writeErr(w, 400, err)
		return
	}
	se := &session{id: randomID(12), running: map[string]context.CancelFunc{}, database: conn.CurrentDatabase(ctx)}
	se.cur.Store(conn)
	s.mu.Lock()
	s.sessions[se.id] = se
	s.mu.Unlock()
	writeJSON(w, 200, map[string]any{"id": se.id, "connection": s.dto(stored), "version": version,
		"database": conn.CurrentDatabase(ctx), "schema": conn.Schema(), "schemas": nonNil(schemas)})
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// schemas returns the current schema list (it changes with CREATE SCHEMA,
// ATTACH DATABASE, …).
func (s *Server) schemas(w http.ResponseWriter, r *http.Request) {
	se, err := s.session(r)
	if err != nil {
		writeErr(w, 410, err)
		return
	}
	c := se.c()
	schemas, err := c.Schemas(r.Context())
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 200, map[string]any{"schema": c.Schema(), "schemas": nonNil(schemas)})
}

// databases lists the databases on the server (PostgreSQL: every database
// the user can connect to).
func (s *Server) databases(w http.ResponseWriter, r *http.Request) {
	se, err := s.session(r)
	if err != nil {
		writeErr(w, 410, err)
		return
	}
	c := se.c()
	dbs, err := c.Databases(r.Context())
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 200, map[string]any{"database": c.CurrentDatabase(r.Context()), "databases": nonNil(dbs)})
}

// forkSession opens a new session to another database on the same server
// (PostgreSQL databases can't be queried across one connection), so several
// databases can be open side by side.
func (s *Server) forkSession(w http.ResponseWriter, r *http.Request) {
	se, err := s.session(r)
	if err != nil {
		writeErr(w, 410, err)
		return
	}
	var in struct{ Database string }
	if err := decode(r, &in); err != nil || strings.TrimSpace(in.Database) == "" {
		writeErr(w, 400, errors.New("choose a database"))
		return
	}
	base := se.c()
	if base.Config.Driver == model.DriverSQLite {
		writeErr(w, 400, errors.New("a SQLite connection is one file; open another file as a new connection"))
		return
	}
	cfg := *base.Config
	cfg.DBName, cfg.SelectedSchema = in.Database, ""
	cred, err := db.LoadCredentials(s.secrets, cfg.ID)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	conn, err := db.Open(ctx, &cfg, cred)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	schemas, err := conn.Schemas(ctx)
	if err != nil {
		conn.Close()
		writeErr(w, 400, err)
		return
	}
	version, _ := conn.ServerVersion(ctx)
	fork := &session{id: randomID(12), running: map[string]context.CancelFunc{}, database: in.Database}
	fork.cur.Store(conn)
	s.mu.Lock()
	s.sessions[fork.id] = fork
	s.mu.Unlock()
	writeJSON(w, 200, map[string]any{"id": fork.id, "connection": s.dto(s.storedOr(&cfg)), "version": version,
		"database": conn.CurrentDatabase(ctx), "schema": conn.Schema(), "schemas": nonNil(schemas)})
}

// storedOr returns the saved connection with this ID, or c.
func (s *Server) storedOr(c *model.Connection) *model.Connection {
	if st := s.store.Connection(c.ID); st != nil {
		return st
	}
	return c
}

func (s *Server) closeSession(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	se, ok := s.sessions[r.PathValue("sid")]
	delete(s.sessions, r.PathValue("sid"))
	s.mu.Unlock()
	if ok {
		se.c().Close()
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) useSchema(w http.ResponseWriter, r *http.Request) {
	se, err := s.session(r)
	if err != nil {
		writeErr(w, 410, err)
		return
	}
	var in struct{ Schema string }
	if err := decode(r, &in); err != nil {
		writeErr(w, 400, err)
		return
	}
	if err := se.c().UseSchema(r.Context(), in.Schema); err != nil {
		writeErr(w, 400, err)
		return
	}
	if c := s.store.Connection(se.c().Config.ID); c != nil {
		c.SelectedSchema = in.Schema
		s.store.Save()
	}
	writeJSON(w, 200, map[string]string{"schema": se.c().Schema()})
}

type objectDTO struct {
	Schema string `json:"schema"`
	Name   string `json:"name"`
	Kind   string `json:"kind"`
}

func (s *Server) objects(w http.ResponseWriter, r *http.Request) {
	se, err := s.session(r)
	if err != nil {
		writeErr(w, 410, err)
		return
	}
	objs, err := se.c().Objects(r.Context(), r.URL.Query().Get("schema"))
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	out := make([]objectDTO, len(objs))
	for i, o := range objs {
		out[i] = objectDTO{o.Schema, o.Name, string(o.Kind)}
	}
	writeJSON(w, 200, out)
}

type columnDTO struct {
	Name          string         `json:"name"`
	Type          string         `json:"type"`
	Nullable      bool           `json:"nullable"`
	Default       *string        `json:"default"`
	PrimaryKey    bool           `json:"primaryKey"`
	Comment       string         `json:"comment"`
	Values        []string       `json:"values,omitempty"`
	References    *refDTO        `json:"references,omitempty"`
	Collation     string         `json:"collation"`
	AutoIncrement bool           `json:"autoIncrement"`
	OnUpdateNow   bool           `json:"onUpdateNow"`
	DefaultKind   db.DefaultKind `json:"defaultKind"`
	DefaultText   string         `json:"defaultText"`
}

type refDTO struct {
	Name     string   `json:"name"`
	Schema   string   `json:"schema"`
	Table    string   `json:"table"`
	From     []string `json:"from"`
	Columns  []string `json:"columns"`
	OnDelete string   `json:"onDelete"`
	OnUpdate string   `json:"onUpdate"`
}

func toRefDTO(f *db.ForeignRef) *refDTO {
	if f == nil {
		return nil
	}
	return &refDTO{f.Name, f.Schema, f.Table, f.From, f.Columns, f.OnDelete, f.OnUpdate}
}

type indexDTO struct {
	Name       string   `json:"name"`
	Columns    []string `json:"columns"`
	Unique     bool     `json:"unique"`
	Primary    bool     `json:"primary"`
	Type       string   `json:"type"`
	Constraint bool     `json:"constraint"`
}

type checkDTO struct {
	Name       string `json:"name"`
	Expression string `json:"expression"`
}

type tableInfoDTO struct {
	Comment       string     `json:"comment"`
	Engine        string     `json:"engine"`
	Collation     string     `json:"collation"`
	AutoIncrement int64      `json:"autoIncrement"`
	Checks        []checkDTO `json:"checks"`
}

func tableRef(r *http.Request) db.TableRef {
	q := r.URL.Query()
	return db.TableRef{Schema: q.Get("schema"), Name: q.Get("table")}
}

func (s *Server) structure(w http.ResponseWriter, r *http.Request) {
	se, err := s.session(r)
	if err != nil {
		writeErr(w, 410, err)
		return
	}
	ref := tableRef(r)
	cols, err := se.c().Columns(r.Context(), ref)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	idx, err := se.c().Indexes(r.Context(), ref)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	cout := make([]columnDTO, len(cols))
	for i, c := range cols {
		cout[i] = columnDTO{c.Name, c.Type, c.Nullable, c.Default, c.PrimaryKey, c.Comment, c.Values, toRefDTO(c.References),
			c.Collation, c.AutoIncrement, c.OnUpdateNow, c.DefaultKind, c.DefaultText}
	}
	iout := make([]indexDTO, len(idx))
	for i, x := range idx {
		iout[i] = indexDTO{x.Name, nonNil(x.Columns), x.Unique, x.Primary, x.Type, x.Constraint}
	}
	// Foreign keys and table settings are extras: missing privileges must
	// not hide the table.
	fout := []*refDTO{}
	if fks, err := se.c().ForeignKeys(r.Context(), ref); err == nil {
		for i := range fks {
			fout = append(fout, toRefDTO(&fks[i]))
		}
	}
	info := tableInfoDTO{Checks: []checkDTO{}}
	if ti, err := se.c().TableInfo(r.Context(), ref); err == nil {
		info = tableInfoDTO{ti.Comment, ti.Engine, ti.Collation, ti.AutoIncrement, []checkDTO{}}
		for _, ck := range ti.Checks {
			info.Checks = append(info.Checks, checkDTO{ck.Name, ck.Expression})
		}
	}
	writeJSON(w, 200, map[string]any{"columns": cout, "indexes": iout, "foreignKeys": fout, "table": info})
}

// alterTable previews (dryRun) or applies changes to a table's structure.
func (s *Server) alterTable(w http.ResponseWriter, r *http.Request) {
	se, err := s.session(r)
	if err != nil {
		writeErr(w, 410, err)
		return
	}
	var in struct {
		Table  tableIn      `json:"table"`
		Ops    []db.AlterOp `json:"ops"`
		DryRun bool         `json:"dryRun"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, 400, err)
		return
	}
	stmts, err := se.c().Alter(r.Context(), db.TableRef{Schema: in.Table.Schema, Name: in.Table.Name}, in.Ops, in.DryRun)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": !in.DryRun, "statements": nonNil(stmts)})
}

func (s *Server) definition(w http.ResponseWriter, r *http.Request) {
	se, err := s.session(r)
	if err != nil {
		writeErr(w, 410, err)
		return
	}
	ref := tableRef(r)
	ddl, err := se.c().Definition(r.Context(), db.Object{Schema: ref.Schema, Name: ref.Name, Kind: db.ObjectKind(r.URL.Query().Get("kind"))})
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 200, map[string]string{"ddl": ddl})
}

// rowsOut converts values to JSON: strings, with null for SQL NULL.
func rowsOut(rows [][]db.Value) [][]*string {
	out := make([][]*string, len(rows))
	for i, row := range rows {
		r := make([]*string, len(row))
		for j, v := range row {
			if !v.Null {
				t := v.Text
				r[j] = &t
			}
		}
		out[i] = r
	}
	return out
}

func valuesIn(m map[string]*string) map[string]db.Value {
	out := make(map[string]db.Value, len(m))
	for k, v := range m {
		if v == nil {
			out[k] = db.NullValue
		} else {
			out[k] = db.Text(*v)
		}
	}
	return out
}

type tableIn struct {
	Schema string `json:"schema"`
	Name   string `json:"name"`
}

type pageIn struct {
	Filters []struct {
		Column string `json:"column"`
		Op     string `json:"op"`
		Value  string `json:"value"`
		Value2 string `json:"value2"`
	} `json:"filters"`
	RawWhere string `json:"rawWhere"`
	Sort     []struct {
		Column string `json:"column"`
		Dir    string `json:"dir"`
	} `json:"sort"`
	Limit  int `json:"limit"`
	Offset int `json:"offset"`
}

func (p pageIn) page() db.Page {
	var out db.Page
	for _, f := range p.Filters {
		out.Filters = append(out.Filters, db.Filter{Column: f.Column, Op: db.Op(f.Op), Value: f.Value, Value2: f.Value2})
	}
	for _, so := range p.Sort {
		dir := db.Asc
		if strings.EqualFold(so.Dir, "desc") {
			dir = db.Desc
		}
		out.Sort = append(out.Sort, db.Sort{Column: so.Column, Dir: dir})
	}
	out.RawWhere, out.Limit, out.Offset = p.RawWhere, p.Limit, p.Offset
	return out
}

func (s *Server) rows(w http.ResponseWriter, r *http.Request) {
	se, err := s.session(r)
	if err != nil {
		writeErr(w, 410, err)
		return
	}
	var in struct {
		Table   tableIn  `json:"table"`
		Page    pageIn   `json:"page"`
		Columns []string `json:"columns"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, 400, err)
		return
	}
	ref := db.TableRef{Schema: in.Table.Schema, Name: in.Table.Name}
	page := in.Page.page()
	start := time.Now()
	rs, err := se.c().FetchRows(r.Context(), ref, page, in.Columns)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	total, err := se.c().CountRows(r.Context(), ref, page, in.Columns)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 200, map[string]any{"columns": nonNil(rs.Columns), "types": nonNil(rs.Types), "rows": rowsOut(rs.Rows),
		"total": total, "ms": time.Since(start).Milliseconds()})
}

func (s *Server) changes(w http.ResponseWriter, r *http.Request) {
	se, err := s.session(r)
	if err != nil {
		writeErr(w, 410, err)
		return
	}
	var in struct {
		Table   tableIn `json:"table"`
		HasPK   bool    `json:"hasPK"`
		DryRun  bool    `json:"dryRun"`
		Changes []struct {
			Kind   string             `json:"kind"`
			Key    map[string]*string `json:"key"`
			Values map[string]*string `json:"values"`
		} `json:"changes"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, 400, err)
		return
	}
	ref := db.TableRef{Schema: in.Table.Schema, Name: in.Table.Name}
	var changes []db.Change
	var preview []string
	for _, c := range in.Changes {
		ch := db.Change{Key: valuesIn(c.Key), Values: valuesIn(c.Values)}
		switch c.Kind {
		case "update":
			ch.Kind = db.ChangeUpdate
		case "insert":
			ch.Kind = db.ChangeInsert
		case "delete":
			ch.Kind = db.ChangeDelete
		default:
			writeErr(w, 400, fmt.Errorf("unknown change kind %q", c.Kind))
			return
		}
		st, err := db.BuildChange(se.c().Dialect, ref, ch, in.HasPK)
		if err != nil {
			writeErr(w, 400, err)
			return
		}
		preview = append(preview, db.Preview(se.c().Dialect, st))
		changes = append(changes, ch)
	}
	if in.DryRun {
		writeJSON(w, 200, map[string]any{"statements": nonNil(preview)})
		return
	}
	start := time.Now()
	err = se.c().ApplyChanges(r.Context(), ref, changes, in.HasPK)
	s.record(se, "commit", strings.Join(preview, "\n"), time.Since(start), int64(len(changes)), err)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "statements": nonNil(preview)})
}

// addColumn previews (dryRun) or runs ALTER TABLE … ADD COLUMN.
func (s *Server) addColumn(w http.ResponseWriter, r *http.Request) {
	se, err := s.session(r)
	if err != nil {
		writeErr(w, 410, err)
		return
	}
	var in struct {
		Table  tableIn      `json:"table"`
		Column db.ColumnDef `json:"column"`
		DryRun bool         `json:"dryRun"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, 400, err)
		return
	}
	ref := db.TableRef{Schema: in.Table.Schema, Name: in.Table.Name}
	if in.DryRun {
		stmts, err := db.BuildAddColumn(se.c().Dialect, ref, in.Column)
		if err != nil {
			writeErr(w, 400, err)
			return
		}
		writeJSON(w, 200, map[string]any{"statements": stmts})
		return
	}
	stmts, err := se.c().AddColumn(r.Context(), ref, in.Column)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "statements": stmts})
}

// drop previews (dryRun) or drops tables and views.
func (s *Server) drop(w http.ResponseWriter, r *http.Request) {
	se, err := s.session(r)
	if err != nil {
		writeErr(w, 410, err)
		return
	}
	var in struct {
		Objects []objectDTO `json:"objects"`
		Cascade bool        `json:"cascade"`
		DryRun  bool        `json:"dryRun"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, 400, err)
		return
	}
	objs := make([]db.Object, len(in.Objects))
	for i, o := range in.Objects {
		objs[i] = db.Object{Schema: o.Schema, Name: o.Name, Kind: db.ObjectKind(o.Kind)}
	}
	if in.DryRun {
		stmts, err := db.BuildDrop(se.c().Dialect, objs, in.Cascade)
		if err != nil {
			writeErr(w, 400, err)
			return
		}
		writeJSON(w, 200, map[string]any{"statements": stmts})
		return
	}
	stmts, err := se.c().Drop(r.Context(), objs, in.Cascade)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "statements": stmts})
}

type resultDTO struct {
	Statement    string      `json:"statement"`
	Columns      []string    `json:"columns,omitempty"`
	Types        []string    `json:"types,omitempty"`
	Rows         [][]*string `json:"rows,omitempty"`
	HasRows      bool        `json:"hasRows"`
	RowsAffected int64       `json:"rowsAffected"`
	Ms           float64     `json:"ms"`
	Truncated    bool        `json:"truncated"`
	Error        *apiError   `json:"error,omitempty"`
}

func (s *Server) execute(w http.ResponseWriter, r *http.Request) {
	se, err := s.session(r)
	if err != nil {
		writeErr(w, 410, err)
		return
	}
	var in struct {
		SQL     string `json:"sql"`
		RowCap  int    `json:"rowCap"`
		QueryID string `json:"queryId"`
		// Schema is the database the query tab belongs to; the session
		// switches to it first when several are open.
		Schema string `json:"schema"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, 400, err)
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	if in.QueryID != "" {
		se.mu.Lock()
		se.running[in.QueryID] = cancel
		se.mu.Unlock()
		defer func() {
			se.mu.Lock()
			delete(se.running, in.QueryID)
			se.mu.Unlock()
		}()
	}
	results, err := se.c().ExecuteIn(ctx, in.Schema, in.SQL, in.RowCap)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	s.recordResults(se, results)
	out := make([]resultDTO, len(results))
	for i, res := range results {
		d := resultDTO{Statement: res.Statement, RowsAffected: res.RowsAffected,
			Ms: float64(res.Duration.Microseconds()) / 1000, Truncated: res.Truncated}
		if res.Set != nil {
			d.HasRows = true
			d.Columns, d.Types, d.Rows = nonNil(res.Set.Columns), nonNil(res.Set.Types), rowsOut(res.Set.Rows)
		}
		if res.Err != nil {
			f := Explain(res.Err)
			d.Error = &apiError{Title: f.Title, Hint: f.Hint, Detail: res.Err.Error()}
		}
		out[i] = d
	}
	writeJSON(w, 200, map[string]any{"results": out, "schema": se.c().Schema()})
}

func (s *Server) cancel(w http.ResponseWriter, r *http.Request) {
	se, err := s.session(r)
	if err != nil {
		writeErr(w, 410, err)
		return
	}
	var in struct {
		QueryID string `json:"queryId"`
	}
	decode(r, &in)
	se.mu.Lock()
	c, ok := se.running[in.QueryID]
	se.mu.Unlock()
	if ok {
		c()
	}
	writeJSON(w, 200, map[string]bool{"cancelled": ok})
}

// completions returns table and column names of a schema for the editor's
// autocomplete. Large schemas are capped.
func (s *Server) completions(w http.ResponseWriter, r *http.Request) {
	se, err := s.session(r)
	if err != nil {
		writeErr(w, 410, err)
		return
	}
	schema := r.URL.Query().Get("schema")
	objs, err := se.c().Objects(r.Context(), schema)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	tables := map[string][]string{}
	for i, o := range objs {
		if i >= 400 {
			tables[o.Name] = []string{}
			continue
		}
		cols, err := se.c().Columns(r.Context(), db.TableRef{Schema: o.Schema, Name: o.Name})
		names := make([]string, 0, len(cols))
		if err == nil {
			for _, c := range cols {
				names = append(names, c.Name)
			}
		}
		tables[o.Name] = names
	}
	writeJSON(w, 200, map[string]any{"tables": tables})
}

// ---- saved queries and history

func (s *Server) record(se *session, kind, sql string, d time.Duration, rows int64, err error) {
	c := se.c()
	e := model.HistoryEntry{ConnectionID: c.Config.ID, Database: se.database, Schema: c.Schema(), Kind: kind, SQL: sql,
		At: time.Now().UTC(), Ms: float64(d.Microseconds()) / 1000, Rows: rows}
	if err != nil {
		e.Error = err.Error()
	}
	s.history.Add(e)
}

func (s *Server) recordResults(se *session, results []db.Result) {
	c := se.c()
	entries := make([]model.HistoryEntry, 0, len(results))
	for _, r := range results {
		e := model.HistoryEntry{ConnectionID: c.Config.ID, Database: se.database, Schema: c.Schema(), Kind: "query", SQL: r.Statement,
			At: time.Now().UTC(), Ms: float64(r.Duration.Microseconds()) / 1000, Rows: r.RowsAffected}
		if r.Set != nil {
			e.Rows = int64(len(r.Set.Rows))
		}
		if r.Err != nil {
			e.Error = r.Err.Error()
		}
		entries = append(entries, e)
	}
	s.history.Add(entries...)
}

func (s *Server) listQueries(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.queries.List(r.URL.Query().Get("connection")))
}

func (s *Server) saveQuery(w http.ResponseWriter, r *http.Request) {
	var q model.SavedQuery
	if err := decode(r, &q); err != nil {
		writeErr(w, 400, err)
		return
	}
	q.ID = r.PathValue("id")
	saved, err := s.queries.Upsert(q)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 200, saved)
}

func (s *Server) deleteQuery(w http.ResponseWriter, r *http.Request) {
	if err := s.queries.Delete(r.PathValue("id")); err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) listHistory(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 {
		limit = 300
	}
	writeJSON(w, 200, s.history.List(q.Get("connection"), q.Get("q"), limit))
}

func (s *Server) clearHistory(w http.ResponseWriter, r *http.Request) {
	if err := s.history.Clear(r.URL.Query().Get("connection")); err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}
