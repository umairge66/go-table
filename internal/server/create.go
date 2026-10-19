package server

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"gotable/internal/db"
	"gotable/internal/model"
)

// createTable previews (dryRun) or runs CREATE TABLE.
func (s *Server) createTable(w http.ResponseWriter, r *http.Request) {
	se, err := s.session(r)
	if err != nil {
		writeErr(w, 410, err)
		return
	}
	var in struct {
		Schema string      `json:"schema"`
		Table  db.NewTable `json:"table"`
		DryRun bool        `json:"dryRun"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, 400, err)
		return
	}
	if in.Schema == "" {
		in.Schema = se.c().Schema()
	}
	if in.DryRun {
		stmts, err := db.BuildCreateTable(se.c().Dialect, in.Schema, in.Table)
		if err != nil {
			writeErr(w, 400, err)
			return
		}
		writeJSON(w, 200, map[string]any{"statements": stmts})
		return
	}
	stmts, err := se.c().CreateTable(r.Context(), in.Schema, in.Table)
	s.record(se, "query", strings.Join(stmts, "\n"), 0, 0, err)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "statements": stmts})
}

// createDatabase previews (dryRun) or runs CREATE DATABASE / CREATE SCHEMA.
func (s *Server) createDatabase(w http.ResponseWriter, r *http.Request) {
	se, err := s.session(r)
	if err != nil {
		writeErr(w, 410, err)
		return
	}
	var in struct {
		Database db.NewDatabase `json:"database"`
		DryRun   bool           `json:"dryRun"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, 400, err)
		return
	}
	if in.DryRun {
		stmt, err := db.BuildCreateDatabase(se.c().Dialect, in.Database)
		if err != nil {
			writeErr(w, 400, err)
			return
		}
		writeJSON(w, 200, map[string]any{"statements": []string{stmt}})
		return
	}
	stmt, err := se.c().CreateDatabase(r.Context(), in.Database)
	s.record(se, "query", stmt, 0, 0, err)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "statements": []string{stmt}})
}

// createSQLiteFile makes a new, empty SQLite database file and saves a
// connection to it. The place is a typed path or a save-dialog handle.
func (s *Server) createSQLiteFile(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name    string    `json:"name"`
		Path    string    `json:"path"`
		Handle  string    `json:"handle"`
		GroupID string    `json:"groupId"`
		Tag     model.Tag `json:"tag"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, 400, err)
		return
	}
	path := strings.TrimSpace(in.Path)
	if in.Handle != "" {
		p, err := s.takeTarget(in.Handle)
		if err != nil {
			writeErr(w, 400, err)
			return
		}
		path = p
	} else {
		path = expandHome(path)
	}
	if path == "" || !filepath.IsAbs(path) {
		writeErr(w, 400, errors.New("choose where to create the database file (an absolute path)"))
		return
	}
	// A save dialog may already have asked about replacing a file; never
	// replace one here anyway, since that would discard a database.
	if _, err := os.Stat(path); err == nil {
		writeErr(w, 400, fmt.Errorf("%s already exists; open it as a connection instead", path))
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		writeErr(w, 400, err)
		return
	}
	c := model.NewConnection(model.DriverSQLite)
	c.Path = path
	c.Name = strings.TrimSpace(in.Name)
	if c.Name == "" {
		c.Name = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	}
	c.GroupID, c.Tag = in.GroupID, in.Tag
	normalize(c)
	if err := c.Validate(); err != nil {
		writeErr(w, 400, err)
		return
	}
	conn, err := db.Open(r.Context(), c, db.Credentials{}) // creates the file
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	conn.Close()
	s.store.Upsert(c)
	if err := s.store.Save(); err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, s.dto(c))
}

// typeInfo lists what the table designer can offer for the schema.
func (s *Server) typeInfo(w http.ResponseWriter, r *http.Request) {
	se, err := s.session(r)
	if err != nil {
		writeErr(w, 410, err)
		return
	}
	schema := r.URL.Query().Get("schema")
	if schema == "" {
		schema = se.c().Schema()
	}
	info, err := se.c().TypeInfo(r.Context(), schema)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 200, info)
}
