package server

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"gotable/internal/db"
)

// The ER diagram, the process list and the server's users.

const adminIdle = 3 * time.Minute

// withAdmin runs fn on the session's second connection, opening it first.
func (s *Server) withAdmin(ctx context.Context, se *session, fn func(c *db.Conn) error) error {
	se.adminMu.Lock()
	defer se.adminMu.Unlock()
	if se.admin == nil {
		cfg := *se.c().Config
		if cfg.Driver.IsFileBased() {
			return errors.New("a database file has no server with users or processes")
		}
		cred, err := db.LoadCredentials(s.secrets, cfg.ID)
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(ctx, db.SignInTime(&cfg, 30*time.Second))
		defer cancel()
		if se.admin, err = db.Open(ctx, &cfg, cred); err != nil {
			return err
		}
	}
	if se.adminTimer != nil {
		se.adminTimer.Stop()
	}
	se.adminTimer = time.AfterFunc(adminIdle, se.closeAdmin)
	return fn(se.admin)
}

func (se *session) closeAdmin() {
	se.adminMu.Lock()
	defer se.adminMu.Unlock()
	if se.adminTimer != nil {
		se.adminTimer.Stop()
		se.adminTimer = nil
	}
	if se.admin != nil {
		se.admin.Close()
		se.admin = nil
	}
}

func (s *Server) diagram(w http.ResponseWriter, r *http.Request) {
	se, err := s.session(r)
	if err != nil {
		writeErr(w, 410, err)
		return
	}
	tables, err := se.c().Diagram(r.Context(), r.URL.Query().Get("schema"))
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	type colDTO struct {
		Name       string `json:"name"`
		Type       string `json:"type"`
		Nullable   bool   `json:"nullable"`
		PrimaryKey bool   `json:"primaryKey"`
	}
	type tableDTO struct {
		Name        string    `json:"name"`
		Columns     []colDTO  `json:"columns"`
		ForeignKeys []*refDTO `json:"foreignKeys"`
	}
	out := make([]tableDTO, len(tables))
	for i, t := range tables {
		out[i] = tableDTO{Name: t.Name, Columns: []colDTO{}, ForeignKeys: []*refDTO{}}
		for _, c := range t.Columns {
			out[i].Columns = append(out[i].Columns, colDTO{c.Name, c.Type, c.Nullable, c.PrimaryKey})
		}
		for k := range t.ForeignKeys {
			out[i].ForeignKeys = append(out[i].ForeignKeys, toRefDTO(&t.ForeignKeys[k]))
		}
	}
	writeJSON(w, 200, map[string]any{"tables": out})
}

func (s *Server) processes(w http.ResponseWriter, r *http.Request) {
	se, err := s.session(r)
	if err != nil {
		writeErr(w, 410, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	var list *db.ProcessList
	if err := s.withAdmin(ctx, se, func(c *db.Conn) (err error) { list, err = c.Processes(ctx); return err }); err != nil {
		writeErr(w, 400, err)
		return
	}
	type procDTO struct {
		ID        string   `json:"id"`
		User      string   `json:"user"`
		Client    string   `json:"client"`
		App       string   `json:"app"`
		Database  string   `json:"database"`
		State     string   `json:"state"`
		Wait      string   `json:"wait"`
		Query     string   `json:"query"`
		Active    bool     `json:"active"`
		Seconds   float64  `json:"seconds"`
		BlockedBy []string `json:"blockedBy"`
	}
	out := make([]procDTO, len(list.Processes))
	for i, p := range list.Processes {
		p.BlockedBy = nonNil(p.BlockedBy)
		out[i] = procDTO(p)
	}
	writeJSON(w, 200, map[string]any{"processes": out, "canStop": list.CanStop, "canKill": list.CanKill})
}

func (s *Server) stopProcess(w http.ResponseWriter, r *http.Request) {
	se, err := s.session(r)
	if err != nil {
		writeErr(w, 410, err)
		return
	}
	var in struct {
		ID   string `json:"id"`
		Kill bool   `json:"kill"`
	}
	if err := decode(r, &in); err != nil || in.ID == "" {
		writeErr(w, 400, errors.New("choose a process"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if err := s.withAdmin(ctx, se, func(c *db.Conn) error { return c.StopProcess(ctx, in.ID, in.Kill) }); err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

type attributeDTO struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Hint  string `json:"hint"`
}

type presetDTO struct {
	Label      string   `json:"label"`
	Privileges []string `json:"privileges"`
}

type levelDTO struct {
	Key        string      `json:"key"`
	Label      string      `json:"label"`
	Privileges []string    `json:"privileges"`
	Needs      []string    `json:"needs"`
	Future     bool        `json:"future"`
	Presets    []presetDTO `json:"presets"`
}

type accountDTO struct {
	Name       string          `json:"name"`
	Host       string          `json:"host"`
	Attributes map[string]bool `json:"attributes"`
	Roles      []string        `json:"roles"`
	Locked     bool            `json:"locked"`
	Role       bool            `json:"role"`
	System     bool            `json:"system"`
	Note       string          `json:"note"`
}

type grantDTO struct {
	Level       string   `json:"level"`
	Database    string   `json:"database"`
	Schema      string   `json:"schema"`
	Object      string   `json:"object"`
	Privileges  []string `json:"privileges"`
	GrantOption bool     `json:"grantOption"`
	ReadOnly    string   `json:"readOnly"`
}

func (s *Server) users(w http.ResponseWriter, r *http.Request) {
	se, err := s.session(r)
	if err != nil {
		writeErr(w, 410, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	var a *db.UserAdmin
	var accounts []db.Account
	if err := s.withAdmin(ctx, se, func(c *db.Conn) (err error) { a, accounts, err = c.Users(ctx); return err }); err != nil {
		writeErr(w, 400, err)
		return
	}
	attrs := []attributeDTO{}
	for _, x := range a.Attributes {
		attrs = append(attrs, attributeDTO(x))
	}
	levels := []levelDTO{}
	for _, l := range a.Levels {
		d := levelDTO{l.Key, l.Label, nonNil(l.Privileges), nonNil(l.Needs), l.Future, []presetDTO{}}
		for _, p := range l.Presets {
			d.Presets = append(d.Presets, presetDTO(p))
		}
		levels = append(levels, d)
	}
	out := make([]accountDTO, len(accounts))
	for i, x := range accounts {
		x.Roles = nonNil(x.Roles)
		out[i] = accountDTO(x)
	}
	writeJSON(w, 200, map[string]any{"users": out, "admin": map[string]any{
		"me": a.Me, "hostLabel": a.HostLabel, "hostDefault": a.HostDefault, "attributes": attrs, "levels": levels, "roles": nonNil(a.Roles),
		"databases": nonNil(a.Databases), "grantOption": a.GrantOption, "canLock": a.CanLock, "canReassign": a.CanReassign,
		"privilegeWord": a.PrivilegeWord, "current": a.Current}})
}

func (s *Server) userGrants(w http.ResponseWriter, r *http.Request) {
	se, err := s.session(r)
	if err != nil {
		writeErr(w, 410, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	q := r.URL.Query()
	var grants []db.Grant
	if err := s.withAdmin(ctx, se, func(c *db.Conn) (err error) {
		grants, err = c.UserGrants(ctx, q.Get("name"), q.Get("host"))
		return err
	}); err != nil {
		writeErr(w, 400, err)
		return
	}
	out := make([]grantDTO, len(grants))
	for i, g := range grants {
		g.Privileges = nonNil(g.Privileges)
		out[i] = grantDTO(g)
	}
	writeJSON(w, 200, map[string]any{"grants": out})
}

// changeUsers previews (dryRun) or makes changes to accounts. What is shown
// and remembered has no passwords in it.
func (s *Server) changeUsers(w http.ResponseWriter, r *http.Request) {
	se, err := s.session(r)
	if err != nil {
		writeErr(w, 410, err)
		return
	}
	var in struct {
		Ops    []db.UserOp `json:"ops"`
		DryRun bool        `json:"dryRun"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, 400, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	var shown []string
	start := time.Now()
	err = s.withAdmin(ctx, se, func(c *db.Conn) (err error) { shown, err = c.ChangeUsers(ctx, in.Ops, in.DryRun); return err })
	if !in.DryRun && len(shown) > 0 {
		s.record(se, "commit", strings.Join(shown, ";\n")+";", time.Since(start), 0, err)
	}
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 200, map[string]any{"statements": nonNil(shown), "ok": !in.DryRun})
}
