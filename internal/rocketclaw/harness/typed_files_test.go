package harness

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"testing/synctest"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
	"go.starlark.net/starlark"

	"github.com/Rocketable/platform/internal/rocketclaw/codemode"
)

func resultText(v starlark.Value, err error) string {
	if err != nil {
		return "error: " + err.Error()
	}

	return v.String()
}

func readTextValue(t *testing.T, files fileSystem, name string, offset int) *codemode.TextValue {
	t.Helper()

	value, err := files.Read(t.Context(), name, offset)
	require.NoError(t, err)

	text, ok := value.(*codemode.TextValue)
	require.True(t, ok, value.Type())

	return text
}

func numberedView(t *testing.T, text *codemode.TextValue) string {
	t.Helper()

	numbered, err := text.Attr("numbered")
	require.NoError(t, err)

	out, err := starlark.Call(&starlark.Thread{}, numbered, nil, nil)
	require.NoError(t, err)

	return string(out.(starlark.String))
}

func runTypedScript(t *testing.T, files fileSystem, code string) string {
	t.Helper()

	permissions := parsePermissionYAML(t, "read: allow\nedit: allow\nglob: allow\ngrep: allow")
	factory := &toolFactory{baseTools: makeSandboxedTools(files, nil)}
	model, hosts := factory.assembleTools(&Agent{Permission: permissions})
	looper := &looper{Journal: InertJournal{}, observations: &turnObservations{journal: InertJournal{}}, Permissions: permissions, Tools: model, CodeModeHosts: hosts}
	ctx := context.WithValue(t.Context(), toolCallContextKey{}, toolCallContext{looper: looper, observations: looper.observations})

	raw, err := json.Marshal(executeParams{Code: code})
	require.NoError(t, err)

	result, err := model[executeToolName].Call(ctx, raw, nil, emptyToolCallMetadata())
	require.NoError(t, err)

	return result.Output
}

func typedFilesFixture(t *testing.T) (dir string, sfs *sandboxedFileSystem) {
	t.Helper()

	dir = t.TempDir()
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })

	var dot bytes.Buffer
	require.NoError(t, png.Encode(&dot, image.NewRGBA(image.Rect(0, 0, 3, 2))))

	for name, content := range map[string]string{
		"notes.txt":    "alpha\nbeta\ngamma\n",
		"other.txt":    "needle here\nno\nneedle again",
		"dot.png":      dot.String(),
		"blob.bin":     "a\x00b",
		"sub/inner.md": "inner",
	} {
		require.NoError(t, root.MkdirAll(filepath.Dir(name), 0o755))
		require.NoError(t, root.WriteFile(name, []byte(content), 0o644))
	}

	for i, name := range []string{"notes.txt", "other.txt"} {
		at := time.UnixMilli(1_700_000_000_000 + int64(i)*1000)
		require.NoError(t, root.Chtimes(name, at, at))
	}

	return dir, &sandboxedFileSystem{root: root}
}

func TestExecuteReadReturnsText(t *testing.T) {
	t.Parallel()

	_, sfs := typedFilesFixture(t)

	got := runTypedScript(t, sfs, `def main():
    t = read(filePath="notes.txt", offset=2)
    whole = read(filePath="notes.txt")
    return {
        "type": type(t), "str": str(t), "ok": t.ok, "error": t.error, "truthy": bool(t),
        "path": t.path, "name": t.name, "mime": t.mime, "size": t.size,
        "start_line": t.start_line, "total_lines": t.total_lines,
        "lines": t.lines(), "numbered": t.numbered(), "upper": t.upper(), "eq": t == "beta\ngamma\n",
        "search": [str(m) for m in whole.search("^g")], "json": json.encode(t),
    }
`)
	require.JSONEq(t, `{
		"type": "text", "str": "beta\ngamma\n", "ok": true, "error": null, "truthy": true,
		"path": "notes.txt", "name": "notes.txt", "mime": "text/plain; charset=utf-8", "size": 17,
		"start_line": 2, "total_lines": 3,
		"lines": ["beta", "gamma"],
		"numbered": "2: beta\n3: gamma",
		"upper": "BETA\nGAMMA\n", "eq": true,
		"search": ["notes.txt:3: gamma"], "json": "\"beta\\ngamma\\n\""
	}`, got)
}

func TestExecuteReadReturnsFile(t *testing.T) {
	t.Parallel()

	_, sfs := typedFilesFixture(t)

	got := runTypedScript(t, sfs, `def main():
    f = read(filePath="dot.png")
    return {"type": type(f), "ok": f.ok, "name": f.name, "mime": f.mime, "width": f.width, "height": f.height, "source": f.source, "same": f.size == len(f.bytes())}
`)
	require.JSONEq(t, `{"type": "file", "ok": true, "name": "dot.png", "mime": "image/png", "width": 3, "height": 2, "source": "read dot.png", "same": true}`, got)
}

func exifTIFFRotated(orientation byte) []byte {
	return []byte{'I', 'I', 0x2a, 0, 8, 0, 0, 0, 1, 0, 0x12, 0x01, 3, 0, 1, 0, 0, 0, orientation, 0, 0, 0, 0, 0, 0, 0}
}

func exifJPEG(t *testing.T, orientation byte) []byte {
	t.Helper()

	var encoded bytes.Buffer
	require.NoError(t, jpeg.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, 4, 2)), nil))

	app1 := append([]byte("Exif\x00\x00"), exifTIFFRotated(orientation)...)

	return slices.Concat([]byte{0xff, 0xd8, 0xff, 0xe1, 0, byte(len(app1) + 2)}, app1, encoded.Bytes()[2:])
}

func hugeEXIFJPEG(t *testing.T) []byte {
	t.Helper()

	data := exifJPEG(t, 6)
	sof := bytes.Index(data, []byte{0xff, 0xc0})
	binary.BigEndian.PutUint16(data[sof+5:], 3000)
	binary.BigEndian.PutUint16(data[sof+7:], 4000)

	return data
}

func TestExecuteReadStraightensEXIFOrientedImages(t *testing.T) {
	t.Parallel()

	_, sfs := typedFilesFixture(t)

	lossy, err := base64.StdEncoding.DecodeString("UklGRiIAAABXRUJQVlA4IBYAAAAwAQCdASoBAAEADsD+JaQAA3AAAAAA")
	require.NoError(t, err)

	webpBody := slices.Concat([]byte("WEBP"), lossy[12:], []byte("EXIF\x1a\x00\x00\x00"), exifTIFFRotated(8))
	upright := exifJPEG(t, 1)

	for name, content := range map[string][]byte{
		"turned.jpg":  exifJPEG(t, 6),
		"upright.jpg": upright,
		"huge.jpg":    hugeEXIFJPEG(t),
		"turned.webp": slices.Concat([]byte("RIFF"), binary.LittleEndian.AppendUint32(nil, uint32(len(webpBody))), webpBody),
	} {
		require.NoError(t, sfs.root.WriteFile(name, content, 0o644))
	}

	got := runTypedScript(t, sfs, `def main():
    files = [read(filePath=p) for p in ["turned.jpg", "upright.jpg", "turned.webp"]]
    huge = read(filePath="huge.jpg")
    return {
        "files": [[f.name, f.mime, f.width, f.height, f.size == len(f.bytes()), f.source] for f in files],
        "upright_unchanged": files[1].size == `+strconv.Itoa(len(upright))+`,
        "huge": [type(huge), huge.error],
    }
`)
	require.JSONEq(t, `{
		"files": [
			["turned.jpg", "image/jpeg", 2, 4, true, "read turned.jpg"],
			["upright.jpg", "image/jpeg", 4, 2, true, "read upright.jpg"],
			["turned.jpg", "image/jpeg", 1, 1, true, "read turned.webp"]
		],
		"upright_unchanged": true,
		"huge": ["error", "read: image too large to straighten (12 MP; limit 10.24 MP)"]
	}`, got)
}

func TestMCPImageBlocksAreStraightened(t *testing.T) {
	t.Parallel()

	result, err := newMCPResult("demo.photo", "", &mcp.CallToolResult{Content: []mcp.Content{
		&mcp.ImageContent{Data: exifJPEG(t, 8), MIMEType: "image/jpeg"},
		&mcp.ImageContent{Data: hugeEXIFJPEG(t), MIMEType: "image/jpeg"},
	}})
	require.NoError(t, err)

	content, _ := result.(starlark.HasAttrs).Attr("content")
	turned, ok := content.(*starlark.List).Index(0).(*codemode.File)
	require.True(t, ok)

	width, height, _ := turned.Dimensions()
	require.Equal(t, [3]any{"image-1.jpeg", 2, 4}, [3]any{turned.Name(), width, height})

	refused, ok := content.(*starlark.List).Index(1).(*codemode.Error)
	require.True(t, ok)
	require.Equal(t, [2]string{"demo.photo", "image too large to straighten (12 MP; limit 10.24 MP)"}, [2]string{refused.Tool(), refused.Message()})
}

func TestExecuteFailuresAreErrorResults(t *testing.T) {
	t.Parallel()

	dir, sfs := typedFilesFixture(t)
	outside := filepath.Join(filepath.Dir(dir), "outside")

	got := runTypedScript(t, sfs, `def main():
    outside = `+strconv.Quote(outside)+`
    results = [
        read(filePath="missing.txt"),
        read(filePath="blob.bin"),
        read(filePath="notes.txt", offset=9),
        read(filePath=".env"),
        glob(pattern="*.txt", path=outside),
        grep(pattern=""),
        apply_patch(patchText="*** Begin Patch\n*** Update File: missing.txt\n@@\n-a\n+b\n*** End Patch"),
    ]
    return [[type(r), r.ok, bool(r), r.error] for r in results]
`)

	want, err := json.Marshal([][]any{
		{"error", false, false, "read: File not found: missing.txt"},
		{"error", false, false, "read: Cannot read binary file: blob.bin"},
		{"error", false, false, "read: Offset 9 is out of range for this file (3 lines)"},
		{"error", false, false, "read: " + deniedEnvAccessMessage(".env")},
		{"error", false, false, "glob: path escapes root: " + outside},
		{"error", false, false, "grep: pattern is required"},
		{"error", false, false, "apply_patch: check permission: apply_patch verification failed: Failed to read file to update: " + filepath.Join(dir, "missing.txt")},
	})
	require.NoError(t, err)
	require.JSONEq(t, string(want), got)
}

func TestExecuteApplyPatchReturnsText(t *testing.T) {
	t.Parallel()

	_, sfs := typedFilesFixture(t)

	got := runTypedScript(t, sfs, `def main():
    r = apply_patch(patchText="*** Begin Patch\n*** Add File: made.txt\n+made\n*** End Patch")
    return [type(r), r.ok, str(r), str(read(filePath="made.txt"))]
`)
	require.JSONEq(t, `["text", true, "Success. Updated the following files:\nA made.txt", "made\n"]`, got)
}

func TestExecuteGlobReturnsPaths(t *testing.T) {
	t.Parallel()

	dir, sfs := typedFilesFixture(t)
	notes, other := filepath.Join(dir, "notes.txt"), filepath.Join(dir, "other.txt")

	got := runTypedScript(t, sfs, `def main():
    g = glob(pattern="*.txt")
    e = glob(pattern="*.none")
    first = g[0]
    return {
        "type": type(g), "len": len(g), "str": str(g), "json": json.decode(json.encode(g)), "truthy": bool(g), "ok": g.ok,
        "path_type": type(first), "path": first.path, "name": first.name, "mtime": first.mtime, "endswith": first.endswith(".txt"),
        "names": [p.name for p in g], "slice": [p.name for p in g[1:]], "reread": str(read(filePath=first)),
        "empty": [str(e), bool(e), e.ok, len(e)],
    }
`)

	want, err := json.Marshal(map[string]any{
		"type": "glob_result", "len": 2, "str": other + "\n" + notes, "json": []string{other, notes}, "truthy": true, "ok": true,
		"path_type": "path", "path": other, "name": "other.txt", "mtime": 1_700_000_001_000, "endswith": true,
		"names": []string{"other.txt", "notes.txt"}, "slice": []string{"notes.txt"}, "reread": "needle here\nno\nneedle again",
		"empty": []any{"No files found", false, true, 0},
	})
	require.NoError(t, err)
	require.JSONEq(t, string(want), got)
}

func TestExecuteGrepReturnsMatches(t *testing.T) {
	t.Parallel()

	dir, sfs := typedFilesFixture(t)
	other := filepath.Join(dir, "other.txt")

	got := runTypedScript(t, sfs, `def main():
    m = grep(pattern="needle", include="*.txt")
    first = m[0]
    return {
        "type": type(m), "len": len(m), "str": str(m), "json": json.decode(json.encode(m)), "partial": m.partial, "truthy": bool(m),
        "match_type": type(first), "path": first.path, "line": first.line, "text": first.text,
        "lines": [x.line for x in m[1:]],
    }
`)

	want, err := json.Marshal(map[string]any{
		"type": "grep_result", "len": 2, "str": "Found 2 matches\n" + other + ":\n  Line 1: needle here\n  Line 3: needle again",
		"json":    []map[string]any{{"path": other, "line": 1, "text": "needle here"}, {"path": other, "line": 3, "text": "needle again"}},
		"partial": false, "truthy": true,
		"match_type": "match", "path": other, "line": 1, "text": "needle here",
		"lines": []int{3},
	})
	require.NoError(t, err)
	require.JSONEq(t, string(want), got)
}

func TestReadWithoutSizeLimit(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })

	big := "\x89PNG\r\n\x1a\n" + strings.Repeat("x", 12<<20)
	require.NoError(t, root.WriteFile("big.png", []byte(big), 0o644))
	require.NoError(t, root.WriteFile("big.txt", []byte(big[8:]), 0o644))

	sfs := &sandboxedFileSystem{root: root}

	value, err := sfs.Read(t.Context(), "big.png", 1)
	require.NoError(t, err)
	require.Equal(t, len(big), value.(*codemode.File).Size())
	require.Equal(t, big[8:], readTextValue(t, sfs, "big.txt", 1).String())
}

func openDescriptorsUnder(t *testing.T, dir string) []string {
	t.Helper()

	entries, err := os.ReadDir("/proc/self/fd")
	require.NoError(t, err)

	var open []string

	for _, entry := range entries {
		target, err := os.Readlink("/proc/self/fd/" + entry.Name())
		if err == nil && strings.HasPrefix(target, dir+string(filepath.Separator)) {
			open = append(open, target)
		}
	}

	return open
}

func TestReadClosesEveryDescriptor(t *testing.T) {
	dir, sfs := typedFilesFixture(t)
	require.NoError(t, sfs.root.MkdirAll("folder/deeper", 0o755))
	require.NoError(t, sfs.root.WriteFile("folder/deeper/note.txt", []byte("note"), 0o644))

	held, err := sfs.root.Open("notes.txt")
	require.NoError(t, err)
	require.Equal(t, []string{filepath.Join(dir, "notes.txt")}, openDescriptorsUnder(t, dir))
	require.NoError(t, held.Close())

	cancelled, cancel := context.WithCancel(t.Context())
	cancel()

	easy := &easyFiles{shell: CustomShell{Dir: filepath.Join(dir, "folder")}}
	for _, read := range []struct {
		files fileSystem
		name  string
		want  string
	}{
		{sfs, "notes.txt", "alpha\nbeta\ngamma\n"},
		{sfs, "dot.png", "dot.png (image/png, 3 × 2, 96 B)"},
		{sfs, "blob.bin", "error: Cannot read binary file: blob.bin"},
		{sfs, "missing.txt", "error: File not found: missing.txt"},
		{sfs, "sub", "error: failed to read file: sub"},
		{sfs, "sub/inner.md", "inner"},
		{easy, "deeper/note.txt", "note"},
		{easy, "deeper", "error: failed to read file: " + filepath.Join(dir, "folder", "deeper")},
		{easy, "missing.txt", "error: File not found: " + filepath.Join(dir, "folder", "missing.txt")},
		{easy, "../notes.txt", "error: " + filepath.Join(dir, "notes.txt") + " is outside this agent's folders"},
	} {
		for _, ctx := range []context.Context{t.Context(), cancelled} {
			require.Equal(t, read.want, resultText(read.files.Read(ctx, read.name, 1)), read.name)
		}
	}

	require.Empty(t, openDescriptorsUnder(t, dir))
}

func emfile() error {
	return &os.PathError{Op: "open", Path: "x", Err: syscall.EMFILE}
}

func TestDescriptorWaitRetriesUntilOpen(t *testing.T) {
	dir, _ := typedFilesFixture(t)
	path := filepath.Join(dir, "notes.txt")

	for _, shortage := range []syscall.Errno{syscall.EMFILE, syscall.ENFILE} {
		synctest.Test(t, func(t *testing.T) {
			d := newDescriptorWait()
			attempt := &mockReadAttempt{}
			attempt.openFunc = func() (*os.File, error) {
				if len(attempt.openCalls()) <= 4 {
					return nil, fmt.Errorf("open %s: %w", path, shortage)
				}

				return os.Open(path)
			}

			start := time.Now()
			file, err := d.open(t.Context(), attempt)
			require.NoError(t, err)
			require.Equal(t, (10+20+40+80)*time.Millisecond, time.Since(start), "a wait doubles after every failed attempt")
			require.Len(t, attempt.openCalls(), 5)

			content, err := d.load(file)
			require.NoError(t, err)
			require.Equal(t, "alpha\nbeta\ngamma\n", content)
		})
	}
}

func TestDescriptorWaitBacksOffToOneSecondUntilCancelled(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		d := newDescriptorWait()
		attempt := &mockReadAttempt{openFunc: func() (*os.File, error) { return nil, emfile() }}

		ctx, cancel := context.WithTimeout(t.Context(), 3500*time.Millisecond)
		defer cancel()

		start := time.Now()
		_, err := d.open(ctx, attempt)
		require.ErrorIs(t, err, errReadCancelled)
		require.Equal(t, 3500*time.Millisecond, time.Since(start))
		require.Len(t, attempt.openCalls(), 10, "attempts at 0, 10, 30, 70, 150, 310, 630, 1270, 2270 and 3270 ms")
	})
}

func TestDescriptorWaitNeverRetriesAfterCancellation(t *testing.T) {
	for range 50 {
		d := newDescriptorWait()
		ctx, cancel := context.WithCancel(t.Context())
		attempt := &mockReadAttempt{openFunc: func() (*os.File, error) {
			cancel()

			other, err := os.Open(os.DevNull)
			require.NoError(t, err)
			_, err = d.load(other)
			require.NoError(t, err)

			return nil, emfile()
		}}

		_, err := d.open(ctx, attempt)
		require.ErrorIs(t, err, errReadCancelled)
		require.Len(t, attempt.openCalls(), 1, "the wake and the cancellation are both ready when the wait starts")
	}
}

func TestDescriptorWaitWakesWhenAReadCloses(t *testing.T) {
	dir, _ := typedFilesFixture(t)
	path := filepath.Join(dir, "notes.txt")

	synctest.Test(t, func(t *testing.T) {
		d := newDescriptorWait()
		attempt := &mockReadAttempt{}
		attempt.openFunc = func() (*os.File, error) {
			if len(attempt.openCalls()) == 1 {
				return nil, emfile()
			}

			return os.Open(path)
		}

		opened := make(chan error)
		start := time.Now()

		go func() {
			file, err := d.open(t.Context(), attempt)
			if err == nil {
				_, err = d.load(file)
			}

			opened <- err
		}()

		synctest.Wait()
		require.Len(t, attempt.openCalls(), 1)

		other, err := os.Open(path)
		require.NoError(t, err)
		_, err = d.load(other)
		require.NoError(t, err)

		require.NoError(t, <-opened)
		require.Zero(t, time.Since(start), "woken before the 10 ms timer")
		require.Len(t, attempt.openCalls(), 2)
	})
}

func TestDescriptorWaitCancelsWhileWaitingForTheRetrySlot(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		d := newDescriptorWait()
		d.retrySlot <- struct{}{}

		attempt := &mockReadAttempt{openFunc: func() (*os.File, error) { return nil, emfile() }}
		ctx, cancel := context.WithCancel(t.Context())
		failed := make(chan error)

		go func() {
			_, err := d.open(ctx, attempt)
			failed <- err
		}()

		time.Sleep(time.Second)
		synctest.Wait()
		require.Len(t, attempt.openCalls(), 1, "the retry waits for the slot another retry holds")

		cancel()
		require.ErrorIs(t, <-failed, errReadCancelled)
		require.Len(t, attempt.openCalls(), 1)
	})
}

func TestDescriptorWaitReturnsOtherErrorsAtOnce(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		d := newDescriptorWait()
		errNotFound := errors.New("File not found: x")
		attempt := &mockReadAttempt{openFunc: func() (*os.File, error) { return nil, errNotFound }}

		start := time.Now()
		_, err := d.open(t.Context(), attempt)
		require.ErrorIs(t, err, errNotFound)
		require.Zero(t, time.Since(start))
		require.Len(t, attempt.openCalls(), 1)
	})
}

func TestExecuteReplaysTypedResults(t *testing.T) {
	t.Parallel()

	dir, sfs := typedFilesFixture(t)
	permissions := parsePermissionYAML(t, "read: allow\nglob: allow\ngrep: allow")
	journal := recordingJournal()
	observations := &turnObservations{journal: journal, turnID: "turn-1"}
	execute := func(code string) string {
		t.Helper()

		model, hosts := (&toolFactory{baseTools: makeSandboxedTools(sfs, nil)}).assembleTools(&Agent{Permission: permissions})
		looper := &looper{Journal: journal, observations: observations, Permissions: permissions, Tools: model, CodeModeHosts: hosts}
		ctx := context.WithValue(t.Context(), toolCallContextKey{}, toolCallContext{looper: looper, observations: looper.observations, callID: "call-1"})

		raw, err := json.Marshal(executeParams{Code: code})
		require.NoError(t, err)

		result, err := model[executeToolName].Call(ctx, raw, nil, emptyToolCallMetadata())
		require.NoError(t, err)

		return result.Output
	}

	const script = `def main():
    t = read(filePath="notes.txt", offset=2)
    g = glob(pattern="*.txt")
    m = grep(pattern="needle", include="*.txt")
    f = read(filePath="dot.png")
    x = read(filePath="missing.txt")
    return [type(t), str(t), t.path, t.size, t.start_line, t.total_lines, t.numbered(),
            type(g), [p.name for p in g], g[0].mtime, type(m), str(m), m.partial,
            g.pattern, g.path, m.pattern, m.include, x.path, x.name, type(f), str(f.error) if not f.ok else f.sha256]
`

	first := execute(script)

	require.NoError(t, sfs.root.WriteFile("notes.txt", []byte("changed\n"), 0o644))
	require.NoError(t, sfs.root.WriteFile("new.txt", []byte("needle new"), 0o644))

	require.Equal(t, first, execute(script), "text, glob and grep come back from the journal, the unchanged file is read again")

	require.NoError(t, sfs.root.WriteFile("dot.png", []byte("\x89PNG\r\n\x1a\nother"), 0o644))

	var replayed []any
	require.NoError(t, json.Unmarshal([]byte(execute(script)), &replayed))
	require.Equal(t, "read: file changed since the interrupted run: dot.png", replayed[len(replayed)-1])

	var original []any
	require.NoError(t, json.Unmarshal([]byte(first), &original))
	require.Equal(t, []any{"file", "error"}, []any{original[len(original)-2], replayed[len(replayed)-2]})
	require.Equal(t, original[:len(original)-2], replayed[:len(replayed)-2])
	require.Equal(t, []any{"text", "beta\ngamma\n", "notes.txt", 17.0, 2.0, 3.0}, original[:6])
	require.Equal(t, []any{"other.txt", "notes.txt"}, original[8])
	require.Contains(t, original[11], filepath.Join(dir, "other.txt"))
	require.Equal(t, []any{"*.txt", "", "needle", "*.txt", "missing.txt", "missing.txt"}, original[13:19], "search details and a failed read's details come back on replay too")
}
