// Package app contains application-level services shared by the CLI and web
// interfaces.
package app

import (
	"context"
	"database/sql"
	"fmt"
	"os"

	"github.com/pjg/face_me_bro/internal/config"
	"github.com/pjg/face_me_bro/internal/db"
)

// App owns the process-wide configuration and database connection.
type App struct {
	Config config.Resolved
	DB     *sql.DB
}

// Open loads configuration and opens the configured database.
func Open(ctx context.Context, configPath string, noMigrate bool) (*App, error) {
	return OpenWithOptions(ctx, configPath, noMigrate, db.OpenOptions{})
}

// OpenWithOptions opens the application with database access appropriate for
// the command being run. Read-only commands use SQLite read-only connections;
// only status and serve should pass migration checks.
func OpenWithOptions(ctx context.Context, configPath string, noMigrate bool, options db.OpenOptions) (*App, error) {
	settings, err := config.Load(configPath)
	if err != nil {
		return nil, err
	}
	if err := ensureRuntimeDirectories(settings); err != nil {
		return nil, err
	}
	options.NoMigrate = noMigrate
	options.BackupDir = settings.BackupRoot
	database, err := db.OpenWithOptions(ctx, settings.DatabasePath, options)
	if err != nil {
		return nil, err
	}
	return &App{Config: settings, DB: database}, nil
}

// Close releases resources owned by the application.
func (a *App) Close() error {
	if a == nil || a.DB == nil {
		return nil
	}
	return a.DB.Close()
}

// Status is the stable data returned by the status command.
type Status struct {
	ConfigFile           string  `json:"config_file"`
	RuntimeDataRoot      string  `json:"runtime_data_root"`
	DatabasePath         string  `json:"database_path"`
	ModelRoot            string  `json:"model_root"`
	LogRoot              string  `json:"log_root"`
	TempRoot             string  `json:"temp_root"`
	ExportRoot           string  `json:"export_root"`
	BackupRoot           string  `json:"backup_root"`
	NeighborSearchMethod string  `json:"neighbor_search_method"`
	PipelineID           *string `json:"pipeline_id"`
	Provider             string  `json:"provider"`
}

// Status returns resolved paths and the latest registered pipeline, if any.
func (a *App) Status(ctx context.Context) (Status, error) {
	status := Status{
		ConfigFile:           a.Config.ConfigFile,
		RuntimeDataRoot:      a.Config.RuntimeDataRoot,
		DatabasePath:         a.Config.DatabasePath,
		ModelRoot:            a.Config.ModelRoot,
		LogRoot:              a.Config.LogRoot,
		TempRoot:             a.Config.TempRoot,
		ExportRoot:           a.Config.ExportRoot,
		BackupRoot:           a.Config.BackupRoot,
		NeighborSearchMethod: a.Config.NeighborSearchMethod,
		Provider:             "not configured",
	}
	var id, provider string
	err := a.DB.QueryRowContext(ctx, `
		SELECT id, execution_provider
		FROM pipelines
		ORDER BY created_at DESC, id DESC
		LIMIT 1`).Scan(&id, &provider)
	if err == sql.ErrNoRows {
		return status, nil
	}
	if err != nil {
		return Status{}, fmt.Errorf("read active pipeline: %w", err)
	}
	status.PipelineID = &id
	status.Provider = provider
	return status, nil
}

func ensureRuntimeDirectories(settings config.Resolved) error {
	for _, path := range []string{
		settings.RuntimeDataRoot,
		settings.ModelRoot,
		settings.LogRoot,
		settings.TempRoot,
		settings.ExportRoot,
		settings.BackupRoot,
	} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			return fmt.Errorf("create runtime directory %q: %w", path, err)
		}
	}
	return nil
}
