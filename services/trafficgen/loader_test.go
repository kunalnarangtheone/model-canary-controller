package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func writePromptFile(t *testing.T, content any) string {
	t.Helper()
	b, err := json.Marshal(content)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	f := filepath.Join(t.TempDir(), "prompts.json")
	if err := os.WriteFile(f, b, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	return f
}

func TestLoader_ValidFile(t *testing.T) {
	var prompts []Prompt
	for range 20 {
		prompts = append(prompts, Prompt{ID: "f001", Type: "factual", Text: "q"})
		prompts = append(prompts, Prompt{ID: "r001", Type: "reasoning", Text: "q"})
		prompts = append(prompts, Prompt{ID: "i001", Type: "instruction", Text: "q"})
	}
	path := writePromptFile(t, promptFile{Version: "1", Prompts: prompts})

	got, err := LoadPrompts(path, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 60 {
		t.Fatalf("want 60 prompts, got %d", len(got))
	}
	counts := map[string]int{}
	for _, p := range got {
		counts[p.Type]++
	}
	for _, typ := range []string{"factual", "reasoning", "instruction"} {
		if counts[typ] != 20 {
			t.Errorf("type %q: want 20, got %d", typ, counts[typ])
		}
	}
}

func TestLoader_MissingFile(t *testing.T) {
	_, err := LoadPrompts("/no/such/file.json", nil)
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestLoader_TypeFilter(t *testing.T) {
	path := writePromptFile(t, promptFile{Prompts: []Prompt{
		{ID: "f001", Type: "factual", Text: "q"},
		{ID: "r001", Type: "reasoning", Text: "q"},
		{ID: "i001", Type: "instruction", Text: "q"},
	}})

	got, err := LoadPrompts(path, []string{"factual"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0].Type != "factual" {
		t.Errorf("want 1 factual prompt, got %v", got)
	}
}

func TestLoader_NoMatchingTypes(t *testing.T) {
	path := writePromptFile(t, promptFile{Prompts: []Prompt{
		{ID: "f001", Type: "factual", Text: "q"},
	}})
	_, err := LoadPrompts(path, []string{"reasoning"})
	if err == nil {
		t.Fatal("expected error when no prompts match filter")
	}
}
