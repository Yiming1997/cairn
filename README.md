# Cairn

**A local, MCP-native workspace index for AI agents.**

Cairn builds a small, fast index of your code and documents so an agent can ask
"where is this symbol?", "which file talks about this?", or "what does this file
contain?" in **one tool call** instead of a long `grep` + read loop.

A *cairn* is a stack of stones that marks a trail. Cairn does the same for a
workspace: it marks where things are, without dumping the whole repository into
the model's context.

> Status: early `v0.1.0`. The index is read-only. It is **not** a sandbox, a
> semantic search engine, or a replacement for a language server.

## Why

Coding agents usually explore a repository by running several searches and then
reading whole files. That is slow and expensive in tokens.

Cairn turns that into:

```
search("SlowStartGain")
  → path, line range, heading, short snippet
```

On a real Go repository, this reduced the returned bytes by about **30×** for
exact lookups, and one tool call replaced 3–4 `grep`/`read` calls. See
[BENCH.md](BENCH.md) for the methodology and caveats.

## Features

- **Code + docs in one index** — Go symbols and Markdown/text chunks
- **Go-aware** — package, function, method, type, field, const, var; text-level refs
- **Local and deterministic** — no embeddings, no API keys, no vector database
- **MCP native** — `index`, `search`, `symbols`, `refs`, `outline`, `status`
- **Token-budgeted results** — top-N snippets with file and line numbers
- **Single binary** — Go, no Python/Node runtime
- **Bounded parsing** — short file-parse tasks run on agilePool; a bad file does
  not take down the index

## Quick start

Requirements: Go 1.23+.

```bash
git clone https://github.com/Yiming1997/cairn.git
cd cairn
go build -o cairn .
```

Index and query from the CLI:

```bash
./cairn --workspace /path/to/repo --status
./cairn --workspace /path/to/repo --search "SlowStartGain" --limit 5
./cairn --workspace /path/to/repo --symbols "SlowStartGain"
./cairn --workspace /path/to/repo --outline README.md
```

Start the MCP server:

```bash
./cairn --workspace /path/to/repo --workers 4
```

Cairn indexes once at startup, then speaks MCP over stdio. Logs go to stderr;
stdout is reserved for the protocol.

## DSH

Add a row to your DSH profile's `cordis.patch.yml`:

```yaml
- insert:
    - id: cairn
      name: "@deepseek-ai/dsh-mcp-client"
      config:
        serverName: cairn
        transport: stdio
        command: /absolute/path/to/cairn
        args:
          - --workspace
          - /absolute/path/to/workspace
          - --workers
          - "4"
        cwd: /absolute/path/to/workspace
        toolCallTimeoutMs: 30000
        failOnStartupError: false
```

A ready-to-edit example is in
[examples/dsh/cordis.patch.yml](examples/dsh/cordis.patch.yml).

After reloading DSH, the tools appear as:

```
mcp__cairn__index
mcp__cairn__search
mcp__cairn__symbols
mcp__cairn__refs
mcp__cairn__outline
mcp__cairn__status
```

## Tools

| Tool | Purpose |
|---|---|
| `index` | Build or rebuild the index. Optional `paths` are relative to the workspace. |
| `search` | Search code and docs. Returns path, line range, heading, score, snippet. |
| `symbols` | Find Go definitions by name (exact, prefix, substring). |
| `refs` | Find text-level references to a symbol. Not type-aware. |
| `outline` | Show a file's Markdown headings or Go symbols. |
| `status` | Show file/chunk/symbol counts, build time, and roots. |

### Example

```json
{
  "name": "search",
  "arguments": {
    "query": "KILL_ON_JOB_CLOSE",
    "limit": 5
  }
}
```

```text
procguard/docs/design.md:453-463 heading=6.6 root Job score=8.13
  ... short snippet ...
```

## How it works

```
main
  → App.rebuild
      → collectFiles
      → agilePool: parse files in parallel
          → parseGo / parseMarkdown / parseText
      → Index.build
          → chunks → tokens → postings
          → symbols → SymbolMap
  → MCP / CLI
      → search / symbols / refs / outline
```

- **Chunks** are the searchable pieces of a file, with `path`, `start_line`,
  `end_line`, `heading`, and text.
- **Postings** map a token to `(document, chunk, term frequency)`.
- **SymbolMap** maps a lowercase symbol name to its declarations.
- Search uses BM25-style scoring plus a small exact-phrase boost.
- The index is immutable after build; a rebuild swaps the pointer atomically.

The parse fan-out runs on
[agilePool](https://github.com/Yiming1997/agilePool). The pool only handles short
CPU parse tasks; it does not own the index or hold long-running work.

### Worker tuning

For many small files, 2–4 parse workers are usually enough; 8+ can be slower
because file I/O and scheduling overhead dominate. Use more workers for large
files or heavy parsers.

## Benchmark

On `agileLimiter` (42 files, 485 chunks, 648 symbols):

| Query | grep + read | Cairn search | Reduction |
|---|---:|---:|---:|
| `SlowStartGain` | 4 calls / 36.9 KB | 1 call / 1.17 KB | **31×** |
| `deadband` | 4 calls / 33.2 KB | 1 call / 1.16 KB | **29×** |
| `coordinated omission` | 3 calls / 37.6 KB | 1 call / 1.17 KB | **32×** |

Index build: **16 ms** on this corpus. Full details and caveats:
[BENCH.md](BENCH.md).

## Limitations

- Supports `.go`, `.md`, `.markdown`, and `.txt` only. PDF/DOCX/PPTX/XLSX are not
  implemented yet.
- Lexical search only: paraphrases and synonyms are not supported.
- `refs` is text-level, not `go/types`-aware.
- No `fsnotify` incremental updates yet; rebuild on startup or via `index`.
- No persistent index yet; each process builds in memory.
- Snippets are shortcuts, not a substitute for reading code before editing.
- MCP protocol is the legacy `2025-06-18` revision; modern protocol features are
  not implemented.
- It is **not a sandbox**. Commands and files are handled with the current user's
  permissions.

## Roadmap

- PDF / DOCX / PPTX / XLSX text extraction
- Incremental indexing with `fsnotify`
- Persistent index and fast startup
- Include/exclude rules and `path_prefix` in the CLI
- Optional embeddings, after lexical retrieval is solid
- A separate `cairn guard` process supervisor for long-running agent commands
- A workspace daemon + MCP shims for multi-subagent sharing

## Contributing

Issues and pull requests are welcome. See [CONTRIBUTING.md](CONTRIBUTING.md).
Please run:

```bash
go test ./...
go vet ./...
```

## Security

Cairn reads files in the configured workspace and exposes them to the MCP client.
It does not execute commands and is not a sandbox. See [SECURITY.md](SECURITY.md).

## License

MIT. See [LICENSE](LICENSE).

---

[中文说明](README.zh.md)
