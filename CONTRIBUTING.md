# Contributing

Thanks for taking a look at Cairn.

## Development

```bash
git clone https://github.com/Yiming1997/cairn.git
cd cairn
go test ./...
go vet ./...
go build -o cairn .
```

Requirements: Go 1.23+.

## Pull requests

- Keep changes focused; one behavior per PR.
- Add or update tests for indexing, parsing, search, or MCP behavior.
- Run `go test ./...` and `go vet ./...` before opening a PR.
- Update `README.md`, `README.zh.md`, and `BENCH.md` if behavior or numbers change.
- Do not commit built binaries or local index data (see `.gitignore`).

## Areas that need help

- PDF / DOCX / PPTX / XLSX text extraction
- `fsnotify` incremental indexing
- Persistent index and fast startup
- Include/exclude rules
- MCP protocol tests
- Windows-specific path and encoding tests

## Code layout

```
main.go       flags, CLI, startup
mcp.go        MCP stdio server and tool dispatch
index.go      file collection, parsing, postings, search
*_test.go     regression tests
examples/     client configuration examples
```
