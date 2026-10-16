package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"io"
	"math"
	"strings"

	"github.com/pjg/face_me_bro/internal/app"
)

type stringListFlag []string

func (v *stringListFlag) String() string { return strings.Join(*v, ",") }
func (v *stringListFlag) Set(value string) error {
	for _, item := range strings.Split(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			*v = append(*v, item)
		}
	}
	return nil
}

type chartBucket struct {
	LowerPercent int     `json:"lower_percent"`
	UpperPercent int     `json:"upper_percent"`
	Count        int     `json:"count"`
	Percent      float64 `json:"percent"`
}

type chartMetric struct {
	Name          string        `json:"name"`
	Population    int           `json:"population"`
	SampleCount   int           `json:"sample_count"`
	ExcludedCount int           `json:"excluded_count"`
	Buckets       []chartBucket `json:"buckets"`
}

type chartReport struct {
	Scope              string        `json:"scope"`
	CollectionIDs      []string      `json:"collection_ids,omitempty"`
	ActiveClusterCount int           `json:"active_cluster_count"`
	Metrics            []chartMetric `json:"metrics"`
}

type chartSeries struct {
	Population int
	Values     []float64
}

func chartCommand(ctx context.Context, application *app.App, args []string, jsonOutput bool, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("chart", flag.ContinueOnError)
	flags.SetOutput(stderr)
	format := flags.String("format", "text", "output format: text, markdown, or json")
	all := flags.Bool("all", false, "include all faces in active clusters (the default)")
	var collectionIDs stringListFlag
	flags.Var(&collectionIDs, "collection-id", "limit to a collection; repeat or use comma-separated IDs")
	flags.Var(&collectionIDs, "collections", "limit to comma-separated collection IDs")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("chart does not accept positional arguments")
	}
	if *all && len(collectionIDs) > 0 {
		return fmt.Errorf("--all cannot be combined with collection filters")
	}
	if jsonOutput {
		*format = "json"
	}
	if *format != "text" && *format != "markdown" && *format != "json" {
		return fmt.Errorf("--format must be text, markdown, or json")
	}
	report, err := buildChartReport(ctx, application.DB, collectionIDs)
	if err != nil {
		return err
	}
	switch *format {
	case "json":
		return writeJSON(stdout, envelope("chart", report))
	case "markdown":
		return writeMarkdownChart(stdout, report)
	default:
		return writeTextChart(stdout, report)
	}
}

func buildChartReport(ctx context.Context, database *sql.DB, collectionIDs []string) (chartReport, error) {
	faceFilter, faceArgs := chartCollectionFilter(collectionIDs, "pa.id")
	clusterFilter, clusterArgs := chartClusterCollectionFilter(collectionIDs)
	activeFaces := `WITH active_faces AS (
	SELECT DISTINCT cm.face_id
	FROM cluster_memberships cm
	JOIN clusters c ON c.id = cm.cluster_id
	JOIN clustering_runs cr ON cr.id = c.clustering_run_id AND cr.status = 'active'
	JOIN faces f ON f.id = cm.face_id
	JOIN analyses a ON a.id = f.analysis_id
	JOIN photo_assets pa ON pa.id = a.asset_id
	WHERE 1=1 ` + faceFilter + `
)
`
	activeClusters := `SELECT DISTINCT c.id
	FROM clusters c
	JOIN clustering_runs cr ON cr.id = c.clustering_run_id AND cr.status = 'active'
	WHERE 1=1 ` + clusterFilter
	activeClusterCount, err := countChartRows(ctx, database, "SELECT COUNT(*) FROM ("+activeClusters+")", clusterArgs...)
	if err != nil {
		return chartReport{}, err
	}
	detection, err := readChartSeries(ctx, database, activeFaces+"SELECT f.detection_confidence FROM active_faces af JOIN faces f ON f.id = af.face_id", faceArgs...)
	if err != nil {
		return chartReport{}, err
	}
	quality, err := readChartSeries(ctx, database, activeFaces+"SELECT f.quality FROM active_faces af JOIN faces f ON f.id = af.face_id", faceArgs...)
	if err != nil {
		return chartReport{}, err
	}
	consistency, err := readChartSeries(ctx, database, "WITH active_clusters AS ("+activeClusters+") SELECT c.consistency FROM active_clusters ac JOIN clusters c ON c.id = ac.id", clusterArgs...)
	if err != nil {
		return chartReport{}, err
	}
	connectivity, err := readChartSeries(ctx, database, "WITH active_clusters AS ("+activeClusters+") SELECT c.connectivity FROM active_clusters ac JOIN clusters c ON c.id = ac.id", clusterArgs...)
	if err != nil {
		return chartReport{}, err
	}
	similarity, err := readChartSeries(ctx, database, activeFaces+`, latest_candidates AS (
	SELECT rc.face_id, rc.similarity,
	ROW_NUMBER() OVER (PARTITION BY rc.face_id ORDER BY rc.created_at DESC, rc.rank ASC, rc.id DESC) AS row_number
	FROM recognition_candidates rc JOIN active_faces af ON af.face_id = rc.face_id
)
SELECT lc.similarity FROM active_faces af
LEFT JOIN latest_candidates lc ON lc.face_id = af.face_id AND lc.row_number = 1`, faceArgs...)
	if err != nil {
		return chartReport{}, err
	}
	scope := "all faces in active clusters"
	if len(collectionIDs) > 0 {
		scope = "faces in selected collections"
	}
	return chartReport{Scope: scope, CollectionIDs: append([]string(nil), collectionIDs...), ActiveClusterCount: activeClusterCount, Metrics: []chartMetric{
		makeChartMetric("detection confidence", detection), makeChartMetric("quality", quality),
		makeChartMetricRange("quality (0-15%)", quality, 15, 1),
		makeChartMetric("consistency", consistency), makeChartMetric("connectivity", connectivity), makeChartMetric("similarity", similarity),
	}}, nil
}

func chartCollectionFilter(collectionIDs []string, assetExpression string) (string, []any) {
	if len(collectionIDs) == 0 {
		return "", nil
	}
	placeholders := make([]string, len(collectionIDs))
	args := make([]any, len(collectionIDs))
	for i, id := range collectionIDs {
		placeholders[i], args[i] = "?", id
	}
	return "AND EXISTS (SELECT 1 FROM photo_paths pp_scope WHERE pp_scope.asset_id = " + assetExpression + " AND pp_scope.collection_id IN (" + strings.Join(placeholders, ",") + ") AND pp_scope.missing_at IS NULL)", args
}

func chartClusterCollectionFilter(collectionIDs []string) (string, []any) {
	if len(collectionIDs) == 0 {
		return "", nil
	}
	placeholders := make([]string, len(collectionIDs))
	args := make([]any, len(collectionIDs))
	for i, id := range collectionIDs {
		placeholders[i], args[i] = "?", id
	}
	return "AND EXISTS (SELECT 1 FROM cluster_memberships cm_scope JOIN faces f_scope ON f_scope.id = cm_scope.face_id JOIN analyses a_scope ON a_scope.id = f_scope.analysis_id JOIN photo_paths pp_scope ON pp_scope.asset_id = a_scope.asset_id WHERE cm_scope.cluster_id = c.id AND pp_scope.collection_id IN (" + strings.Join(placeholders, ",") + ") AND pp_scope.missing_at IS NULL)", args
}

func countChartRows(ctx context.Context, database *sql.DB, query string, args ...any) (int, error) {
	var count int
	if err := database.QueryRowContext(ctx, query, args...).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

func readChartSeries(ctx context.Context, database *sql.DB, query string, args ...any) (chartSeries, error) {
	rows, err := database.QueryContext(ctx, query, args...)
	if err != nil {
		return chartSeries{}, err
	}
	defer rows.Close()
	var series chartSeries
	for rows.Next() {
		var value sql.NullFloat64
		if err := rows.Scan(&value); err != nil {
			return chartSeries{}, err
		}
		series.Population++
		if value.Valid {
			series.Values = append(series.Values, value.Float64)
		}
	}
	if err := rows.Err(); err != nil {
		return chartSeries{}, err
	}
	return series, nil
}

func makeChartMetric(name string, series chartSeries) chartMetric {
	return makeChartMetricRange(name, series, 100, 5)
}

func makeChartMetricRange(name string, series chartSeries, upperPercent, bucketWidth int) chartMetric {
	bucketCount := upperPercent / bucketWidth
	buckets := make([]chartBucket, bucketCount)
	for i := range buckets {
		buckets[i] = chartBucket{LowerPercent: i * bucketWidth, UpperPercent: (i + 1) * bucketWidth}
	}
	sampleCount := 0
	for _, value := range series.Values {
		percent := value * 100
		if percent < 0 || percent > float64(upperPercent) {
			continue
		}
		index := int(percent) / bucketWidth
		if index >= bucketCount {
			index = bucketCount - 1
		}
		buckets[index].Count++
		sampleCount++
	}
	for i := range buckets {
		if sampleCount > 0 {
			buckets[i].Percent = float64(buckets[i].Count) * 100 / float64(sampleCount)
		}
	}
	return chartMetric{Name: name, Population: series.Population, SampleCount: sampleCount, ExcludedCount: series.Population - sampleCount, Buckets: buckets}
}

func writeTextChart(stdout io.Writer, report chartReport) error {
	fmt.Fprintf(stdout, "Scope: %s\nActive clusters: %d\n", report.Scope, report.ActiveClusterCount)
	for _, metric := range report.Metrics {
		fmt.Fprintf(stdout, "\n%s (n=%d", metric.Name, metric.SampleCount)
		if metric.ExcludedCount > 0 {
			fmt.Fprintf(stdout, ", excluded=%d", metric.ExcludedCount)
		}
		fmt.Fprintln(stdout, ")")
		maxPercent := 0.0
		for _, bucket := range metric.Buckets {
			if bucket.Percent > maxPercent {
				maxPercent = bucket.Percent
			}
		}
		for _, bucket := range metric.Buckets {
			if bucket.Count == 0 {
				continue
			}
			barLength := int(math.Round(bucket.Percent / maxPercent * 40))
			if barLength < 1 {
				barLength = 1
			}
			fmt.Fprintf(stdout, "  %d-%d%% %5d %6.1f%% %s\n", bucket.LowerPercent, bucket.UpperPercent, bucket.Count, bucket.Percent, strings.Repeat("#", barLength))
		}
	}
	return nil
}

func writeMarkdownChart(stdout io.Writer, report chartReport) error {
	fmt.Fprintf(stdout, "# Active-cluster score histograms\n\nScope: %s  \nActive clusters: %d\n", report.Scope, report.ActiveClusterCount)
	for _, metric := range report.Metrics {
		fmt.Fprintf(stdout, "\n## %s (n=%d)\n\n| Bucket | Count | Percent | Histogram |\n|:--|--:|--:|:--|\n", metric.Name, metric.SampleCount)
		for _, bucket := range metric.Buckets {
			if bucket.Count == 0 {
				continue
			}
			barLength := int(math.Round(bucket.Percent / 2))
			if barLength < 1 {
				barLength = 1
			}
			fmt.Fprintf(stdout, "| %d-%d%% | %d | %.1f%% | `%s` |\n", bucket.LowerPercent, bucket.UpperPercent, bucket.Count, bucket.Percent, strings.Repeat("#", barLength))
		}
	}
	return nil
}
