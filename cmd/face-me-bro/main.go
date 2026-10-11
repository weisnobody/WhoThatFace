package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
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
	"github.com/pjg/face_me_bro/internal/review"
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
	help := flags.Bool("help", false, "show command help")
	flags.BoolVar(help, "h", false, "show command help")
	if err := flags.Parse(args); err != nil {
		return err
	}
	command := "status"
	if flags.NArg() > 0 {
		command = flags.Arg(0)
	}
	if *help || command == "help" || command == "?" {
		helpArgs := flags.Args()[1:]
		if *help && flags.NArg() > 0 {
			helpArgs = []string{command}
		}
		return helpCommand(stdout, *jsonOutput, helpArgs)
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
	application, err := app.OpenWithOptions(ctx, *configPath, *noMigrate, db.OpenOptions{
		SkipMigrations: command != "status" && command != "serve",
		ReadOnly:       commandIsReadOnly(command, flags.Args()[1:]),
	})
	if err != nil {
		return err
	}
	defer application.Close()
	return dispatch(ctx, application, command, flags.Args()[1:], *jsonOutput, stdout, stderr)
}

func commandIsReadOnly(command string, args []string) bool {
	subcommand := ""
	if len(args) > 0 {
		subcommand = args[0]
	}
	switch command {
	case "collection":
		return subcommand == "list" || subcommand == "get"
	case "scan":
		return subcommand == "status"
	case "job":
		return subcommand == "list" || subcommand == "get" || subcommand == "status"
	case "account":
		return subcommand == "list" || subcommand == "get"
	case "setup":
		return subcommand == "check"
	case "pipeline":
		return subcommand == "list" || subcommand == "get"
	case "config":
		return subcommand == "show"
	case "people":
		return subcommand == "list" || subcommand == "get"
	case "face":
		return subcommand == "get" || subcommand == "list"
	case "cluster":
		return subcommand == "get" || subcommand == "list"
	case "audit":
		return subcommand == "list"
	case "search":
		return true
	case "backup", "export":
		return true
	default:
		return false
	}
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
		fmt.Fprintf(stdout, "Config file:      %s\nRuntime root:     %s\nDatabase:         %s\nModel root:       %s\nLog root:         %s\nTemp root:        %s\nExport root:      %s\nBackup root:      %s\nSearch method:    %s\n", status.ConfigFile, status.RuntimeDataRoot, status.DatabasePath, status.ModelRoot, status.LogRoot, status.TempRoot, status.ExportRoot, status.BackupRoot, status.NeighborSearchMethod)
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
	case "config":
		return configCommand(application, args, jsonOutput, stdout, stderr)
	case "worker":
		return workerCommand(ctx, application, args, stdout, stderr)
	case "people":
		return peopleCommand(ctx, application, args, jsonOutput, stdout, stderr)
	case "face":
		return faceCommand(ctx, application, args, jsonOutput, stdout, stderr)
	case "cluster":
		return clusterCommand(ctx, application, args, jsonOutput, stdout, stderr)
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
		return fmt.Errorf("unknown command %q (run 'face-me-bro help' for available commands)", command)
	}
}

type helpTopic struct {
	Usage       string `json:"usage"`
	Description string `json:"description"`
	Options     string `json:"options,omitempty"`
}

var commandHelp = map[string]helpTopic{
	"status":     {Usage: "face-me-bro [global options] status", Description: "Show resolved paths, pipeline, provider, and search configuration."},
	"collection": {Usage: "face-me-bro collection <add|get|list|remove|set-search> [options]", Description: "Create, inspect, list, remove, or configure a photo collection.", Options: "add: --name NAME --root PATH; get: --collection-id ID; list: --limit N --page N; remove: --collection-id ID --yes; set-search: --collection-id ID --method exact|ann"},
	"scan":       {Usage: "face-me-bro scan [start|status|pause|resume|cancel|retry] [options]", Description: "Scan collection paths, persist metadata, and control scan runs.", Options: "start: --collection-id ID; status: --limit N --page N; pause/resume/cancel/retry: SCAN_RUN_ID"},
	"job":        {Usage: "face-me-bro job <list|get|status|pause|resume|cancel|retry> [options]", Description: "Inspect and control background processing jobs.", Options: "list: --limit N --page N; get: JOB_ID; status: --job-id ID; controls use JOB_ID"},
	"account":    {Usage: "face-me-bro account <get|list|create|disable|enable|reset-password|grant> [options]", Description: "Manage accounts, passwords, roles, and collection permissions.", Options: "get: --account-id ID; list: --limit N --page N; create: --username USER --password PASSWORD --role ROLE; grant: --account-id ID --collection-id ID; identity changes require --yes"},
	"setup":      {Usage: "face-me-bro setup check", Description: "Validate Python, dependencies, model checksums, and runtime compatibility."},
	"serve":      {Usage: "face-me-bro serve [--address HOST:PORT]", Description: "Start the authenticated web review service."},
	"pipeline":   {Usage: "face-me-bro pipeline <get|register|list> [options]", Description: "Register or inspect model and preprocessing pipeline identities.", Options: "get: --pipeline-id; list: --limit N --page N; register: --model-family --model-version --provider --model-checksums-json --detector-config-json --preprocessing-config-json"},
	"config":     {Usage: "face-me-bro config <show|set-search> [options]", Description: "Show or change global application configuration.", Options: "set-search: --method exact|ann"},
	"worker":     {Usage: "face-me-bro worker", Description: "Run the background worker loop for queued analysis and recognition jobs."},
	"people":     {Usage: "face-me-bro people <get|list|create> [options]", Description: "Inspect, list, or create people records.", Options: "get: --person-id; list: --limit N --page N; create: --name NAME [--provisional]"},
	"face":       {Usage: "face-me-bro face <get|list|assign|disposition|reference> [options]", Description: "Inspect or change faces, including assignments, dispositions, and reference status; list faces with their current state.", Options: "get: --face-id; list: --limit N --page N; assign: --face-id ID --person-id ID --state STATE; disposition: --face-id ID --disposition VALUE; reference: --face-id ID --person-id ID --reference; changes require --yes"},
	"cluster":    {Usage: "face-me-bro cluster <run|get|list|review> [options]", Description: "Run grouping, list or inspect clusters, and review clusters.", Options: "run: [--collection-id ID] [--scope all|unresolved-only|subset]; get/review: --cluster-id ID; list: [--run-id ID] --limit N --page N; review changes require --yes"},
	"search":     {Usage: "face-me-bro search [options]", Description: "Search photos by person, directory, capture date, and assignment state.", Options: "--person-id ID --directory PATH --from DATE --until DATE [--include-provisional]"},
	"audit":      {Usage: "face-me-bro audit <list|purge> [options]", Description: "List or purge retained audit events.", Options: "list: [--entity-type TYPE] [--entity-id ID] --limit N --page N; purge: --age-days N"},
	"backup":     {Usage: "face-me-bro backup create", Description: "Create a database backup in the configured backup directory."},
	"restore":    {Usage: "face-me-bro restore --input FILE --yes", Description: "Replace the configured database from a backup after explicit confirmation."},
	"export":     {Usage: "face-me-bro export metadata [--output FILE]", Description: "Export application metadata as versioned JSON."},
	"purge":      {Usage: "face-me-bro purge derived --yes", Description: "Delete derived face, assignment, clustering, and job data after confirmation."},
}

func helpCommand(stdout io.Writer, jsonOutput bool, args []string) error {
	if len(args) > 1 {
		return fmt.Errorf("help accepts at most one command")
	}
	if len(args) == 1 {
		topic, ok := commandHelp[args[0]]
		if !ok {
			return fmt.Errorf("unknown help topic %q", args[0])
		}
		if jsonOutput {
			return writeJSON(stdout, envelope("help", map[string]any{"command": args[0], "help": topic}))
		}
		fmt.Fprintf(stdout, "Usage: %s\n\n%s\n", topic.Usage, topic.Description)
		if topic.Options != "" {
			fmt.Fprintf(stdout, "\nOptions and parameters: %s\n", topic.Options)
		}
		fmt.Fprintln(stdout, "\nGlobal options: --config PATH, --no-migrate, --json, --help")
		return nil
	}
	if jsonOutput {
		return writeJSON(stdout, envelope("help", map[string]any{"commands": commandHelp, "global_options": "--config PATH, --no-migrate, --json, --help"}))
	}
	fmt.Fprintln(stdout, "Usage: face-me-bro [global options] <command> [options]")
	fmt.Fprintln(stdout, "\nCommands:")
	keys := make([]string, 0, len(commandHelp))
	for key := range commandHelp {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		fmt.Fprintf(stdout, "  %-12s %s\n", key, commandHelp[key].Description)
	}
	fmt.Fprintln(stdout, "\nGlobal options:")
	fmt.Fprintln(stdout, "  --config PATH       Use an alternate configuration file or directory.")
	fmt.Fprintln(stdout, "  --no-migrate        Refuse to create or upgrade the database schema.")
	fmt.Fprintln(stdout, "  --json              Emit versioned JSON output where supported.")
	fmt.Fprintln(stdout, "  --help, -h          Show this help or help for the selected command.")
	fmt.Fprintln(stdout, "\nUse 'face-me-bro help <command>' for command-specific usage and parameters.")
	return nil
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
		`DELETE FROM reference_recommendation_decisions`, `DELETE FROM reference_recommendations`, `DELETE FROM current_face_dispositions`, `DELETE FROM face_disposition_events`,
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
	_ = audit.NewStore(application.DB).Record(ctx, audit.Event{Action: "derived-purge", EntityType: "database", EntityID: application.Config.DatabasePath, New: map[string]any{"confirmed": true}})
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
	for _, table := range []string{"collections", "photo_assets", "photo_paths", "pipelines", "analyses", "faces", "face_correspondences", "people", "person_state_events", "assignment_events", "current_face_assignments", "face_disposition_events", "current_face_dispositions", "reference_recommendations", "reference_recommendation_decisions", "reference_selection_events", "current_face_references", "clustering_runs", "clusters", "cluster_memberships", "cluster_review_events", "jobs", "job_items", "scan_runs", "scan_items", "accounts", "collection_permissions", "audit_events"} {
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
		return fmt.Errorf("collection requires add, get, list, remove, or set-search")
	}
	switch args[0] {
	case "set-enabled":
		flags := flag.NewFlagSet("collection set-enabled", flag.ContinueOnError)
		flags.SetOutput(stderr)
		collectionID := flags.String("collection-id", "", "collection ID")
		enabled := flags.Bool("enabled", true, "enable or disable collection")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if *collectionID == "" {
			return fmt.Errorf("collection set-enabled requires --collection-id")
		}
		result, err := application.DB.ExecContext(ctx, `UPDATE collections SET enabled = ?, updated_at = ? WHERE id = ?`, boolInt(*enabled), time.Now().UTC().Format(time.RFC3339Nano), *collectionID)
		if err != nil {
			return err
		}
		if count, _ := result.RowsAffected(); count != 1 {
			return fmt.Errorf("collection %q does not exist", *collectionID)
		}
		return writeCommandResult(stdout, jsonOutput, "collection set-enabled", map[string]any{"collection_id": *collectionID, "enabled": *enabled})
	case "get":
		flags := flag.NewFlagSet("collection get", flag.ContinueOnError)
		flags.SetOutput(stderr)
		collectionID := flags.String("collection-id", "", "collection ID")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if *collectionID == "" {
			return fmt.Errorf("collection get requires --collection-id")
		}
		var id, name, root, created, updated, method string
		var enabled int
		if err := application.DB.QueryRowContext(ctx, `SELECT id, name, root_path, enabled, created_at, updated_at, neighbor_search_method FROM collections WHERE id = ?`, *collectionID).Scan(&id, &name, &root, &enabled, &created, &updated, &method); err != nil {
			return err
		}
		return writeCommandResult(stdout, jsonOutput, "collection get", map[string]any{"id": id, "name": name, "root": root, "enabled": enabled == 1, "created_at": created, "updated_at": updated, "neighbor_search_method": method})
	case "set-search":
		flags := flag.NewFlagSet("collection set-search", flag.ContinueOnError)
		flags.SetOutput(stderr)
		collectionID := flags.String("collection-id", "", "collection ID")
		method := flags.String("method", "exact", "neighbor search method: exact or ann")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if *collectionID == "" {
			return fmt.Errorf("collection set-search requires --collection-id")
		}
		if *method != "exact" && *method != "ann" {
			return fmt.Errorf("search method must be exact or ann")
		}
		result, err := application.DB.ExecContext(ctx, "UPDATE collections SET neighbor_search_method = ?, updated_at = ? WHERE id = ?", *method, time.Now().UTC().Format(time.RFC3339Nano), *collectionID)
		if err != nil {
			return err
		}
		if count, _ := result.RowsAffected(); count != 1 {
			return fmt.Errorf("collection %q does not exist", *collectionID)
		}
		return writeCommandResult(stdout, jsonOutput, "collection set-search", map[string]any{"collection_id": *collectionID, "method": *method})
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
		flags := flag.NewFlagSet("collection list", flag.ContinueOnError)
		flags.SetOutput(stderr)
		limit, page := paginationFlags(flags)
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		query, queryArgs, err := applyPagination("SELECT id, name, root_path, enabled, neighbor_search_method FROM collections ORDER BY name", nil, *limit, *page)
		if err != nil {
			return err
		}
		rows, err := application.DB.QueryContext(ctx, query, queryArgs...)
		if err != nil {
			return err
		}
		defer rows.Close()
		type collection struct {
			ID           string `json:"id"`
			Name         string `json:"name"`
			Root         string `json:"root"`
			Enabled      bool   `json:"enabled"`
			SearchMethod string `json:"neighbor_search_method"`
		}
		var result []collection
		for rows.Next() {
			var item collection
			var enabled int
			if err := rows.Scan(&item.ID, &item.Name, &item.Root, &enabled, &item.SearchMethod); err != nil {
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
		fmt.Fprintln(stdout, "id\tname\troot\tneighbor_search_method")
		for _, item := range result {
			fmt.Fprintf(stdout, "%s\t%s\t%s\t%s\n", item.ID, item.Name, item.Root, item.SearchMethod)
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
			if args[0] == "resume" || args[0] == "retry" {
				var collectionID, root string
				if err := application.DB.QueryRowContext(ctx, `SELECT sr.collection_id, c.root_path FROM scan_runs sr JOIN collections c ON c.id = sr.collection_id WHERE sr.id = ?`, args[1]).Scan(&collectionID, &root); err != nil {
					return err
				}
				if err := store.SetScanControl(ctx, args[1], args[0]); err != nil {
					return err
				}
				records, err := (media.Scanner{}).Scan(root)
				if err != nil {
					_ = store.FinishScan(ctx, args[1], "failed", err.Error())
					return err
				}
				if err := store.PersistScanRun(ctx, collectionID, args[1], records); err != nil {
					_ = store.FinishScan(ctx, args[1], "failed", err.Error())
					return err
				}
				if err := store.FinishScan(ctx, args[1], "completed", ""); err != nil {
					return err
				}
			} else if err := store.SetScanControl(ctx, args[1], args[0]); err != nil {
				return err
			}
			if jsonOutput {
				return writeJSON(stdout, envelope("scan "+args[0], map[string]string{"scan_run_id": args[1], "action": args[0]}))
			}
			fmt.Fprintln(stdout, "ok")
			return nil
		}
		flags := flag.NewFlagSet("scan status", flag.ContinueOnError)
		flags.SetOutput(stderr)
		limit, page := paginationFlags(flags)
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		query, queryArgs, err := applyPagination("SELECT id, collection_id, status, created_at, completed_at FROM scan_runs ORDER BY created_at DESC", nil, *limit, *page)
		if err != nil {
			return err
		}
		rows, err := application.DB.QueryContext(ctx, query, queryArgs...)
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
		fmt.Fprintln(stdout, "id\tcollection_id\tstatus")
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
	mediaStore := media.NewStore(application.DB)
	scanRunID, err := mediaStore.StartScan(ctx, *collectionID)
	if err != nil {
		return err
	}
	scanner := media.Scanner{Control: func() (string, error) {
		var status string
		err := application.DB.QueryRowContext(ctx, `SELECT status FROM scan_runs WHERE id = ?`, scanRunID).Scan(&status)
		return status, err
	}}
	records, err := scanner.ScanContext(ctx, root)
	if err != nil {
		status := "failed"
		if errors.Is(err, context.Canceled) {
			status = "cancelled"
		}
		_ = mediaStore.FinishScan(ctx, scanRunID, status, err.Error())
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
		return fmt.Errorf("job requires list, get, status, pause, resume, cancel, or retry")
	}
	store := jobs.NewStore(application.DB)
	if args[0] == "throttle" {
		flags := flag.NewFlagSet("job throttle", flag.ContinueOnError)
		flags.SetOutput(stderr)
		mode := flags.String("mode", string(jobs.ThrottleAutomatic), "automatic, full-speed, reduced, or paused")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if err := store.SetThrottleMode(ctx, jobs.ThrottleMode(*mode)); err != nil {
			return err
		}
		return writeCommandResult(stdout, jsonOutput, "job throttle", map[string]string{"mode": *mode})
	}
	if args[0] == "status" {
		flags := flag.NewFlagSet("job status", flag.ContinueOnError)
		flags.SetOutput(stderr)
		jobID := flags.String("job-id", "", "job ID")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if *jobID == "" {
			return fmt.Errorf("job status requires --job-id")
		}
		report, err := currentJobStatus(ctx, store, *jobID)
		if err != nil {
			return err
		}
		if jsonOutput {
			return writeJSON(stdout, envelope("job status", report))
		}
		current := "-"
		if report.CurrentItem != nil {
			current = *report.CurrentItem
		}
		eta := "-"
		if report.EstimatedTimeRemaining != nil {
			eta = *report.EstimatedTimeRemaining
		}
		started := "-"
		if report.StartedAt != nil {
			started = report.StartedAt.Format(time.RFC3339Nano)
		}
		fmt.Fprintf(stdout, "Job:                         %s\nStatus:                      %s\nItems completed:             %d\nItems remaining:             %d\nCurrent item:                %s\nPercentage complete:         %.1f%%\nTime started:                %s\nTime running:                %s\nEstimated time remaining:    %s\nFailed item count:           %d\n", report.JobID, report.Status, report.ItemsCompleted, report.ItemsRemaining, current, report.PercentageComplete, started, report.TimeRunning, eta, report.FailedItems)
		return nil
	}
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
		fmt.Fprintf(stdout, "%s\t%s\t%s\t%d\tattempts=%d/%d\n", job.ID, job.Kind, job.Status, job.Priority, job.Attempts, job.MaxAttempts)
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
	if len(args) == 0 || args[0] != "list" {
		return fmt.Errorf("job requires list, get, status, pause, resume, cancel, or retry")
	}
	flags := flag.NewFlagSet("job list", flag.ContinueOnError)
	flags.SetOutput(stderr)
	limit, page := paginationFlags(flags)
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	query, queryArgs, err := applyPagination("SELECT id, kind, priority, status, created_at, attempts, max_attempts FROM jobs ORDER BY created_at DESC", nil, *limit, *page)
	if err != nil {
		return err
	}
	rows, err := application.DB.QueryContext(ctx, query, queryArgs...)
	if err != nil {
		return err
	}
	defer rows.Close()
	type item struct {
		ID          string `json:"id"`
		Kind        string `json:"kind"`
		Priority    int    `json:"priority"`
		Status      string `json:"status"`
		CreatedAt   string `json:"created_at"`
		Attempts    int    `json:"attempts"`
		MaxAttempts int    `json:"max_attempts"`
	}
	var result []item
	for rows.Next() {
		var value item
		if err := rows.Scan(&value.ID, &value.Kind, &value.Priority, &value.Status, &value.CreatedAt, &value.Attempts, &value.MaxAttempts); err != nil {
			return err
		}
		result = append(result, value)
	}
	if jsonOutput {
		return writeJSON(stdout, envelope("job list", result))
	}
	fmt.Fprintln(stdout, "id\tkind\tstatus\tpriority\tattempts")
	for _, value := range result {
		fmt.Fprintf(stdout, "%s\t%s\t%s\t%d\tattempts=%d/%d\n", value.ID, value.Kind, value.Status, value.Priority, value.Attempts, value.MaxAttempts)
	}
	return rows.Err()
}

type jobStatusReport struct {
	JobID                         string      `json:"job_id"`
	Status                        jobs.Status `json:"status"`
	ItemsCompleted                int         `json:"items_completed"`
	ItemsProcessed                int         `json:"items_processed"`
	ItemsRemaining                int         `json:"items_remaining"`
	CurrentItem                   *string     `json:"current_item"`
	PercentageComplete            float64     `json:"percentage_complete"`
	StartedAt                     *time.Time  `json:"started_at,omitempty"`
	TimeRunning                   string      `json:"time_running"`
	TimeRunningSeconds            float64     `json:"time_running_seconds"`
	EstimatedTimeRemaining        *string     `json:"estimated_time_remaining,omitempty"`
	EstimatedTimeRemainingSeconds *float64    `json:"estimated_time_remaining_seconds,omitempty"`
	FailedItems                   int         `json:"failed_items"`
}

func currentJobStatus(ctx context.Context, store jobs.Store, jobID string) (jobStatusReport, error) {
	job, err := store.Get(ctx, jobID)
	if err != nil {
		return jobStatusReport{}, err
	}
	items, err := store.Items(ctx, jobID)
	if err != nil {
		return jobStatusReport{}, err
	}
	completed, failed, skipped := 0, 0, 0
	var current *jobs.Item
	for index := range items {
		item := &items[index]
		switch item.Status {
		case "completed":
			completed++
		case "failed":
			failed++
		case "skipped":
			skipped++
		case "running":
			if current == nil || item.UpdatedAt.After(current.UpdatedAt) {
				current = item
			}
		}
	}
	processed := completed + failed + skipped
	remaining := len(items) - processed
	if remaining < 0 {
		remaining = 0
	}
	percentage := 0.0
	if len(items) > 0 {
		percentage = float64(processed) * 100 / float64(len(items))
	}
	end := time.Now().UTC()
	if job.CompletedAt != nil {
		end = job.CompletedAt.UTC()
	}
	running := time.Duration(0)
	if job.StartedAt != nil && end.After(job.StartedAt.UTC()) {
		running = end.Sub(job.StartedAt.UTC())
	}
	report := jobStatusReport{JobID: job.ID, Status: job.Status, ItemsCompleted: completed, ItemsProcessed: processed, ItemsRemaining: remaining, PercentageComplete: percentage, StartedAt: job.StartedAt, TimeRunning: running.String(), TimeRunningSeconds: running.Seconds(), FailedItems: failed}
	if current != nil {
		value := current.Key
		report.CurrentItem = &value
	}
	if processed > 0 && remaining > 0 && running > 0 {
		seconds := running.Seconds() * float64(remaining) / float64(processed)
		report.EstimatedTimeRemainingSeconds = &seconds
		duration := (time.Duration(seconds * float64(time.Second))).Round(time.Second)
		value := duration.String()
		report.EstimatedTimeRemaining = &value
	}
	return report, nil
}

func accountCommand(ctx context.Context, application *app.App, args []string, jsonOutput bool, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("account requires get, create, list, disable, enable, or reset-password")
	}
	store := auth.NewStore(application.DB)
	if args[0] == "get" {
		flags := flag.NewFlagSet("account get", flag.ContinueOnError)
		flags.SetOutput(stderr)
		accountID := flags.String("account-id", "", "account ID")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if *accountID == "" {
			return fmt.Errorf("account get requires --account-id")
		}
		var id, username, role, created, updated string
		var disabled, mustChange int
		if err := application.DB.QueryRowContext(ctx, `SELECT id, username, role, disabled_at IS NOT NULL, must_change_password, created_at, updated_at FROM accounts WHERE id = ?`, *accountID).Scan(&id, &username, &role, &disabled, &mustChange, &created, &updated); err != nil {
			return err
		}
		return writeCommandResult(stdout, jsonOutput, "account get", map[string]any{"id": id, "username": username, "role": role, "disabled": disabled == 1, "must_change_password": mustChange == 1, "created_at": created, "updated_at": updated})
	}
	if args[0] == "list" {
		flags := flag.NewFlagSet("account list", flag.ContinueOnError)
		flags.SetOutput(stderr)
		limit, page := paginationFlags(flags)
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		query, queryArgs, err := applyPagination("SELECT id, username, role, disabled_at IS NOT NULL FROM accounts ORDER BY username", nil, *limit, *page)
		if err != nil {
			return err
		}
		rows, err := application.DB.QueryContext(ctx, query, queryArgs...)
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
		fmt.Fprintln(stdout, "id\tusername\trole\tdisabled")
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
	if args[0] == "dont-track" {
		flags := flag.NewFlagSet("people dont-track", flag.ContinueOnError)
		flags.SetOutput(stderr)
		personID := flags.String("person-id", "", "person ID")
		value := flags.Bool("value", true, "set or clear don't-track")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if *personID == "" {
			return fmt.Errorf("people dont-track requires --person-id")
		}
		if err := review.NewStore(application.DB).SetPersonDontTrack(ctx, *personID, *value, review.Actor{Type: "user"}, "cli"); err != nil {
			return err
		}
		return writeCommandResult(stdout, jsonOutput, "people dont-track", map[string]any{"person_id": *personID, "dont_track": *value})
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
		if jsonOutput {
			return writeJSON(stdout, envelope("account "+args[0], map[string]any{"account_id": *accountID, "disabled": args[0] == "disable"}))
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
		if jsonOutput {
			return writeJSON(stdout, envelope("account grant", map[string]any{"account_id": *accountID, "collection_id": *collectionID, "view": *view, "modify": *modify, "undo": *undo}))
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
		return fmt.Errorf("people requires get, create, or list")
	}
	if args[0] == "get" {
		flags := flag.NewFlagSet("people get", flag.ContinueOnError)
		flags.SetOutput(stderr)
		personID := flags.String("person-id", "", "person ID")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if *personID == "" {
			return fmt.Errorf("people get requires --person-id")
		}
		var id, name, created, updated string
		var provisional, dontTrack int
		if err := application.DB.QueryRowContext(ctx, `SELECT id, COALESCE(NULLIF(name, ''), substr(id, -8)), is_provisional, dont_track, created_at, updated_at FROM people WHERE id = ?`, *personID).Scan(&id, &name, &provisional, &dontTrack, &created, &updated); err != nil {
			return err
		}
		return writeCommandResult(stdout, jsonOutput, "people get", map[string]any{"id": id, "name": name, "provisional": provisional == 1, "dont_track": dontTrack == 1, "created_at": created, "updated_at": updated})
	}
	if args[0] == "create" {
		flags := flag.NewFlagSet("people create", flag.ContinueOnError)
		flags.SetOutput(stderr)
		name := flags.String("name", "", "person name")
		provisional := flags.Bool("provisional", false, "mark provisional")
		yes := flags.Bool("yes", false, "confirm creation of an identity record")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if *name == "" || !*yes {
			return fmt.Errorf("people create requires --name and --yes")
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
	flags := flag.NewFlagSet("people list", flag.ContinueOnError)
	flags.SetOutput(stderr)
	limit, page := paginationFlags(flags)
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	query, queryArgs, err := applyPagination("SELECT id, COALESCE(NULLIF(name, ''), substr(id, -8)), is_provisional, dont_track FROM people ORDER BY COALESCE(NULLIF(name, ''), substr(id, -8)), id", nil, *limit, *page)
	if err != nil {
		return err
	}
	rows, err := application.DB.QueryContext(ctx, query, queryArgs...)
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
	fmt.Fprintln(stdout, "id\tname")
	for _, value := range result {
		fmt.Fprintf(stdout, "%s\t%s\n", value.ID, value.Name)
	}
	return rows.Err()
}

func configCommand(application *app.App, args []string, jsonOutput bool, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("config requires show or set-search")
	}
	if args[0] == "set-search" {
		flags := flag.NewFlagSet("config set-search", flag.ContinueOnError)
		flags.SetOutput(stderr)
		method := flags.String("method", application.Config.NeighborSearchMethod, "neighbor search method: exact or ann")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if *method != "exact" && *method != "ann" {
			return fmt.Errorf("search method must be exact or ann")
		}
		settings := map[string]any{}
		if data, err := os.ReadFile(application.Config.ConfigFile); err == nil {
			if err := json.Unmarshal(data, &settings); err != nil {
				return err
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		settings["neighbor_search_method"] = *method
		data, err := json.MarshalIndent(settings, "", "  ")
		if err != nil {
			return err
		}
		if err := os.WriteFile(application.Config.ConfigFile, append(data, '\n'), 0o600); err != nil {
			return err
		}
		return writeCommandResult(stdout, jsonOutput, "config set-search", map[string]string{"method": *method, "config_file": application.Config.ConfigFile})
	}
	if args[0] != "show" || len(args) != 1 {
		return fmt.Errorf("config requires show or set-search")
	}
	status, err := application.Status(context.Background())
	if err != nil {
		return err
	}
	if jsonOutput {
		return writeJSON(stdout, envelope("config show", status))
	}
	data, err := json.MarshalIndent(status, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(stdout, string(data))
	return err
}

func faceCommand(ctx context.Context, application *app.App, args []string, jsonOutput bool, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("face requires get, assign, disposition, or reference")
	}
	flags := flag.NewFlagSet("face "+args[0], flag.ContinueOnError)
	flags.SetOutput(stderr)
	faceID := flags.String("face-id", "", "face ID")
	personID := flags.String("person-id", "", "person ID")
	state := flags.String("state", "", "assignment state")
	disposition := flags.String("disposition", "", "face disposition")
	reference := flags.Bool("reference", false, "set as reference")
	yes := flags.Bool("yes", false, "confirm identity-changing operation")
	limit, page := paginationFlags(flags)
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if args[0] == "list" {
		query := `SELECT f.id, f.analysis_id, f.face_index, f.detection_confidence, f.quality, COALESCE(a.state, 'unassigned'), COALESCE(a.person_id, ''), COALESCE(NULLIF(p.name, ''), substr(p.id, -8), '')
			FROM faces f LEFT JOIN current_face_assignments a ON a.face_id = f.id LEFT JOIN people p ON p.id = a.person_id ORDER BY f.created_at, f.id`
		query, queryArgs, err := applyPagination(query, nil, *limit, *page)
		if err != nil {
			return err
		}
		rows, err := application.DB.QueryContext(ctx, query, queryArgs...)
		if err != nil {
			return err
		}
		defer rows.Close()
		type face struct {
			ID                  string  `json:"id"`
			AnalysisID          string  `json:"analysis_id"`
			Index               int     `json:"face_index"`
			DetectionConfidence float64 `json:"detection_confidence"`
			Quality             float64 `json:"quality"`
			State               string  `json:"state"`
			PersonID            string  `json:"person_id"`
			PersonName          string  `json:"person_name"`
		}
		var result []face
		for rows.Next() {
			var item face
			var detection, quality sql.NullFloat64
			if err := rows.Scan(&item.ID, &item.AnalysisID, &item.Index, &detection, &quality, &item.State, &item.PersonID, &item.PersonName); err != nil {
				return err
			}
			if detection.Valid {
				item.DetectionConfidence = detection.Float64
			}
			if quality.Valid {
				item.Quality = quality.Float64
			}
			result = append(result, item)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if jsonOutput {
			return writeJSON(stdout, envelope("face list", result))
		}
		fmt.Fprintln(stdout, "id\tstate\tperson_name\tquality\tdetection_confidence")
		for _, item := range result {
			fmt.Fprintf(stdout, "%s\t%s\t%s\t%g\t%g\n", item.ID, item.State, item.PersonName, item.Quality, item.DetectionConfidence)
		}
		return nil
	}
	if *faceID == "" {
		return fmt.Errorf("face %s requires --face-id", args[0])
	}
	if args[0] == "get" {
		var currentState, currentPerson, currentEvent string
		var dispositionValue, referenceValue sql.NullString
		if err := application.DB.QueryRowContext(ctx, `SELECT COALESCE(state, 'unassigned'), COALESCE(person_id, ''), event_id FROM current_face_assignments WHERE face_id = ?`, *faceID).Scan(&currentState, &currentPerson, &currentEvent); err != nil && err != sql.ErrNoRows {
			return err
		}
		_ = application.DB.QueryRowContext(ctx, `SELECT disposition FROM current_face_dispositions WHERE face_id = ?`, *faceID).Scan(&dispositionValue)
		_ = application.DB.QueryRowContext(ctx, `SELECT reference FROM current_face_references WHERE face_id = ?`, *faceID).Scan(&referenceValue)
		result := map[string]any{"face_id": *faceID, "state": currentState, "person_id": currentPerson, "event_id": currentEvent, "disposition": dispositionValue.String, "reference": referenceValue.String == "1"}
		if jsonOutput {
			return writeJSON(stdout, envelope("face get", result))
		}
		return writeJSON(stdout, result)
	}
	if !*yes {
		return fmt.Errorf("face %s requires --yes", args[0])
	}
	store := review.NewStore(application.DB)
	switch args[0] {
	case "assign":
		if *state == "" {
			return fmt.Errorf("face assign requires --state")
		}
		assignment, err := store.Assign(ctx, *faceID, *personID, review.AssignmentState(*state), review.Actor{Type: "user"}, review.Evidence{}, "cli")
		if err != nil {
			return err
		}
		if jsonOutput {
			return writeJSON(stdout, envelope("face assign", assignment))
		}
		fmt.Fprintln(stdout, assignment.EventID)
		return nil
	case "disposition":
		if *disposition == "" {
			return fmt.Errorf("face disposition requires --disposition")
		}
		if err := store.SetDisposition(ctx, *faceID, *disposition, true, review.Actor{Type: "user"}, "cli"); err != nil {
			return err
		}
		return writeCommandResult(stdout, jsonOutput, "face disposition", map[string]any{"face_id": *faceID, "disposition": *disposition})
	case "reference":
		if *personID == "" {
			return fmt.Errorf("face reference requires --person-id")
		}
		if err := store.SetReference(ctx, *faceID, *personID, *reference, review.Actor{Type: "user"}, "cli"); err != nil {
			return err
		}
		return writeCommandResult(stdout, jsonOutput, "face reference", map[string]any{"face_id": *faceID, "person_id": *personID, "reference": *reference})
	default:
		return fmt.Errorf("unknown face command %q", args[0])
	}
}

func clusterCommand(ctx context.Context, application *app.App, args []string, jsonOutput bool, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("cluster requires run, get, list, or review")
	}
	if args[0] == "run" {
		flags := flag.NewFlagSet("cluster run", flag.ContinueOnError)
		flags.SetOutput(stderr)
		collectionID := flags.String("collection-id", "", "collection ID used for the per-collection search method")
		scope := flags.String("scope", application.Config.ClusteringConfig.CandidateScope, "all, unresolved-only, or subset")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		settings := application.Config.ClusteringConfig
		if *collectionID != "" {
			if err := application.DB.QueryRowContext(ctx, "SELECT neighbor_search_method FROM collections WHERE id = ?", *collectionID).Scan(&settings.NeighborSearchMethod); err != nil {
				return err
			}
		}
		runID, err := clustering.NewProcessor(application.DB, settings).Run(ctx, *scope)
		if err != nil {
			return err
		}
		return writeCommandResult(stdout, jsonOutput, "cluster run", map[string]string{"run_id": runID, "neighbor_search_method": settings.NeighborSearchMethod})
	}
	if args[0] == "list" {
		flags := flag.NewFlagSet("cluster list", flag.ContinueOnError)
		flags.SetOutput(stderr)
		runID := flags.String("run-id", "", "clustering run ID")
		limit, page := paginationFlags(flags)
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		query := `SELECT c.id, c.clustering_run_id, c.cluster_key, COALESCE(c.proposed_person_id, ''), COALESCE(NULLIF(p.name, ''), substr(p.id, -8), ''), c.review_status, c.is_mixed, c.purity, c.connectivity, COUNT(cm.face_id)
			FROM clusters c LEFT JOIN people p ON p.id = c.proposed_person_id LEFT JOIN cluster_memberships cm ON cm.cluster_id = c.id`
		var queryArgs []any
		if *runID != "" {
			query += " WHERE c.clustering_run_id = ?"
			queryArgs = append(queryArgs, *runID)
		}
		query += " GROUP BY c.id ORDER BY c.created_at, c.id"
		var err error
		query, queryArgs, err = applyPagination(query, queryArgs, *limit, *page)
		if err != nil {
			return err
		}
		rows, err := application.DB.QueryContext(ctx, query, queryArgs...)
		if err != nil {
			return err
		}
		defer rows.Close()
		type cluster struct {
			ID                 string  `json:"id"`
			RunID              string  `json:"clustering_run_id"`
			Key                string  `json:"cluster_key"`
			ProposedPersonID   string  `json:"proposed_person_id"`
			ProposedPersonName string  `json:"proposed_person_name"`
			ReviewStatus       string  `json:"review_status"`
			Mixed              bool    `json:"mixed"`
			Purity             float64 `json:"purity"`
			Connectivity       float64 `json:"connectivity"`
			Faces              int     `json:"faces"`
		}
		var result []cluster
		for rows.Next() {
			var item cluster
			var mixed int
			var purity, connectivity sql.NullFloat64
			if err := rows.Scan(&item.ID, &item.RunID, &item.Key, &item.ProposedPersonID, &item.ProposedPersonName, &item.ReviewStatus, &mixed, &purity, &connectivity, &item.Faces); err != nil {
				return err
			}
			item.Mixed = mixed == 1
			if purity.Valid {
				item.Purity = purity.Float64
			}
			if connectivity.Valid {
				item.Connectivity = connectivity.Float64
			}
			result = append(result, item)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if jsonOutput {
			return writeJSON(stdout, envelope("cluster list", result))
		}
		fmt.Fprintln(stdout, "id\treview_status\tfaces\tproposed_person_name\tclustering_run_id")
		for _, item := range result {
			fmt.Fprintf(stdout, "%s\t%s\t%d\t%s\t%s\n", item.ID, item.ReviewStatus, item.Faces, item.ProposedPersonName, item.RunID)
		}
		return nil
	}
	flags := flag.NewFlagSet("cluster "+args[0], flag.ContinueOnError)
	flags.SetOutput(stderr)
	clusterID := flags.String("cluster-id", "", "cluster ID")
	status := flags.String("status", "reviewed", "review status")
	defaultPersonID := flags.String("default-person-id", "", "default person ID")
	expectedVersion := flags.Int("expected-version", 0, "optimistic review version")
	faceID := flags.String("face-id", "", "optional face decision ID")
	facePersonID := flags.String("face-person-id", "", "person ID for the face decision")
	faceState := flags.String("face-state", "", "state for the face decision")
	disposition := flags.String("disposition", "", "face disposition")
	yes := flags.Bool("yes", false, "confirm identity-changing operation")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if *clusterID == "" {
		return fmt.Errorf("cluster %s requires --cluster-id", args[0])
	}
	if args[0] == "get" {
		var key, reviewStatus string
		var version, mixed int
		var purity, connectivity sql.NullFloat64
		if err := application.DB.QueryRowContext(ctx, `SELECT cluster_key, review_status, review_version, is_mixed, purity, connectivity FROM clusters WHERE id = ?`, *clusterID).Scan(&key, &reviewStatus, &version, &mixed, &purity, &connectivity); err != nil {
			return err
		}
		result := map[string]any{"id": *clusterID, "cluster_key": key, "review_status": reviewStatus, "review_version": version, "mixed": mixed == 1, "purity": purity.Float64, "connectivity": connectivity.Float64}
		return writeCommandResult(stdout, jsonOutput, "cluster get", result)
	}
	if args[0] != "review" {
		return fmt.Errorf("unknown cluster command %q", args[0])
	}
	if !*yes {
		return fmt.Errorf("cluster review requires --yes")
	}
	var decisions []review.ClusterFaceDecision
	if *faceID != "" {
		decisions = append(decisions, review.ClusterFaceDecision{FaceID: *faceID, PersonID: *facePersonID, State: review.AssignmentState(*faceState), Disposition: *disposition})
	}
	if err := review.NewStore(application.DB).ReviewCluster(ctx, *clusterID, *defaultPersonID, *status, *expectedVersion, decisions, review.Actor{Type: "user"}, "cli"); err != nil {
		return err
	}
	return writeCommandResult(stdout, jsonOutput, "cluster review", map[string]any{"cluster_id": *clusterID, "status": *status, "expected_version": *expectedVersion + 1})
}

func writeCommandResult(stdout io.Writer, jsonOutput bool, command string, result any) error {
	if jsonOutput {
		return writeJSON(stdout, envelope(command, result))
	}
	data, err := json.Marshal(result)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(stdout, string(data))
	return err
}

// paginationFlags uses zero-based pages so page 10 with a limit of 20 starts
// at row 201, matching the CLI's documented offset semantics.
func paginationFlags(flags *flag.FlagSet) (*int, *int) {
	return flags.Int("limit", 100, "maximum rows to return"), flags.Int("page", 0, "zero-based page number")
}

func applyPagination(query string, args []any, limit, page int) (string, []any, error) {
	if limit < 1 {
		return "", nil, fmt.Errorf("--limit must be positive")
	}
	if page < 0 {
		return "", nil, fmt.Errorf("--page must be non-negative")
	}
	return query + " LIMIT ? OFFSET ?", append(args, limit, page*limit), nil
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
	reviewMode := flags.Bool("review-mode", false, "show faces needing review; excludes assigned faces by default")
	includeAssignedSystem := flags.Bool("include-assigned-system", false, "include system-assigned faces in review mode")
	includeAssignedUser := flags.Bool("include-assigned-user", false, "include user-assigned faces in review mode")
	states := flags.String("states", "", "comma-separated assignment states")
	if err := flags.Parse(args); err != nil {
		return err
	}
	options.PersonID, options.RelativePrefix, options.CaptureDateFrom, options.CaptureDateUntil, options.IncludeProvisional = *person, *directory, *from, *until, *includeProvisional
	options.ReviewMode, options.IncludeAssignedSystem, options.IncludeAssignedUser = *reviewMode, *includeAssignedSystem, *includeAssignedUser
	if *states != "" {
		options.AssignmentStates = strings.Split(*states, ",")
	}
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
	if len(args) == 0 {
		return fmt.Errorf("audit requires list or purge")
	}
	if args[0] == "list" {
		flags := flag.NewFlagSet("audit list", flag.ContinueOnError)
		flags.SetOutput(stderr)
		entityType := flags.String("entity-type", "", "filter by entity type")
		entityID := flags.String("entity-id", "", "filter by entity ID")
		limit, page := paginationFlags(flags)
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		query := `SELECT id, COALESCE(account_id, ''), action, entity_type, entity_id, COALESCE(target_event_id, ''), COALESCE(previous_json, ''), COALESCE(new_json, ''), conflict, created_at FROM audit_events WHERE 1 = 1`
		var queryArgs []any
		if *entityType != "" {
			query += " AND entity_type = ?"
			queryArgs = append(queryArgs, *entityType)
		}
		if *entityID != "" {
			query += " AND entity_id = ?"
			queryArgs = append(queryArgs, *entityID)
		}
		query += " ORDER BY created_at DESC, id DESC"
		var err error
		query, queryArgs, err = applyPagination(query, queryArgs, *limit, *page)
		if err != nil {
			return err
		}
		rows, err := application.DB.QueryContext(ctx, query, queryArgs...)
		if err != nil {
			return err
		}
		defer rows.Close()
		type event struct {
			ID            string          `json:"id"`
			AccountID     string          `json:"account_id"`
			Action        string          `json:"action"`
			EntityType    string          `json:"entity_type"`
			EntityID      string          `json:"entity_id"`
			TargetEventID string          `json:"target_event_id"`
			Previous      json.RawMessage `json:"previous"`
			New           json.RawMessage `json:"new"`
			Conflict      bool            `json:"conflict"`
			CreatedAt     string          `json:"created_at"`
		}
		var result []event
		for rows.Next() {
			var item event
			var previous, current string
			var conflict int
			if err := rows.Scan(&item.ID, &item.AccountID, &item.Action, &item.EntityType, &item.EntityID, &item.TargetEventID, &previous, &current, &conflict, &item.CreatedAt); err != nil {
				return err
			}
			item.Previous = json.RawMessage(previous)
			item.New = json.RawMessage(current)
			item.Conflict = conflict == 1
			result = append(result, item)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if jsonOutput {
			return writeJSON(stdout, envelope("audit list", result))
		}
		fmt.Fprintln(stdout, "created_at\taction\tentity_type\tentity_id\tid")
		for _, item := range result {
			fmt.Fprintf(stdout, "%s\t%s\t%s\t%s\t%s\n", item.CreatedAt, item.Action, item.EntityType, item.EntityID, item.ID)
		}
		return nil
	}
	if args[0] != "purge" {
		return fmt.Errorf("audit requires list or purge")
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
	if len(args) == 0 {
		return fmt.Errorf("pipeline requires get, register, or list")
	}
	if args[0] == "get" {
		flags := flag.NewFlagSet("pipeline get", flag.ContinueOnError)
		flags.SetOutput(stderr)
		pipelineID := flags.String("pipeline-id", "", "pipeline ID")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if *pipelineID == "" {
			return fmt.Errorf("pipeline get requires --pipeline-id")
		}
		var id, family, version, checksums, detector, preprocessing, dtype, normalization, provider, dependencies, created string
		var dimension int
		if err := application.DB.QueryRowContext(ctx, `SELECT id, model_family, model_version, model_checksums_json, detector_config_json, preprocessing_config_json, embedding_dimension, embedding_dtype, normalization, execution_provider, dependency_versions_json, created_at FROM pipelines WHERE id = ?`, *pipelineID).Scan(&id, &family, &version, &checksums, &detector, &preprocessing, &dimension, &dtype, &normalization, &provider, &dependencies, &created); err != nil {
			return err
		}
		result := map[string]any{"id": id, "model_family": family, "model_version": version, "model_checksums": json.RawMessage(checksums), "detector_config": json.RawMessage(detector), "preprocessing_config": json.RawMessage(preprocessing), "embedding_dimension": dimension, "embedding_dtype": dtype, "normalization": normalization, "execution_provider": provider, "dependency_versions": json.RawMessage(dependencies), "created_at": created}
		return writeCommandResult(stdout, jsonOutput, "pipeline get", result)
	}
	if args[0] == "list" {
		flags := flag.NewFlagSet("pipeline list", flag.ContinueOnError)
		flags.SetOutput(stderr)
		limit, page := paginationFlags(flags)
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		query, queryArgs, err := applyPagination(`SELECT id, model_family, model_version, embedding_dimension, embedding_dtype, normalization, execution_provider, model_checksums_json, detector_config_json, preprocessing_config_json FROM pipelines ORDER BY created_at DESC`, nil, *limit, *page)
		if err != nil {
			return err
		}
		rows, err := application.DB.QueryContext(ctx, query, queryArgs...)
		if err != nil {
			return err
		}
		defer rows.Close()
		var result []map[string]any
		for rows.Next() {
			var id, family, version, dtype, normalization, provider, checksums, detector, preprocessing string
			var dimension int
			if err := rows.Scan(&id, &family, &version, &dimension, &dtype, &normalization, &provider, &checksums, &detector, &preprocessing); err != nil {
				return err
			}
			result = append(result, map[string]any{"id": id, "model_family": family, "model_version": version, "embedding_dimension": dimension, "embedding_dtype": dtype, "normalization": normalization, "execution_provider": provider, "model_checksums": json.RawMessage(checksums), "detector_config": json.RawMessage(detector), "preprocessing_config": json.RawMessage(preprocessing)})
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if jsonOutput {
			return writeJSON(stdout, envelope("pipeline list", result))
		}
		fmt.Fprintln(stdout, "id\tmodel_family\tmodel_version\texecution_provider")
		for _, item := range result {
			fmt.Fprintf(stdout, "%s\t%s\t%s\t%s\n", item["id"], item["model_family"], item["model_version"], item["execution_provider"])
		}
		return nil
	}
	if args[0] != "register" {
		return fmt.Errorf("pipeline requires get, register, or list")
	}
	flags := flag.NewFlagSet("pipeline register", flag.ContinueOnError)
	flags.SetOutput(stderr)
	family := flags.String("model-family", "buffalo_l", "model family")
	version := flags.String("model-version", "2.0.0", "model version")
	dimension := flags.Int("embedding-dimension", 512, "embedding dimension")
	provider := flags.String("provider", "CPUExecutionProvider", "execution provider")
	checksums := flags.String("model-checksums-json", "{}", "model checksum JSON object")
	detector := flags.String("detector-config-json", "{}", "detector configuration JSON object")
	preprocessing := flags.String("preprocessing-config-json", "{}", "preprocessing configuration JSON object")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	var checksumValue map[string]string
	if err := json.Unmarshal([]byte(*checksums), &checksumValue); err != nil {
		return fmt.Errorf("invalid model checksums JSON: %w", err)
	}
	var detectorValue, preprocessingValue any
	if err := json.Unmarshal([]byte(*detector), &detectorValue); err != nil {
		return fmt.Errorf("invalid detector config JSON: %w", err)
	}
	if err := json.Unmarshal([]byte(*preprocessing), &preprocessingValue); err != nil {
		return fmt.Errorf("invalid preprocessing config JSON: %w", err)
	}
	pipelineID, err := analysis.NewStore(application.DB).RegisterPipeline(ctx, analysis.Pipeline{ModelFamily: *family, ModelVersion: *version, ModelChecksums: checksumValue, DetectorConfig: detectorValue, PreprocessingConfig: preprocessingValue, EmbeddingDimension: *dimension, EmbeddingDType: "float32", Normalization: "l2", ExecutionProvider: *provider})
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
	if len(args) > 0 && (args[0] == "status" || args[0] == "stop") {
		pidPath := filepath.Join(application.Config.RuntimeDataRoot, "worker.pid")
		data, err := os.ReadFile(pidPath)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				fmt.Fprintln(stdout, "stopped")
				return nil
			}
			return err
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
		if err != nil {
			return fmt.Errorf("invalid worker pid file: %w", err)
		}
		process, err := os.FindProcess(pid)
		if err != nil {
			return err
		}
		if args[0] == "status" {
			if err := process.Signal(syscall.Signal(0)); err != nil {
				fmt.Fprintln(stdout, "stopped")
				_ = os.Remove(pidPath)
				return nil
			}
			fmt.Fprintf(stdout, "running\t%d\n", pid)
			return nil
		}
		if err := process.Signal(syscall.SIGTERM); err != nil && !errors.Is(err, os.ErrProcessDone) {
			return err
		}
		_ = os.Remove(pidPath)
		fmt.Fprintln(stdout, "stopping")
		return nil
	}
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
	var modelFamily, provider, checksumsJSON, detectorJSON, preprocessingJSON string
	var dimension int
	if err := application.DB.QueryRowContext(ctx, `
		SELECT model_family, execution_provider, model_checksums_json, detector_config_json, preprocessing_config_json, embedding_dimension
		FROM pipelines WHERE id = ?`, *pipelineID).Scan(&modelFamily, &provider, &checksumsJSON, &detectorJSON, &preprocessingJSON, &dimension); err != nil {
		return fmt.Errorf("load pipeline contract: %w", err)
	}
	var checksums map[string]string
	if err := json.Unmarshal([]byte(checksumsJSON), &checksums); err != nil {
		return fmt.Errorf("decode pipeline model checksums: %w", err)
	}
	client := worker.NewClientForPipeline(*pythonPath, worker.Expectation{
		Model:              modelFamily,
		Provider:           provider,
		ModelChecksums:     checksums,
		EmbeddingDimension: dimension,
		EmbeddingDType:     "float32",
		EmbeddingMetric:    "cosine",
		Modules:            []string{"detection", "recognition"},
	}, *workerScript, "--model", modelFamily, "--model-root", application.Config.ModelRoot,
		"--providers", provider, "--model-checksums-json", checksumsJSON,
		"--detector-config-json", detectorJSON, "--preprocessing-config-json", preprocessingJSON,
		"--modules", "detection,recognition")
	if err := client.Start(ctx); err != nil {
		return err
	}
	defer client.Close()
	pidPath := filepath.Join(application.Config.RuntimeDataRoot, "worker.pid")
	if err := os.WriteFile(pidPath, []byte(strconv.Itoa(os.Getpid())+"\n"), 0o600); err != nil {
		return err
	}
	defer os.Remove(pidPath)
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
			rows, err := application.DB.QueryContext(jobContext, `SELECT DISTINCT asset_id FROM photo_paths WHERE collection_id = ? AND missing_at IS NULL`, payload.CollectionID)
			if err != nil {
				return err
			}
			var assetIDs []string
			for rows.Next() {
				var assetID string
				if err := rows.Scan(&assetID); err != nil {
					rows.Close()
					return err
				}
				assetIDs = append(assetIDs, assetID)
			}
			if err := rows.Close(); err != nil {
				return err
			}
			if err := jobStore.EnsureItems(jobContext, job.ID, assetIDs); err != nil {
				return err
			}
			if err := processor.ProcessCollectionWithItemProgress(jobContext, payload.CollectionID, func(assetID string, itemErr error) error {
				claimed, claimErr := jobStore.ClaimItem(jobContext, job.ID, assetID)
				if claimErr != nil {
					return claimErr
				}
				if !claimed {
					return nil
				}
				if itemErr != nil {
					return jobStore.FailItem(jobContext, job.ID, assetID, "analysis_failed", itemErr.Error(), false)
				}
				return jobStore.CompleteItem(jobContext, job.ID, assetID)
			}); err != nil {
				return err
			}
			_, err = jobStore.Enqueue(jobContext, "recognition", 1, map[string]string{"pipeline_id": *pipelineID})
			return err
		},
		"recognition": func(jobContext context.Context, job jobs.Job) error {
			faceIDs, err := jobFaceIDs(jobContext, application.DB)
			if err != nil {
				return err
			}
			if err := jobStore.EnsureItems(jobContext, job.ID, faceIDs); err != nil {
				return err
			}
			_ = jobStore.UpdateProgress(jobContext, job.ID, map[string]any{"phase": "recognition", "items": len(faceIDs), "completed": 0})
			if err := recognition.NewProcessor(application.DB, application.Config.RecognitionThresholds, *pipelineID).ProcessAll(jobContext); err != nil {
				return err
			}
			if err := completeJobItems(jobContext, jobStore, job.ID, faceIDs); err != nil {
				return err
			}
			_ = jobStore.UpdateProgress(jobContext, job.ID, map[string]any{"phase": "recognition", "items": len(faceIDs), "completed": len(faceIDs)})
			_, err = jobStore.Enqueue(jobContext, "clustering", 2, map[string]string{"scope": application.Config.ClusteringConfig.CandidateScope})
			return err
		},
		"reference-reprocess": func(jobContext context.Context, job jobs.Job) error {
			var payload struct {
				PersonID string `json:"person_id"`
			}
			if err := json.Unmarshal(job.Payload, &payload); err != nil {
				return err
			}
			if payload.PersonID == "" {
				return fmt.Errorf("reference reprocess job has no person_id")
			}
			recognitionProcessor := recognition.NewProcessor(application.DB, application.Config.RecognitionThresholds, *pipelineID)
			if err := recognitionProcessor.ProcessPerson(jobContext, payload.PersonID); err != nil {
				return err
			}
			rows, err := application.DB.QueryContext(jobContext, `SELECT DISTINCT face_id FROM (
				SELECT ca.face_id FROM current_face_assignments ca WHERE ca.person_id = ?
				UNION SELECT fr.face_id FROM current_face_references fr WHERE fr.person_id = ?
				UNION SELECT rc.face_id FROM recognition_candidates rc WHERE rc.person_id = ?
				UNION SELECT cm.face_id FROM cluster_memberships cm JOIN clusters c ON c.id = cm.cluster_id WHERE c.proposed_person_id = ?
			) ORDER BY face_id`, payload.PersonID, payload.PersonID, payload.PersonID, payload.PersonID)
			if err != nil {
				return err
			}
			var affectedFaces []string
			for rows.Next() {
				var faceID string
				if err := rows.Scan(&faceID); err != nil {
					rows.Close()
					return err
				}
				affectedFaces = append(affectedFaces, faceID)
			}
			if err := rows.Close(); err != nil {
				return err
			}
			clusteringConfig := application.Config.ClusteringConfig
			if len(affectedFaces) > 0 {
				if _, err := clustering.NewProcessor(application.DB, clusteringConfig).RunFaces(jobContext, affectedFaces); err != nil {
					return err
				}
			}
			return recognitionProcessor.RefreshRecommendations(jobContext, payload.PersonID)
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
			clusteringConfig := application.Config.ClusteringConfig
			faceIDs, err := jobFaceIDs(jobContext, application.DB)
			if err != nil {
				return err
			}
			if err := jobStore.EnsureItems(jobContext, job.ID, faceIDs); err != nil {
				return err
			}
			_ = jobStore.UpdateProgress(jobContext, job.ID, map[string]any{"phase": "clustering", "items": len(faceIDs), "completed": 0})
			_, err = clustering.NewProcessor(application.DB, clusteringConfig).Run(jobContext, payload.Scope)
			if err != nil {
				return err
			}
			if err := completeJobItems(jobContext, jobStore, job.ID, faceIDs); err != nil {
				return err
			}
			_ = jobStore.UpdateProgress(jobContext, job.ID, map[string]any{"phase": "clustering", "items": len(faceIDs), "completed": len(faceIDs)})
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

func jobFaceIDs(ctx context.Context, database *sql.DB) ([]string, error) {
	rows, err := database.QueryContext(ctx, `SELECT f.id FROM faces f LEFT JOIN current_face_assignments ca ON ca.face_id = f.id WHERE f.embedding IS NOT NULL AND COALESCE(ca.state, 'unassigned') <> 'assigned-user' ORDER BY f.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func completeJobItems(ctx context.Context, store jobs.Store, jobID string, ids []string) error {
	for _, id := range ids {
		claimed, err := store.ClaimItem(ctx, jobID, id)
		if err != nil {
			return err
		}
		if claimed {
			if err := store.CompleteItem(ctx, jobID, id); err != nil {
				return err
			}
		}
	}
	return nil
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
