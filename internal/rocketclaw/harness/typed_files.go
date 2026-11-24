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
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
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
		uprightMIMEType, upright, err := protocol.StraightenImage(mimeType, content)
		if err != nil {
			return nil, fmt.Errorf("%w", err)
		}

		name := filepath.Base(path)
		if uprightMIMEType != mimeType {
			name = strings.TrimSuffix(name, filepath.Ext(name)) + ".jpg"
		}

		return codemode.NewFile(codemode.FileSpec{Name: name, Mime: uprightMIMEType, Content: upright, Source: "read " + path}), nil
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

	return newReadText(path, cmp.Or(mime.TypeByExtension(filepath.Ext(path)), "text/plain"), content[start:], len(content), offset, totalLines), nil
}

func newReadText(path, mimeType, content string, size, startLine, totalLines int) *codemode.TextValue {
	numbered := starlark.NewBuiltin("numbered", func(_ *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
		if err := starlark.UnpackPositionalArgs(b.Name(), args, kwargs, 0); err != nil {
			return nil, fmt.Errorf("numbered: %w", err)
		}

		var (
			output strings.Builder
			digits [20]byte
		)

		lineNumber := startLine
		for line := range strings.Lines(content) {
			number := strconv.AppendInt(digits[:0], int64(lineNumber), 10)

			if output.Len() > 0 {
				output.WriteByte('\n')
			} else {
				output.Grow(len(content) + len(number) + len(": "))
			}

			output.Write(number)
			output.WriteString(": ")
			output.WriteString(strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r"))

			lineNumber++
		}

		return starlark.String(output.String()), nil
	})

	return codemode.NewText("text", content, starlark.StringDict{
		"path": starlark.String(path), "name": starlark.String(filepath.Base(path)), "mime": starlark.String(mimeType), "size": starlark.MakeInt(size),
		"start_line": starlark.MakeInt(startLine), "total_lines": starlark.MakeInt(totalLines), "numbered": numbered,
	}, nil)
}

func typedToolResult(v starlark.Value, err error) (ToolResult, error) {
	if err != nil {
		return ToolResult{}, err
	}

	return ToolResult{Output: v.String(), Data: v}, nil
}

type searchMatch interface {
	globMatch | grepMatch
	value() starlark.Value
}

func (m globMatch) value() starlark.Value {
	return codemode.NewText("path", m.Path, starlark.StringDict{"path": starlark.String(m.Path), "name": starlark.String(filepath.Base(m.Path)), "mtime": starlark.MakeInt64(m.MTime)}, nil)
}

func (m grepMatch) value() starlark.Value { return m.match }

type searchResult[M searchMatch] struct {
	kind, text string
	matches    []M
	attrs      starlark.StringDict
}

func newGlobResult(matches []globMatch, partial bool, search globToolParams) *searchResult[globMatch] {
	slices.SortFunc(matches, func(a, b globMatch) int {
		return cmp.Or(cmp.Compare(b.MTime, a.MTime), strings.Compare(a.Path, b.Path))
	})

	output := make([]string, len(matches))
	for i, match := range matches {
		output[i] = match.Path
	}

	return newSearchResult("glob_result", output, matches, partial, starlark.StringDict{"pattern": starlark.String(search.Pattern), "path": starlark.String(search.Path)})
}

func newGrepResult(matches []grepMatch, partial bool, search grepToolParams) *searchResult[grepMatch] {
	slices.SortFunc(matches, func(a, b grepMatch) int {
		return cmp.Or(cmp.Compare(b.MTime, a.MTime), strings.Compare(a.Path, b.Path), cmp.Compare(a.Line, b.Line))
	})

	output := []string{fmt.Sprintf("Found %d matches", len(matches))}

	current := ""

	for i := range matches {
		match := &matches[i]

		match.match = codemode.NewMatch(match.Path, match.Line, match.Text)
		if current != match.Path {
			if current != "" {
				output = append(output, "")
			}

			current = match.Path
			output = append(output, match.Path+":")
		}

		output = append(output, fmt.Sprintf("  Line %d: %s", match.Line, match.Text))
	}

	return newSearchResult("grep_result", output, matches, partial, starlark.StringDict{"pattern": starlark.String(search.Pattern), "path": starlark.String(search.Path), "include": starlark.String(search.Include)})
}

func newSearchResult[M searchMatch](kind string, output []string, matches []M, partial bool, attrs starlark.StringDict) *searchResult[M] {
	if partial {
		output = append(output, "", "(Some paths were inaccessible and skipped)")
	}

	if len(matches) == 0 {
		output = []string{"No files found"}
	}

	attrs["partial"] = starlark.Bool(partial)

	return &searchResult[M]{kind: kind, text: strings.Join(output, "\n"), matches: matches, attrs: attrs}
}

func (r *searchResult[M]) String() string             { return r.text }
func (r *searchResult[M]) Type() string               { return r.kind }
func (r *searchResult[M]) Freeze()                    {}
func (r *searchResult[M]) Truth() starlark.Bool       { return len(r.matches) > 0 }
func (r *searchResult[M]) Ok() bool                   { return true }
func (r *searchResult[M]) Len() int                   { return len(r.matches) }
func (r *searchResult[M]) Index(i int) starlark.Value { return r.matches[i].value() }
func (r *searchResult[M]) Iterate() starlark.Iterator { return r.items().Iterate() }
func (r *searchResult[M]) Slice(start, end, step int) starlark.Value {
	return r.items().Slice(start, end, step)
}

func (r *searchResult[M]) items() starlark.Tuple {
	items := make(starlark.Tuple, len(r.matches))
	for i, match := range r.matches {
		items[i] = match.value()
	}

	return items
}

func (r *searchResult[M]) Hash() (uint32, error) {
	return 0, fmt.Errorf("unhashable type: %s", r.kind)
}

func (r *searchResult[M]) Attr(name string) (starlark.Value, error) {
	if v, ok := codemode.ResultAttr(r, nil, name); ok {
		return v, nil
	}

	return r.attrs[name], nil
}

func (r *searchResult[M]) AttrNames() []string { return codemode.ResultAttrNames(r.attrs.Keys()...) }

type typedHostKind string

const (
	typedHostText typedHostKind = "text"
	typedHostFile typedHostKind = "file"
	typedHostGlob typedHostKind = "glob"
	typedHostGrep typedHostKind = "grep"
)

type typedHostStep struct {
	Kind       typedHostKind `json:"kind"`
	Path       string        `json:"path,omitempty"`
	Mime       string        `json:"mime,omitempty"`
	Size       int           `json:"size,omitempty"`
	StartLine  int           `json:"start_line,omitempty"`
	TotalLines int           `json:"total_lines,omitempty"`
	SHA256     string        `json:"sha256,omitempty"`
	Glob       []globMatch   `json:"glob,omitempty"`
	Grep       []grepMatch   `json:"grep,omitempty"`
	Partial    bool          `json:"partial,omitempty"`
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
	case *searchResult[globMatch]:
		return &typedHostStep{Kind: typedHostGlob, Glob: v.matches, Partial: bool(v.attrs["partial"].Truth())}
	case *searchResult[grepMatch]:
		return &typedHostStep{Kind: typedHostGrep, Grep: v.matches, Partial: bool(v.attrs["partial"].Truth())}
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
		value = newReadText(s.Path, s.Mime, output, s.Size, s.StartLine, s.TotalLines)
	case typedHostGlob:
		var search globToolParams

		_ = json.Unmarshal(raw, &search)
		value = newGlobResult(s.Glob, s.Partial, search)
	case typedHostGrep:
		var search grepToolParams

		_ = json.Unmarshal(raw, &search)
		value = newGrepResult(s.Grep, s.Partial, search)
	}

	return ToolResult{Output: output, Data: value}, nil
}

var (
	_ codemode.Result    = (*searchResult[globMatch])(nil)
	_ starlark.Sliceable = (*searchResult[globMatch])(nil)
	_ starlark.HasAttrs  = (*searchResult[grepMatch])(nil)
)
