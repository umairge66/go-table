//go:build cgo

package db

import (
	"path/filepath"
	"testing"

	"gotable/internal/model"
)

func TestDuckDB(t *testing.T) {
	cfg := model.NewConnection(model.DriverDuckDB)
	cfg.Name = "test"
	cfg.Path = filepath.Join(t.TempDir(), "test.duckdb")
	runSuite(t, openTest(t, cfg, ""))
}
