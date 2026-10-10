package perf

import (
	"context"
	"testing"
)

func TestRunSmallBenchmarkIncludesRequiredStages(t *testing.T) {
	report, err := Run(context.Background(), Options{Scales: []int{32}, Dimension: 8, K: 4, ExactMax: 32, Database: false})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, measurement := range report.Measurements {
		seen[measurement.Name] = true
	}
	for _, name := range []string{"scan.first", "scan.unchanged-rescan", "scan.duplicate-heavy", "analysis.synthetic", "recognition.exact", "grouping.exact", "grouping.ann", "memory.synthetic-workload", "adaptive-throttling"} {
		if !seen[name] {
			t.Errorf("missing measurement %q", name)
		}
	}
}

func TestParseScales(t *testing.T) {
	values, err := ParseScales("10000, 50000")
	if err != nil || len(values) != 2 || values[1] != 50000 {
		t.Fatalf("scales = %#v, err = %v", values, err)
	}
	if _, err := ParseScales("0"); err == nil {
		t.Fatal("zero scale should fail")
	}
}
