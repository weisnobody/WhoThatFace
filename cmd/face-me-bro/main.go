package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/pjg/face_me_bro/internal/app"
	"github.com/pjg/face_me_bro/internal/auth"
	"github.com/pjg/face_me_bro/internal/jobs"
	"github.com/pjg/face_me_bro/internal/media"
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
	default:
		return fmt.Errorf("unknown command %q (available: status, collection, scan, job, account)", command)
	}
}

func collectionCommand(ctx context.Context, application *app.App, args []string, jsonOutput bool, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("collection requires add or list")
	}
	switch args[0] {
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
	if err := media.NewStore(application.DB).PersistScan(ctx, *collectionID, records); err != nil {
		return err
	}
	job, err := jobs.NewStore(application.DB).Enqueue(ctx, "analysis", 5, map[string]any{"collection_id": *collectionID})
	if err != nil {
		return err
	}
	result := map[string]any{"collection_id": *collectionID, "files": len(records), "supported": countSupported(records), "analysis_job_id": job.ID}
	if jsonOutput {
		return writeJSON(stdout, envelope("scan", result))
	}
	fmt.Fprintf(stdout, "Scanned %d files; %d supported; analysis job %s\n", len(records), countSupported(records), job.ID)
	return nil
}

func jobCommand(ctx context.Context, application *app.App, args []string, jsonOutput bool, stdout, stderr io.Writer) error {
	if len(args) != 1 || args[0] != "list" {
		return fmt.Errorf("job requires list")
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
	if len(args) == 0 || args[0] != "reset-password" {
		return fmt.Errorf("account requires reset-password")
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
