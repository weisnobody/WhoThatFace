package recognition

import "testing"

func TestDefaultThresholdsDirectAssignmentSimilarity(t *testing.T) {
	if got := DefaultThresholds().DirectAssignmentSimilarity; got != 0.75 {
		t.Fatalf("direct assignment similarity = %v, want 0.75", got)
	}
}

func TestClassifyStandaloneIgnoresClusterMetrics(t *testing.T) {
	candidate := Candidate{Similarity: .8, Margin: .1, Support: 6, Quality: .8, Detection: .8, Purity: 0, Connectivity: 0}
	if got := ClassifyStandalone(candidate, DefaultThresholds()); got != EvidenceHigh {
		t.Fatalf("level = %q", got)
	}
}
