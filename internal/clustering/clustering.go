// Package clustering implements the MVP exact mutual-kNN face grouping
// algorithm. Its neighbor-search interface can later be replaced by ANN.
package clustering

import (
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
	K                   int
	SimilarityThreshold float32
	AlgorithmVersion    string
}

func DefaultConfig() Config {
	return Config{K: 40, SimilarityThreshold: 0.45, AlgorithmVersion: "mutual-knn-v1"}
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
	Key          string
	FaceIDs      []string
	Purity       float32
	Connectivity float32
	Mixed        bool
}

type Result struct {
	Clusters []Cluster
	Edges    []Edge
}

// Group constructs mutual-kNN edges above the configured threshold and
// returns connected components. Every input face appears in exactly one
// cluster, including isolated singleton faces.
func Group(faces []Face, config Config) (Result, error) {
	if config.K <= 0 {
		return Result{}, fmt.Errorf("k must be positive")
	}
	if config.SimilarityThreshold < 0 || config.SimilarityThreshold > 1 {
		return Result{}, fmt.Errorf("similarity threshold must be between 0 and 1")
	}
	for index := range faces {
		if faces[index].ID == "" || len(faces[index].Embedding) == 0 {
			return Result{}, fmt.Errorf("face %d has an empty ID or embedding", index)
		}
		if index > 0 && len(faces[index].Embedding) != len(faces[0].Embedding) {
			return Result{}, fmt.Errorf("face %q has an embedding dimension mismatch", faces[index].ID)
		}
	}
	neighbors := make([][]Neighbor, len(faces))
	for index, face := range faces {
		for otherIndex, other := range faces {
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
		people := make(map[string]int)
		knownFaces := 0
		for _, member := range members {
			cluster.FaceIDs = append(cluster.FaceIDs, faces[member].ID)
			if faces[member].PersonID != "" {
				people[faces[member].PersonID]++
				knownFaces++
			}
		}
		if len(people) > 0 {
			maxCount := 0
			for _, count := range people {
				if count > maxCount {
					maxCount = count
				}
			}
			cluster.Purity = float32(maxCount) / float32(knownFaces)
			cluster.Mixed = len(people) > 1
		}
		internalEdges := countInternalEdges(edges, cluster.FaceIDs)
		if len(cluster.FaceIDs) > 1 {
			cluster.Connectivity = float32(internalEdges) / float32(len(cluster.FaceIDs)-1)
			if cluster.Connectivity > 1 {
				cluster.Connectivity = 1
			}
		} else {
			cluster.Connectivity = 1
		}
		clusters = append(clusters, cluster)
	}
	sort.Slice(clusters, func(left, right int) bool { return clusters[left].Key < clusters[right].Key })
	return Result{Clusters: clusters, Edges: edges}, nil
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
