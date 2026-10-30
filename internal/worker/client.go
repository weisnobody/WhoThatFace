package worker

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/weisnobody/whothatface/internal/id"
)

// Client manages one persistent worker process. Calls are serialized because
// the MVP worker owns one loaded model and processes one image at a time.
type Client struct {
	command        string
	args           []string
	mu             sync.Mutex
	process        *exec.Cmd
	input          io.WriteCloser
	output         *json.Decoder
	ack            Message
	minor          int
	expectation    *Expectation
	RequestTimeout time.Duration
}

// NewClient creates a client; it does not start the process until Start.
func NewClient(command string, args ...string) *Client {
	return &Client{command: command, args: args, RequestTimeout: 5 * time.Minute}
}

// NewClientForPipeline creates a client that rejects workers which do not
// advertise the requested model and preprocessing contract.
func NewClientForPipeline(command string, expectation Expectation, args ...string) *Client {
	return &Client{command: command, args: args, expectation: &expectation, RequestTimeout: 5 * time.Minute}
}

// Start launches the process and negotiates the worker protocol.
func (c *Client) Start(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.startLocked(ctx)
}

func (c *Client) startLocked(ctx context.Context) error {
	if c.process != nil {
		return fmt.Errorf("worker is already running")
	}
	process := exec.CommandContext(ctx, c.command, c.args...)
	input, err := process.StdinPipe()
	if err != nil {
		return fmt.Errorf("create worker stdin: %w", err)
	}
	output, err := process.StdoutPipe()
	if err != nil {
		input.Close()
		return fmt.Errorf("create worker stdout: %w", err)
	}
	if err := process.Start(); err != nil {
		input.Close()
		return fmt.Errorf("start worker: %w", err)
	}
	c.process = process
	c.input = input
	c.output = json.NewDecoder(bufio.NewReaderSize(output, 10*1024*1024))
	requestID, err := id.NewUUIDv7()
	if err != nil {
		c.stopLocked()
		return err
	}
	if err := c.sendLocked(Message{ProtocolVersion: ProtocolMajor, ProtocolMajor: ProtocolMajor, ProtocolMinor: ProtocolMinor, Command: "hello", ID: requestID}); err != nil {
		c.stopLocked()
		return err
	}
	var response Message
	if err := c.output.Decode(&response); err != nil {
		c.stopLocked()
		return fmt.Errorf("read worker handshake: %w", err)
	}
	if response.Command == "error" {
		c.stopLocked()
		return workerError(response.Error)
	}
	if response.Command != "hello_ack" || response.ProtocolVersion != ProtocolMajor || (response.ProtocolMajor != 0 && response.ProtocolMajor != ProtocolMajor) {
		c.stopLocked()
		return fmt.Errorf("invalid worker handshake response")
	}
	c.ack = response
	c.minor = ProtocolMinor
	if response.ProtocolMinor > 0 && response.ProtocolMinor < c.minor {
		c.minor = response.ProtocolMinor
	}
	if err := validateCapabilities(response.Capabilities, c.expectation); err != nil {
		c.stopLocked()
		return err
	}
	return nil
}

func validateCapabilities(capabilities *Capabilities, expected *Expectation) error {
	if capabilities == nil {
		return fmt.Errorf("worker handshake omitted capabilities")
	}
	if capabilities.InsightFaceVersion == "" || capabilities.InsightFaceVersion == "unavailable" {
		return fmt.Errorf("worker InsightFace is unavailable")
	}
	if capabilities.EmbeddingDimension <= 0 || capabilities.EmbeddingDType != "float32" || capabilities.EmbeddingMetric != "cosine" {
		return fmt.Errorf("worker embedding capabilities are incompatible")
	}
	if len(capabilities.Providers) == 0 || len(capabilities.Models) == 0 {
		return fmt.Errorf("worker did not report execution provider and model")
	}
	if capabilities.Preprocessing == "" || len(capabilities.Modules) == 0 {
		return fmt.Errorf("worker did not report preprocessing and module capabilities")
	}
	if len(capabilities.SupportedCommands) > 0 && !contains(capabilities.SupportedCommands, "analyze") {
		return fmt.Errorf("worker does not support analyze command")
	}
	if expected == nil {
		return nil
	}
	if expected.PipelineID != "" && capabilities.PipelineID != expected.PipelineID {
		return fmt.Errorf("worker pipeline %q does not match required %q", capabilities.PipelineID, expected.PipelineID)
	}
	if expected.Model != "" && !contains(capabilities.Models, expected.Model) {
		return fmt.Errorf("worker does not provide required model %q", expected.Model)
	}
	if expected.Provider != "" && !contains(capabilities.Providers, expected.Provider) {
		return fmt.Errorf("worker does not provide required execution provider %q", expected.Provider)
	}
	if expected.EmbeddingDimension > 0 && capabilities.EmbeddingDimension != expected.EmbeddingDimension {
		return fmt.Errorf("worker embedding dimension %d does not match required %d", capabilities.EmbeddingDimension, expected.EmbeddingDimension)
	}
	if expected.EmbeddingDType != "" && capabilities.EmbeddingDType != expected.EmbeddingDType {
		return fmt.Errorf("worker embedding dtype %q does not match required %q", capabilities.EmbeddingDType, expected.EmbeddingDType)
	}
	if expected.EmbeddingMetric != "" && capabilities.EmbeddingMetric != expected.EmbeddingMetric {
		return fmt.Errorf("worker embedding metric %q does not match required %q", capabilities.EmbeddingMetric, expected.EmbeddingMetric)
	}
	if expected.Preprocessing != "" && capabilities.Preprocessing != expected.Preprocessing {
		return fmt.Errorf("worker preprocessing %q does not match required %q", capabilities.Preprocessing, expected.Preprocessing)
	}
	if expected.DetectorSize != "" && capabilities.DetectorSize != expected.DetectorSize {
		return fmt.Errorf("worker detector size %q does not match required %q", capabilities.DetectorSize, expected.DetectorSize)
	}
	for _, module := range expected.Modules {
		if !contains(capabilities.Modules, module) {
			return fmt.Errorf("worker does not provide required module %q", module)
		}
	}
	for name, checksum := range expected.ModelChecksums {
		if checksum == "" || capabilities.ModelChecksums[name] != checksum {
			return fmt.Errorf("worker model checksum for %q does not match the pipeline", name)
		}
	}
	return nil
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

// Capabilities returns the negotiated worker capabilities.
func (c *Client) Capabilities() Capabilities {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ack.Capabilities == nil {
		return Capabilities{}
	}
	return *c.ack.Capabilities
}

// Analyze sends one image request and returns the worker response.
func (c *Client) Analyze(ctx context.Context, imagePath, pipelineID string, maxPixels int64) (Message, error) {
	identity, err := inspectFileIdentity(imagePath)
	if err != nil {
		return Message{}, err
	}
	return c.AnalyzeWithIdentity(ctx, imagePath, pipelineID, maxPixels, identity.sha256, identity.size)
}

// ResourceLimits are the per-request image safety limits.
type ResourceLimits struct {
	MaxPixels int64 `json:"max_pixels,omitempty"`
	MaxWidth  int   `json:"max_width,omitempty"`
	MaxHeight int   `json:"max_height,omitempty"`
}

// AnalyzeWithLimits sends an image request with all configured resource limits.
func (c *Client) AnalyzeWithLimits(ctx context.Context, imagePath, pipelineID string, limits ResourceLimits, expectedSHA256 string, expectedSize int64) (Message, error) {
	return c.analyzeWithLimits(ctx, imagePath, pipelineID, limits, expectedSHA256, expectedSize)
}

// AnalyzeWithIdentity binds the request to the identity captured by the
// scanner. The worker still rechecks the path immediately before inference,
// while this method rejects a path that no longer matches the persisted asset.
func (c *Client) AnalyzeWithIdentity(ctx context.Context, imagePath, pipelineID string, maxPixels int64, expectedSHA256 string, expectedSize int64) (Message, error) {
	return c.analyzeWithLimits(ctx, imagePath, pipelineID, ResourceLimits{MaxPixels: maxPixels}, expectedSHA256, expectedSize)
}

func (c *Client) analyzeWithLimits(ctx context.Context, imagePath, pipelineID string, limits ResourceLimits, expectedSHA256 string, expectedSize int64) (Message, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.process == nil {
		return Message{}, fmt.Errorf("worker is not running")
	}
	requestID, err := id.NewUUIDv7()
	if err != nil {
		return Message{}, err
	}
	identity, err := inspectFileIdentity(imagePath)
	if err != nil {
		return Message{}, err
	}
	if expectedSHA256 != "" && (identity.sha256 != expectedSHA256 || (expectedSize > 0 && identity.size != expectedSize)) {
		return Message{}, fmt.Errorf("file identity does not match scanned asset")
	}
	if expectedSHA256 == "" {
		expectedSHA256 = identity.sha256
	}
	if expectedSize == 0 {
		expectedSize = identity.size
	}
	if err := c.sendLocked(Message{ProtocolVersion: ProtocolMajor, ProtocolMajor: ProtocolMajor, ProtocolMinor: c.minor, Command: "analyze", ID: requestID, Path: imagePath, ExpectedSHA256: expectedSHA256, ExpectedSize: expectedSize, Model: &ModelInfo{PipelineID: pipelineID}, MaxPixels: limits.MaxPixels, MaxWidth: limits.MaxWidth, MaxHeight: limits.MaxHeight}); err != nil {
		_ = c.restartLocked()
		return Message{}, err
	}
	responseChannel := make(chan struct {
		message Message
		err     error
	}, 1)
	go func() {
		var response Message
		decodeErr := c.output.Decode(&response)
		responseChannel <- struct {
			message Message
			err     error
		}{response, decodeErr}
	}()
	requestContext := ctx
	if c.RequestTimeout > 0 {
		var cancel context.CancelFunc
		requestContext, cancel = context.WithTimeout(ctx, c.RequestTimeout)
		defer cancel()
	}
	select {
	case result := <-responseChannel:
		if result.err != nil {
			_ = c.restartLocked()
			return Message{}, fmt.Errorf("read worker response: %w", result.err)
		}
		if result.message.ID != requestID {
			_ = c.restartLocked()
			return Message{}, fmt.Errorf("worker response ID %q does not match request", result.message.ID)
		}
		if result.message.ProtocolVersion != 0 && result.message.ProtocolVersion != ProtocolMajor {
			_ = c.restartLocked()
			return Message{}, fmt.Errorf("worker response protocol version %d does not match request", result.message.ProtocolVersion)
		}
		if result.message.Command == "error" || (result.message.OK != nil && !*result.message.OK) {
			if result.message.Error != nil && result.message.Error.Retryable {
				_ = c.restartLocked()
			}
			return result.message, workerError(result.message.Error)
		}
		return result.message, nil
	case <-requestContext.Done():
		_ = c.restartLocked()
		return Message{}, requestContext.Err()
	}
}

func (c *Client) restartLocked() error {
	if err := c.stopLocked(); err != nil {
		// A killed worker commonly returns a non-zero exit status; the restart
		// is still useful and the original request remains failed.
		_ = err
	}
	return c.startLocked(context.Background())
}

type fileIdentity struct {
	sha256 string
	size   int64
}

func inspectFileIdentity(path string) (fileIdentity, error) {
	file, err := os.Open(path)
	if err != nil {
		return fileIdentity{}, fmt.Errorf("open image for identity: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return fileIdentity{}, fmt.Errorf("stat image for identity: %w", err)
	}
	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return fileIdentity{}, fmt.Errorf("hash image for identity: %w", err)
	}
	return fileIdentity{sha256: hex.EncodeToString(hasher.Sum(nil)), size: info.Size()}, nil
}

// Close stops the worker process.
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stopLocked()
}

func (c *Client) sendLocked(message Message) error {
	return json.NewEncoder(c.input).Encode(message)
}

func (c *Client) stopLocked() error {
	if c.process == nil {
		return nil
	}
	_ = c.input.Close()
	_ = c.process.Process.Kill()
	err := c.process.Wait()
	c.process = nil
	c.input = nil
	c.output = nil
	return err
}

func workerError(value *WorkerError) error {
	if value == nil {
		return fmt.Errorf("worker returned an unspecified error")
	}
	if value.Detail != "" {
		return fmt.Errorf("worker %s: %s (%s)", value.Code, value.Message, value.Detail)
	}
	return fmt.Errorf("worker %s: %s", value.Code, value.Message)
}
