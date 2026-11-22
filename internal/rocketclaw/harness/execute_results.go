package harness

import (
	"bufio"
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	starjson "go.starlark.net/lib/json"
	"go.starlark.net/starlark"
	"golang.org/x/sys/unix"

	"github.com/Rocketable/platform/internal/rocketclaw/codemode"
)

const (
	executeHeadMaxLines = 2000
	executeHeadMaxBytes = 50 * 1024
	defaultSpillRel     = ".rocketcode/spill"
	deniedSpillAccess   = "execute output storage is private; use load_execute_result"
	retainedResultTTL   = 7 * 24 * time.Hour
	executeReturnBudget = 20 << 20
)

type RetainedFiles struct {
	ResultID string
}

type toolResultError struct {
	result ToolResult
	err    error
}

func (e toolResultError) Error() string { return e.result.Output }

func (e toolResultError) Unwrap() error { return e.err }

type returnedFile struct {
	file     *codemode.File
	deferred bool
}

type executeReturn struct {
	numbers map[string]int
	files   []returnedFile
}

func convertExecuteReturn(value starlark.Value, suffix string) (text string, files []returnedFile, failed bool, err error) {
	conversion := executeReturn{numbers: map[string]int{}}

	text, err = conversion.text(value)
	if err != nil {
		return "", nil, false, err
	}

	head, _ := clipExecuteHead(text + suffix)
	remaining, deferred := executeReturnBudget-len(head), false

	for i := range conversion.files {
		if size := conversion.files[i].file.Size(); size <= remaining {
			remaining -= size
		} else {
			conversion.files[i].deferred, deferred = true, true
		}
	}

	if deferred {
		if text, err = conversion.text(value); err != nil {
			return "", nil, false, err
		}
	}

	return text + suffix, conversion.files, codemode.IsFailure(value), nil
}

func (c *executeReturn) text(value starlark.Value) (string, error) {
	switch v := codemode.Decoded(value).(type) {
	case starlark.String:
		return string(v), nil
	case *codemode.File:
		if !isSupportedAttachmentMIME(v.Mime()) {
			return "Not attached: " + v.String() + ": the model cannot take " + v.Mime(), nil
		}

		if n := c.number(v); !c.files[n-1].deferred {
			return fmt.Sprintf("Attached file %d: %s", n, v), nil
		}

		return "", nil
	case codemode.Result:
		return v.String(), nil
	}

	encoded, err := starlark.Call(&starlark.Thread{Name: "execute return"}, starjson.Module.Members["encode"], starlark.Tuple{c.placeholders(value, nil)}, nil)
	if err != nil {
		return "", fmt.Errorf("encode main result: %w", err)
	}

	text, _ := starlark.AsString(encoded)

	return text, nil
}

func (c *executeReturn) placeholders(value starlark.Value, path []starlark.Value) starlark.Value {
	value = codemode.Decoded(value)

	switch v := value.(type) {
	case *codemode.File:
		return c.placeholder(v)
	case starlark.Tuple:
		return starlark.Tuple(c.elements(v, path))
	case *starlark.List:
		if slices.Contains(path, value) {
			return value
		}

		return starlark.NewList(c.elements(v, append(path, value)))
	case *starlark.Dict:
		if slices.Contains(path, value) {
			return value
		}

		items := v.Items()
		slices.SortFunc(items, func(a, b starlark.Tuple) int {
			x, _ := starlark.AsString(a[0])
			y, _ := starlark.AsString(b[0])

			return strings.Compare(x, y)
		})

		path = append(path, value)
		dict := starlark.NewDict(len(items))

		for _, item := range items {
			_ = dict.SetKey(item[0], c.placeholders(item[1], path))
		}

		return dict
	}

	return value
}

func (c *executeReturn) elements(seq starlark.Indexable, path []starlark.Value) []starlark.Value {
	elements := make([]starlark.Value, seq.Len())
	for i := range elements {
		elements[i] = c.placeholders(seq.Index(i), path)
	}

	return elements
}

func (c *executeReturn) placeholder(f *codemode.File) *starlark.Dict {
	fields := map[string]starlark.Value{"name": starlark.String(f.Name()), "mime": starlark.String(f.Mime()), "size": starlark.MakeInt(f.Size())}
	if width, height, ok := f.Dimensions(); ok {
		fields["width"], fields["height"] = starlark.MakeInt(width), starlark.MakeInt(height)
	}

	if isSupportedAttachmentMIME(f.Mime()) {
		n := c.number(f)
		fields["attachment"] = starlark.MakeInt(n)

		if c.files[n-1].deferred {
			fields["deferred"] = starlark.True
		}
	} else {
		fields["not_attached"] = starlark.True
	}

	dict := starlark.NewDict(len(fields))
	for key, field := range fields {
		_ = dict.SetKey(starlark.String(key), field)
	}

	return dict
}

func (c *executeReturn) number(f *codemode.File) int {
	n, ok := c.numbers[f.SHA256()]
	if !ok {
		c.files = append(c.files, returnedFile{file: f})
		n = len(c.files)
		c.numbers[f.SHA256()] = n
	}

	return n
}

func fileAttachment(f *codemode.File) Attachment {
	mimeType := normalizeMIME(f.Mime())

	return Attachment{MIME: mimeType, Filename: f.Name(), URL: "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString([]byte(f.Content()))}
}

func executeToolResult(text string, files []returnedFile, id string) ToolResult {
	if head, oversized := clipExecuteHead(text); oversized {
		text = strings.TrimRight(head, "\n") + "\n\n...output truncated...\n\n" +
			"Full output: result_id=\"" + id + "\".\n" +
			"Fetch the rest with the top-level load_execute_result tool, using this result_id, start_line, limit and line_numbers.\n"
	}

	result := ToolResult{Output: text}

	var held []string

	fetch := ""

	for i, f := range files {
		if !f.deferred {
			result.Attachments = append(result.Attachments, fileAttachment(f.file))

			continue
		}

		mebibytes := strings.TrimSuffix(strconv.FormatFloat(float64(f.file.Size())/(1<<20), 'f', 1, 64), ".0")
		held = append(held, fmt.Sprintf("%d %s (%s MiB)", i+1, f.file.Name(), mebibytes))
		fetch = cmp.Or(fetch, fmt.Sprintf("Fetch with load_execute_result(result_id=%q, attachment=%d).", id, i+1))
	}

	if len(held) == 0 {
		return result
	}

	if len(held) > 1 {
		fetch = fmt.Sprintf("Fetch each with load_execute_result(result_id=%q, attachment=n).", id)
	}

	trailer := "Not attached (over this call's 20 MiB budget): " + strings.Join(held, ", ") + ". " + fetch
	if result.Output != "" {
		trailer = strings.TrimRight(result.Output, "\n") + "\n\n" + trailer
	}

	result.Output = trailer

	return result
}

type storedExecuteFile struct {
	Number   int    `json:"number"`
	Name     string `json:"name"`
	MIME     string `json:"mime"`
	Attached bool   `json:"attached"`
}

func writeExecuteResult(root *os.Root, base, text string, files []returnedFile, keepAttached bool) error {
	if err := root.MkdirAll(filepath.Dir(base), 0o700); err != nil {
		return fmt.Errorf("create dir: %w", err)
	}

	if _, oversized := clipExecuteHead(text); oversized {
		if err := writeRootFile(root, base+".txt", text); err != nil {
			return err
		}
	}

	var stored []storedExecuteFile

	for i, f := range files {
		if f.deferred || keepAttached {
			stored = append(stored, storedExecuteFile{Number: i + 1, Name: f.file.Name(), MIME: f.file.Mime(), Attached: !f.deferred})
		}
	}

	if len(stored) == 0 {
		return nil
	}

	for _, entry := range stored {
		if err := writeRootFile(root, fmt.Sprintf("%s.%d", base, entry.Number), files[entry.Number-1].file.Content()); err != nil {
			return err
		}
	}

	manifest, _ := json.Marshal(stored)

	return writeRootFile(root, base+".files.json", string(manifest))
}

func writeRootFile(root *os.Root, name, content string) error {
	file, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err == nil {
		_, err = file.WriteString(content)
		if errClose := file.Close(); err == nil {
			err = errClose
		}
	}

	if err != nil {
		return fmt.Errorf("write file: %w", err)
	}

	return nil
}

func isExecuteSpillPath(root *os.Root, spillRel, path string) bool {
	if spillRel == "" {
		return false
	}

	name, err := normalizeRootName(root, path)
	if err == nil && pathWithinRoot(spillRel, name) {
		return true
	}

	spill, err := root.Stat(spillRel)
	if err != nil {
		return !errors.Is(err, os.ErrNotExist)
	}

	for ancestor := filepath.Clean(path); ancestor != "." && ancestor != "/"; ancestor = filepath.Dir(ancestor) {
		info, errStat := root.Stat(ancestor)
		if filepath.IsAbs(ancestor) {
			info, errStat = os.Stat(ancestor)
		}

		if errors.Is(errStat, os.ErrNotExist) {
			continue
		}

		if errStat != nil || os.SameFile(spill, info) {
			return true
		}
	}

	return false
}

func clipExecuteHead(text string) (string, bool) {
	end := 0
	lines := 0

	for line := range strings.SplitAfterSeq(text[:min(len(text), executeHeadMaxBytes)], "\n") {
		end += len(line)

		lines++
		if lines == executeHeadMaxLines {
			break
		}
	}

	return text[:end], end < len(text)
}

var errUnknownExecuteResult = errors.New("unknown or expired execute result")

func retainExecuteResult(root *os.Root, dir, text string, files []returnedFile) (ToolResult, error) {
	if _, oversized := clipExecuteHead(text); !oversized && len(files) == 0 {
		return executeToolResult(text, files, ""), nil
	}

	sweepRetainedDir(root, dir)

	id := uuid.NewString()

	if err := writeExecuteResult(root, filepath.Join(dir, id), text, files, true); err != nil {
		return ToolResult{}, fmt.Errorf("write retained execute result: %w", err)
	}

	result := executeToolResult(text, files, id)
	if len(result.Attachments) > 0 {
		result.Data = RetainedFiles{ResultID: id}
	}

	return result, nil
}

func sweepRetainedDir(root *os.Root, dir string) {
	entries, _ := fs.ReadDir(root.FS(), dir)
	for _, entry := range entries {
		if info, err := entry.Info(); err == nil && time.Since(info.ModTime()) > retainedResultTTL {
			_ = root.Remove(filepath.Join(dir, entry.Name()))
		}
	}
}

func SweepRetainedResults(root *os.Root, dir string) {
	entries, _ := fs.ReadDir(root.FS(), dir)
	for _, entry := range entries {
		if entry.IsDir() {
			retained := filepath.Join(dir, entry.Name())
			sweepRetainedDir(root, retained)
			_ = root.Remove(retained)
		}
	}
}

func (l *looper) restoreTurnExecuteResults(turnID string) {
	l.spillMu.Lock()
	defer l.spillMu.Unlock()

	l.spillTurnID = turnID
	l.spillResults = make(map[string]string)

	if l.spillRel == "" || l.promptExpansion.root == nil {
		return
	}

	dir := filepath.ToSlash(filepath.Join(l.spillRel, turnID))

	entries, _ := fs.ReadDir(l.promptExpansion.root.FS(), dir)
	for _, entry := range entries {
		if id := strings.TrimSuffix(strings.TrimSuffix(entry.Name(), ".txt"), ".files.json"); id != entry.Name() && entry.Type().IsRegular() {
			l.spillResults[id] = dir + "/" + id + ".txt"
		}
	}
}

func (l *looper) deleteTurnExecuteResults() {
	l.spillMu.Lock()
	defer l.spillMu.Unlock()

	turnID := l.spillTurnID
	l.spillTurnID = ""
	l.spillResults = nil

	if turnID == "" || l.spillRel == "" || l.promptExpansion.root == nil {
		return
	}

	_ = l.promptExpansion.root.RemoveAll(filepath.ToSlash(filepath.Join(l.spillRel, turnID)))
}

func (l *looper) saveExecuteResult(text string, files []returnedFile) (ToolResult, error) {
	if _, oversized := clipExecuteHead(text); !oversized && !slices.ContainsFunc(files, func(f returnedFile) bool { return f.deferred }) {
		return executeToolResult(text, files, ""), nil
	}

	if l.promptExpansion.root == nil || l.spillRel == "" {
		return ToolResult{}, fmt.Errorf("execute result exceeds %d lines, %d bytes of text or the return budget and no spill directory is configured", executeHeadMaxLines, executeHeadMaxBytes)
	}

	l.spillMu.Lock()
	defer l.spillMu.Unlock()

	if l.spillTurnID == "" {
		return ToolResult{}, fmt.Errorf("execute result exceeds %d lines, %d bytes of text or the return budget outside a turn", executeHeadMaxLines, executeHeadMaxBytes)
	}

	id := uuid.NewString()

	base := filepath.ToSlash(filepath.Join(l.spillRel, l.spillTurnID, id))
	if err := writeExecuteResult(l.promptExpansion.root, base, text, files, false); err != nil {
		return ToolResult{}, fmt.Errorf("write execute spill: %w", err)
	}

	l.spillResults[id] = base + ".txt"

	return executeToolResult(text, files, id), nil
}

type loadExecuteResultParams struct {
	ResultID    string `json:"result_id"`
	StartLine   int    `json:"start_line"`
	Limit       int    `json:"limit"`
	LineNumbers bool   `json:"line_numbers"`
	Attachment  int    `json:"attachment"`
}

func (l *looper) loadExecuteResult(ctx context.Context, retainedRel string, params loadExecuteResultParams) (result ToolResult, err error) {
	if params.StartLine < 0 || params.Limit < 0 || params.Attachment < 0 {
		return ToolResult{}, errors.New("start_line, limit and attachment must not be negative")
	}

	start := max(params.StartLine, 1)

	limit := min(cmp.Or(params.Limit, executeHeadMaxLines), executeHeadMaxLines)

	l.spillMu.Lock()
	defer l.spillMu.Unlock()

	base, live, err := l.storedExecuteResult(retainedRel, params.ResultID)
	if err != nil {
		return ToolResult{}, err
	}

	if params.Attachment > 0 {
		return loadStoredAttachment(l.promptExpansion.root, base, live, params.Attachment)
	}

	file, err := openStoredExecuteResult(l.promptExpansion.root, base+".txt", live)
	if err != nil {
		return ToolResult{}, err
	}

	defer func() {
		if errClose := file.Close(); errClose != nil {
			err = errors.Join(err, fmt.Errorf("close execute result: %w", errClose))
		}
	}()

	const contentBudget = executeHeadMaxBytes - 1024

	reader := bufio.NewReader(file)

	var page strings.Builder

	lineNumber, count := 1, 0
	for count < limit {
		if err := ctx.Err(); err != nil {
			return ToolResult{}, fmt.Errorf("load execute result: %w", err)
		}

		if _, err := reader.Peek(1); err != nil {
			if err != io.EOF {
				return ToolResult{}, fmt.Errorf("read execute result: %w", err)
			}

			break
		}

		prefix := ""
		if params.LineNumbers {
			prefix = fmt.Sprintf("%d: ", lineNumber)
		}

		bound := contentBudget - len(prefix)

		var line []byte

		for {
			if err := ctx.Err(); err != nil {
				return ToolResult{}, fmt.Errorf("load execute result: %w", err)
			}

			fragment, err := reader.ReadSlice('\n')
			if lineNumber >= start {
				line = append(line, fragment[:min(len(fragment), bound+utf8.UTFMax-len(line))]...)
			}

			if err == bufio.ErrBufferFull {
				continue
			}

			if err != nil && err != io.EOF {
				return ToolResult{}, fmt.Errorf("read execute result: %w", err)
			}

			break
		}

		if lineNumber < start {
			lineNumber++
			continue
		}

		if count > 0 && page.Len()+len(prefix)+len(line) > contentBudget {
			return TextToolResult(page.String() + fmt.Sprintf("\n[next_start_line=%d]\n", lineNumber)), nil
		}

		page.WriteString(prefix)

		shortened := len(line) > bound
		if shortened {
			end := bound
			for end > 0 && !utf8.RuneStart(line[end]) {
				end--
			}

			line = line[:end]
		}

		page.Write(line)

		if shortened {
			fmt.Fprintf(&page, "\n[line %d shortened; remaining bytes omitted]\n", lineNumber)
		}

		lineNumber++
		count++

		if shortened {
			break
		}
	}

	if _, err := reader.Peek(1); err == io.EOF {
		return TextToolResult(page.String() + "\n[EOF]\n"), nil
	} else if err != nil {
		return ToolResult{}, fmt.Errorf("read execute result: %w", err)
	}

	return TextToolResult(page.String() + fmt.Sprintf("\n[next_start_line=%d]\n", lineNumber)), nil
}

func (l *looper) storedExecuteResult(retainedRel, resultID string) (base string, live bool, err error) {
	if path, ok := l.spillResults[resultID]; ok {
		return strings.TrimSuffix(path, ".txt"), true, nil
	}

	id, errParse := uuid.Parse(resultID)
	if errParse != nil {
		return "", false, errUnknownExecuteResult
	}

	return filepath.Join(retainedRel, id.String()), false, nil
}

func loadStoredAttachment(root *os.Root, base string, live bool, number int) (ToolResult, error) {
	files, err := storedExecuteFiles(root, base, live, number)
	if err != nil {
		return ToolResult{}, err
	}

	if len(files) == 0 {
		return ToolResult{}, fmt.Errorf("this execute result has no stored attachment %d", number)
	}

	return ToolResult{Output: fmt.Sprintf("Attachment %d: %s", number, files[0]), Attachments: []Attachment{fileAttachment(files[0])}}, nil
}

func LoadRetainedFiles(root *os.Root, dir, resultID string) ([]*codemode.File, error) {
	return storedExecuteFiles(root, filepath.Join(dir, resultID), false, 0)
}

func storedExecuteFiles(root *os.Root, base string, live bool, number int) ([]*codemode.File, error) {
	manifest, err := readStoredExecuteResult(root, base+".files.json", live)
	if err != nil {
		return nil, err
	}

	var stored []storedExecuteFile
	if err := json.Unmarshal([]byte(manifest), &stored); err != nil {
		return nil, fmt.Errorf("decode stored execute files: %w", err)
	}

	var files []*codemode.File

	for _, entry := range stored {
		if entry.Number != number && (number != 0 || !entry.Attached) {
			continue
		}

		content, err := readStoredExecuteResult(root, fmt.Sprintf("%s.%d", base, entry.Number), live)
		if err != nil {
			return nil, err
		}

		files = append(files, codemode.NewFile(codemode.FileSpec{Name: entry.Name, Mime: entry.MIME, Content: content}))
	}

	return files, nil
}

func readStoredExecuteResult(root *os.Root, path string, live bool) (_ string, err error) {
	file, err := openStoredExecuteResult(root, path, live)
	if err != nil {
		return "", err
	}

	defer func() {
		if errClose := file.Close(); errClose != nil {
			err = errors.Join(err, fmt.Errorf("close execute result: %w", errClose))
		}
	}()

	var content strings.Builder
	if _, err := io.Copy(&content, file); err != nil {
		return "", fmt.Errorf("read execute result: %w", err)
	}

	return content.String(), nil
}

func openStoredExecuteResult(root *os.Root, path string, live bool) (_ *os.File, err error) {
	file, err := openExecuteResult(root, path)
	if err != nil && !live {
		return nil, errUnknownExecuteResult
	}

	if err != nil {
		return nil, errors.New("stored execute result unavailable")
	}

	defer func() {
		if err != nil {
			err = errors.Join(err, file.Close())
		}
	}()

	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("stored execute result unavailable")
	}

	if !live && time.Since(info.ModTime()) > retainedResultTTL {
		_ = root.Remove(path)
		return nil, errUnknownExecuteResult
	}

	return file, nil
}

func openExecuteResult(root *os.Root, path string) (_ *os.File, err error) {
	file, err := root.Open(".")
	if err != nil {
		return nil, fmt.Errorf("open execute result root: %w", err)
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, file.Close())
		}
	}()

	for part := range strings.SplitSeq(path, "/") {
		fd, err := unix.Openat(int(file.Fd()), part, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
		if err != nil {
			return nil, fmt.Errorf("open execute result component: %w", err)
		}

		errClose := file.Close()
		file = os.NewFile(uintptr(fd), part)

		if errClose != nil {
			return nil, fmt.Errorf("close execute result directory: %w", errClose)
		}
	}

	return file, nil
}

func resolveSpillRel(root *os.Root, spillDir string) (string, error) {
	if spillDir == "" {
		if err := root.MkdirAll(defaultSpillRel, 0o700); err != nil {
			return "", fmt.Errorf("create execute spill dir: %w", err)
		}

		return defaultSpillRel, nil
	}

	info, err := os.Stat(spillDir)
	if err != nil {
		if !os.IsNotExist(err) {
			return "", fmt.Errorf("resolve execute spill dir %q: %w", spillDir, err)
		}

		if err := os.MkdirAll(spillDir, 0o700); err != nil {
			return "", fmt.Errorf("create execute spill dir %q: %w", spillDir, err)
		}

		info, err = os.Stat(spillDir)
		if err != nil {
			return "", fmt.Errorf("resolve execute spill dir %q: %w", spillDir, err)
		}
	}

	if !info.IsDir() {
		return "", fmt.Errorf("resolve execute spill dir %q: not a directory", spillDir)
	}

	rootAbs, err := filepath.Abs(root.Name())
	if err != nil {
		return "", fmt.Errorf("resolve workspace root %q: %w", root.Name(), err)
	}

	spillAbs, err := filepath.Abs(spillDir)
	if err != nil {
		return "", fmt.Errorf("resolve execute spill dir %q: %w", spillDir, err)
	}

	rel, err := filepath.Rel(rootAbs, spillAbs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("resolve execute spill dir %q: must be inside workspace root", spillDir)
	}

	rel = filepath.ToSlash(filepath.Clean(rel))
	if err := root.MkdirAll(rel, 0o700); err != nil {
		return "", fmt.Errorf("create execute spill dir: %w", err)
	}

	return rel, nil
}
