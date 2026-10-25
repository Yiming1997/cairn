package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"

	agilepool "github.com/Yiming1997/agilePool/v2"
)

const (
	maxFileBytes   = 2 * 1024 * 1024
	maxChunkChars  = 8000
	maxSearchLimit = 50
)

type Symbol struct {
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	Signature string `json:"signature,omitempty"`
	Receiver  string `json:"receiver,omitempty"`
	Package   string `json:"package,omitempty"`
	File      string `json:"file"`
	Line      int    `json:"line"`
}

type OutlineItem struct {
	Kind  string `json:"kind"`
	Name  string `json:"name"`
	Line  int    `json:"line"`
	Level int    `json:"level,omitempty"`
}

type Chunk struct {
	Path      string `json:"path"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
	Heading   string `json:"heading,omitempty"`
	Text      string `json:"text"`
	Length    int    `json:"-"`
}

type Document struct {
	Path    string        `json:"path"`
	AbsPath string        `json:"abs_path"`
	Ext     string        `json:"ext"`
	Size    int64         `json:"size"`
	Chunks  []Chunk       `json:"chunks"`
	Symbols []Symbol      `json:"symbols"`
	Outline []OutlineItem `json:"outline"`
}

type posting struct {
	Doc   int
	Chunk int
	TF    int
}

type SearchResult struct {
	Path    string  `json:"path"`
	Line    int     `json:"line"`
	EndLine int     `json:"end_line"`
	Heading string  `json:"heading,omitempty"`
	Score   float64 `json:"score"`
	Snippet string  `json:"snippet"`
}

type Index struct {
	Workspace string
	Roots     []string
	Docs      []*Document
	SymbolMap map[string][]Symbol
	Postings  map[string][]posting
	DF        map[string]int
	AvgLen    float64
	BuildTime time.Duration
	BuiltAt   time.Time
	Files     int
	Chunks    int
	SymbolCnt int
	Errors    int
	Workers   int
}

func (x *Index) Status() string {
	return fmt.Sprintf(
		"workspace=%s\nroots=%s\nfiles=%d chunks=%d symbols=%d errors=%d\nindex_workers=%d build_ms=%d built_at=%s\n",
		x.Workspace, strings.Join(x.Roots, ","), x.Files, x.Chunks, x.SymbolCnt, x.Errors,
		x.Workers, x.BuildTime.Milliseconds(), x.BuiltAt.Format(time.RFC3339),
	)
}

func collectFiles(roots []string) ([]string, error) {
	seen := make(map[string]bool)
	var files []string
	for _, root := range roots {
		info, err := os.Stat(root)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("not a directory: %s", root)
		}
		err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				switch d.Name() {
				case ".git", "node_modules", "vendor", "dist", "build", ".dsh", ".agents", "__pycache__", "target":
					return filepath.SkipDir
				}
				return nil
			}
			ext := strings.ToLower(filepath.Ext(path))
			if ext != ".go" && ext != ".md" && ext != ".markdown" && ext != ".txt" {
				return nil
			}
			info, err := d.Info()
			if err != nil || info.Size() == 0 || info.Size() > maxFileBytes {
				return nil
			}
			abs, err := filepath.Abs(path)
			if err != nil || seen[abs] {
				return nil
			}
			seen[abs] = true
			files = append(files, abs)
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Strings(files)
	return files, nil
}

func BuildIndex(workspace string, roots []string, workers int) (*Index, error) {
	if workers <= 0 {
		workers = runtime.GOMAXPROCS(0)
		if workers > 8 {
			workers = 8
		}
		if workers < 2 {
			workers = 2
		}
	}
	files, err := collectFiles(roots)
	if err != nil {
		return nil, err
	}

	start := time.Now()
	results := make([]*Document, len(files))
	var errors atomic.Int64
	pool := agilepool.NewPool(agilepool.NewConfig(
		agilepool.WithWorkerNumCapacity(int64(workers)),
		agilepool.WithBlockMode(agilepool.BLOCK),
		agilepool.WithTaskQueueSize(int64(len(files)+1)),
		agilepool.WithStatsSamplePeriod(10*time.Millisecond),
	))
	for i, path := range files {
		i, path := i, path
		pool.Submit(agilepool.TaskFunc(func() error {
			doc, err := parseFile(workspace, path)
			if err != nil {
				// A bad file must not take down the index.
				errors.Add(1)
				return nil
			}
			results[i] = doc
			return nil
		}))
	}
	pool.Wait()
	pool.Close()

	x := &Index{
		Workspace: workspace,
		Roots:     append([]string(nil), roots...),
		SymbolMap: make(map[string][]Symbol),
		Postings:  make(map[string][]posting),
		DF:        make(map[string]int),
		Workers:   workers,
		Errors:    int(errors.Load()),
	}
	for _, doc := range results {
		if doc != nil {
			x.Docs = append(x.Docs, doc)
		}
	}
	x.build()
	x.BuildTime = time.Since(start)
	x.BuiltAt = time.Now()
	return x, nil
}

func (x *Index) build() {
	x.Files = len(x.Docs)
	totalTokens := 0
	for di, doc := range x.Docs {
		x.SymbolCnt += len(doc.Symbols)
		for _, sym := range doc.Symbols {
			key := strings.ToLower(sym.Name)
			x.SymbolMap[key] = append(x.SymbolMap[key], sym)
		}
		for ci := range doc.Chunks {
			ch := &doc.Chunks[ci]
			toks := tokenize(ch.Text)
			ch.Length = len(toks)
			totalTokens += len(toks)
			x.Chunks++
			tf := make(map[string]int, len(toks))
			for _, t := range toks {
				tf[t]++
			}
			for t, n := range tf {
				x.Postings[t] = append(x.Postings[t], posting{Doc: di, Chunk: ci, TF: n})
				x.DF[t]++
			}
		}
	}
	if x.Chunks > 0 {
		x.AvgLen = float64(totalTokens) / float64(x.Chunks)
	}
	if x.AvgLen == 0 {
		x.AvgLen = 1
	}
}

func parseFile(workspace, abs string) (*Document, error) {
	src, err := os.ReadFile(abs)
	if err != nil {
		return nil, err
	}
	rel, err := filepath.Rel(workspace, abs)
	if err != nil || strings.HasPrefix(rel, "..") {
		rel = abs
	}
	rel = filepath.ToSlash(rel)
	doc := &Document{Path: rel, AbsPath: abs, Ext: strings.ToLower(filepath.Ext(abs)), Size: int64(len(src))}
	switch doc.Ext {
	case ".go":
		if err := parseGo(doc, abs, src); err != nil {
			return nil, err
		}
	case ".md", ".markdown":
		parseMarkdown(doc, src)
	default:
		parseText(doc, src)
	}
	return doc, nil
}

func parseGo(doc *Document, abs string, src []byte) error {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, abs, src, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		return err
	}
	lines := strings.Split(string(src), "\n")
	pkg := f.Name.Name
	doc.Outline = append(doc.Outline, OutlineItem{Kind: "package", Name: pkg, Line: fset.Position(f.Package).Line})

	add := func(start, end int, heading string) {
		if start < 1 {
			start = 1
		}
		if end > len(lines) {
			end = len(lines)
		}
		if start > end {
			return
		}
		text := strings.Join(lines[start-1:end], "\n")
		if len(text) > maxChunkChars {
			text = text[:maxChunkChars] + "\n…"
		}
		doc.Chunks = append(doc.Chunks, Chunk{
			Path: doc.Path, StartLine: start, EndLine: end, Heading: heading, Text: text,
		})
	}

	headerEnd := 20
	if headerEnd > len(lines) {
		headerEnd = len(lines)
	}
	add(1, headerEnd, "package "+pkg)

	for _, decl := range f.Decls {
		start := fset.Position(decl.Pos()).Line
		end := fset.Position(decl.End()).Line
		chunkStart := start
		if chunkStart > 2 {
			chunkStart -= 2
		}
		switch d := decl.(type) {
		case *ast.FuncDecl:
			kind := "func"
			recv := ""
			if d.Recv != nil && len(d.Recv.List) > 0 {
				kind = "method"
				recv = exprString(d.Recv.List[0].Type)
			}
			sig := ""
			if start-1 < len(lines) {
				sig = strings.TrimSpace(lines[start-1])
			}
			sym := Symbol{Name: d.Name.Name, Kind: kind, Signature: sig, Receiver: recv, Package: pkg, File: doc.Path, Line: start}
			doc.Symbols = append(doc.Symbols, sym)
			doc.Outline = append(doc.Outline, OutlineItem{Kind: kind, Name: sym.Name, Line: start})
			add(chunkStart, end, sym.Name)
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				switch s := spec.(type) {
				case *ast.TypeSpec:
					line := fset.Position(s.Pos()).Line
					sig := ""
					if line-1 < len(lines) {
						sig = strings.TrimSpace(lines[line-1])
					}
					sym := Symbol{Name: s.Name.Name, Kind: "type", Signature: sig, Package: pkg, File: doc.Path, Line: line}
					doc.Symbols = append(doc.Symbols, sym)
					doc.Outline = append(doc.Outline, OutlineItem{Kind: "type", Name: sym.Name, Line: line})
					add(line, fset.Position(s.End()).Line, sym.Name)
					if st, ok := s.Type.(*ast.StructType); ok {
						for _, field := range st.Fields.List {
							fline := fset.Position(field.Pos()).Line
							fsig := ""
							if fline-1 < len(lines) {
								fsig = strings.TrimSpace(lines[fline-1])
							}
							for _, name := range field.Names {
								doc.Symbols = append(doc.Symbols, Symbol{
									Name: name.Name, Kind: "field", Signature: fsig, Package: pkg, File: doc.Path, Line: fline,
								})
							}
						}
					}
				case *ast.ValueSpec:
					for _, name := range s.Names {
						line := fset.Position(name.Pos()).Line
						kind := strings.ToLower(d.Tok.String())
						sym := Symbol{Name: name.Name, Kind: kind, Package: pkg, File: doc.Path, Line: line}
						doc.Symbols = append(doc.Symbols, sym)
						doc.Outline = append(doc.Outline, OutlineItem{Kind: kind, Name: sym.Name, Line: line})
					}
				}
			}
		}
	}
	return nil
}

func exprString(e ast.Expr) string {
	switch t := e.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.StarExpr:
		return "*" + exprString(t.X)
	case *ast.SelectorExpr:
		return exprString(t.X) + "." + t.Sel.Name
	case *ast.IndexExpr:
		return exprString(t.X) + "[" + exprString(t.Index) + "]"
	case *ast.IndexListExpr:
		var parts []string
		for _, idx := range t.Indices {
			parts = append(parts, exprString(idx))
		}
		return exprString(t.X) + "[" + strings.Join(parts, ",") + "]"
	default:
		return ""
	}
}

func parseMarkdown(doc *Document, src []byte) {
	lines := strings.Split(string(src), "\n")
	type heading struct {
		line  int
		level int
		text  string
	}
	var heads []heading
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "#") {
			continue
		}
		level := 0
		for level < len(trimmed) && trimmed[level] == '#' {
			level++
		}
		if level == 0 || level > 6 || level >= len(trimmed) || trimmed[level] != ' ' {
			continue
		}
		text := strings.TrimSpace(trimmed[level:])
		heads = append(heads, heading{line: i + 1, level: level, text: text})
		doc.Outline = append(doc.Outline, OutlineItem{Kind: "heading", Name: text, Line: i + 1, Level: level})
	}
	if len(heads) == 0 {
		addTextChunks(doc, lines, 1, len(lines), "")
		return
	}
	if heads[0].line > 1 {
		addTextChunks(doc, lines, 1, heads[0].line-1, "")
	}
	for i, h := range heads {
		end := len(lines)
		if i+1 < len(heads) {
			end = heads[i+1].line - 1
		}
		addTextChunks(doc, lines, h.line, end, h.text)
	}
}

func addTextChunks(doc *Document, lines []string, start, end int, heading string) {
	if start < 1 {
		start = 1
	}
	if end > len(lines) {
		end = len(lines)
	}
	const step = 60
	for s := start; s <= end; s += step {
		e := s + step - 1
		if e > end {
			e = end
		}
		text := strings.Join(lines[s-1:e], "\n")
		if strings.TrimSpace(text) == "" {
			continue
		}
		if len(text) > maxChunkChars {
			text = text[:maxChunkChars] + "\n…"
		}
		doc.Chunks = append(doc.Chunks, Chunk{Path: doc.Path, StartLine: s, EndLine: e, Heading: heading, Text: text})
	}
}

func parseText(doc *Document, src []byte) {
	lines := strings.Split(string(src), "\n")
	addTextChunks(doc, lines, 1, len(lines), "")
}

func tokenize(s string) []string {
	var out []string
	rs := []rune(s)
	for i := 0; i < len(rs); {
		r := rs[i]
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' {
			start := i
			for i < len(rs) {
				r = rs[i]
				if !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_') {
					break
				}
				i++
			}
			addWord(string(rs[start:i]), &out)
			continue
		}
		if isCJK(r) {
			start := i
			for i < len(rs) && isCJK(rs[i]) {
				i++
			}
			runes := rs[start:i]
			if len(runes) == 1 {
				out = append(out, string(runes))
			} else {
				for j := 0; j+1 < len(runes); j++ {
					out = append(out, string(runes[j:j+2]))
				}
			}
			continue
		}
		i++
	}
	return out
}

func addWord(w string, out *[]string) {
	lower := strings.ToLower(w)
	if lower != "" {
		*out = append(*out, lower)
	}
	start := 0
	for i := 1; i < len(w); i++ {
		if w[i] >= 'A' && w[i] <= 'Z' && w[i-1] >= 'a' && w[i-1] <= 'z' {
			part := strings.ToLower(w[start:i])
			if part != "" && part != lower {
				*out = append(*out, part)
			}
			start = i
		}
	}
	if start > 0 {
		part := strings.ToLower(w[start:])
		if part != "" && part != lower {
			*out = append(*out, part)
		}
	}
}

func isCJK(r rune) bool {
	return r >= 0x4E00 && r <= 0x9FFF
}

func (x *Index) Search(query string, limit int, pathPrefix string) []SearchResult {
	if limit <= 0 {
		limit = 8
	}
	if limit > maxSearchLimit {
		limit = maxSearchLimit
	}
	tokens := tokenize(query)
	if len(tokens) == 0 {
		return nil
	}
	uniq := make(map[string]bool)
	for _, t := range tokens {
		uniq[t] = true
	}
	scores := make(map[[2]int]float64)
	for t := range uniq {
		p := x.Postings[t]
		if len(p) == 0 {
			continue
		}
		df := float64(x.DF[t])
		idf := math.Log(1 + (float64(x.Chunks)-df+0.5)/(df+0.5))
		for _, post := range p {
			doc := x.Docs[post.Doc]
			if pathPrefix != "" && !strings.HasPrefix(doc.Path, pathPrefix) {
				continue
			}
			dl := float64(doc.Chunks[post.Chunk].Length)
			tf := float64(post.TF)
			denom := tf + 1.2*(1-0.75+0.75*dl/x.AvgLen)
			key := [2]int{post.Doc, post.Chunk}
			scores[key] += idf * tf * 2.2 / denom
		}
	}
	lowerQuery := strings.ToLower(query)
	for key := range scores {
		doc := x.Docs[key[0]]
		ch := doc.Chunks[key[1]]
		if strings.Contains(strings.ToLower(ch.Text), lowerQuery) {
			scores[key] += 2.0
		}
	}
	type scored struct {
		key   [2]int
		score float64
	}
	list := make([]scored, 0, len(scores))
	for k, v := range scores {
		list = append(list, scored{key: k, score: v})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].score > list[j].score })
	if len(list) > limit {
		list = list[:limit]
	}
	out := make([]SearchResult, 0, len(list))
	for _, s := range list {
		doc := x.Docs[s.key[0]]
		ch := doc.Chunks[s.key[1]]
		out = append(out, SearchResult{
			Path: doc.Path, Line: ch.StartLine, EndLine: ch.EndLine, Heading: ch.Heading,
			Score: s.score, Snippet: snippet(ch.Text, query, 220),
		})
	}
	return out
}

func snippet(text, query string, width int) string {
	lower := strings.ToLower(text)
	q := strings.ToLower(query)
	idx := strings.Index(lower, q)
	if idx < 0 {
		for _, t := range tokenize(query) {
			if i := strings.Index(lower, t); i >= 0 {
				idx = i
				break
			}
		}
	}
	if idx < 0 {
		idx = 0
	}
	start := idx - width/2
	if start < 0 {
		start = 0
	}
	end := start + width
	if end > len(text) {
		end = len(text)
	}
	for start > 0 && !utf8.RuneStart(text[start]) {
		start--
	}
	for end < len(text) && !utf8.RuneStart(text[end]) {
		end++
	}
	s := strings.Join(strings.Fields(text[start:end]), " ")
	if start > 0 {
		s = "…" + s
	}
	if end < len(text) {
		s += "…"
	}
	return s
}

func (x *Index) Symbols(query string, limit int) []Symbol {
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return nil
	}
	var exact, prefix, contains []Symbol
	for key, syms := range x.SymbolMap {
		for _, sym := range syms {
			switch {
			case key == q:
				exact = append(exact, sym)
			case strings.HasPrefix(key, q):
				prefix = append(prefix, sym)
			case strings.Contains(key, q):
				contains = append(contains, sym)
			}
		}
	}
	out := append(append(exact, prefix...), contains...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].File != out[j].File {
			return out[i].File < out[j].File
		}
		return out[i].Line < out[j].Line
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

func (x *Index) Refs(symbol string, limit int) []SearchResult {
	if limit <= 0 {
		limit = 20
	}
	if limit > 50 {
		limit = 50
	}
	tokens := tokenize(symbol)
	if len(tokens) == 0 {
		return nil
	}
	seen := make(map[[2]int]bool)
	out := make([]SearchResult, 0)
	for _, token := range tokens {
		for _, post := range x.Postings[token] {
			key := [2]int{post.Doc, post.Chunk}
			if seen[key] {
				continue
			}
			seen[key] = true
			doc := x.Docs[post.Doc]
			ch := doc.Chunks[post.Chunk]
			out = append(out, SearchResult{
				Path: doc.Path, Line: ch.StartLine, EndLine: ch.EndLine, Heading: ch.Heading,
				Score: float64(post.TF), Snippet: snippet(ch.Text, symbol, 180),
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		return out[i].Line < out[j].Line
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

func (x *Index) Outline(path string) []OutlineItem {
	path = filepath.ToSlash(path)
	for _, doc := range x.Docs {
		if doc.Path == path || strings.HasSuffix(doc.Path, "/"+path) || filepath.Base(doc.Path) == filepath.Base(path) {
			return doc.Outline
		}
	}
	return nil
}

func (x *Index) DocCount() int { return len(x.Docs) }
