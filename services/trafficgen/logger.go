package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// LogLine is one JSONL record written per response.
type LogLine struct {
	ExperimentID string `json:"experiment_id"` // unique run identifier shared across all requests in one generator invocation
	Seq          int64  `json:"seq"`           // monotonically increasing request counter within this experiment
	PromptID     string `json:"prompt_id"`     // stable identifier from prompts.json; same prompt reused across windows
	PromptType   string `json:"prompt_type"`   // category from prompts.json: "factual", "reasoning", or "instruction"
	Variant      string `json:"variant"`       // routing label assigned by the splitter: INCUMBENT, CANDIDATE_A, CANDIDATE_B, or CONTROL
	ResponseText string `json:"response_text"` // raw text returned by the worker; empty on error or refusal
	InputTokens  int32  `json:"input_tokens"`  // prompt token count reported by the worker (used for cost accounting)
	OutputTokens int32  `json:"output_tokens"` // completion token count reported by the worker (used for cost and length distribution)
	LatencyMs    int64  `json:"latency_ms"`    // worker-reported end-to-end inference time in milliseconds
	WallMs       int64  `json:"wall_ms"`       // generator round-trip time including network; always >= LatencyMs
	Refused      bool   `json:"refused"`       // true when the worker explicitly declined to answer (refusal-rate guardrail input)
	Error        string `json:"error"`         // non-empty when the RPC or worker returned an error; mutually exclusive with ResponseText
	TsUnixMs     int64  `json:"ts_unix_ms"`    // Unix timestamp in milliseconds when the response was received
}

// Logger writes LogLines to a JSONL file or stdout. Writes are serialised by
// a mutex so multiple sender goroutines can call Write concurrently.
type Logger struct {
	mu  sync.Mutex
	w   *bufio.Writer
	f   *os.File // nil when writing to stdout
	seq int64
}

// NewLogger opens path for writing and returns a Logger backed by a 256 KiB
// buffer. Pass "-" to write to stdout instead of a file.
func NewLogger(path string) (*Logger, error) {
	if path == "-" {
		return &Logger{w: bufio.NewWriterSize(os.Stdout, 256*1024)}, nil
	}
	f, err := os.OpenFile(filepath.Clean(path), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open log file: %w", err)
	}
	return &Logger{w: bufio.NewWriterSize(f, 256*1024), f: f}, nil
}

// Write serializes line as JSON, stamps Seq and TsUnixMs, and appends it to
// the buffered writer. Safe for concurrent use.
func (l *Logger) Write(line LogLine) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.seq++
	line.Seq = l.seq
	line.TsUnixMs = time.Now().UnixMilli()

	b, err := json.Marshal(line)
	if err != nil {
		return fmt.Errorf("marshal log line: %w", err)
	}
	if _, err := l.w.Write(b); err != nil {
		return fmt.Errorf("write log line: %w", err)
	}
	return l.w.WriteByte('\n')
}

// Close flushes the buffer and closes the underlying file. Must be called
// exactly once after all writes complete.
func (l *Logger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if err := l.w.Flush(); err != nil {
		return fmt.Errorf("flush log: %w", err)
	}
	if l.f != nil {
		return l.f.Close()
	}
	return nil
}

// WriterFrom returns the underlying io.Writer for testing.
func WriterFrom(w io.Writer) *Logger {
	return &Logger{w: bufio.NewWriter(w)}
}
