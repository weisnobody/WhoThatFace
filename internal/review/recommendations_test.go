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
