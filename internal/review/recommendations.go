package review

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"time"

	"github.com/weisnobody/whothatface/internal/id"
)

const MaxReferenceRecommendations = 20

type ReferenceCandidate struct {
	FaceID        string
	Quality       float32
	Detection     float32
	Similarity    float32
	Margin        float32
	Support       int
	Purity        float32
	Connectivity  float32
	Distance      float32
	DiversityNote string
	DuplicateNote string
}

type Recommendation struct {
	ID       string
	FaceID   string
	PersonID string
	Score    float32
}

// RankRecommendations scores candidates using evidence quality and a modest
// diversity bonus. The caller supplies only eligible assigned faces, including
// assigned-user and assigned-system faces and assigned don't-track faces.
func RankRecommendations(candidates []ReferenceCandidate, limit int) []ReferenceCandidate {
	if limit <= 0 || limit > MaxReferenceRecommendations {
		limit = MaxReferenceRecommendations
	}
	result := append([]ReferenceCandidate(nil), candidates...)
	sort.SliceStable(result, func(left, right int) bool {
		return recommendationScore(result[left]) > recommendationScore(result[right])
	})
	// A face can be encountered more than once when recommendation metadata
	// comes from historical relationships. Recommendations are face-level, so
	// retain only the highest-scoring occurrence before assigning the limit.
	unique := result[:0]
	seenFaces := make(map[string]struct{}, len(result))
	for _, candidate := range result {
		if _, exists := seenFaces[candidate.FaceID]; exists {
			continue
		}
		seenFaces[candidate.FaceID] = struct{}{}
		unique = append(unique, candidate)
	}
	result = unique
	if len(result) > limit {
		result = result[:limit]
	}
	return result
}

// GenerateRecommendations archives the previous recommendation set and stores
// the new run as the current set. Every generated row remains auditable.
func (s Store) GenerateRecommendations(ctx context.Context, personID string, candidates []ReferenceCandidate) ([]Recommendation, error) {
	runID, err := id.NewUUIDv7()
	if err != nil {
		return nil, err
	}
	selected := RankRecommendations(candidates, MaxReferenceRecommendations)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE reference_recommendations SET is_current = 0 WHERE person_id = ? AND is_current = 1`, personID); err != nil {
		return nil, err
	}
	result := make([]Recommendation, 0, len(selected))
	for _, candidate := range selected {
		recommendationID, err := id.NewUUIDv7()
		if err != nil {
			return nil, err
		}
		score := recommendationScore(candidate)
		_, err = tx.ExecContext(ctx, `
			INSERT INTO reference_recommendations (
				id, face_id, person_id, recommendation_run_id, is_current,
				recommendation_score, quality, detection_confidence, similarity,
				margin, support_count, purity, connectivity, diversity_reason,
				duplicate_reason, created_at
			) VALUES (?, ?, ?, ?, 1, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, recommendationID,
			candidate.FaceID, personID, runID, score, candidate.Quality, candidate.Detection,
			candidate.Similarity, candidate.Margin, candidate.Support, candidate.Purity,
			candidate.Connectivity, candidate.DiversityNote, candidate.DuplicateNote, now)
		if err != nil {
			return nil, fmt.Errorf("persist recommendation: %w", err)
		}
		result = append(result, Recommendation{ID: recommendationID, FaceID: candidate.FaceID, PersonID: personID, Score: score})
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return result, nil
}

func (s Store) CurrentRecommendations(ctx context.Context, personID string) ([]Recommendation, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, face_id, person_id, recommendation_score FROM reference_recommendations WHERE person_id = ? AND is_current = 1 ORDER BY recommendation_score DESC`, personID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []Recommendation
	for rows.Next() {
		var value Recommendation
		var score sql.NullFloat64
		if err := rows.Scan(&value.ID, &value.FaceID, &value.PersonID, &score); err != nil {
			return nil, err
		}
		if score.Valid {
			value.Score = float32(score.Float64)
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

func recommendationScore(candidate ReferenceCandidate) float32 {
	return 0.25*clamp(candidate.Quality) +
		0.20*clamp(candidate.Detection) +
		0.20*clamp(candidate.Similarity) +
		0.10*clamp(candidate.Margin) +
		0.10*clamp(float32(candidate.Support)/10) +
		0.10*clamp(candidate.Purity) +
		0.05*clamp(candidate.Connectivity) +
		0.05*clamp(candidate.Distance)
}

func clamp(value float32) float32 {
	if value < 0 {
		return 0
	}
	if value > 1 {
		return 1
	}
	return value
}
