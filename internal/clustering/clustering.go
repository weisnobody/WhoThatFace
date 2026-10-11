// Package clustering implements the MVP exact mutual-kNN face grouping
// algorithm. Its neighbor-search interface can later be replaced by ANN.
package clustering

import (
	"context"
	"fmt"
	"math"
	"sort"
)

type Face struct {
	ID        string
	Embedding []float32
	PersonID  string
}

type Config struct {
	K                    int
	SimilarityThreshold  float32
	AlgorithmVersion     string
	NeighborSearchMethod string
	CandidateScope       string
}

func DefaultConfig() Config {
	return Config{K: 40, SimilarityThreshold: 0.45, AlgorithmVersion: "mutual-knn-v1", NeighborSearchMethod: "exact", CandidateScope: "all"}
}

type Neighbor struct {
	FaceID     string
	Similarity float32
}

type Edge struct {
	LeftID     string
	RightID    string
	Similarity float32
}

type Cluster struct {
	Key              string
	FaceIDs          []string
	ProposedPersonID string
	Purity           float32
	Connectivity     float32
	Mixed            bool
}

type Result struct {
	Clusters []Cluster
	Edges    []Edge
}

// CalculatePurity implements the grouping benchmark definition: the largest
// manually identified person count divided by all manually identified faces.
// Unidentified faces do not contribute to either side of the ratio.
func CalculatePurity(personIDs []string) float32 {
	counts := make(map[string]int)
	known := 0
	for _, personID := range personIDs {
		if personID == "" {
			continue
		}
		counts[personID]++
		known++
	}
	if known == 0 {
		return 0
	}
	best := 0
	for _, count := range counts {
		if count > best {
			best = count
		}
	}
	return float32(best) / float32(known)
}

// CalculateConnectivity is the normalized spanning-edge measure used by the
// grouping benchmark. A connected n-face cluster needs n-1 internal edges;
// singleton clusters are therefore fully connected.
func CalculateConnectivity(faceCount, internalEdges int) float32 {
	if faceCount <= 1 {
		return 1
	}
	value := float32(internalEdges) / float32(faceCount-1)
	if value > 1 {
		return 1
	}
	if value < 0 {
		return 0
	}
	return value
}

// Group constructs mutual-kNN edges above the configured threshold and
// returns connected components. Every input face appears in exactly one
// cluster, including isolated singleton faces.
func Group(faces []Face, config Config) (Result, error) {
	return GroupContext(context.Background(), faces, config)
}

func GroupContext(ctx context.Context, faces []Face, config Config) (Result, error) {
	if config.K <= 0 {
		return Result{}, fmt.Errorf("k must be positive")
	}
	if config.SimilarityThreshold < 0 || config.SimilarityThreshold > 1 {
		return Result{}, fmt.Errorf("similarity threshold must be between 0 and 1")
	}
	if config.NeighborSearchMethod == "" {
		config.NeighborSearchMethod = "exact"
	}
	if config.NeighborSearchMethod != "exact" && config.NeighborSearchMethod != "ann" {
		return Result{}, fmt.Errorf("neighbor search method must be exact or ann")
	}
	for index := range faces {
		select {
		case <-ctx.Done():
			return Result{}, ctx.Err()
		default:
		}
		if faces[index].ID == "" || len(faces[index].Embedding) == 0 {
			return Result{}, fmt.Errorf("face %d has an empty ID or embedding", index)
		}
		if index > 0 && len(faces[index].Embedding) != len(faces[0].Embedding) {
			return Result{}, fmt.Errorf("face %q has an embedding dimension mismatch", faces[index].ID)
		}
	}
	neighbors := make([][]Neighbor, len(faces))
	for index, face := range faces {
		candidates := make([]int, len(faces))
		for candidateIndex := range candidates {
			candidates[candidateIndex] = candidateIndex
		}
		if config.NeighborSearchMethod == "ann" {
			candidates = approximateCandidates(faces, index)
		}
		for _, otherIndex := range candidates {
			other := faces[otherIndex]
			if index == otherIndex {
				continue
			}
			similarity := cosine(face.Embedding, other.Embedding)
			if similarity >= config.SimilarityThreshold {
				neighbors[index] = append(neighbors[index], Neighbor{FaceID: other.ID, Similarity: similarity})
			}
		}
		sort.Slice(neighbors[index], func(left, right int) bool {
			if neighbors[index][left].Similarity == neighbors[index][right].Similarity {
				return neighbors[index][left].FaceID < neighbors[index][right].FaceID
			}
			return neighbors[index][left].Similarity > neighbors[index][right].Similarity
		})
		if len(neighbors[index]) > config.K {
			neighbors[index] = neighbors[index][:config.K]
		}
	}

	union := newDisjointSet(len(faces))
	indices := make(map[string]int, len(faces))
	for index, face := range faces {
		indices[face.ID] = index
	}
	var edges []Edge
	for index, list := range neighbors {
		select {
		case <-ctx.Done():
			return Result{}, ctx.Err()
		default:
		}
		for _, neighbor := range list {
			otherIndex := indices[neighbor.FaceID]
			if !containsNeighbor(neighbors[otherIndex], faces[index].ID) || faces[index].ID >= neighbor.FaceID {
				continue
			}
			union.join(index, otherIndex)
			edges = append(edges, Edge{LeftID: faces[index].ID, RightID: neighbor.FaceID, Similarity: neighbor.Similarity})
		}
	}

	groups := make(map[int][]int)
	for index := range faces {
		root := union.find(index)
		groups[root] = append(groups[root], index)
	}
	clusters := make([]Cluster, 0, len(groups))
	for _, members := range groups {
		sort.Slice(members, func(left, right int) bool { return faces[members[left]].ID < faces[members[right]].ID })
		cluster := Cluster{Key: faces[members[0]].ID, FaceIDs: make([]string, 0, len(members))}
		people := make([]string, 0, len(members))
		for _, member := range members {
			cluster.FaceIDs = append(cluster.FaceIDs, faces[member].ID)
			people = append(people, faces[member].PersonID)
		}
		cluster.Purity = CalculatePurity(people)
		knownPeople := make(map[string]struct{})
		for _, personID := range people {
			if personID != "" {
				knownPeople[personID] = struct{}{}
			}
		}
		cluster.Mixed = len(knownPeople) > 1
		internalEdges := countInternalEdges(edges, cluster.FaceIDs)
		cluster.Connectivity = CalculateConnectivity(len(cluster.FaceIDs), internalEdges)
		clusters = append(clusters, cluster)
	}
	sort.Slice(clusters, func(left, right int) bool { return clusters[left].Key < clusters[right].Key })
	return Result{Clusters: clusters, Edges: edges}, nil
}

// approximateCandidates is a deterministic, dependency-free ANN prototype.
// It uses a small sign-projection index and neighboring buckets. The benchmark
// tool compares its recall with exact search before it is used for production
// defaults.
func approximateCandidates(faces []Face, query int) []int {
	const bits = 4
	querySignature := signature(faces[query].Embedding)
	buckets := make(map[uint8][]int)
	for index, face := range faces {
		value := signature(face.Embedding)
		buckets[value] = append(buckets[value], index)
	}
	result := make([]int, 0)
	seen := make(map[int]struct{})
	for distance := 0; distance <= 2; distance++ {
		for mask := 0; mask < 1<<bits; mask++ {
			if bitsSet(uint8(mask)) != distance {
				continue
			}
			for _, index := range buckets[querySignature^uint8(mask)] {
				if _, exists := seen[index]; !exists {
					seen[index] = struct{}{}
					result = append(result, index)
				}
			}
		}
	}
	return result
}

func signature(values []float32) uint8 {
	var result uint8
	for index := 0; index < 8; index++ {
		if index < len(values) && values[index] >= 0 {
			result |= 1 << index
		}
	}
	return result
}

func bitsSet(value uint8) int {
	count := 0
	for value != 0 {
		count += int(value & 1)
		value >>= 1
	}
	return count
}

func containsNeighbor(neighbors []Neighbor, id string) bool {
	for _, neighbor := range neighbors {
		if neighbor.FaceID == id {
			return true
		}
	}
	return false
}

func countInternalEdges(edges []Edge, ids []string) int {
	set := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		set[id] = struct{}{}
	}
	count := 0
	for _, edge := range edges {
		if _, ok := set[edge.LeftID]; ok {
			if _, ok := set[edge.RightID]; ok {
				count++
			}
		}
	}
	return count
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

type disjointSet struct{ parents, sizes []int }

func newDisjointSet(size int) *disjointSet {
	parents := make([]int, size)
	sizes := make([]int, size)
	for index := range parents {
		parents[index], sizes[index] = index, 1
	}
	return &disjointSet{parents: parents, sizes: sizes}
}

func (set *disjointSet) find(value int) int {
	if set.parents[value] != value {
		set.parents[value] = set.find(set.parents[value])
	}
	return set.parents[value]
}

func (set *disjointSet) join(left, right int) {
	left, right = set.find(left), set.find(right)
	if left == right {
		return
	}
	if set.sizes[left] < set.sizes[right] {
		left, right = right, left
	}
	set.parents[right] = left
	set.sizes[left] += set.sizes[right]
}
