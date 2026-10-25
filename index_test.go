package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildAndSearch(t *testing.T) {
	dir := t.TempDir()
	goSrc := `package demo

type Config struct {
	SlowStartGain float64
}

func New(c Config) *Config { return &c }
`
	md := "# Guide\n\nProcess tree cleanup matters for long commands.\n\n## Details\n\nUse SlowStartGain carefully.\n"
	if err := os.WriteFile(filepath.Join(dir, "demo.go"), []byte(goSrc), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.md"), []byte(md), 0o644); err != nil {
		t.Fatal(err)
	}

	idx, err := BuildIndex(dir, []string{dir}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if idx.Files != 2 || idx.Chunks == 0 || idx.SymbolCnt < 2 {
		t.Fatalf("files=%d chunks=%d symbols=%d", idx.Files, idx.Chunks, idx.SymbolCnt)
	}

	results := idx.Search("SlowStartGain", 5, "")
	if len(results) == 0 {
		t.Fatal("search returned no results")
	}
	if !strings.Contains(results[0].Snippet, "SlowStartGain") {
		t.Fatalf("snippet = %q", results[0].Snippet)
	}

	syms := idx.Symbols("SlowStartGain", 10)
	if len(syms) == 0 || syms[0].Kind != "field" {
		t.Fatalf("symbols = %+v", syms)
	}

	outline := idx.Outline("notes.md")
	if len(outline) < 2 || outline[0].Kind != "heading" {
		t.Fatalf("outline = %+v", outline)
	}

	refs := idx.Refs("SlowStartGain", 10)
	if len(refs) == 0 {
		t.Fatal("refs returned no results")
	}
}

func TestSharedPoolRebuild(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("hello cairn\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	pool := newIndexPool(2, 64)
	defer func() {
		pool.Wait()
		pool.Close()
	}()

	for i := 0; i < 3; i++ {
		idx, err := BuildIndexWithPool(dir, []string{dir}, 2, pool)
		if err != nil {
			t.Fatal(err)
		}
		if idx.Files != 1 || idx.Chunks == 0 {
			t.Fatalf("build %d: files=%d chunks=%d", i, idx.Files, idx.Chunks)
		}
	}
}

func TestAppRebuildAfterClose(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("hello cairn\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	app := NewApp(dir, []string{dir}, 2, nil)
	if _, err := app.rebuild(nil); err != nil {
		t.Fatal(err)
	}
	app.Close()
	if _, err := app.rebuild(nil); err == nil {
		t.Fatal("rebuild after Close should fail")
	}
}
