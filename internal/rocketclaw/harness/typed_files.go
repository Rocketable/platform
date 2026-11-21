package harness

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"go.starlark.net/starlark"

	"github.com/Rocketable/platform/internal/rocketclaw/codemode"
)

var descriptors = newDescriptorWait() //nolint:gochecknoglobals // every read in the process shares one wake channel and retry slot (docs/verification/typed-execute-emfile)

var errReadCancelled = errors.New("read cancelled while waiting for a file descriptor")

type descriptorWait struct {
	mu        sync.Mutex
	wake      chan struct{}
	retrySlot chan struct{}
}

type readAttempt interface {
	open() (*os.File, error)
}

func newDescriptorWait() *descriptorWait {
	return &descriptorWait{wake: make(chan struct{}), retrySlot: make(chan struct{}, 1)}
}

func descriptorShortage(err error) bool {
	return errors.Is(err, syscall.EMFILE) || errors.Is(err, syscall.ENFILE)
}

func (d *descriptorWait) open(ctx context.Context, attempt readAttempt) (*os.File, error) {
	backoff := 10 * time.Millisecond
	retrying := false

	for {
		if retrying {
			select {
			case d.retrySlot <- struct{}{}:
			case <-ctx.Done():
				return nil, errReadCancelled
			}
		}

		d.mu.Lock()
		wake := d.wake
		d.mu.Unlock()

		file, err := attempt.open()

		if retrying {
			<-d.retrySlot
		}

		if err == nil {
			return file, nil
		}

		if !descriptorShortage(err) {
			return nil, err
		}

		retrying = true
		timer := time.NewTimer(backoff)

		select {
		case <-wake:
		case <-timer.C:
		case <-ctx.Done():
		}

		timer.Stop()

		if ctx.Err() != nil {
			return nil, errReadCancelled
		}

		backoff = min(2*backoff, time.Second)
	}
}

func (d *descriptorWait) load(file *os.File) (string, error) {
	var content strings.Builder

	info, err := file.Stat()
	if err == nil {
		content.Grow(int(info.Size()))
		_, err = io.Copy(&content, file)
	}

	errClose := file.Close()

	d.mu.Lock()
	close(d.wake)
	d.wake = make(chan struct{})
	d.mu.Unlock()

	if err := errors.Join(err, errClose); err != nil {
		return "", err
	}

	return content.String(), nil
}

type rootedReadAttempt struct {
	sfs  *sandboxedFileSystem
	path string
}

func (a rootedReadAttempt) open() (*os.File, error) {
	a.sfs.mu.Lock()
	defer a.sfs.mu.Unlock()

	if denied := a.sfs.deniedPath(a.path); denied != "" {
		return nil, errors.New(denied)
	}

	name, err := normalizeRootName(a.sfs.root, a.path)
	if err != nil {
		return nil, err
	}

	if err := rejectSymlink(a.sfs, name); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}

	file, err := a.sfs.root.Open(name)
	if descriptorShortage(err) {
		return nil, fmt.Errorf("open %s: %w", a.path, err)
	}

	if err != nil {
		return nil, errors.New("File not found: " + a.path)
	}

	return file, nil
}

type easyReadAttempt struct {
	files *easyFiles
	path  string
}

func (a easyReadAttempt) open() (*os.File, error) {
	a.files.mu.Lock()
	defer a.files.mu.Unlock()

	sfs, name, err := a.files.open(a.path)
	if err != nil {
		return nil, err
	}

	defer func() { _ = sfs.root.Close() }()

	return rootedReadAttempt{sfs: sfs, path: name}.open()
}

func readTyped(ctx context.Context, mu *sync.Mutex, attempt readAttempt, path string, offset int) (starlark.Value, error) {
	if offset < 1 {
		return nil, errors.New("offset must be greater than or equal to 1")
	}

	file, err := descriptors.open(ctx, attempt)
	if err != nil {
		return nil, err
	}

	mu.Lock()
	content, err := descriptors.load(file)
	mu.Unlock()

	if err != nil {
		return nil, errors.New("failed to read file: " + path)
	}

	mimeType := sniffAttachmentMIME([]byte(content[:min(len(content), 12)]), cmp.Or(mime.TypeByExtension(filepath.Ext(path)), "application/octet-stream"))
	if slices.Contains([]string{"image/png", "image/jpeg", "image/gif", "image/webp", "application/pdf"}, mimeType) {
		return newFileValue(codemode.FileSpec{Name: filepath.Base(path), Mime: mimeType, Content: content, Source: "read " + path}), nil
	}

	if strings.IndexByte(content, 0) >= 0 {
		return nil, errors.New("Cannot read binary file: " + path)
	}

	totalLines := strings.Count(content, "\n")
	if content != "" && !strings.HasSuffix(content, "\n") {
		totalLines++
	}

	if offset > totalLines && (totalLines != 0 || offset != 1) {
		return nil, errors.New("Offset " + strconv.Itoa(offset) + " is out of range for this file (" + strconv.Itoa(totalLines) + " lines)")
	}

	start := 0
	for range offset - 1 {
		start += strings.IndexByte(content[start:], '\n') + 1
	}

	text := content[start:]
	attrs := &readText{path: path, mime: cmp.Or(mime.TypeByExtension(filepath.Ext(path)), "text/plain"), content: text, size: len(content), startLine: offset, totalLines: totalLines}

	return codemode.NewText(text, attrs), nil
}

type readText struct {
	path, mime, content         string
	size, startLine, totalLines int
}

func (r *readText) Attr(name string) (starlark.Value, error) {
	switch name {
	case "path":
		return starlark.String(r.path), nil
	case "name":
		return starlark.String(filepath.Base(r.path)), nil
	case "mime":
		return starlark.String(r.mime), nil
	case "size":
		return starlark.MakeInt(r.size), nil
	case "start_line":
		return starlark.MakeInt(r.startLine), nil
	case "total_lines":
		return starlark.MakeInt(r.totalLines), nil
	case "numbered":
		return starlark.NewBuiltin("numbered", func(_ *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
			if err := starlark.UnpackPositionalArgs(b.Name(), args, kwargs, 0); err != nil {
				return nil, fmt.Errorf("numbered: %w", err)
			}

			var output strings.Builder

			output.WriteString("<path>" + r.path + "</path>\n<type>file</type>\n<content>\n")

			n := r.startLine
			for line := range strings.Lines(r.content) {
				if n > r.startLine {
					output.WriteByte('\n')
				}

				fmt.Fprintf(&output, "%d: %s", n, strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r"))
				n++
			}

			fmt.Fprintf(&output, "\n\n(End of file - total %d lines)\n</content>", r.totalLines)

			return starlark.String(output.String()), nil
		}), nil
	default:
		return nil, nil
	}
}

func (*readText) AttrNames() []string {
	return []string{"mime", "name", "numbered", "path", "size", "start_line", "total_lines"}
}

func typedToolResult(v starlark.Value, err error) (ToolResult, error) {
	if err != nil {
		return ToolResult{}, err
	}

	return ToolResult{Output: v.String(), Data: v}, nil
}

func (m *globMatch) String() string       { return m.path }
func (m *globMatch) Text() string         { return m.path }
func (m *globMatch) Type() string         { return "path" }
func (m *globMatch) Freeze()              {}
func (m *globMatch) Truth() starlark.Bool { return true }
func (m *globMatch) Ok() bool             { return true }

func (m *globMatch) Hash() (uint32, error) {
	hash, _ := starlark.String(m.path).Hash()

	return hash, nil
}

func (m *globMatch) MarshalJSON() ([]byte, error) {
	encoded, err := json.Marshal(m.path)
	if err != nil {
		return nil, fmt.Errorf("encode path: %w", err)
	}

	return encoded, nil
}

func (m *globMatch) Attr(name string) (starlark.Value, error) {
	if v, ok := codemode.ResultAttr(m, nil, name); ok {
		return v, nil
	}

	switch name {
	case "path":
		return starlark.String(m.path), nil
	case "name":
		return starlark.String(filepath.Base(m.path)), nil
	case "mtime":
		return starlark.MakeInt64(m.mtime), nil
	}

	method, err := starlark.String(m.path).Attr(name)
	if err != nil {
		return nil, fmt.Errorf("path.%s: %w", name, err)
	}

	return method, nil
}

func (m *globMatch) AttrNames() []string {
	return codemode.ResultAttrNames(append([]string{"mtime", "name", "path"}, starlark.String("").AttrNames()...)...)
}

type globResult struct {
	paths starlark.Tuple
}

func newGlobResult(matches []globMatch) *globResult {
	slices.SortFunc(matches, func(a, b globMatch) int {
		return cmp.Or(cmp.Compare(b.mtime, a.mtime), strings.Compare(a.path, b.path))
	})

	paths := make(starlark.Tuple, len(matches))
	for i := range matches {
		paths[i] = &matches[i]
	}

	return &globResult{paths: paths}
}

func (r *globResult) String() string {
	if len(r.paths) == 0 {
		return "No files found"
	}

	output := make([]string, len(r.paths))
	for i, path := range r.paths {
		output[i] = path.String()
	}

	return strings.Join(output, "\n")
}

func (r *globResult) Type() string               { return "glob_result" }
func (r *globResult) Freeze()                    {}
func (r *globResult) Truth() starlark.Bool       { return len(r.paths) > 0 }
func (r *globResult) Ok() bool                   { return true }
func (r *globResult) Len() int                   { return len(r.paths) }
func (r *globResult) Index(i int) starlark.Value { return r.paths[i] }
func (r *globResult) Iterate() starlark.Iterator { return r.paths.Iterate() }
func (r *globResult) Slice(start, end, step int) starlark.Value {
	return r.paths.Slice(start, end, step)
}

func (r *globResult) Hash() (uint32, error) {
	return 0, errors.New("unhashable type: glob_result")
}

func (r *globResult) MarshalJSON() ([]byte, error) {
	encoded, err := json.Marshal(r.paths)
	if err != nil {
		return nil, fmt.Errorf("encode glob_result: %w", err)
	}

	return encoded, nil
}

func (r *globResult) Attr(name string) (starlark.Value, error) {
	v, _ := codemode.ResultAttr(r, nil, name)

	return v, nil
}

func (r *globResult) AttrNames() []string { return codemode.ResultAttrNames() }

type grepResult struct {
	matches []grepMatch
	items   starlark.Tuple
	partial bool
}

func newGrepResult(matches []grepMatch, partial bool) *grepResult {
	slices.SortFunc(matches, func(a, b grepMatch) int {
		return cmp.Or(cmp.Compare(b.mtime, a.mtime), strings.Compare(a.path, b.path), cmp.Compare(a.line, b.line))
	})

	items := make(starlark.Tuple, len(matches))
	for i, match := range matches {
		items[i] = codemode.NewMatch(match.path, match.line, match.text)
	}

	return &grepResult{matches: matches, items: items, partial: partial}
}

func (r *grepResult) String() string {
	if len(r.matches) == 0 {
		return "No files found"
	}

	output := []string{fmt.Sprintf("Found %d matches", len(r.matches))}

	current := ""
	for _, match := range r.matches {
		if current != match.path {
			if current != "" {
				output = append(output, "")
			}

			current = match.path
			output = append(output, match.path+":")
		}

		output = append(output, fmt.Sprintf("  Line %d: %s", match.line, match.text))
	}

	if r.partial {
		output = append(output, "", "(Some paths were inaccessible and skipped)")
	}

	return strings.Join(output, "\n")
}

func (r *grepResult) Type() string               { return "grep_result" }
func (r *grepResult) Freeze()                    {}
func (r *grepResult) Truth() starlark.Bool       { return len(r.items) > 0 }
func (r *grepResult) Ok() bool                   { return true }
func (r *grepResult) Len() int                   { return len(r.items) }
func (r *grepResult) Index(i int) starlark.Value { return r.items[i] }
func (r *grepResult) Iterate() starlark.Iterator { return r.items.Iterate() }
func (r *grepResult) Slice(start, end, step int) starlark.Value {
	return r.items.Slice(start, end, step)
}

func (r *grepResult) Hash() (uint32, error) {
	return 0, errors.New("unhashable type: grep_result")
}

func (r *grepResult) MarshalJSON() ([]byte, error) {
	encoded, err := json.Marshal(r.items)
	if err != nil {
		return nil, fmt.Errorf("encode grep_result: %w", err)
	}

	return encoded, nil
}

func (r *grepResult) Attr(name string) (starlark.Value, error) {
	if v, ok := codemode.ResultAttr(r, nil, name); ok {
		return v, nil
	}

	if name == "partial" {
		return starlark.Bool(r.partial), nil
	}

	return nil, nil
}

func (r *grepResult) AttrNames() []string { return codemode.ResultAttrNames("partial") }

type typedHostKind string

const (
	typedHostText typedHostKind = "text"
	typedHostFile typedHostKind = "file"
	typedHostGlob typedHostKind = "glob"
	typedHostGrep typedHostKind = "grep"
)

type typedHostMatch struct {
	Path  string `json:"path"`
	Line  int    `json:"line,omitempty"`
	Text  string `json:"text,omitempty"`
	MTime int64  `json:"mtime"`
}

type typedHostStep struct {
	Kind       typedHostKind    `json:"kind"`
	Path       string           `json:"path,omitempty"`
	Mime       string           `json:"mime,omitempty"`
	Size       int              `json:"size,omitempty"`
	StartLine  int              `json:"start_line,omitempty"`
	TotalLines int              `json:"total_lines,omitempty"`
	SHA256     string           `json:"sha256,omitempty"`
	Matches    []typedHostMatch `json:"matches,omitempty"`
	Partial    bool             `json:"partial,omitempty"`
}

func typedHostStepOf(data any) *typedHostStep {
	switch v := data.(type) {
	case *codemode.TextValue:
		attr := func(name string) starlark.Value {
			value, _ := v.Attr(name)

			return value
		}
		number := func(name string) int {
			n, _ := starlark.AsInt32(attr(name))

			return n
		}

		path, fromRead := starlark.AsString(attr("path"))
		if !fromRead {
			return nil
		}

		mimeType, _ := starlark.AsString(attr("mime"))

		return &typedHostStep{Kind: typedHostText, Path: path, Mime: mimeType, Size: number("size"), StartLine: number("start_line"), TotalLines: number("total_lines")}
	case *codemode.File:
		return &typedHostStep{Kind: typedHostFile, Path: strings.TrimPrefix(v.Source(), "read "), SHA256: v.SHA256()}
	case *globResult:
		step := &typedHostStep{Kind: typedHostGlob, Matches: make([]typedHostMatch, len(v.paths))}
		for i, path := range v.paths {
			step.Matches[i] = typedHostMatch{Path: path.(*globMatch).path, MTime: path.(*globMatch).mtime}
		}

		return step
	case *grepResult:
		step := &typedHostStep{Kind: typedHostGrep, Matches: make([]typedHostMatch, len(v.matches)), Partial: v.partial}
		for i, match := range v.matches {
			step.Matches[i] = typedHostMatch{Path: match.path, Line: match.line, Text: match.text, MTime: match.mtime}
		}

		return step
	default:
		return nil
	}
}

func (s *typedHostStep) replay(ctx context.Context, output string, call hostCall, raw json.RawMessage) (ToolResult, error) {
	var value starlark.Value

	switch s.Kind {
	case typedHostFile:
		result, err := call.run(ctx, raw)
		if err != nil {
			return ToolResult{}, err
		}

		if file, ok := result.Data.(*codemode.File); !ok || file.SHA256() != s.SHA256 {
			return ToolResult{}, errors.New("file changed since the interrupted run: " + s.Path)
		}

		return result, nil
	case typedHostText:
		value = codemode.NewText(output, &readText{path: s.Path, mime: s.Mime, content: output, size: s.Size, startLine: s.StartLine, totalLines: s.TotalLines})
	case typedHostGlob:
		matches := make([]globMatch, len(s.Matches))
		for i, match := range s.Matches {
			matches[i] = globMatch{path: match.Path, mtime: match.MTime}
		}

		value = newGlobResult(matches)
	case typedHostGrep:
		matches := make([]grepMatch, len(s.Matches))
		for i, match := range s.Matches {
			matches[i] = grepMatch{path: match.Path, line: match.Line, text: match.Text, mtime: match.MTime}
		}

		value = newGrepResult(matches, s.Partial)
	}

	return ToolResult{Output: output, Data: value}, nil
}

var (
	_ codemode.Result    = (*globMatch)(nil)
	_ codemode.Text      = (*globMatch)(nil)
	_ starlark.HasAttrs  = (*globMatch)(nil)
	_ codemode.Result    = (*globResult)(nil)
	_ starlark.Sliceable = (*globResult)(nil)
	_ starlark.Iterable  = (*globResult)(nil)
	_ starlark.HasAttrs  = (*globResult)(nil)
	_ codemode.Result    = (*grepResult)(nil)
	_ starlark.Sliceable = (*grepResult)(nil)
	_ starlark.Iterable  = (*grepResult)(nil)
	_ starlark.HasAttrs  = (*grepResult)(nil)
)
