# Cairn — value check

Corpus: `D:\code\github\agileLimiter`  
Index: **42 files, 485 chunks, 648 symbols**, built in **16 ms** (warm filesystem cache).  
Search: BM25-style token index, top 5 results, ~220-char snippets.

## Baseline vs index

Baseline simulates a grep-and-read agent:

- 1 `grep` call over the workspace
- read the top 3 matching files in full
- sum grep-line bytes + full-file bytes
- token estimate ≈ bytes / 4

Index: 1 `search` call, 5 results, each with file/line/heading/snippet.

| Query | grep matches | baseline calls | baseline bytes | index calls | index bytes | reduction |
|---|---:|---:|---:|---:|---:|---:|
| `SlowStartGain` | 21 | 4 | 36,879 | 1 | 1,174 | **31.4×** |
| `deadband` | 18 | 4 | 33,190 | 1 | 1,161 | **28.6×** |
| `coordinated omission` | 2 | 3 | 37,588 | 1 | 1,169 | **32.2×** |
| `Classify unreachable` | 0 | 1 | 0 | 1 | 1,365 | n/a |
| `bucket take` | 0 | 1 | 0 | 1 | 1,297 | n/a |

The last two are multi-word queries: exact `grep` finds zero because the words are not adjacent; the token index still finds relevant chunks. That is a bonus, not a guarantee — the index is not semantic and can return false positives.

## What this means

For exact identifiers/terms that exist in the workspace:

- **tool calls**: 3–4 → **1**
- **bytes returned**: 33–37 KB → **~1.2 KB**
- **token estimate**: ~8–9k → **~300**
- **reduction**: **~30×**

The index build is ~16 ms for 42 files, so the cost is negligible compared with the savings on a single lookup.

## Caveats

- Baseline reads full files. If an agent only reads a 50-line window, baseline bytes drop and the reduction is smaller; tool-call reduction remains.
- Snippets can cut important context; this is a retrieval aid, not a replacement for reading the exact code before editing.
- The index is lexical (BM25 + tokenization), not embedding-based. Paraphrases won't match.
- Current formats: `.go`, `.md`, `.txt`. PDF/DOCX/PPTX/XLSX and `fsnotify` are not implemented yet.
- `refs` is text-level, not `go/types`-aware.
- Index includes test files and docs; query strings that appear in tests can rank high. Use `path_prefix` or exclude generated/vendor content.
- One run on one repo; not a statistically rigorous benchmark.

## Verdict

The value is real for **“where is X / what does X say”** queries: one call instead of a grep+read loop, ~30× fewer bytes.  
It is **not** a replacement for reading the final code before editing, and it is not a semantic search engine.

Next steps if this is worth continuing:

1. Add PDF/DOCX/PPTX/XLSX text extraction.
2. Add `fsnotify` incremental indexing.
3. Add `path_prefix`/exclude rules and artifact ignore.
4. Optional embeddings for semantic search, but only after lexical retrieval is solid.
5. Add a DSH config and measure agent adoption on real tasks.
