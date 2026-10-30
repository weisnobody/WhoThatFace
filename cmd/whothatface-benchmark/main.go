package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/weisnobody/whothatface/internal/perf"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	defaults := perf.DefaultOptions()
	output := flag.String("output", "", "JSON report path (stdout when empty)")
	scales := flag.String("scales", "10000,50000,100000,250000", "comma-separated face counts")
	dimension := flag.Int("dimension", defaults.Dimension, "synthetic embedding dimension")
	k := flag.Int("k", defaults.K, "nearest-neighbor k")
	exactMax := flag.Int("exact-max-faces", defaults.ExactMax, "largest population for exact comparison")
	worker := flag.String("worker-path", "", "optional worker script for startup/recovery measurement")
	python := flag.String("python", "python3.11", "Python executable for --worker-path")
	noDatabase := flag.Bool("no-database", false, "skip SQLite size and review-query measurements")
	flag.Parse()
	parsedScales, err := perf.ParseScales(*scales)
	if err != nil {
		return err
	}
	options := defaults
	options.Scales, options.Dimension, options.K, options.ExactMax = parsedScales, *dimension, *k, *exactMax
	options.WorkerPath, options.PythonPath, options.Database = *worker, *python, !*noDatabase
	report, err := perf.Run(context.Background(), options)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if *output == "" {
		_, err = os.Stdout.Write(data)
		return err
	}
	if err := os.WriteFile(*output, data, 0o600); err != nil {
		return err
	}
	fmt.Println(*output)
	return nil
}
