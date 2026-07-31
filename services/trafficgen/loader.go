package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Prompt is a single synthetic inference request loaded from data/prompts.json.
type Prompt struct {
	ID   string `json:"id"`   // uniquely identifies the prompt within the dataset
	Type string `json:"type"` // prompt category: "factual", "reasoning", or "instruction"
	Text string `json:"text"` // raw prompt string sent to the model
}

// promptFile mirrors the top-level structure of data/prompts.json.
// It is only used for decoding; callers receive []Prompt directly.
type promptFile struct {
	Version     string   `json:"version"`     // schema version of the prompts file
	Description string   `json:"description"` // human-readable summary of the prompt set
	Prompts     []Prompt `json:"prompts"`     // ordered list of synthetic prompts
}

// LoadPrompts reads and validates the prompt set from path. It filters by
// allowedTypes when the slice is non-empty; otherwise all prompts are returned.
func LoadPrompts(path string, allowedTypes []string) ([]Prompt, error) {
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil, fmt.Errorf("read prompts file: %w", err)
	}

	var pf promptFile
	if err := json.Unmarshal(data, &pf); err != nil {
		return nil, fmt.Errorf("parse prompts file: %w", err)
	}

	if len(pf.Prompts) == 0 {
		return nil, fmt.Errorf("prompts file contains no prompts")
	}

	if len(allowedTypes) == 0 {
		return pf.Prompts, nil
	}

	allowed := make(map[string]bool, len(allowedTypes))
	for _, t := range allowedTypes {
		allowed[t] = true
	}

	out := make([]Prompt, 0, len(pf.Prompts))
	for _, p := range pf.Prompts {
		if allowed[p.Type] {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no prompts match types %v", allowedTypes)
	}
	return out, nil
}
