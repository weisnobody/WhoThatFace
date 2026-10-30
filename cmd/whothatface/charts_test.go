package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestMakeChartMetricUsesFivePercentBuckets(t *testing.T) {
	metric := makeChartMetric("similarity", chartSeries{Population: 4, Values: []float64{0.05, 0.249, 0.95, 1.0}})
	if metric.SampleCount != 4 || metric.ExcludedCount != 0 {
		t.Fatalf("counts = %d/%d, want 4/0", metric.SampleCount, metric.ExcludedCount)
	}
	for _, want := range []struct {
		index int
		count int
	}{
		{1, 1}, {4, 1}, {19, 2},
	} {
		if metric.Buckets[want.index].Count != want.count {
			t.Errorf("bucket %d count = %d, want %d", want.index, metric.Buckets[want.index].Count, want.count)
		}
	}
}

func TestWriteTextChartOmitsEmptyBuckets(t *testing.T) {
	var output bytes.Buffer
	report := chartReport{Scope: "all faces in active clusters", ActiveClusterCount: 1, Metrics: []chartMetric{makeChartMetric("quality", chartSeries{Population: 1, Values: []float64{0.8}})}}
	if err := writeTextChart(&output, report); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "80-85") || strings.Contains(output.String(), "75-80") {
		t.Fatalf("unexpected chart output: %s", output.String())
	}
}

func TestMakeChartMetricRangeFocusesOnLowQualityTail(t *testing.T) {
	metric := makeChartMetricRange("quality (0-15%)", chartSeries{Population: 3, Values: []float64{0.001, 0.149, 0.20}}, 15, 1)
	if metric.SampleCount != 2 || metric.ExcludedCount != 1 {
		t.Fatalf("counts = %d/%d, want 2/1", metric.SampleCount, metric.ExcludedCount)
	}
	if metric.Buckets[0].Count != 1 || metric.Buckets[14].Count != 1 {
		t.Fatalf("low-quality buckets = %d/%d, want 1/1", metric.Buckets[0].Count, metric.Buckets[14].Count)
	}
}
