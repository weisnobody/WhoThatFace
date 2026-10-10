// Package recognition evaluates face embeddings against approved references.
package recognition

import (
	"fmt"
	"math"
	"sort"
)

type EvidenceLevel string

const (
	EvidenceHigh   EvidenceLevel = "high"
	EvidenceMedium EvidenceLevel = "medium"
	EvidenceLow    EvidenceLevel = "low"
	EvidenceManual EvidenceLevel = "manual-review"
)

type Thresholds struct {
	HighSimilarity     float32
	MediumSimilarity   float32
	LowSimilarity      float32
	Margin             float32
	HighSupport        int
	MediumSupport      int
	HighQuality        float32
	MediumQuality      float32
	HighDetection      float32
	MediumDetection    float32
	HighPurity         float32
	MediumPurity       float32
	HighConnectivity   float32
	MediumConnectivity float32
}

// DefaultThresholds are the production evidence thresholds from the spec.
func DefaultThresholds() Thresholds {
	return Thresholds{
		HighSimilarity: 0.50, MediumSimilarity: 0.40, LowSimilarity: 0.25,
		Margin: 0.05, HighSupport: 5, MediumSupport: 3,
		HighQuality: 0.75, MediumQuality: 0.60,
		HighDetection: 0.75, MediumDetection: 0.60,
		HighPurity: 0.95, MediumPurity: 0.90,
		HighConnectivity: 0.95, MediumConnectivity: 0.90,
	}
}

type Candidate struct {
	PersonID     string
	Similarity   float32
	Margin       float32
	Support      int
	Quality      float32
	Detection    float32
	Purity       float32
	Connectivity float32
	References   bool
	Agreement    bool
	Evidence     EvidenceLevel
}

// Match computes the strongest cosine similarity for each person, then sets
// margin against the second-best person candidate.
func Match(query []float32, references map[string][][]float32, quality, detection float32) ([]Candidate, error) {
	if len(query) == 0 {
		return nil, fmt.Errorf("query embedding is empty")
	}
	var candidates []Candidate
	for personID, embeddings := range references {
		best := float32(-1)
		for _, embedding := range embeddings {
			if len(embedding) != len(query) {
				return nil, fmt.Errorf("embedding dimension mismatch for person %q", personID)
			}
			similarity := cosine(query, embedding)
			if similarity > best {
				best = similarity
			}
		}
		if len(embeddings) > 0 {
			candidates = append(candidates, Candidate{PersonID: personID, Similarity: best, Support: len(embeddings), Quality: quality, Detection: detection, References: true})
		}
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].Similarity > candidates[j].Similarity })
	for index := range candidates {
		if index+1 < len(candidates) {
			candidates[index].Margin = candidates[index].Similarity - candidates[index+1].Similarity
		} else {
			candidates[index].Margin = candidates[index].Similarity
		}
	}
	return candidates, nil
}

// Classify assigns an evidence category to a candidate. Missing metrics are
// represented by zero values. Agreement and references are not required for
// medium or low evidence, matching the defined "either" rules.
func Classify(candidate Candidate, thresholds Thresholds) EvidenceLevel {
	if candidate.Similarity < thresholds.LowSimilarity {
		return EvidenceManual
	}
	level := EvidenceHigh
	level = lower(level, levelFor(candidate.Similarity, thresholds.HighSimilarity, thresholds.MediumSimilarity, thresholds.LowSimilarity))
	level = lower(level, levelFor(candidate.Margin, thresholds.Margin, thresholds.Margin, thresholds.Margin))
	level = lower(level, levelForInt(candidate.Support, thresholds.HighSupport, thresholds.MediumSupport))
	level = lower(level, levelFor(candidate.Quality, thresholds.HighQuality, thresholds.MediumQuality, 0))
	level = lower(level, levelFor(candidate.Detection, thresholds.HighDetection, thresholds.MediumDetection, 0))
	level = lower(level, levelFor(candidate.Purity, thresholds.HighPurity, thresholds.MediumPurity, 0))
	level = lower(level, levelFor(candidate.Connectivity, thresholds.HighConnectivity, thresholds.MediumConnectivity, 0))
	if !candidate.References || !candidate.Agreement {
		level = lower(level, EvidenceMedium)
	}
	return level
}

func cosine(left, right []float32) float32 {
	var dot, leftNorm, rightNorm float64
	for index := range left {
		a, b := float64(left[index]), float64(right[index])
		dot += a * b
		leftNorm += a * a
		rightNorm += b * b
	}
	if leftNorm == 0 || rightNorm == 0 {
		return 0
	}
	return float32(dot / (math.Sqrt(leftNorm) * math.Sqrt(rightNorm)))
}

func levelFor(value, high, medium, low float32) EvidenceLevel {
	if value >= high {
		return EvidenceHigh
	}
	if value >= medium {
		return EvidenceMedium
	}
	if value >= low {
		return EvidenceLow
	}
	return EvidenceManual
}

func levelForInt(value, high, medium int) EvidenceLevel {
	if value >= high {
		return EvidenceHigh
	}
	if value >= medium {
		return EvidenceMedium
	}
	return EvidenceLow
}

func lower(left, right EvidenceLevel) EvidenceLevel {
	if rank(right) < rank(left) {
		return right
	}
	return left
}

func rank(level EvidenceLevel) int {
	switch level {
	case EvidenceHigh:
		return 3
	case EvidenceMedium:
		return 2
	case EvidenceLow:
		return 1
	default:
		return 0
	}
}
