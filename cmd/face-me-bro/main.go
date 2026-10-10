package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/pjg/face_me_bro/internal/analysis"
	"github.com/pjg/face_me_bro/internal/app"
	"github.com/pjg/face_me_bro/internal/audit"
	"github.com/pjg/face_me_bro/internal/auth"
	"github.com/pjg/face_me_bro/internal/clustering"
	"github.com/pjg/face_me_bro/internal/config"
	"github.com/pjg/face_me_bro/internal/db"
	"github.com/pjg/face_me_bro/internal/id"
	"github.com/pjg/face_me_bro/internal/jobs"
	"github.com/pjg/face_me_bro/internal/media"
	"github.com/pjg/face_me_bro/internal/recognition"
	"github.com/pjg/face_me_bro/internal/search"
	"github.com/pjg/face_me_bro/internal/setup"
	"github.com/pjg/face_me_bro/internal/web"
	"github.com/pjg/face_me_bro/internal/worker"
)

const outputSchemaVersion = "1"

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "face-me-bro: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("face-me-bro", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", "", "configuration file or directory")
	noMigrate := flags.Bool("no-migrate", false, "refuse to create or upgrade the database schema")
	jsonOutput := flags.Bool("json", false, "emit versioned JSON output")
	if err := flags.Parse(args); err != nil {
		return err
	}
	command := "status"
	if flags.NArg() > 0 {
		command = flags.Arg(0)
	}
	if command == "restore" {
		restoreFlags := flag.NewFlagSet("restore", flag.ContinueOnError)
		restoreFlags.SetOutput(stderr)
		input := restoreFlags.String("input", "", "backup file")
		confirmed := restoreFlags.Bool("yes", false, "confirm database replacement")
		if err := restoreFlags.Parse(flags.Args()[1:]); err != nil {
			return err
		}
		if *input == "" || !*confirmed {
			return fmt.Errorf("restore requires --input and --yes")
		}
		settings, err := config.Load(*configPath)
		if err != nil {
			return err
		}
		if err := db.Restore(settings.DatabasePath, *input); err != nil {
			return err
		}
		if *jsonOutput {
			return writeJSON(stdout, envelope("restore", map[string]string{"database": settings.DatabasePath}))
		}
		fmt.Fprintln(stdout, settings.DatabasePath)
		return nil
	}
	application, err := app.Open(ctx, *configPath, *noMigrate)
	if err != nil {
		return err
	}
	defer application.Close()
	return dispatch(ctx, application, command, flags.Args()[1:], *jsonOutput, stdout, stderr)
}

func dispatch(ctx context.Context, application *app.App, command string, args []string, jsonOutput bool, stdout, stderr io.Writer) error {
	switch command {
	case "status":
		status, err := application.Status(ctx)
		if err != nil {
			return err
		}
		if jsonOutput {
			return writeJSON(stdout, envelope(command, status))
		}
		fmt.Fprintf(stdout, "Config file:      %s\nRuntime root:     %s\nDatabase:         %s\nModel root:       %s\nLog root:         %s\nTemp root:        %s\nExport root:      %s\nBackup root:      %s\n", status.ConfigFile, status.RuntimeDataRoot, status.DatabasePath, status.ModelRoot, status.LogRoot, status.TempRoot, status.ExportRoot, status.BackupRoot)
		if status.PipelineID == nil {
			fmt.Fprintln(stdout, "Pipeline:         not configured")
		} else {
			fmt.Fprintf(stdout, "Pipeline:         %s\n", *status.PipelineID)
		}
		fmt.Fprintf(stdout, "Provider:         %s\n", status.Provider)
		return nil
	case "collection":
		return collectionCommand(ctx, application, args, jsonOutput, stdout, stderr)
	case "scan":
		return scanCommand(ctx, application, args, jsonOutput, stdout, stderr)
	case "job":
		return jobCommand(ctx, application, args, jsonOutput, stdout, stderr)
	case "account":
		return accountCommand(ctx, application, args, jsonOutput, stdout, stderr)
	case "setup":
		return setupCommand(ctx, application, args, jsonOutput, stdout, stderr)
	case "serve":
		return serveCommand(ctx, application, args, stdout, stderr)
	case "pipeline":
		return pipelineCommand(ctx, application, args, jsonOutput, stdout, stderr)
	case "worker":
		return workerCommand(ctx, application, args, stdout, stderr)
	case "people":
		return peopleCommand(ctx, application, args, jsonOutput, stdout, stderr)
	case "search":
		return searchCommand(ctx, application, args, jsonOutput, stdout, stderr)
	case "audit":
		return auditCommand(ctx, application, args, jsonOutput, stdout, stderr)
	case "backup":
		return backupCommand(application, args, jsonOutput, stdout)
	case "export":
		return exportCommand(ctx, application, args, jsonOutput, stdout, stderr)
	case "purge":
		return purgeCommand(ctx, application, args, jsonOutput, stdout, stderr)
	default:
		return fmt.Errorf("unknown command %q (available: status, collection, scan, job, account, setup, serve, pipeline, worker, people, search)", command)
	}
}

func purgeCommand(ctx context.Context, application *app.App, args []string, jsonOutput bool, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] != "derived" {
		return fmt.Errorf("purge requires derived")
	}
	flags := flag.NewFlagSet("purge derived", flag.ContinueOnError)
	flags.SetOutput(stderr)
	confirmed := flags.Bool("yes", false, "confirm deletion of derived face and analysis data")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if !*confirmed {
		return fmt.Errorf("purge derived requires --yes")
	}
	tx, err := application.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, statement := range []string{
		`DELETE FROM current_face_references`, `DELETE FROM reference_selection_events`,
		`DELETE FROM reference_recommendations`, `DELETE FROM current_face_dispositions`, `DELETE FROM face_disposition_events`,
		`DELETE FROM current_face_assignments`, `DELETE FROM assignment_events`, `DELETE FROM cluster_memberships`,
		`DELETE FROM cluster_review_events`, `DELETE FROM clusters`, `DELETE FROM clustering_runs`,
		`DELETE FROM face_correspondences`, `DELETE FROM faces`, `DELETE FROM analyses`, `DELETE FROM job_items`, `DELETE FROM jobs`,
	} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if jsonOutput {
		return writeJSON(stdout, envelope("purge derived", map[string]bool{"purged": true}))
	}
	fmt.Fprintln(stdout, "ok")
	return nil
}

func exportCommand(ctx context.Context, application *app.App, args []string, jsonOutput bool, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] != "metadata" {
		return fmt.Errorf("export requires metadata")
	}
	flags := flag.NewFlagSet("export metadata", flag.ContinueOnError)
	flags.SetOutput(stderr)
	output := flags.String("output", "", "output JSON file")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	path := *output
	if path == "" {
		path = filepath.Join(application.Config.ExportRoot, "metadata-"+time.Now().UTC().Format("20060102T150405.000000000Z")+".json")
	}
	type record struct {
		Table string           `json:"table"`
		Rows  []map[string]any `json:"rows"`
	}
	result := struct {
		FormatVersion string   `json:"format_version"`
		ExportedAt    string   `json:"exported_at"`
		Application   string   `json:"application"`
		Records       []record `json:"records"`
	}{FormatVersion: "1", ExportedAt: time.Now().UTC().Format(time.RFC3339Nano), Application: "face-me-bro"}
	for _, table := range []string{"collections", "photo_assets", "photo_paths", "people", "current_face_assignments", "current_face_dispositions", "current_face_references", "clustering_runs", "clusters"} {
		rows, err := application.DB.QueryContext(ctx, "SELECT * FROM "+table)
		if err != nil {
			return err
		}
		columns, err := rows.Columns()
		if err != nil {
			rows.Close()
			return err
		}
		var values []map[string]any
		for rows.Next() {
			cells := make([]any, len(columns))
			pointers := make([]any, len(columns))
			for i := range cells {
				pointers[i] = &cells[i]
			}
			if err := rows.Scan(pointers...); err != nil {
				rows.Close()
				return err
			}
			item := map[string]any{}
			for i, column := range columns {
				value := cells[i]
				if bytes, ok := value.([]byte); ok {
					value = fmt.Sprintf("%x", bytes)
				}
				item[column] = value
			}
			values = append(values, item)
		}
		if err := rows.Close(); err != nil {
			return err
		}
		result.Records = append(result.Records, record{Table: table, Rows: values})
	}
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return err
	}
	if jsonOutput {
		return writeJSON(stdout, envelope("export metadata", map[string]string{"path": path, "format_version": "1"}))
	}
	fmt.Fprintln(stdout, path)
	return nil
}

func backupCommand(application *app.App, args []string, jsonOutput bool, stdout io.Writer) error {
	if len(args) != 1 || args[0] != "create" {
		return fmt.Errorf("backup requires create")
	}
	path, err := db.Backup(application.Config.DatabasePath, application.Config.BackupRoot)
	if err != nil {
		return err
	}
	if jsonOutput {
		return writeJSON(stdout, envelope("backup create", map[string]string{"path": path}))
	}
	fmt.Fprintln(stdout, path)
	return nil
}

func collectionCommand(ctx context.Context, application *app.App, args []string, jsonOutput bool, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("collection requires add or list")
	}
	switch args[0] {
	case "remove":
		flags := flag.NewFlagSet("collection remove", flag.ContinueOnError)
		flags.SetOutput(stderr)
		collectionID := flags.String("collection-id", "", "collection ID")
		confirmed := flags.Bool("yes", false, "confirm removal")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if *collectionID == "" || !*confirmed {
			return fmt.Errorf("collection remove requires --collection-id and --yes")
		}
		if err := media.NewStore(application.DB).DeleteCollection(ctx, *collectionID); err != nil {
			return err
		}
		if jsonOutput {
			return writeJSON(stdout, envelope("collection remove", map[string]string{"id": *collectionID}))
		}
		fmt.Fprintln(stdout, "ok")
		return nil
	case "add":
		flags := flag.NewFlagSet("collection add", flag.ContinueOnError)
		flags.SetOutput(stderr)
		name := flags.String("name", "", "collection name")
		root := flags.String("root", "", "collection root directory")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if *name == "" || *root == "" {
			return fmt.Errorf("collection add requires --name and --root")
		}
		resolved, err := filepath.Abs(*root)
		if err != nil {
			return err
		}
		id, err := media.NewStore(application.DB).CreateCollection(ctx, *name, resolved)
		if err != nil {
			return err
		}
		if jsonOutput {
			return writeJSON(stdout, envelope("collection add", map[string]string{"id": id, "name": *name, "root": resolved}))
		}
		fmt.Fprintln(stdout, id)
		return nil
	case "list":
		rows, err := application.DB.QueryContext(ctx, "SELECT id, name, root_path, enabled FROM collections ORDER BY name")
		if err != nil {
			return err
		}
		defer rows.Close()
		type collection struct {
			ID      string `json:"id"`
			Name    string `json:"name"`
			Root    string `json:"root"`
			Enabled bool   `json:"enabled"`
		}
		var result []collection
		for rows.Next() {
			var item collection
			var enabled int
			if err := rows.Scan(&item.ID, &item.Name, &item.Root, &enabled); err != nil {
				return err
			}
			item.Enabled = enabled == 1
			result = append(result, item)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if jsonOutput {
			return writeJSON(stdout, envelope("collection list", result))
		}
		for _, item := range result {
			fmt.Fprintf(stdout, "%s\t%s\t%s\n", item.ID, item.Name, item.Root)
		}
		return nil
	default:
		return fmt.Errorf("unknown collection command %q", args[0])
	}
}

func scanCommand(ctx context.Context, application *app.App, args []string, jsonOutput bool, stdout, stderr io.Writer) error {
	if len(args) > 0 && (args[0] == "status" || args[0] == "pause" || args[0] == "resume" || args[0] == "cancel" || args[0] == "retry") {
		store := media.NewStore(application.DB)
		if args[0] != "status" {
			if len(args) != 2 {
				return fmt.Errorf("scan %s requires a scan run ID", args[0])
			}
			if err := store.SetScanControl(ctx, args[1], args[0]); err != nil {
				return err
			}
			if jsonOutput {
				return writeJSON(stdout, envelope("scan "+args[0], map[string]string{"scan_run_id": args[1], "action": args[0]}))
			}
			fmt.Fprintln(stdout, "ok")
			return nil
		}
		rows, err := application.DB.QueryContext(ctx, `SELECT id, collection_id, status, created_at, completed_at FROM scan_runs ORDER BY created_at DESC`)
		if err != nil {
			return err
		}
		defer rows.Close()
		type scan struct {
			ID           string  `json:"id"`
			CollectionID string  `json:"collection_id"`
			Status       string  `json:"status"`
			CreatedAt    string  `json:"created_at"`
			CompletedAt  *string `json:"completed_at,omitempty"`
		}
		var scans []scan
		for rows.Next() {
			var value scan
			var completed sql.NullString
			if err := rows.Scan(&value.ID, &value.CollectionID, &value.Status, &value.CreatedAt, &completed); err != nil {
				return err
			}
			if completed.Valid {
				value.CompletedAt = &completed.String
			}
			scans = append(scans, value)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if jsonOutput {
			return writeJSON(stdout, envelope("scan status", scans))
		}
		for _, value := range scans {
			fmt.Fprintf(stdout, "%s\t%s\t%s\n", value.ID, value.CollectionID, value.Status)
		}
		return nil
	}
	flags := flag.NewFlagSet("scan", flag.ContinueOnError)
	flags.SetOutput(stderr)
	collectionID := flags.String("collection-id", "", "collection ID")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *collectionID == "" {
		return fmt.Errorf("scan requires --collection-id")
	}
	var root string
	if err := application.DB.QueryRowContext(ctx, "SELECT root_path FROM collections WHERE id = ?", *collectionID).Scan(&root); err != nil {
		return err
	}
	records, err := (media.Scanner{}).Scan(root)
	if err != nil {
		return err
	}
	mediaStore := media.NewStore(application.DB)
	scanRunID, err := mediaStore.StartScan(ctx, *collectionID)
	if err != nil {
		return err
	}
	if err := mediaStore.PersistScanRun(ctx, *collectionID, scanRunID, records); err != nil {
		_ = mediaStore.FinishScan(ctx, scanRunID, "failed", err.Error())
		return err
	}
	if err := mediaStore.FinishScan(ctx, scanRunID, "completed", ""); err != nil {
		return err
	}
	job, err := jobs.NewStore(application.DB).Enqueue(ctx, "analysis", 5, map[string]any{"collection_id": *collectionID})
	if err != nil {
		return err
	}
	result := map[string]any{"collection_id": *collectionID, "scan_run_id": scanRunID, "files": len(records), "supported": countSupported(records), "analysis_job_id": job.ID}
	if jsonOutput {
		return writeJSON(stdout, envelope("scan", result))
	}
	fmt.Fprintf(stdout, "Scanned %d files; %d supported; analysis job %s\n", len(records), countSupported(records), job.ID)
	return nil
}

func jobCommand(ctx context.Context, application *app.App, args []string, jsonOutput bool, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("job requires list, get, pause, resume, cancel, or retry")
	}
	store := jobs.NewStore(application.DB)
	if args[0] == "get" {
		if len(args) != 2 {
			return fmt.Errorf("job get requires an ID")
		}
		job, err := store.Get(ctx, args[1])
		if err != nil {
			return err
		}
		if jsonOutput {
			return writeJSON(stdout, envelope("job get", job))
		}
		fmt.Fprintf(stdout, "%s\t%s\t%s\t%d\n", job.ID, job.Kind, job.Status, job.Priority)
		return nil
	}
	if len(args) == 2 && (args[0] == "pause" || args[0] == "resume" || args[0] == "cancel" || args[0] == "retry") {
		var err error
		switch args[0] {
		case "pause":
			err = store.Pause(ctx, args[1])
		case "resume":
			err = store.Resume(ctx, args[1])
		case "cancel":
			err = store.Cancel(ctx, args[1])
		case "retry":
			err = store.Retry(ctx, args[1])
		}
		if err != nil {
			return err
		}
		if jsonOutput {
			return writeJSON(stdout, envelope("job "+args[0], map[string]any{"id": args[1], "status": args[0]}))
		}
		fmt.Fprintln(stdout, "ok")
		return nil
	}
	if len(args) != 1 || args[0] != "list" {
		return fmt.Errorf("job requires list, get, pause, resume, cancel, or retry")
	}
	rows, err := application.DB.QueryContext(ctx, "SELECT id, kind, priority, status, created_at FROM jobs ORDER BY created_at DESC")
	if err != nil {
		return err
	}
	defer rows.Close()
	type item struct {
		ID        string `json:"id"`
		Kind      string `json:"kind"`
		Priority  int    `json:"priority"`
		Status    string `json:"status"`
		CreatedAt string `json:"created_at"`
	}
	var result []item
	for rows.Next() {
		var value item
		if err := rows.Scan(&value.ID, &value.Kind, &value.Priority, &value.Status, &value.CreatedAt); err != nil {
			return err
		}
		result = append(result, value)
	}
	if jsonOutput {
		return writeJSON(stdout, envelope("job list", result))
	}
	for _, value := range result {
		fmt.Fprintf(stdout, "%s\t%s\t%s\t%d\n", value.ID, value.Kind, value.Status, value.Priority)
	}
	return rows.Err()
}

func accountCommand(ctx context.Context, application *app.App, args []string, jsonOutput bool, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("account requires create, list, disable, enable, or reset-password")
	}
	store := auth.NewStore(application.DB)
	if args[0] == "list" {
		rows, err := application.DB.QueryContext(ctx, "SELECT id, username, role, disabled_at IS NOT NULL FROM accounts ORDER BY username")
		if err != nil {
			return err
		}
		defer rows.Close()
		type account struct {
			ID       string `json:"id"`
			Username string `json:"username"`
			Role     string `json:"role"`
			Disabled bool   `json:"disabled"`
		}
		var result []account
		for rows.Next() {
			var value account
			var disabled int
			if err := rows.Scan(&value.ID, &value.Username, &value.Role, &disabled); err != nil {
				return err
			}
			value.Disabled = disabled == 1
			result = append(result, value)
		}
		if jsonOutput {
			return writeJSON(stdout, envelope("account list", result))
		}
		for _, value := range result {
			fmt.Fprintf(stdout, "%s\t%s\t%s\t%t\n", value.ID, value.Username, value.Role, value.Disabled)
		}
		return rows.Err()
	}
	if args[0] == "create" {
		flags := flag.NewFlagSet("account create", flag.ContinueOnError)
		flags.SetOutput(stderr)
		username := flags.String("username", "", "username")
		password := flags.String("password", "", "password")
		role := flags.String("role", string(auth.Reviewer), "role")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		account, err := store.CreateAccount(ctx, *username, *password, auth.Role(*role), true)
		if err != nil {
			return err
		}
		if jsonOutput {
			return writeJSON(stdout, envelope("account create", account))
		}
		fmt.Fprintln(stdout, account.ID)
		return nil
	}
	if args[0] == "disable" || args[0] == "enable" {
		flags := flag.NewFlagSet("account state", flag.ContinueOnError)
		flags.SetOutput(stderr)
		accountID := flags.String("account-id", "", "account ID")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if *accountID == "" {
			return fmt.Errorf("account state requires --account-id")
		}
		if err := store.SetDisabled(ctx, *accountID, args[0] == "disable"); err != nil {
			return err
		}
		fmt.Fprintln(stdout, "ok")
		return nil
	}
	if args[0] == "grant" {
		flags := flag.NewFlagSet("account grant", flag.ContinueOnError)
		flags.SetOutput(stderr)
		accountID := flags.String("account-id", "", "account ID")
		collectionID := flags.String("collection-id", "", "collection ID")
		view := flags.Bool("view", true, "grant view permission")
		modify := flags.Bool("modify", false, "grant modify permission")
		undo := flags.Bool("undo", true, "grant undo permission")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if *accountID == "" || *collectionID == "" {
			return fmt.Errorf("grant requires --account-id and --collection-id")
		}
		if err := store.SetCollectionPermission(ctx, *accountID, *collectionID, *view, *modify, *undo); err != nil {
			return err
		}
		fmt.Fprintln(stdout, "ok")
		return nil
	}
	if args[0] != "reset-password" {
		return fmt.Errorf("unknown account command %q", args[0])
	}
	flags := flag.NewFlagSet("account reset-password", flag.ContinueOnError)
	flags.SetOutput(stderr)
	accountID := flags.String("account-id", "", "account ID")
	password := flags.String("password", "", "new password")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if *accountID == "" || *password == "" {
		return fmt.Errorf("reset-password requires --account-id and --password")
	}
	if err := auth.NewStore(application.DB).ResetPassword(ctx, *accountID, *password, true); err != nil {
		return err
	}
	if jsonOutput {
		return writeJSON(stdout, envelope("account reset-password", map[string]bool{"reset": true}))
	}
	fmt.Fprintln(stdout, "Password reset; user must change it at next login.")
	return nil
}

func peopleCommand(ctx context.Context, application *app.App, args []string, jsonOutput bool, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("people requires create or list")
	}
	if args[0] == "create" {
		flags := flag.NewFlagSet("people create", flag.ContinueOnError)
		flags.SetOutput(stderr)
		name := flags.String("name", "", "person name")
		provisional := flags.Bool("provisional", false, "mark provisional")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if *name == "" {
			return fmt.Errorf("people create requires --name")
		}
		personID, err := id.NewUUIDv7()
		if err != nil {
			return err
		}
		now := time.Now().UTC().Format(time.RFC3339Nano)
		if _, err := application.DB.ExecContext(ctx, `INSERT INTO people (id, name, is_provisional, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`, personID, *name, boolInt(*provisional), now, now); err != nil {
			return err
		}
		if jsonOutput {
			return writeJSON(stdout, envelope("people create", map[string]any{"id": personID, "name": *name, "provisional": *provisional}))
		}
		fmt.Fprintln(stdout, personID)
		return nil
	}
	if args[0] != "list" {
		return fmt.Errorf("unknown people command %q", args[0])
	}
	rows, err := application.DB.QueryContext(ctx, "SELECT id, COALESCE(name, ''), is_provisional, dont_track FROM people ORDER BY name, id")
	if err != nil {
		return err
	}
	defer rows.Close()
	type person struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		Provisional bool   `json:"provisional"`
		DontTrack   bool   `json:"dont_track"`
	}
	var result []person
	for rows.Next() {
		var value person
		var provisional, dontTrack int
		if err := rows.Scan(&value.ID, &value.Name, &provisional, &dontTrack); err != nil {
			return err
		}
		value.Provisional = provisional == 1
		value.DontTrack = dontTrack == 1
		result = append(result, value)
	}
	if jsonOutput {
		return writeJSON(stdout, envelope("people list", result))
	}
	for _, value := range result {
		fmt.Fprintf(stdout, "%s\t%s\n", value.ID, value.Name)
	}
	return rows.Err()
}

func searchCommand(ctx context.Context, application *app.App, args []string, jsonOutput bool, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("search", flag.ContinueOnError)
	flags.SetOutput(stderr)
	options := search.Options{}
	person := flags.String("person-id", "", "person ID")
	directory := flags.String("directory", "", "relative directory")
	from := flags.String("from", "", "capture date lower bound")
	until := flags.String("until", "", "capture date upper bound")
	includeProvisional := flags.Bool("include-provisional", false, "include provisional assignments")
	if err := flags.Parse(args); err != nil {
		return err
	}
	options.PersonID, options.RelativePrefix, options.CaptureDateFrom, options.CaptureDateUntil, options.IncludeProvisional = *person, *directory, *from, *until, *includeProvisional
	result, err := search.NewStore(application.DB).Photos(ctx, options)
	if err != nil {
		return err
	}
	if jsonOutput {
		return writeJSON(stdout, envelope("search", result))
	}
	for _, photo := range result {
		fmt.Fprintf(stdout, "%s\t%d\t%s\n", photo.RelativePath, photo.FaceCount, photo.CaptureDate)
	}
	return nil
}

func auditCommand(ctx context.Context, application *app.App, args []string, jsonOutput bool, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] != "purge" {
		return fmt.Errorf("audit requires purge")
	}
	flags := flag.NewFlagSet("audit purge", flag.ContinueOnError)
	flags.SetOutput(stderr)
	ageDays := flags.Int("age-days", 365, "remove events older than this many days")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	removed, err := audit.NewStore(application.DB).PurgeBefore(ctx, time.Now().UTC().AddDate(0, 0, -*ageDays))
	if err != nil {
		return err
	}
	if jsonOutput {
		return writeJSON(stdout, envelope("audit purge", map[string]int64{"removed": removed}))
	}
	fmt.Fprintf(stdout, "Removed %d audit events\n", removed)
	return nil
}

func setupCommand(ctx context.Context, application *app.App, args []string, jsonOutput bool, stdout, stderr io.Writer) error {
	if len(args) != 1 || args[0] != "check" {
		return fmt.Errorf("setup requires check")
	}
	report := setup.Validate(ctx, "python3.11", filepath.Join(application.Config.RuntimeDataRoot, "venv"), application.Config.ModelRoot)
	if jsonOutput {
		return writeJSON(stdout, envelope("setup check", report))
	}
	for _, check := range report.Checks {
		state := "FAIL"
		if check.Passed {
			state = "OK"
		}
		fmt.Fprintf(stdout, "%s\t%s\t%s\n", state, check.Name, check.Details)
	}
	if !report.Valid {
		return fmt.Errorf("setup validation failed")
	}
	return nil
}

func serveCommand(ctx context.Context, application *app.App, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	flags.SetOutput(stderr)
	address := flags.String("address", "127.0.0.1:8080", "HTTP listen address")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if _, err := auth.NewStore(application.DB).EnsureBootstrapAdmin(ctx); err != nil {
		return err
	}
	server, err := web.NewServer(auth.NewStore(application.DB), application.DB)
	if err != nil {
		return err
	}
	httpServer := &http.Server{Addr: *address, Handler: server.Handler()}
	go func() {
		<-ctx.Done()
		_ = httpServer.Shutdown(context.Background())
	}()
	fmt.Fprintf(stdout, "Listening on http://%s\n", *address)
	err = httpServer.ListenAndServe()
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

func pipelineCommand(ctx context.Context, application *app.App, args []string, jsonOutput bool, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] != "register" {
		return fmt.Errorf("pipeline requires register")
	}
	flags := flag.NewFlagSet("pipeline register", flag.ContinueOnError)
	flags.SetOutput(stderr)
	family := flags.String("model-family", "buffalo_l", "model family")
	version := flags.String("model-version", "2.0.0", "model version")
	dimension := flags.Int("embedding-dimension", 512, "embedding dimension")
	provider := flags.String("provider", "CPUExecutionProvider", "execution provider")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	pipelineID, err := analysis.NewStore(application.DB).RegisterPipeline(ctx, analysis.Pipeline{ModelFamily: *family, ModelVersion: *version, EmbeddingDimension: *dimension, EmbeddingDType: "float32", Normalization: "l2", ExecutionProvider: *provider})
	if err != nil {
		return err
	}
	if jsonOutput {
		return writeJSON(stdout, envelope("pipeline register", map[string]string{"id": pipelineID}))
	}
	fmt.Fprintln(stdout, pipelineID)
	return nil
}

func workerCommand(ctx context.Context, application *app.App, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("worker", flag.ContinueOnError)
	flags.SetOutput(stderr)
	pipelineID := flags.String("pipeline-id", "", "pipeline ID")
	pythonPath := flags.String("python", "python3.11", "Python executable")
	workerScript := flags.String("script", "worker/insightface_worker.py", "worker script")
	maxPixels := flags.Int64("max-pixels", 100_000_000, "maximum decoded pixels")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *pipelineID == "" {
		return fmt.Errorf("worker requires --pipeline-id")
	}
	client := worker.NewClient(*pythonPath, *workerScript)
	if err := client.Start(ctx); err != nil {
		return err
	}
	defer client.Close()
	processor := analysis.NewProcessor(application.DB, client, *pipelineID, *maxPixels)
	jobStore := jobs.NewStore(application.DB)
	runner := jobs.Runner{Store: jobStore, Handlers: map[string]jobs.Handler{
		"analysis": func(jobContext context.Context, job jobs.Job) error {
			var payload struct {
				CollectionID string `json:"collection_id"`
			}
			if err := json.Unmarshal(job.Payload, &payload); err != nil {
				return err
			}
			if payload.CollectionID == "" {
				return fmt.Errorf("analysis job has no collection_id")
			}
			if err := processor.ProcessCollection(jobContext, payload.CollectionID); err != nil {
				return err
			}
			_, err := jobStore.Enqueue(jobContext, "recognition", 1, map[string]string{"pipeline_id": *pipelineID})
			return err
		},
		"recognition": func(jobContext context.Context, _ jobs.Job) error {
			if err := recognition.NewProcessor(application.DB, recognition.DefaultThresholds()).ProcessAll(jobContext); err != nil {
				return err
			}
			_, err := jobStore.Enqueue(jobContext, "clustering", 2, map[string]string{"scope": "all"})
			return err
		},
		"reference-reprocess": func(jobContext context.Context, job jobs.Job) error {
			if err := recognition.NewProcessor(application.DB, recognition.DefaultThresholds()).ProcessAll(jobContext); err != nil {
				return err
			}
			var payload struct {
				PersonID string `json:"person_id"`
			}
			if err := json.Unmarshal(job.Payload, &payload); err != nil {
				return err
			}
			_, err := clustering.NewProcessor(application.DB, clustering.DefaultConfig()).Run(jobContext, "all")
			return err
		},
		"clustering": func(jobContext context.Context, job jobs.Job) error {
			var payload struct {
				Scope string `json:"scope"`
			}
			if err := json.Unmarshal(job.Payload, &payload); err != nil {
				return err
			}
			if payload.Scope == "" {
				payload.Scope = "all"
			}
			_, err := clustering.NewProcessor(application.DB, clustering.DefaultConfig()).Run(jobContext, payload.Scope)
			return err
		},
	}, IdleWait: time.Second}
	fmt.Fprintln(stdout, "Worker running")
	return runner.Run(ctx)
}

func countSupported(records []media.FileRecord) int {
	count := 0
	for _, record := range records {
		if record.Status == media.StatusSupported {
			count++
		}
	}
	return count
}

func envelope(command string, result any) map[string]any {
	return map[string]any{"schema_version": outputSchemaVersion, "command": command, "result": result, "warnings": []string{}}
}

func writeJSON(stdout io.Writer, value any) error {
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
