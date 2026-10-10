package recognition

import "testing"

func TestClassifyStandaloneIgnoresClusterMetrics(t *testing.T) {
	candidate := Candidate{Similarity: .8, Margin: .1, Support: 6, Quality: .8, Detection: .8, Purity: 0, Connectivity: 0}
	if got := ClassifyStandalone(candidate, DefaultThresholds()); got != EvidenceHigh {
		t.Fatalf("level = %q", got)
	}
}
