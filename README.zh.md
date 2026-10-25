# Cairn

**给 AI agent 的本地、MCP 原生 workspace 索引。**

Cairn 把代码和文档变成可查询的 chunk + symbol，让 agent 用一次调用回答：

- “这个符号在哪定义？”
- “哪段文档提到了它？”
- “这个文件里有什么？”

而不是反复 `grep` + 读整个文件。

> 状态：`v0.1.0`。索引是只读的。它不是沙箱、不是语义搜索引擎、也不是语言服务器替代品。

英文说明：[README.md](README.md)

## 为什么

Coding agent 通常靠多次搜索 + 读整个文件来探索仓库，慢且费 token。  
Cairn 把它变成：

```
search("SlowStartGain")
  → path、行号范围、heading、短 snippet
```

在真实 Go 仓库上，精确查询的返回字节数降低约 **30×**，工具调用从 3–4 次降到 1 次。  
方法和 caveat 见 [BENCH.md](BENCH.md)。

## 特性

- **代码 + 文档一个索引**：Go 符号 + Markdown/text chunk
- **Go 感知**：package、func、method、type、field、const、var；文本级 refs
- **本地、确定性**：无 embedding、无 API key、无向量库
- **MCP 原生**：`index` / `search` / `symbols` / `refs` / `outline` / `status`
- **Token 预算化**：top-N + 文件/行号 + 短 snippet
- **单二进制**：Go，无 Python/Node 运行时
- **有界解析**：短 parse 任务跑在 agilePool 上；坏文件不会拖垮整个索引

## 快速开始

需要 Go 1.23+。

```bash
git clone https://github.com/Yiming1997/cairn.git
cd cairn
go build -o cairn .
```

CLI：

```bash
./cairn --workspace /path/to/repo --status
./cairn --workspace /path/to/repo --search "SlowStartGain" --limit 5
./cairn --workspace /path/to/repo --symbols "SlowStartGain"
./cairn --workspace /path/to/repo --outline README.md
```

MCP stdio：

```bash
./cairn --workspace /path/to/repo --workers 4
```

启动时建一次索引，然后通过 stdio 说 MCP。日志写 stderr，stdout 只走协议。

## DSH 配置

在 `cordis.patch.yml` 加：

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

可编辑示例：[examples/dsh/cordis.patch.yml](examples/dsh/cordis.patch.yml)

重载后工具名：

```
mcp__cairn__index
mcp__cairn__search
mcp__cairn__symbols
mcp__cairn__refs
mcp__cairn__outline
mcp__cairn__status
```

## 工具

| 工具 | 用途 |
|---|---|
| `index` | 建/重建索引；可选 `paths` 子目录 |
| `search` | 跨代码/文档搜索，返回文件、行号、heading、snippet |
| `symbols` | 找 Go 定义：func/method/type/field/const/var |
| `refs` | 文本级引用 |
| `outline` | 单文件大纲：Markdown heading 或 Go 符号 |
| `status` | 文件数、chunk 数、符号数、构建时间 |

## 怎么工作

```
main
  → App.rebuild
      → collectFiles
      → agilePool 并行 parse
          → parseGo / parseMarkdown / parseText
      → Index.build
          → chunks → tokens → postings
          → symbols → SymbolMap
  → MCP / CLI
      → search / symbols / refs / outline
```

- **Chunk**：文件的可搜索片段，带 path/start/end/heading/text
- **Postings**：token → `(doc, chunk, TF)`
- **SymbolMap**：小写符号名 → 定义
- 搜索用 BM25 风格打分 + 精确短语 boost
- 索引构建完成后整体替换，不在旧索引上原地改

解析 fan-out 用 [agilePool](https://github.com/Yiming1997/agilePool)。池只跑短 CPU parse，不持有索引，也不放长任务。

### Worker 调优

很多小文件时，**2–4 个 worker 通常够了；8+ 可能更慢**，因为文件 I/O 和调度开销占主导。大文件/重解析才值得提高到 GOMAXPROCS。

## 实测

在 `agileLimiter`（42 文件、485 chunks、648 symbols）：

| 查询 | grep + read | Cairn search | 降幅 |
|---|---:|---:|---:|
| `SlowStartGain` | 4 calls / 36.9 KB | 1 call / 1.17 KB | **31×** |
| `deadband` | 4 calls / 33.2 KB | 1 call / 1.16 KB | **29×** |
| `coordinated omission` | 3 calls / 37.6 KB | 1 call / 1.17 KB | **32×** |

索引构建 **16 ms**。完整 caveat 见 [BENCH.md](BENCH.md)。

## 局限

- 只支持 `.go`、`.md`、`.markdown`、`.txt`；PDF/DOCX/PPTX/XLSX 还没做
- 纯词法检索，不支持同义改写
- `refs` 是文本级，不是 `go/types`
- 没有 `fsnotify` 增量；启动或 `index` 时重建
- 没有持久化索引；每个进程内存建一次
- snippet 是快捷方式，改代码前仍应读原文件
- MCP 协议只实现 legacy `2025-06-18`
- **不是沙箱**：文件读取使用当前用户权限

## Roadmap

- PDF / DOCX / PPTX / XLSX 文本提取
- `fsnotify` 增量索引
- 持久化索引，快速启动
- include/exclude 规则与 CLI `path_prefix`
- 可选 embedding（先把词法检索做稳）
- 独立的 `cairn guard` 进程监管命令
- workspace daemon + MCP shims，多 subagent 共享

## 贡献

欢迎 issue / PR。见 [CONTRIBUTING.md](CONTRIBUTING.md)。

```bash
go test ./...
go vet ./...
```

## 安全

Cairn 读取配置 workspace 内的文件并暴露给 MCP 客户端。它不执行命令，也不是沙箱。  
见 [SECURITY.md](SECURITY.md)。

## License

MIT。见 [LICENSE](LICENSE)。
