package review

import "testing"

func TestRankRecommendationsLimitsToTwentyAndPrefersEvidence(t *testing.T) {
	var candidates []ReferenceCandidate
	for index := 0; index < 25; index++ {
		value := float32(index) / 25
		candidates = append(candidates, ReferenceCandidate{FaceID: string(rune('a' + index)), Quality: value, Detection: value, Similarity: value, Purity: value, Connectivity: value})
	}
	ranked := RankRecommendations(candidates, MaxReferenceRecommendations)
	if len(ranked) != MaxReferenceRecommendations {
		t.Fatalf("got %d recommendations", len(ranked))
	}
	if ranked[0].Quality <= ranked[len(ranked)-1].Quality {
		t.Errorf("ranking did not prefer evidence: %+v", ranked)
	}
}

func TestRankRecommendationsDeduplicatesFaces(t *testing.T) {
	ranked := RankRecommendations([]ReferenceCandidate{
		{FaceID: "duplicate", Quality: 0.2, Detection: 0.2},
		{FaceID: "duplicate", Quality: 0.9, Detection: 0.9},
		{FaceID: "other", Quality: 0.5, Detection: 0.5},
	}, MaxReferenceRecommendations)
	if len(ranked) != 2 {
		t.Fatalf("got %d recommendations, want 2", len(ranked))
	}
	if ranked[0].FaceID != "duplicate" || ranked[0].Quality != 0.9 {
		t.Fatalf("duplicate face was not represented by its highest-scoring candidate: %+v", ranked)
	}
}
