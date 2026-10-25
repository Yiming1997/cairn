package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"

	agilepool "github.com/Yiming1997/agilePool/v2"
)

const serverInstructions = `Cairn is a local index of the workspace's code and documents. Use mcp__cairn__symbols to find definitions, mcp__cairn__search to find relevant chunks, mcp__cairn__refs for text-level references, and mcp__cairn__outline for a file or package structure. Prefer one of these over reading many files or running repeated grep loops. Call index to rebuild after large changes; status shows index coverage.`

type App struct {
	workspace string
	roots     []string
	workers   int
	pool      *agilepool.Pool

	indexMu sync.Mutex
	mu      sync.RWMutex
	idx     *Index
	closed  bool
}

func NewApp(workspace string, roots []string, workers int, pool *agilepool.Pool) *App {
	workers = normalizeWorkers(workers)
	if pool == nil {
		pool = newIndexPool(workers, defaultIndexQueueSize)
	}
	return &App{workspace: workspace, roots: roots, workers: workers, pool: pool}
}

func (a *App) rebuild(paths []string) (*Index, error) {
	a.indexMu.Lock()
	defer a.indexMu.Unlock()

	a.mu.RLock()
	closed := a.closed
	a.mu.RUnlock()
	if closed {
		return nil, fmt.Errorf("app is closed")
	}

	roots := a.roots
	if len(paths) > 0 {
		roots = make([]string, 0, len(paths))
		for _, p := range paths {
			if !filepath.IsAbs(p) {
				p = filepath.Join(a.workspace, p)
			}
			abs, err := filepath.Abs(p)
			if err != nil {
				return nil, err
			}
			roots = append(roots, abs)
		}
	}
	idx, err := BuildIndexWithPool(a.workspace, roots, a.workers, a.pool)
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	a.idx = idx
	a.mu.Unlock()
	return idx, nil
}

// Close waits for the current rebuild (if any), drains the shared pool, and
// closes it. It is safe to call once at the end of the process.
func (a *App) Close() {
	a.indexMu.Lock()
	defer a.indexMu.Unlock()

	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return
	}
	a.closed = true
	a.mu.Unlock()

	a.pool.Wait()
	a.pool.Close()
}

func (a *App) current() *Index {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.idx
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type toolDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

type callParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

func serveMCP(app *App) error {
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	var writeMu sync.Mutex
	var wg sync.WaitGroup
	write := func(resp *rpcResponse) {
		if resp == nil {
			return
		}
		writeMu.Lock()
		defer writeMu.Unlock()
		_ = json.NewEncoder(os.Stdout).Encode(resp)
	}
	for scanner.Scan() {
		var req rpcRequest
		if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
			log.Printf("mcp: invalid json: %v", err)
			continue
		}
		if req.Method == "" || len(req.ID) == 0 {
			continue
		}
		switch req.Method {
		case "initialize", "ping", "tools/list":
			write(handleRPC(app, &req))
		case "tools/call":
			wg.Add(1)
			go func(r *rpcRequest) {
				defer wg.Done()
				write(handleRPC(app, r))
			}(&req)
		default:
			write(&rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32601, Message: "method not found: " + req.Method}})
		}
	}
	wg.Wait()
	return scanner.Err()
}

func handleRPC(app *App, req *rpcRequest) *rpcResponse {
	switch req.Method {
	case "initialize":
		return &rpcResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: map[string]any{
				"protocolVersion": "2025-06-18",
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      map[string]any{"name": "cairn", "version": "0.1.0"},
				"instructions":    serverInstructions,
			},
		}
	case "ping":
		return &rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{}}
	case "tools/list":
		return &rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{"tools": toolDefs()}}
	case "tools/call":
		return handleToolCall(app, req)
	default:
		return &rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32601, Message: "method not found: " + req.Method}}
	}
}

func handleToolCall(app *App, req *rpcRequest) *rpcResponse {
	var p callParams
	if err := json.Unmarshal(req.Params, &p); err != nil {
		return toolError(req.ID, "invalid_args", "invalid params: "+err.Error())
	}
	idx := app.current()
	switch p.Name {
	case "index":
		var a struct {
			Paths []string `json:"paths"`
		}
		_ = json.Unmarshal(p.Arguments, &a)
		built, err := app.rebuild(a.Paths)
		if err != nil {
			return toolError(req.ID, "index_failed", err.Error())
		}
		return toolOK(req.ID, fmt.Sprintf("indexed files=%d chunks=%d symbols=%d errors=%d in %dms",
			built.Files, built.Chunks, built.SymbolCnt, built.Errors, built.BuildTime.Milliseconds()))
	case "search":
		if idx == nil {
			return toolError(req.ID, "not_indexed", "call index first")
		}
		var a struct {
			Query      string `json:"query"`
			Limit      int    `json:"limit"`
			PathPrefix string `json:"path_prefix"`
		}
		if err := json.Unmarshal(p.Arguments, &a); err != nil || strings.TrimSpace(a.Query) == "" {
			return toolError(req.ID, "invalid_args", "query must not be empty")
		}
		return toolOK(req.ID, formatSearch(idx.Search(a.Query, a.Limit, a.PathPrefix)))
	case "outline":
		if idx == nil {
			return toolError(req.ID, "not_indexed", "call index first")
		}
		var a struct {
			Path string `json:"path"`
		}
		if err := json.Unmarshal(p.Arguments, &a); err != nil || a.Path == "" {
			return toolError(req.ID, "invalid_args", "path must not be empty")
		}
		return toolOK(req.ID, formatOutline(a.Path, idx.Outline(a.Path)))
	case "symbols":
		if idx == nil {
			return toolError(req.ID, "not_indexed", "call index first")
		}
		var a struct {
			Query string `json:"query"`
			Limit int    `json:"limit"`
		}
		if err := json.Unmarshal(p.Arguments, &a); err != nil || strings.TrimSpace(a.Query) == "" {
			return toolError(req.ID, "invalid_args", "query must not be empty")
		}
		return toolOK(req.ID, formatSymbols(idx.Symbols(a.Query, a.Limit)))
	case "refs":
		if idx == nil {
			return toolError(req.ID, "not_indexed", "call index first")
		}
		var a struct {
			Symbol string `json:"symbol"`
			Limit  int    `json:"limit"`
		}
		if err := json.Unmarshal(p.Arguments, &a); err != nil || strings.TrimSpace(a.Symbol) == "" {
			return toolError(req.ID, "invalid_args", "symbol must not be empty")
		}
		return toolOK(req.ID, formatRefs(idx.Refs(a.Symbol, a.Limit)))
	case "status":
		if idx == nil {
			return toolOK(req.ID, "not_indexed")
		}
		return toolOK(req.ID, idx.Status())
	default:
		return toolError(req.ID, "unknown_tool", "unknown tool: "+p.Name)
	}
}

func toolOK(id json.RawMessage, text string) *rpcResponse {
	return &rpcResponse{JSONRPC: "2.0", ID: id, Result: map[string]any{
		"content": []map[string]any{{"type": "text", "text": text}},
		"isError": false,
	}}
}

func toolError(id json.RawMessage, code, message string) *rpcResponse {
	return &rpcResponse{JSONRPC: "2.0", ID: id, Result: map[string]any{
		"content": []map[string]any{{"type": "text", "text": fmt.Sprintf("error code=%s message=%s", code, message)}},
		"isError": true,
	}}
}

func formatSearch(results []SearchResult) string {
	if len(results) == 0 {
		return "no results"
	}
	var b strings.Builder
	for _, r := range results {
		fmt.Fprintf(&b, "%s:%d-%d", r.Path, r.Line, r.EndLine)
		if r.Heading != "" {
			fmt.Fprintf(&b, " heading=%s", r.Heading)
		}
		fmt.Fprintf(&b, " score=%.2f\n  %s\n", r.Score, r.Snippet)
	}
	return strings.TrimRight(b.String(), "\n")
}

func formatSymbols(syms []Symbol) string {
	if len(syms) == 0 {
		return "no symbols"
	}
	var b strings.Builder
	for _, s := range syms {
		name := s.Name
		if s.Receiver != "" {
			name = s.Receiver + "." + name
		}
		fmt.Fprintf(&b, "%s:%d %s %s", s.File, s.Line, s.Kind, name)
		if s.Signature != "" {
			fmt.Fprintf(&b, "  %s", s.Signature)
		}
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
}

func formatRefs(results []SearchResult) string {
	if len(results) == 0 {
		return "no refs"
	}
	var b strings.Builder
	for _, r := range results {
		fmt.Fprintf(&b, "%s:%d  %s\n", r.Path, r.Line, r.Snippet)
	}
	return strings.TrimRight(b.String(), "\n")
}

func formatOutline(path string, items []OutlineItem) string {
	if len(items) == 0 {
		return "no outline for " + path
	}
	var b strings.Builder
	b.WriteString(path)
	b.WriteByte('\n')
	for _, it := range items {
		fmt.Fprintf(&b, "  %d %s %s", it.Line, it.Kind, it.Name)
		if it.Level > 0 {
			fmt.Fprintf(&b, " (h%d)", it.Level)
		}
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
}

func schemaObject(props map[string]any, required ...string) map[string]any {
	s := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

func toolDefs() []toolDef {
	return []toolDef{
		{
			Name:        "index",
			Description: "Build or rebuild the workspace index. Optional paths are relative to the workspace; omit to index the configured roots.",
			InputSchema: schemaObject(map[string]any{
				"paths": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			}),
		},
		{
			Name:        "search",
			Description: "Search indexed code and documents by keyword. Returns file, line range, heading, and a short snippet. Use this before reading many files.",
			InputSchema: schemaObject(map[string]any{
				"query":       map[string]any{"type": "string"},
				"limit":       map[string]any{"type": "integer", "minimum": 1, "maximum": 50},
				"path_prefix": map[string]any{"type": "string"},
			}, "query"),
		},
		{
			Name:        "symbols",
			Description: "Find Go definitions by symbol name. Exact matches rank first, then prefix and substring matches.",
			InputSchema: schemaObject(map[string]any{
				"query": map[string]any{"type": "string"},
				"limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 100},
			}, "query"),
		},
		{
			Name:        "refs",
			Description: "Find text-level references to a symbol across indexed code and documents. This is not type-aware.",
			InputSchema: schemaObject(map[string]any{
				"symbol": map[string]any{"type": "string"},
				"limit":  map[string]any{"type": "integer", "minimum": 1, "maximum": 50},
			}, "symbol"),
		},
		{
			Name:        "outline",
			Description: "Return the outline of one indexed file: Markdown headings or Go package/functions/types.",
			InputSchema: schemaObject(map[string]any{
				"path": map[string]any{"type": "string"},
			}, "path"),
		},
		{
			Name:        "status",
			Description: "Show index coverage, build time, and configured roots.",
			InputSchema: schemaObject(map[string]any{}),
		},
	}
}

func runCLI(app *App, search, symbols, outline string, limit int) {
	idx := app.current()
	if idx == nil {
		fmt.Println("not indexed")
		return
	}
	switch {
	case search != "":
		fmt.Println(formatSearch(idx.Search(search, limit, "")))
	case symbols != "":
		fmt.Println(formatSymbols(idx.Symbols(symbols, limit)))
	case outline != "":
		fmt.Println(formatOutline(outline, idx.Outline(outline)))
	default:
		fmt.Println(idx.Status())
	}
}
