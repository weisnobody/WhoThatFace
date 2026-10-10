package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/pjg/face_me_bro/internal/app"
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
	if command != "status" {
		return fmt.Errorf("unknown command %q (available: status)", command)
	}

	application, err := app.Open(ctx, *configPath, *noMigrate)
	if err != nil {
		return err
	}
	defer application.Close()
	status, err := application.Status(ctx)
	if err != nil {
		return err
	}
	if *jsonOutput {
		return writeJSON(stdout, map[string]any{
			"schema_version": outputSchemaVersion,
			"command":        "status",
			"result":         status,
			"warnings":       []string{},
		})
	}
	fmt.Fprintf(stdout, "Config file:      %s\n", status.ConfigFile)
	fmt.Fprintf(stdout, "Runtime root:     %s\n", status.RuntimeDataRoot)
	fmt.Fprintf(stdout, "Database:         %s\n", status.DatabasePath)
	fmt.Fprintf(stdout, "Model root:       %s\n", status.ModelRoot)
	fmt.Fprintf(stdout, "Log root:         %s\n", status.LogRoot)
	fmt.Fprintf(stdout, "Temp root:        %s\n", status.TempRoot)
	fmt.Fprintf(stdout, "Export root:      %s\n", status.ExportRoot)
	fmt.Fprintf(stdout, "Backup root:      %s\n", status.BackupRoot)
	if status.PipelineID == nil {
		fmt.Fprintln(stdout, "Pipeline:         not configured")
	} else {
		fmt.Fprintf(stdout, "Pipeline:         %s\n", *status.PipelineID)
	}
	fmt.Fprintf(stdout, "Provider:         %s\n", status.Provider)
	return nil
}

func writeJSON(stdout io.Writer, value any) error {
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}
