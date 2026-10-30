// Package perf provides repeatable synthetic performance and scale benchmarks.
// The generated data is intentionally not a substitute for a real-library
// benchmark; it makes runtime, memory, and algorithm comparisons repeatable.
package perf

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/weisnobody/whothatface/internal/jobs"
	_ "modernc.org/sqlite"
)

type Options struct {
	Scales        []int
	Dimension     int
	K             int
	ExactMax      int
	Database      bool
	WorkerPath    string
	PythonPath    string
	WorkerTimeout time.Duration
}

type Measurement struct {
	Name         string  `json:"name"`
	Population   int     `json:"population"`
	Status       string  `json:"status"`
	DurationMS   float64 `json:"duration_ms,omitempty"`
	Operations   int     `json:"operations,omitempty"`
	OperationsPS float64 `json:"operations_per_second,omitempty"`
	MemoryBytes  uint64  `json:"memory_bytes,omitempty"`
	Details      string  `json:"details,omitempty"`
}

type Report struct {
	SchemaVersion string        `json:"schema_version"`
	GeneratedAt   string        `json:"generated_at"`
	GoVersion     string        `json:"go_version"`
	OS            string        `json:"os"`
	Architecture  string        `json:"architecture"`
	Scales        []int         `json:"scales"`
	Measurements  []Measurement `json:"measurements"`
}

func DefaultOptions() Options {
	return Options{Scales: []int{10000, 50000, 100000, 250000}, Dimension: 32, K: 40, ExactMax: 10000, Database: true, WorkerTimeout: 10 * time.Second}
}

func Run(ctx context.Context, options Options) (Report, error) {
	defaults := DefaultOptions()
	if len(options.Scales) == 0 {
		options.Scales = defaults.Scales
	}
	if options.Dimension <= 0 {
		options.Dimension = defaults.Dimension
	}
	if options.K <= 0 {
		options.K = defaults.K
	}
	if options.ExactMax <= 0 {
		options.ExactMax = defaults.ExactMax
	}
	if options.WorkerTimeout <= 0 {
		options.WorkerTimeout = defaults.WorkerTimeout
	}

	report := Report{SchemaVersion: "1", GeneratedAt: time.Now().UTC().Format(time.RFC3339Nano), GoVersion: runtime.Version(), OS: runtime.GOOS, Architecture: runtime.GOARCH, Scales: append([]int(nil), options.Scales...)}
	for _, size := range options.Scales {
		if size <= 0 {
			return Report{}, fmt.Errorf("benchmark scale must be positive")
		}
		if err := ctx.Err(); err != nil {
			return report, err
		}
		vectors := syntheticVectors(size, options.Dimension)
		report.Measurements = append(report.Measurements, measureScan(size)...)
		report.Measurements = append(report.Measurements, measureDuplicateScan(size), measureAnalysis(vectors), measureRecognition(vectors, options.K), measureMemory(vectors))
		report.Measurements = append(report.Measurements, measureNeighbors(ctx, vectors, options.K, options.ExactMax)...)
		if options.Database {
			measurement, err := measureDatabase(ctx, size)
			if err != nil {
				return report, err
			}
			report.Measurements = append(report.Measurements, measurement...)
		}
	}
	report.Measurements = append(report.Measurements, measureAdaptivePolicy(), measureWorkerLifecycle(options))
	return report, nil
}

func syntheticVectors(count, dimension int) [][]float32 {
	random := rand.New(rand.NewSource(int64(count*1009 + dimension)))
	result := make([][]float32, count)
	for index := range result {
		vector := make([]float32, dimension)
		var norm float64
		for component := range vector {
			value := random.NormFloat64()
			vector[component] = float32(value)
			norm += value * value
		}
		factor := float32(1 / math.Sqrt(norm))
		for component := range vector {
			vector[component] *= factor
		}
		result[index] = vector
	}
	return result
}

func measureScan(size int) []Measurement {
	start := time.Now()
	paths := make([]string, size)
	for index := range paths {
		paths[index] = fmt.Sprintf("group/%08d/photo.jpg", index)
	}
	first := time.Since(start)
	start = time.Now()
	seen := make(map[string]struct{}, size)
	for _, path := range paths {
		seen[path] = struct{}{}
	}
	unchanged := time.Since(start)
	firstResult := measurement("scan.first", size, first, size, "synthetic recursive path enumeration")
	unchangedResult := measurement("scan.unchanged-rescan", size, unchanged, size, fmt.Sprintf("matched_existing=%d", len(seen)))
	return []Measurement{firstResult, unchangedResult}
}

func measureDuplicateScan(size int) Measurement {
	start := time.Now()
	assets := make(map[string]struct{}, size/4+1)
	for index := 0; index < size; index++ {
		assets[fmt.Sprintf("hash-%08d", index%(size/4+1))] = struct{}{}
	}
	return measurement("scan.duplicate-heavy", size, time.Since(start), size, fmt.Sprintf("%d unique assets", len(assets)))
}

func measureAnalysis(vectors [][]float32) Measurement {
	start := time.Now()
	checksum := float32(0)
	for _, vector := range vectors {
		checksum += vector[0]
	}
	return measurement("analysis.synthetic", len(vectors), time.Since(start), len(vectors), fmt.Sprintf("checksum=%.6f", checksum))
}

func measureMemory(vectors [][]float32) Measurement {
	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)
	return Measurement{Name: "memory.synthetic-workload", Population: len(vectors), Status: "ok", MemoryBytes: stats.Alloc, Details: "Go heap allocation after synthetic embeddings"}
}

func measureRecognition(vectors [][]float32, k int) Measurement {
	start := time.Now()
	queries := min(len(vectors), 256)
	comparisons := 0
	for query := 0; query < queries; query++ {
		best := float32(-2)
		for candidate, vector := range vectors {
			if candidate == query {
				continue
			}
			value := cosine(vectors[query], vector)
			if value > best {
				best = value
			}
			comparisons++
		}
	}
	return measurement("recognition.exact", len(vectors), time.Since(start), comparisons, fmt.Sprintf("queries=%d k=%d", queries, k))
}

func measureNeighbors(ctx context.Context, vectors [][]float32, k, exactMax int) []Measurement {
	queries := min(len(vectors), 256)
	result := []Measurement{}
	start := time.Now()
	ann := approximateNeighbors(vectors, queries, k)
	annDetails := fmt.Sprintf("queries=%d k=%d recall=%.4f", queries, k, ann.recall)
	if len(vectors) > exactMax {
		annDetails = fmt.Sprintf("queries=%d k=%d recall=not-measured", queries, k)
	}
	result = append(result, measurement("grouping.ann", len(vectors), time.Since(start), queries, annDetails))
	if len(vectors) > exactMax {
		result = append(result, Measurement{Name: "grouping.exact", Population: len(vectors), Status: "skipped", Details: fmt.Sprintf("exact benchmark capped at %d faces", exactMax)})
		return result
	}
	if err := ctx.Err(); err != nil {
		return result
	}
	start = time.Now()
	exact := exactNeighbors(vectors, queries, k)
	result = append(result, measurement("grouping.exact", len(vectors), time.Since(start), queries, fmt.Sprintf("queries=%d k=%d", queries, k)))
	result[0].Details = fmt.Sprintf("queries=%d k=%d recall=%.4f", queries, k, recall(exact, ann.neighbors))
	return result
}

type neighborResult struct {
	neighbors [][]int
	recall    float64
}

func exactNeighbors(vectors [][]float32, queries, k int) [][]int {
	result := make([][]int, queries)
	for query := 0; query < queries; query++ {
		values := make([]scored, 0, len(vectors)-1)
		for candidate, vector := range vectors {
			if candidate != query {
				values = append(values, scored{candidate, cosine(vectors[query], vector)})
			}
		}
		sort.Slice(values, func(left, right int) bool { return values[left].score > values[right].score })
		result[query] = ids(values[:min(k, len(values))])
	}
	return result
}

func approximateNeighbors(vectors [][]float32, queries, k int) neighborResult {
	buckets := map[uint16][]int{}
	for index, vector := range vectors {
		buckets[signature(vector)] = append(buckets[signature(vector)], index)
	}
	result := make([][]int, queries)
	for query := 0; query < queries; query++ {
		candidates := append([]int{}, buckets[signature(vectors[query])]...)
		querySignature := signature(vectors[query])
		for mask := 0; mask < 16; mask++ {
			if bitsSet(uint16(mask)) > 2 {
				continue
			}
			candidates = append(candidates, buckets[querySignature^uint16(mask)]...)
		}
		values := make([]scored, 0, len(candidates))
		for _, candidate := range candidates {
			if candidate != query {
				values = append(values, scored{candidate, cosine(vectors[query], vectors[candidate])})
			}
		}
		sort.Slice(values, func(left, right int) bool { return values[left].score > values[right].score })
		result[query] = ids(values[:min(k, len(values))])
	}
	return neighborResult{neighbors: result}
}

type scored struct {
	id    int
	score float32
}

func ids(values []scored) []int {
	result := make([]int, len(values))
	for index, value := range values {
		result[index] = value.id
	}
	return result
}
func recall(expected, actual [][]int) float64 {
	total, found := 0, 0
	for index := range expected {
		set := map[int]struct{}{}
		for _, id := range expected[index] {
			set[id] = struct{}{}
		}
		for _, id := range actual[index] {
			if _, ok := set[id]; ok {
				found++
			}
		}
		total += len(expected[index])
	}
	if total == 0 {
		return 0
	}
	return float64(found) / float64(total)
}
func signature(vector []float32) uint16 {
	var result uint16
	for index := 0; index < 4 && index < len(vector); index++ {
		if vector[index] >= 0 {
			result |= 1 << index
		}
	}
	return result
}

func bitsSet(value uint16) int {
	count := 0
	for value != 0 {
		count += int(value & 1)
		value >>= 1
	}
	return count
}
func cosine(left, right []float32) float32 {
	var value float32
	for index := range left {
		value += left[index] * right[index]
	}
	return value
}

func measureDatabase(ctx context.Context, size int) ([]Measurement, error) {
	path := filepath.Join(os.TempDir(), fmt.Sprintf("whothatface-perf-%d.sqlite3", time.Now().UnixNano()))
	defer os.Remove(path)
	database, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	defer database.Close()
	if _, err := database.ExecContext(ctx, "CREATE TABLE review_faces (id INTEGER PRIMARY KEY, person_id TEXT, review_status TEXT)"); err != nil {
		return nil, err
	}
	start := time.Now()
	transaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	statement, err := transaction.PrepareContext(ctx, "INSERT INTO review_faces(id, person_id, review_status) VALUES (?, ?, ?)")
	if err != nil {
		return nil, err
	}
	for index := 0; index < size; index++ {
		if _, err := statement.ExecContext(ctx, index, fmt.Sprintf("person-%d", index%100), "unreviewed"); err != nil {
			statement.Close()
			transaction.Rollback()
			return nil, err
		}
	}
	statement.Close()
	if err := transaction.Commit(); err != nil {
		return nil, err
	}
	first := time.Since(start)
	start = time.Now()
	var count int
	if err := database.QueryRowContext(ctx, "SELECT COUNT(*) FROM review_faces WHERE review_status = 'unreviewed'").Scan(&count); err != nil {
		return nil, err
	}
	query := measurement("database.write", size, first, size, "synthetic face rows")
	read := measurement("review.query", size, time.Since(start), 1, fmt.Sprintf("rows=%d", count))
	start = time.Now()
	_, err = database.ExecContext(ctx, "INSERT INTO review_faces(id, person_id, review_status) VALUES (?, ?, ?)", size+1, "person-0", "unreviewed")
	if err != nil {
		return nil, err
	}
	update := measurement("incremental-update", size, time.Since(start), 1, "one additional face")
	info, _ := os.Stat(path)
	memory := Measurement{Name: "database.size", Population: size, Status: "ok"}
	if info != nil {
		memory.MemoryBytes = uint64(info.Size())
	}
	return []Measurement{query, read, update, memory}, nil
}

func measureAdaptivePolicy() Measurement {
	start := time.Now()
	modes := map[jobs.ThrottleMode]int{}
	for _, activity := range []jobs.Activity{{Available: true}, {Available: true, UserActive: true}, {Available: true, Load: 1000}, {Available: false}} {
		policy := jobs.ResourcePolicy{Mode: jobs.ThrottleAutomatic, Probe: func() jobs.Activity { return activity }}
		modes[policy.EffectiveMode()]++
	}
	return measurement("adaptive-throttling", 4, time.Since(start), 4, fmt.Sprintf("modes=%v", modes))
}

func measureWorkerLifecycle(options Options) Measurement {
	if options.WorkerPath == "" {
		return Measurement{Name: "worker.lifecycle", Status: "skipped", Details: "set --worker-path to benchmark the configured worker"}
	}
	python := options.PythonPath
	if python == "" {
		python = "python3.11"
	}
	startup, startupErr := runWorkerOnce(python, options.WorkerPath, options.WorkerTimeout)
	if startupErr != nil {
		return Measurement{Name: "worker.lifecycle", Status: "error", DurationMS: float64(startup.Microseconds()) / 1000, Operations: 1, Details: "startup failed; restart not attempted: " + startupErr.Error()}
	}
	restart, restartErr := runWorkerOnce(python, options.WorkerPath, options.WorkerTimeout)
	if restartErr != nil {
		status := "error"
		if restartErr == context.DeadlineExceeded {
			status = "timeout"
		}
		return Measurement{Name: "worker.lifecycle", Status: status, DurationMS: float64((startup + restart).Microseconds()) / 1000, Operations: 2, Details: "startup succeeded; restart/recovery failed: " + restartErr.Error()}
	}
	return measurement("worker.lifecycle", 1, startup+restart, 2, fmt.Sprintf("startup_ms=%.3f restart_recovery_ms=%.3f", float64(startup.Microseconds())/1000, float64(restart.Microseconds())/1000))
}

func runWorkerOnce(python, workerPath string, timeout time.Duration) (time.Duration, error) {
	start := time.Now()
	command := exec.Command(python, workerPath)
	input, err := command.StdinPipe()
	if err != nil {
		return time.Since(start), err
	}
	if err := command.Start(); err != nil {
		return time.Since(start), err
	}
	_, _ = input.Write([]byte(`{"type":"hello","id":"benchmark","protocol_major":1,"protocol_minor":0}` + "\n"))
	_ = input.Close()
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	select {
	case err := <-done:
		return time.Since(start), err
	case <-time.After(timeout):
		_ = command.Process.Kill()
		<-done
		return time.Since(start), context.DeadlineExceeded
	}
}

func measurement(name string, population int, duration time.Duration, operations int, details string) Measurement {
	result := Measurement{Name: name, Population: population, Status: "ok", DurationMS: float64(duration.Microseconds()) / 1000, Operations: operations, Details: details}
	if duration > 0 {
		result.OperationsPS = float64(operations) / duration.Seconds()
	}
	return result
}
func min(left, right int) int {
	if left < right {
		return left
	}
	return right
}

func WriteJSON(path string, report Report) error {
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o600)
}

func ParseScales(value string) ([]int, error) {
	var result []int
	for _, field := range strings.Split(value, ",") {
		parsed, err := strconv.Atoi(strings.TrimSpace(field))
		if err != nil || parsed <= 0 {
			return nil, fmt.Errorf("invalid benchmark scale %q", field)
		}
		result = append(result, parsed)
	}
	return result, nil
}
