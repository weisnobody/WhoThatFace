package clustering

import "testing"

func TestGroupBuildsMutualKNNComponents(t *testing.T) {
	faces := []Face{
		{ID: "a", Embedding: []float32{1, 0}, PersonID: "alice"},
		{ID: "b", Embedding: []float32{0.99, 0.1}, PersonID: "alice"},
		{ID: "c", Embedding: []float32{0, 1}, PersonID: "bob"},
	}
	result, err := Group(faces, Config{K: 1, SimilarityThreshold: 0.9})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Clusters) != 2 || len(result.Edges) != 1 {
		t.Fatalf("result = %+v", result)
	}
	if len(result.Clusters[0].FaceIDs) != 2 {
		t.Errorf("first cluster = %+v", result.Clusters[0])
	}
	if result.Clusters[0].Purity != 1 {
		t.Errorf("purity = %v", result.Clusters[0].Purity)
	}
}

func TestGroupReportsMixedCluster(t *testing.T) {
	faces := []Face{
		{ID: "a", Embedding: []float32{1, 0}, PersonID: "alice"},
		{ID: "b", Embedding: []float32{0.99, 0.1}, PersonID: "bob"},
	}
	result, err := Group(faces, Config{K: 1, SimilarityThreshold: 0.9})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Clusters[0].Mixed || result.Clusters[0].Purity != 0.5 {
		t.Errorf("cluster = %+v", result.Clusters[0])
	}
}
