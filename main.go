package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
)

const version = "0.1.0"

func main() {
	var (
		workspace = flag.String("workspace", "", "workspace root to index (defaults to current directory)")
		workers   = flag.Int("workers", 0, "agilePool parse workers (defaults to min(GOMAXPROCS, 8))")
		search    = flag.String("search", "", "CLI: search and exit")
		symbols   = flag.String("symbols", "", "CLI: find symbols and exit")
		outline   = flag.String("outline", "", "CLI: print a file outline and exit")
		limit     = flag.Int("limit", 8, "CLI result limit")
		status    = flag.Bool("status", false, "CLI: print index status and exit")
		showVer   = flag.Bool("version", false, "print version and exit")
	)
	flag.Parse()
	if *showVer {
		fmt.Println("cairn " + version)
		return
	}
	ws := *workspace
	if ws == "" {
		ws, _ = os.Getwd()
	}
	abs, err := filepath.Abs(ws)
	if err != nil {
		log.Fatalf("workspace: %v", err)
	}
	info, err := os.Stat(abs)
	if err != nil || !info.IsDir() {
		log.Fatalf("workspace is not a directory: %s", abs)
	}

	pool := newIndexPool(*workers, defaultIndexQueueSize)
	app := NewApp(abs, []string{abs}, *workers, pool)
	defer app.Close()
	log.SetOutput(os.Stderr)
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)

	idx, err := app.rebuild(nil)
	if err != nil {
		log.Printf("initial index failed: %v", err)
	} else {
		log.Printf("indexed %d files, %d chunks, %d symbols in %dms",
			idx.Files, idx.Chunks, idx.SymbolCnt, idx.BuildTime.Milliseconds())
	}

	if *status {
		if idx != nil {
			fmt.Println(idx.Status())
		}
		return
	}
	if *search != "" || *symbols != "" || *outline != "" {
		runCLI(app, *search, *symbols, *outline, *limit)
		return
	}

	log.Printf("cairn %s: workspace=%s (MCP stdio)", version, abs)
	if err := serveMCP(app); err != nil {
		if !strings.Contains(err.Error(), "file already closed") {
			log.Printf("mcp: %v", err)
		}
	}
}
