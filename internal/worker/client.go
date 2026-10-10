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

	"github.com/pjg/face_me_bro/internal/id"
)

// Client manages one persistent worker process. Calls are serialized because
// the MVP worker owns one loaded model and processes one image at a time.
type Client struct {
	command string
	args    []string
	mu      sync.Mutex
	process *exec.Cmd
	input   io.WriteCloser
	output  *json.Decoder
	ack     Message
	minor   int
}

// NewClient creates a client; it does not start the process until Start.
func NewClient(command string, args ...string) *Client {
	return &Client{command: command, args: args}
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
	if err := c.sendLocked(Message{Type: "hello", ID: requestID, ProtocolMajor: ProtocolMajor, ProtocolMinor: ProtocolMinor}); err != nil {
		c.stopLocked()
		return err
	}
	var response Message
	if err := c.output.Decode(&response); err != nil {
		c.stopLocked()
		return fmt.Errorf("read worker handshake: %w", err)
	}
	if response.Type == "error" {
		c.stopLocked()
		return workerError(response.Error)
	}
	if response.Type != "hello_ack" || response.ProtocolMajor != ProtocolMajor || response.ProtocolMinor < 0 {
		c.stopLocked()
		return fmt.Errorf("invalid worker handshake response")
	}
	c.ack = response
	if response.ProtocolMinor < ProtocolMinor {
		c.minor = response.ProtocolMinor
	} else {
		c.minor = ProtocolMinor
	}
	return nil
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
	if err := c.sendLocked(Message{Type: "analyze", ID: requestID, ProtocolMajor: ProtocolMajor, ProtocolMinor: c.minor, ImagePath: imagePath, ExpectedSHA256: identity.sha256, ExpectedSize: identity.size, PipelineID: pipelineID, MaxPixels: maxPixels}); err != nil {
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
		if result.message.Type == "error" {
			if result.message.Error != nil && result.message.Error.Retryable {
				_ = c.restartLocked()
			}
			return result.message, workerError(result.message.Error)
		}
		return result.message, nil
	case <-ctx.Done():
		_ = c.restartLocked()
		return Message{}, ctx.Err()
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
	return fmt.Errorf("worker %s: %s", value.Code, value.Message)
}
