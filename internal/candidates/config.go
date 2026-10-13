// Package candidates finds possible person matches without changing identity
// assignments or cluster membership.
package candidates

type Config struct {
	MaxProvisionalFaces   int     `json:"max_provisional_faces"`
	MaxCandidates         int     `json:"max_candidates"`
	MinSimilarity         float32 `json:"min_similarity"`
	IncludeAssignedSystem bool    `json:"include_assigned_system"`
	IncludeDontTrack      bool    `json:"include_dont_track"`
}

func DefaultConfig() Config {
	return Config{
		MaxProvisionalFaces: 5,
		MaxCandidates:       5,
		MinSimilarity:       0.25,
	}
}
