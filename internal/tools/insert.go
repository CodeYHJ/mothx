package tools

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

const insertInMemoryLimit int64 = 32 * 1024 * 1024

// InsertTool inserts content at one structural position in a text file.
type InsertTool struct{ registry *Registry }

func NewInsertTool(r *Registry) *InsertTool { return &InsertTool{registry: r} }
func (t *InsertTool) Name() string          { return "insert" }
func (t *InsertTool) Description() string {
	return "Insert content into an existing UTF-8 text file at the beginning, end, before a line, or after a line. Only inserts at one structural position; use edit for exact text replacement or deletion and write for whole-file creation or overwrite."
}
func (t *InsertTool) PromptSnippet() string { return "Insert content at a structural file position" }
func (t *InsertTool) PromptGuidelines() []string {
	return []string{
		"Use insert only for one insertion at the file head, tail, before a 1-based line, or after a 1-based line",
		"Use edit for exact text replacements, deletions, or text-matched insertions",
		"Use write for creating or completely rewriting a file",
	}
}
func (t *InsertTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"},"content":{"type":"string"},"position":{"type":"object","properties":{"type":{"type":"string","enum":["head","tail","before_line","after_line"]},"line":{"type":"integer","minimum":1}},"required":["type"]},"create_if_missing":{"type":"boolean","default":false},"ensure_newline":{"type":"boolean","default":true},"dedupe":{"type":"object","properties":{"enabled":{"type":"boolean","default":false},"mode":{"type":"string","enum":["exact","trimmed","line"],"default":"exact"}}},"dry_run":{"type":"boolean","default":false}},"required":["path","content","position"]}`)
}

type insertPosition struct {
	Type string
	Line int
}
type LineRange struct{ Number, Start, End int }
type insertDedupe struct {
	Enabled bool
	Mode    string
}

func (t *InsertTool) Execute(ctx context.Context, params map[string]any) (ToolResult, error) {
	path, _ := params["path"].(string)
	if path == "" {
		return ToolResult{}, fmt.Errorf("path is required")
	}
	content, ok := params["content"].(string)
	if !ok || content == "" {
		return ToolResult{}, fmt.Errorf("content is required and must not be empty")
	}
	if !utf8.ValidString(content) {
		return ToolResult{}, fmt.Errorf("content is not valid UTF-8")
	}
	position, err := parseInsertPosition(params["position"])
	if err != nil {
		return ToolResult{}, err
	}
	createIfMissing, ok := boolParam(params, "create_if_missing", false)
	if !ok {
		return ToolResult{}, fmt.Errorf("create_if_missing must be a boolean")
	}
	ensureNewline, ok := boolParam(params, "ensure_newline", true)
	if !ok {
		return ToolResult{}, fmt.Errorf("ensure_newline must be a boolean")
	}
	if createIfMissing && position.Type != "head" && position.Type != "tail" {
		return ToolResult{}, fmt.Errorf("create_if_missing only supports head or tail")
	}
	dedupe, err := parseInsertDedupe(params["dedupe"])
	if err != nil {
		return ToolResult{}, err
	}
	dryRun, ok := boolParam(params, "dry_run", false)
	if !ok {
		return ToolResult{}, fmt.Errorf("dry_run must be a boolean")
	}
	for _, key := range []string{"match", "match_mode", "occurrence"} {
		if _, exists := params[key]; exists {
			return ToolResult{}, fmt.Errorf("%s is not supported; use edit for text-matched insertion", key)
		}
	}
	path, err = t.registry.ResolvePath(path)
	if err != nil {
		return ToolResult{}, fmt.Errorf("invalid path: %w", err)
	}
	release, err := t.registry.acquireFileLock(ctx, path, t.Name())
	if err != nil {
		return ToolResult{}, err
	}
	defer release()
	infoBefore, statErr := os.Stat(path)
	if statErr == nil && infoBefore.Mode().IsRegular() && infoBefore.Size() > insertInMemoryLimit {
		return t.executeLargeInsert(ctx, path, content, position, createIfMissing, ensureNewline, dedupe, dryRun, infoBefore)
	}
	data, err := os.ReadFile(path)
	var info os.FileInfo
	if err == nil {
		info, err = os.Stat(path)
		if err != nil {
			return ToolResult{}, fmt.Errorf("stat file: %w", err)
		}
		if !info.Mode().IsRegular() {
			return ToolResult{}, fmt.Errorf("path is not a regular file: %s", path)
		}
		if !utf8.Valid(data) {
			return ToolResult{}, fmt.Errorf("file is not valid UTF-8: %s", path)
		}
		if bytes.IndexByte(data, 0) >= 0 {
			return ToolResult{}, fmt.Errorf("refusing to modify binary file: %s", path)
		}
	} else if !createIfMissing || !os.IsNotExist(err) {
		return ToolResult{}, fmt.Errorf("read file: %w", err)
	}
	if info == nil {
		data = nil
	}
	if dedupe.Enabled {
		var matched bool
		content, matched = applyInsertDedupe(string(data), content, dedupe.Mode)
		if matched && content == "" {
			return NewInsertToolResult(fmt.Sprintf("Content already exists; no changes made to %s", path), nil, &InsertResult{Path: path, Deduped: true}), nil
		}
	}
	offset, err := computeInsertOffset(data, position)
	if err != nil {
		return ToolResult{}, err
	}
	inserted := normalizeInsertContent(data, offset, []byte(content), position.Type, ensureNewline)
	newData := make([]byte, 0, len(data)+len(inserted))
	newData = append(newData, data[:offset]...)
	newData = append(newData, inserted...)
	newData = append(newData, data[offset:]...)
	diff := BuildFileDiff(path, string(data), string(newData))
	result := &InsertResult{Path: path, Changed: true, DryRun: dryRun, InsertedBytes: len(inserted), Position: position.Type, Line: position.Line, Offset: int64(offset)}
	if dryRun {
		return NewInsertToolResult(fmt.Sprintf("Would insert %d bytes into %s\n%s", len(inserted), path, formatFileDiffSummary(diff)), diff, result), nil
	}
	mode := os.FileMode(0644)
	if info != nil {
		currentInfo, statErr := os.Stat(path)
		currentData, readErr := os.ReadFile(path)
		if statErr != nil || readErr != nil || !bytes.Equal(currentData, data) || currentInfo.Mode().Perm() != info.Mode().Perm() {
			return ToolResult{}, fmt.Errorf("concurrent modification detected: %s", path)
		}
		mode = info.Mode().Perm()
	}
	if err := writeFileAtomicWithMode(path, newData, mode); err != nil {
		return ToolResult{}, fmt.Errorf("atomic write failed: %w", err)
	}
	return NewInsertToolResult(fmt.Sprintf("Inserted %d bytes into %s\n%s", len(inserted), path, formatFileDiffSummary(diff)), diff, result), nil
}

func boolParam(params map[string]any, name string, def bool) (bool, bool) {
	v, exists := params[name]
	if !exists {
		return def, true
	}
	b, ok := v.(bool)
	return b, ok
}
func parseInsertPosition(raw any) (insertPosition, error) {
	m, ok := raw.(map[string]any)
	if !ok {
		return insertPosition{}, fmt.Errorf("position is required and must be an object")
	}
	for key := range m {
		if key != "type" && key != "line" {
			return insertPosition{}, fmt.Errorf("unsupported position field: %s", key)
		}
	}
	typ, _ := m["type"].(string)
	if typ != "head" && typ != "tail" && typ != "before_line" && typ != "after_line" {
		return insertPosition{}, fmt.Errorf("invalid position type: %s", typ)
	}
	line, has := m["line"]
	if typ == "head" || typ == "tail" {
		if has {
			return insertPosition{}, fmt.Errorf("line is not allowed for position type %s", typ)
		}
		return insertPosition{Type: typ}, nil
	}
	if !has {
		return insertPosition{}, fmt.Errorf("line is required for position type %s", typ)
	}
	n, ok := integerParam(line)
	if !ok || n < 1 {
		return insertPosition{}, fmt.Errorf("line must be an integer greater than or equal to 1")
	}
	return insertPosition{Type: typ, Line: n}, nil
}
func integerParam(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case float64:
		return int(n), n == float64(int(n))
	default:
		return 0, false
	}
}
func parseInsertDedupe(raw any) (insertDedupe, error) {
	if raw == nil {
		return insertDedupe{}, nil
	}
	m, ok := raw.(map[string]any)
	if !ok {
		return insertDedupe{}, fmt.Errorf("dedupe must be an object")
	}
	for key := range m {
		if key != "enabled" && key != "mode" {
			return insertDedupe{}, fmt.Errorf("unsupported dedupe field: %s", key)
		}
	}
	d := insertDedupe{}
	if v, exists := m["enabled"]; exists {
		d.Enabled, ok = v.(bool)
		if !ok {
			return insertDedupe{}, fmt.Errorf("dedupe.enabled must be a boolean")
		}
	}
	d.Mode, _ = m["mode"].(string)
	if d.Mode == "" {
		d.Mode = "exact"
	}
	if d.Mode != "exact" && d.Mode != "trimmed" && d.Mode != "line" {
		return insertDedupe{}, fmt.Errorf("invalid dedupe mode: %s", d.Mode)
	}
	return d, nil
}
func buildInsertLineIndex(data []byte) []LineRange {
	if len(data) == 0 {
		return nil
	}
	lines := make([]LineRange, 0)
	start := 0
	for i, b := range data {
		if b == '\n' {
			lines = append(lines, LineRange{len(lines) + 1, start, i + 1})
			start = i + 1
		}
	}
	if start < len(data) {
		lines = append(lines, LineRange{len(lines) + 1, start, len(data)})
	}
	return lines
}
func computeInsertOffset(data []byte, p insertPosition) (int, error) {
	switch p.Type {
	case "head":
		return 0, nil
	case "tail":
		return len(data), nil
	case "before_line", "after_line":
		lines := buildInsertLineIndex(data)
		if p.Line < 1 || p.Line > len(lines) {
			return 0, fmt.Errorf("line %d out of range; file has %d lines", p.Line, len(lines))
		}
		if p.Type == "before_line" {
			return lines[p.Line-1].Start, nil
		}
		return lines[p.Line-1].End, nil
	default:
		return 0, fmt.Errorf("invalid position type: %s", p.Type)
	}
}
func normalizeInsertContent(data []byte, offset int, content []byte, typ string, ensure bool) []byte {
	if !ensure {
		return content
	}
	r := append([]byte(nil), content...)
	has := len(r) > 0 && r[len(r)-1] == '\n'
	switch typ {
	case "head", "before_line":
		if len(data) > 0 && !has {
			r = append(r, '\n')
		}
	case "tail":
		if offset > 0 && data[offset-1] != '\n' {
			r = append([]byte{'\n'}, r...)
		}
	case "after_line":
		if offset > 0 && data[offset-1] != '\n' {
			r = append([]byte{'\n'}, r...)
		}
		if !has {
			r = append(r, '\n')
		}
	}
	return r
}
func applyInsertDedupe(existing, content, mode string) (string, bool) {
	switch mode {
	case "trimmed":
		if strings.Contains(strings.TrimSpace(existing), strings.TrimSpace(content)) {
			return "", true
		}
	case "line":
		set := map[string]struct{}{}
		for _, l := range strings.Split(strings.TrimSuffix(existing, "\n"), "\n") {
			if l != "" {
				set[strings.TrimSpace(l)] = struct{}{}
			}
		}
		var missing []string
		for _, l := range strings.Split(strings.TrimSuffix(content, "\n"), "\n") {
			if l == "" || func() bool { _, ok := set[strings.TrimSpace(l)]; return !ok }() {
				missing = append(missing, l)
			}
		}
		if len(missing) == 0 {
			return "", true
		}
		return strings.Join(missing, "\n"), false
	default:
		if strings.Contains(existing, content) {
			return "", true
		}
	}
	return content, false
}
func insertDedupeMatches(existing, content, mode string) bool {
	_, ok := applyInsertDedupe(existing, content, mode)
	return ok
}

func (t *InsertTool) executeLargeInsert(ctx context.Context, path, content string, p insertPosition, create, ensure bool, d insertDedupe, dry bool, info os.FileInfo) (ToolResult, error) {
	if create {
		return ToolResult{}, fmt.Errorf("large-file create_if_missing is not supported")
	}
	if d.Enabled {
		return ToolResult{}, fmt.Errorf("dedupe is not supported for files larger than %d bytes", insertInMemoryLimit)
	}
	if err := ctx.Err(); err != nil {
		return ToolResult{}, err
	}
	f, err := os.Open(path)
	if err != nil {
		return ToolResult{}, err
	}
	defer f.Close()
	if err := validateLargeText(f); err != nil {
		return ToolResult{}, err
	}
	off, err := scanLargeInsertOffset(f, p)
	if err != nil {
		return ToolResult{}, err
	}
	var before byte
	if off > 0 {
		_, err = f.Seek(off-1, io.SeekStart)
		if err != nil {
			return ToolResult{}, err
		}
		if _, err = io.ReadFull(f, []byte{before}); err != nil {
			return ToolResult{}, err
		}
	}
	inserted := normalizeLargeInsertContent([]byte(content), before, off, info.Size(), p.Type, ensure)
	r := &InsertResult{Path: path, Changed: true, DryRun: dry, InsertedBytes: len(inserted), Position: p.Type, Line: p.Line, Offset: off}
	if dry {
		return NewInsertToolResult(fmt.Sprintf("Would insert %d bytes into %s (large file; diff omitted)", len(inserted), path), nil, r), nil
	}
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		return ToolResult{}, err
	}
	if err = streamAtomicInsert(path, f, off, inserted, info.Mode().Perm()); err != nil {
		return ToolResult{}, fmt.Errorf("atomic write failed: %w", err)
	}
	return NewInsertToolResult(fmt.Sprintf("Inserted %d bytes into %s (large file; diff omitted)", len(inserted), path), nil, r), nil
}
func validateLargeText(f *os.File) error {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	r := bufio.NewReaderSize(f, 128*1024)
	for {
		p, err := r.ReadBytes('\n')
		if len(p) > 0 {
			if !utf8.Valid(p) {
				return fmt.Errorf("file is not valid UTF-8")
			}
			if bytes.IndexByte(p, 0) >= 0 {
				return fmt.Errorf("refusing to modify binary file")
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}
func scanLargeInsertOffset(f *os.File, p insertPosition) (int64, error) {
	if p.Type == "head" {
		return 0, nil
	}
	if p.Type == "tail" {
		i, e := f.Stat()
		if e != nil {
			return 0, e
		}
		return i.Size(), nil
	}
	if _, e := f.Seek(0, io.SeekStart); e != nil {
		return 0, e
	}
	r := bufio.NewReaderSize(f, 128*1024)
	var off int64
	line := 1
	for {
		part, e := r.ReadBytes('\n')
		if len(part) > 0 {
			end := off + int64(len(part))
			if line == p.Line {
				if p.Type == "before_line" {
					return off, nil
				}
				return end, nil
			}
			off = end
			if part[len(part)-1] == '\n' {
				line++
			}
		}
		if e == io.EOF {
			break
		}
		if e != nil {
			return 0, e
		}
	}
	return 0, fmt.Errorf("line %d out of range; file has %d lines", p.Line, line)
}
func normalizeLargeInsertContent(c []byte, b byte, off, size int64, typ string, ensure bool) []byte {
	if !ensure {
		return c
	}
	r := append([]byte(nil), c...)
	if (typ == "head" || typ == "before_line") && size > 0 && (len(r) == 0 || r[len(r)-1] != '\n') {
		r = append(r, '\n')
	}
	if (typ == "tail" || typ == "after_line") && off > 0 && b != '\n' {
		r = append([]byte{'\n'}, r...)
	}
	if typ == "after_line" && (len(r) == 0 || r[len(r)-1] != '\n') {
		r = append(r, '\n')
	}
	return r
}
func streamAtomicInsert(path string, src *os.File, off int64, inserted []byte, mode os.FileMode) error {
	tmp, e := os.CreateTemp(filepath.Dir(path), ".mothx-insert-*")
	if e != nil {
		return e
	}
	tp := tmp.Name()
	clean := func() { _ = tmp.Close(); _ = os.Remove(tp) }
	if e = tmp.Chmod(mode); e != nil {
		clean()
		return e
	}
	if _, e = io.CopyN(tmp, src, off); e != nil && e != io.EOF {
		clean()
		return e
	}
	if _, e = tmp.Write(inserted); e != nil {
		clean()
		return e
	}
	if _, e = src.Seek(off, io.SeekStart); e != nil {
		clean()
		return e
	}
	if _, e = io.Copy(tmp, src); e != nil {
		clean()
		return e
	}
	if e = tmp.Sync(); e != nil {
		clean()
		return e
	}
	if e = tmp.Close(); e != nil {
		_ = os.Remove(tp)
		return e
	}
	if e = os.Rename(tp, path); e != nil {
		_ = os.Remove(tp)
		return e
	}
	return nil
}

func writeFileAtomicWithMode(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".mothx-insert-*")
	if err != nil {
		return err
	}
	tp := tmp.Name()
	clean := func() { _ = tmp.Close(); _ = os.Remove(tp) }
	if err := tmp.Chmod(mode.Perm()); err != nil {
		clean()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		clean()
		return err
	}
	if err := tmp.Sync(); err != nil {
		clean()
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tp)
		return err
	}
	if err := os.Rename(tp, path); err != nil {
		_ = os.Remove(tp)
		return err
	}
	return nil
}
