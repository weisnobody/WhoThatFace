// Package candidates finds possible person matches without changing identity
// assignments or cluster membership.
package candidates

type Config struct {
	MaxProvisionalFaces int     `json:"max_provisional_faces"`
	MaxTemporaryFaces   int     `json:"max_temporary_faces"`
	MaxCandidates       int     `json:"max_candidates"`
	MinSimilarity       float32 `json:"min_similarity"`
	IncludeDontTrack    bool    `json:"include_dont_track"`
}

func DefaultConfig() Config {
	return Config{
		MaxProvisionalFaces: 5,
		MaxTemporaryFaces:   50,
		MaxCandidates:       5,
		MinSimilarity:       0.25,
	}
}
