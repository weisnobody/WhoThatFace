package recognition

import "testing"

func TestMatchSortsCandidatesAndComputesMargin(t *testing.T) {
	candidates, err := Match([]float32{1, 0}, map[string][][]float32{
		"alice": {{1, 0}, {0.8, 0.2}},
		"bob":   {{0.8, 0.6}},
	}, 0.8, 0.9)
	if err != nil {
		t.Fatal(err)
	}
	if candidates[0].PersonID != "alice" || candidates[0].Support != 2 {
		t.Fatalf("candidates = %+v", candidates)
	}
	if candidates[0].Margin <= 0 {
		t.Errorf("margin = %v", candidates[0].Margin)
	}
}

func TestMatchUsesDeterministicTieBreakers(t *testing.T) {
	candidates, err := Match([]float32{1, 0}, map[string][][]float32{
		"zulu":  {{1, 0}},
		"alpha": {{1, 0}},
	}, 0.8, 0.9)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 2 || candidates[0].PersonID != "alpha" || candidates[1].PersonID != "zulu" {
		t.Fatalf("tie order = %+v", candidates)
	}
}

func TestClassifyRequiresAllHighEvidence(t *testing.T) {
	thresholds := DefaultThresholds()
	candidate := Candidate{Similarity: 0.8, Margin: 0.1, Support: 6, Quality: 0.8, Detection: 0.8, Purity: 0.98, Connectivity: 0.98, References: true, Agreement: true}
	if got := Classify(candidate, thresholds); got != EvidenceHigh {
		t.Fatalf("Classify() = %q, want high", got)
	}
	candidate.Quality = 0.6
	if got := Classify(candidate, thresholds); got != EvidenceMedium {
		t.Fatalf("Classify() with medium quality = %q, want medium", got)
	}
}

func TestClassifyLowSimilarityRequiresManualReview(t *testing.T) {
	candidate := Candidate{Similarity: 0.24, References: true, Agreement: true}
	if got := Classify(candidate, DefaultThresholds()); got != EvidenceManual {
		t.Fatalf("Classify() = %q, want manual-review", got)
	}
}
