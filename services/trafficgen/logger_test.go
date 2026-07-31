package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestLogger_Write(t *testing.T) {
	var buf bytes.Buffer
	l := WriterFrom(&buf)

	line := LogLine{
		ExperimentID: "exp1",
		PromptID:     "r007",
		PromptType:   "reasoning",
		Variant:      "VARIANT_CANDIDATE_A",
		ResponseText: "hello",
		InputTokens:  10,
		OutputTokens: 20,
		LatencyMs:    300,
		WallMs:       310,
		Refused:      false,
	}
	if err := l.Write(line); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := l.Write(line); err != nil {
		t.Fatalf("Write 2: %v", err)
	}

	// Flush underlying buffer.
	_ = l.w.Flush()

	scanner := bufio.NewScanner(&buf)
	seq := int64(0)
	for scanner.Scan() {
		seq++
		var got LogLine
		if err := json.Unmarshal(scanner.Bytes(), &got); err != nil {
			t.Fatalf("unmarshal line %d: %v", seq, err)
		}
		if got.Seq != seq {
			t.Errorf("line %d: seq=%d want %d", seq, got.Seq, seq)
		}
		if got.TsUnixMs == 0 {
			t.Errorf("line %d: ts_unix_ms not set", seq)
		}
		if got.ExperimentID != "exp1" {
			t.Errorf("line %d: experiment_id=%q", seq, got.ExperimentID)
		}
		if got.PromptID != "r007" {
			t.Errorf("line %d: prompt_id=%q", seq, got.PromptID)
		}
	}
	if seq != 2 {
		t.Errorf("want 2 lines, got %d", seq)
	}
}

func TestLogger_LineDelimited(t *testing.T) {
	var buf bytes.Buffer
	l := WriterFrom(&buf)
	for i := 0; i < 5; i++ {
		_ = l.Write(LogLine{PromptID: "x"})
	}
	_ = l.w.Flush()

	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 5 {
		t.Errorf("want 5 newline-delimited records, got %d", len(lines))
	}
}
