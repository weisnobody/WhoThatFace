package worker

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

const integrationWorker = `import json, sys, time
mode = sys.argv[1]
count = 0
for line in sys.stdin:
    message = json.loads(line)
    if message.get("command") == "hello":
        print(json.dumps({"protocol_version": 1, "id": message["id"], "command": "hello_ack", "ok": True, "capabilities": {"worker_version": "test", "python_version": "3.11", "insightface_version": "test", "providers": ["CPUExecutionProvider"], "models": ["buffalo_l"], "embedding_dimension": 512, "embedding_dtype": "float32", "embedding_metric": "cosine", "modules": ["detection", "recognition"], "preprocessing": "test"}}), flush=True)
    elif message.get("command") == "analyze":
        count += 1
        if mode == "malformed":
            print("not-json", flush=True)
        elif mode == "sleep":
            time.sleep(10)
        else:
            print(json.dumps({"protocol_version": 1, "id": message["id"], "ok": True, "image": {"width": 10, "height": 10}, "faces": []}), flush=True)
`

func integrationClient(t *testing.T, mode string) (*Client, string) {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, "worker.py")
	if err := os.WriteFile(script, []byte(integrationWorker), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "photo.jpg")
	if err := os.WriteFile(path, []byte("test-photo"), 0o600); err != nil {
		t.Fatal(err)
	}
	client := NewClient("python3", script, mode)
	if err := client.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client, path
}

func TestClientSerializesConcurrentRequests(t *testing.T) {
	client, path := integrationClient(t, "normal")
	var group sync.WaitGroup
	errors := make(chan error, 2)
	for range 2 {
		group.Add(1)
		go func() {
			defer group.Done()
			_, err := client.Analyze(context.Background(), path, "pipeline-1", 1000)
			errors <- err
		}()
	}
	group.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestClientRejectsChangedFileBeforeRequest(t *testing.T) {
	client, path := integrationClient(t, "normal")
	identity, err := inspectFileIdentity(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("changed-photo"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := client.AnalyzeWithIdentity(context.Background(), path, "pipeline-1", 1000, identity.sha256, identity.size); err == nil {
		t.Fatal("expected changed-file identity error")
	}
}

func TestClientRestartsAfterMalformedWorkerOutput(t *testing.T) {
	client, path := integrationClient(t, "malformed")
	if _, err := client.Analyze(context.Background(), path, "pipeline-1", 1000); err == nil {
		t.Fatal("expected malformed response error")
	}
}

func TestClientRestartsOnCancellation(t *testing.T) {
	client, path := integrationClient(t, "sleep")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := client.Analyze(ctx, path, "pipeline-1", 1000); err == nil {
		t.Fatal("expected cancellation error")
	}
}

func TestClientEnforcesRequestTimeout(t *testing.T) {
	client, path := integrationClient(t, "sleep")
	client.RequestTimeout = 20 * time.Millisecond
	started := time.Now()
	if _, err := client.Analyze(context.Background(), path, "pipeline-1", 1000); err == nil {
		t.Fatal("expected request timeout")
	} else if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("request timeout took %s", elapsed)
	}
}

func TestValidateCapabilitiesAgainstPipeline(t *testing.T) {
	capabilities := &Capabilities{
		InsightFaceVersion: "2.0.0",
		Providers:          []string{"CPUExecutionProvider"},
		Models:             []string{"buffalo_l"},
		EmbeddingDimension: 512,
		EmbeddingDType:     "float32",
		EmbeddingMetric:    "cosine",
		ModelChecksums:     map[string]string{"model.onnx": "abc"},
		Modules:            []string{"detection", "recognition"},
		Preprocessing:      "pillow-exif-transpose-opencv-bgr-v1",
	}
	err := validateCapabilities(capabilities, &Expectation{
		Model:              "buffalo_l",
		Provider:           "CPUExecutionProvider",
		ModelChecksums:     map[string]string{"model.onnx": "abc"},
		EmbeddingDimension: 512,
		EmbeddingDType:     "float32",
		EmbeddingMetric:    "cosine",
		Modules:            []string{"detection", "recognition"},
		Preprocessing:      "pillow-exif-transpose-opencv-bgr-v1",
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestValidateCapabilitiesRejectsChecksumMismatch(t *testing.T) {
	capabilities := &Capabilities{
		InsightFaceVersion: "2.0.0", Providers: []string{"CPUExecutionProvider"},
		Models: []string{"buffalo_l"}, EmbeddingDimension: 512,
		EmbeddingDType: "float32", EmbeddingMetric: "cosine",
		ModelChecksums: map[string]string{"model.onnx": "actual"},
		Modules:        []string{"detection", "recognition"}, Preprocessing: "pillow-exif-transpose-opencv-bgr-v1",
	}
	if err := validateCapabilities(capabilities, &Expectation{Model: "buffalo_l", ModelChecksums: map[string]string{"model.onnx": "expected"}}); err == nil {
		t.Fatal("expected checksum mismatch")
	}
}

func TestValidateCapabilitiesRejectsMissingAnalyzeCommand(t *testing.T) {
	capabilities := &Capabilities{InsightFaceVersion: "2.0.0", Providers: []string{"CPUExecutionProvider"}, Models: []string{"buffalo_l"}, EmbeddingDimension: 512, EmbeddingDType: "float32", EmbeddingMetric: "cosine", Modules: []string{"detection", "recognition"}, Preprocessing: "test", SupportedCommands: []string{"hello"}}
	if err := validateCapabilities(capabilities, nil); err == nil {
		t.Fatal("expected missing analyze command error")
	}
}
