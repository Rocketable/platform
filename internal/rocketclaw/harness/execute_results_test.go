package harness

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"image"
	"image/png"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
	"go.starlark.net/starlark"
	"golang.org/x/sync/errgroup"

	"github.com/Rocketable/platform/internal/rocketclaw/codemode"
	"github.com/Rocketable/platform/internal/rocketclaw/sandbox"
)

func TestClipExecuteHead(t *testing.T) {
	t.Parallel()

	for _, text := range []string{"", "ok", "€\r\n\xff", strings.Repeat("a\n", 10), strings.Repeat("a\n", executeHeadMaxLines), strings.Repeat("x", executeHeadMaxBytes)} {
		got, oversized := clipExecuteHead(text)
		require.False(t, oversized)
		require.Equal(t, text, got)
	}

	head, oversized := clipExecuteHead(strings.Repeat("line\n", 2100))
	require.True(t, oversized)
	require.Equal(t, strings.Repeat("line\n", executeHeadMaxLines), head)

	long := strings.Repeat("x", executeHeadMaxBytes+10)
	head, oversized = clipExecuteHead(long)
	require.True(t, oversized)
	require.Equal(t, long[:executeHeadMaxBytes], head)
	head, oversized = clipExecuteHead(strings.Repeat("x", executeHeadMaxBytes-1) + "\nnext")
	require.True(t, oversized)
	require.Equal(t, strings.Repeat("x", executeHeadMaxBytes-1)+"\n", head)

	long = strings.Repeat("€", executeHeadMaxBytes/3+1)
	head, oversized = clipExecuteHead(long)
	require.True(t, oversized)
	require.Equal(t, long[:executeHeadMaxBytes], head)
}

func TestExecuteSpillPathRefusesWhatItCannotCheck(t *testing.T) {
	t.Parallel()

	workspace, err := os.OpenRoot(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, workspace.Close()) })

	require.NoError(t, workspace.MkdirAll("moved/spill", 0o700))
	require.NoError(t, workspace.WriteFile("moved/spill/result.txt", []byte("private-result\n"), 0o600))
	require.NoError(t, workspace.Symlink(filepath.Join(workspace.Name(), "moved"), ".rocketcode"))
	require.NoError(t, workspace.Symlink(filepath.Join(workspace.Name(), "moved", "spill"), "alias"))
	require.NoError(t, workspace.Mkdir(".tmp", 0o700))

	_, err = workspace.Stat(defaultSpillRel)
	require.ErrorContains(t, err, "path escapes from parent")

	sfs := &sandboxedFileSystem{root: workspace, spillRel: defaultSpillRel}
	_, err = sfs.Read(t.Context(), "moved/spill/result.txt", 1)
	require.EqualError(t, err, deniedSpillAccess)

	shellTemp := testShellTempConfig(t, workspace, filepath.Join(workspace.Name(), ".tmp"))
	sss := newSandboxedShellSystem(workspace, &shellTemp, nil, &sandbox.Runtime{})
	sss.spillRel = defaultSpillRel
	got := sss.Bash(t.Context(), &CustomShell{}, &bashParams{Command: "cat " + filepath.Join(workspace.Name(), "moved", "spill", "result.txt")})
	require.Equal(t, deniedSpillAccess, got.Output)

	require.NoError(t, workspace.Remove(".rocketcode"))
	require.NoError(t, workspace.Rename("moved", ".rocketcode"))
	_, err = sfs.Read(t.Context(), "alias/result.txt", 1)
	require.EqualError(t, err, deniedSpillAccess)
}

func TestSweepRetainedResults(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })

	require.NoError(t, root.MkdirAll("retained/empty", 0o700))

	for _, tc := range []struct {
		path string
		age  time.Duration
	}{
		{"retained/expired/old.txt", retainedResultTTL + time.Hour},
		{"retained/mixed/old.txt", retainedResultTTL + time.Hour},
		{"retained/mixed/fresh.txt", retainedResultTTL - time.Hour},
		{"retained/recent/fresh.txt", retainedResultTTL - time.Hour},
		{"retained/regular.txt", retainedResultTTL + time.Hour},
	} {
		require.NoError(t, root.MkdirAll(filepath.Dir(tc.path), 0o700))
		require.NoError(t, root.WriteFile(tc.path, []byte(tc.path), 0o600))

		modified := time.Now().Add(-tc.age)
		require.NoError(t, root.Chtimes(tc.path, modified, modified))
	}

	SweepRetainedResults(root, "retained")

	for _, path := range []string{"retained/empty", "retained/expired", "retained/mixed/old.txt"} {
		_, err := root.Stat(path)
		require.ErrorIs(t, err, os.ErrNotExist, path)
	}

	for _, path := range []string{"retained/mixed/fresh.txt", "retained/recent/fresh.txt", "retained/regular.txt"} {
		data, err := root.ReadFile(path)
		require.NoError(t, err)
		require.Equal(t, path, string(data))
	}
}

func TestSaveExecuteResult(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })

	loop := &looper{
		Journal:         InertJournal{},
		observations:    &turnObservations{journal: InertJournal{}},
		promptExpansion: promptExpansionEnvironment{root: root},
		spillRel:        defaultSpillRel,
		Permissions:     PermissionSet{},
		CodeModeHosts:   map[string]looperTool{},
	}
	loop.restoreTurnExecuteResults("turn-1")

	small, err := loop.saveExecuteResult("ok", nil)
	require.NoError(t, err)
	require.Equal(t, TextToolResult("ok"), small)
	require.Empty(t, loop.spillResults)

	full := strings.Repeat("line\n", 2100)
	got, err := loop.saveExecuteResult(full, nil)
	require.NoError(t, err)
	require.Contains(t, got.Output, "...output truncated...")
	require.Contains(t, got.Output, "load_execute_result")
	require.NotContains(t, got.Output, "expires", "the answer names no lifetime, so the foreground and background answers match")
	require.NotContains(t, got.Output, defaultSpillRel)
	require.NotContains(t, got.Output, "read(")

	_, err = loop.saveExecuteResult(full, nil)
	require.NoError(t, err)
	require.Len(t, loop.spillResults, 2)

	for id, path := range loop.spillResults {
		require.NotContains(t, id, "/")

		raw, err := root.ReadFile(path)
		require.NoError(t, err)
		require.Equal(t, full, string(raw))
	}

	require.Empty(t, loop.Permissions.Buckets)
	require.Empty(t, loop.CodeModeHosts)

	before := maps.Clone(loop.spillResults)
	resumed := &looper{promptExpansion: loop.promptExpansion, spillRel: loop.spillRel, Permissions: loop.Permissions}
	resumed.restoreTurnExecuteResults("turn-1")
	require.Equal(t, before, resumed.spillResults)

	for id := range before {
		page, err := resumed.loadExecuteResult(t.Context(), "", loadExecuteResultParams{ResultID: id, StartLine: 2100})
		require.NoError(t, err)
		require.Equal(t, "line\n\n[EOF]\n", page.Output)
	}

	_, err = resumed.saveExecuteResult(full+"more\n", nil)
	require.NoError(t, err)
	require.Len(t, resumed.spillResults, 3)
	require.Empty(t, resumed.Permissions.Buckets)
	require.Empty(t, resumed.CodeModeHosts)

	loop.deleteTurnExecuteResults()

	require.Empty(t, loop.spillResults)

	_, err = root.Stat(defaultSpillRel + "/turn-1")
	require.ErrorIs(t, err, os.ErrNotExist)
	resumed.restoreTurnExecuteResults("turn-1")

	for id := range before {
		_, err := resumed.loadExecuteResult(t.Context(), "", loadExecuteResultParams{ResultID: id})
		require.EqualError(t, err, "unknown or expired execute result")
	}
}

func TestSaveExecuteResultAllocationBudget(t *testing.T) {
	cost := testing.Benchmark(BenchmarkSaveExecuteResult)
	require.Positive(t, cost.N)
	require.Less(t, cost.AllocedBytesPerOp(), int64(512<<10))
}

func BenchmarkSaveExecuteResult(b *testing.B) {
	root, err := os.OpenRoot(b.TempDir())
	require.NoError(b, err)
	b.Cleanup(func() { require.NoError(b, root.Close()) })

	loop := &looper{promptExpansion: promptExpansionEnvironment{root: root}, spillRel: defaultSpillRel}
	loop.restoreTurnExecuteResults("turn")

	full := strings.Repeat("line\n", 1<<18)

	for b.Loop() {
		for _, name := range []string{"turn", "retained"} {
			var got ToolResult
			if name == "retained" {
				got, err = retainExecuteResult(root, "retained", full, nil)
			} else {
				got, err = loop.saveExecuteResult(full, nil)
			}

			require.NoError(b, err)

			_, tail, found := strings.Cut(got.Output, `result_id="`)
			require.True(b, found)

			id, _, _ := strings.Cut(tail, `"`)

			path := loop.spillResults[id]
			if name == "retained" {
				path = "retained/" + id + ".txt"
			}

			require.NoError(b, root.Remove(path))
			delete(loop.spillResults, id)
		}
	}
}

func TestLoadExecuteResultPages(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })

	loop := &looper{promptExpansion: promptExpansionEnvironment{root: root}, spillRel: defaultSpillRel}
	loop.restoreTurnExecuteResults("turn")
	t.Cleanup(loop.deleteTurnExecuteResults)
	_, err = loop.saveExecuteResult(strings.Repeat("line\n", 2100), nil)
	require.NoError(t, err)

	for id, path := range loop.spillResults {
		for _, tc := range []struct {
			name, text, want string
			start, limit     int
			numbers          bool
		}{
			{"defaults", "one\n\nthree", "one\n\nthree\n[EOF]\n", 0, 0, false},
			{"numbered", "one\ntwo\nthree\nfour", "2: two\n3: three\n\n[next_start_line=4]\n", 2, 2, true},
			{"unnumbered", "one\ntwo\nthree\nfour", "two\nthree\n\n[next_start_line=4]\n", 2, 2, false},
			{"crlf", "one\r\n\r\nthree\r\n", "1: one\r\n2: \r\n3: three\r\n\n[EOF]\n", 1, 3, true},
			{"empty", "", "\n[EOF]\n", 1, 0, false},
			{"past EOF", "one\n", "\n[EOF]\n", 2, 1, false},
			{"no final newline", "one\ntwo", "two\n[EOF]\n", 2, 1, false},
			{"limit capped", strings.Repeat("x\n", 2001), strings.Repeat("x\n", 2000) + "\n[next_start_line=2001]\n", 1, 3000, false},
			{"late page", strings.Repeat("x\n", 2001), "2001: x\n\n[EOF]\n", 2001, 10, true},
		} {
			t.Run(tc.name, func(t *testing.T) {
				require.NoError(t, root.WriteFile(path, []byte(tc.text), 0o600))
				page, err := loop.loadExecuteResult(t.Context(), "", loadExecuteResultParams{ResultID: id, StartLine: tc.start, Limit: tc.limit, LineNumbers: tc.numbers})
				require.NoError(t, err)
				require.Equal(t, tc.want, page.Output)
			})
		}

		for _, params := range []loadExecuteResultParams{{ResultID: id, StartLine: -1}, {ResultID: id, Limit: -1}} {
			_, err := loop.loadExecuteResult(t.Context(), "", params)
			require.Error(t, err)
		}

		require.NoError(t, root.Remove(path))
		_, err := loop.loadExecuteResult(t.Context(), "", loadExecuteResultParams{ResultID: id})
		require.EqualError(t, err, "stored execute result unavailable")
	}
}

func TestLoadExecuteResultByteBoundAndContinuation(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })

	loop := &looper{promptExpansion: promptExpansionEnvironment{root: root}, spillRel: defaultSpillRel}
	loop.restoreTurnExecuteResults("turn")
	t.Cleanup(loop.deleteTurnExecuteResults)

	for _, full := range []string{strings.Repeat("€", 1024*1024) + "\nshort\n", "first\n" + strings.Repeat("x", executeHeadMaxBytes-1024-1) + "\nshort\n" + strings.Repeat("tail\n", 2100)} {
		out, err := loop.saveExecuteResult(full, nil)
		require.NoError(t, err)

		_, footer, _ := strings.Cut(out.Output, "result_id=\"")
		id, _, _ := strings.Cut(footer, "\"")
		path := loop.spillResults[id]
		require.NotEmpty(t, path)

		before := maps.Clone(loop.spillResults)
		page, err := loop.loadExecuteResult(t.Context(), "", loadExecuteResultParams{ResultID: id})
		require.NoError(t, err)
		require.LessOrEqual(t, len(page.Output), executeHeadMaxBytes)
		require.True(t, utf8.ValidString(page.Output))
		require.Contains(t, page.Output, "[next_start_line=2]")

		if strings.HasPrefix(full, "€") {
			for _, numbers := range []bool{false, true} {
				page, err = loop.loadExecuteResult(t.Context(), "", loadExecuteResultParams{ResultID: id, LineNumbers: numbers})
				require.NoError(t, err)

				prefix := ""
				if numbers {
					prefix = "1: "
				}

				want := prefix + strings.Repeat("€", (executeHeadMaxBytes-1024-len(prefix))/3) + "\n[line 1 shortened; remaining bytes omitted]\n\n[next_start_line=2]\n"
				require.Equal(t, want, page.Output)
				require.LessOrEqual(t, len(page.Output), executeHeadMaxBytes)
			}

			page, err = loop.loadExecuteResult(t.Context(), "", loadExecuteResultParams{ResultID: id, StartLine: 2})
			require.NoError(t, err)
			require.Equal(t, "short\n\n[EOF]\n", page.Output)
		} else {
			require.Equal(t, "first\n\n[next_start_line=2]\n", page.Output)
			page, err = loop.loadExecuteResult(t.Context(), "", loadExecuteResultParams{ResultID: id, StartLine: 2})
			require.NoError(t, err)
			require.Equal(t, strings.Repeat("x", executeHeadMaxBytes-1024-1)+"\n\n[next_start_line=3]\n", page.Output)
		}

		require.Equal(t, before, loop.spillResults)

		raw, err := root.ReadFile(path)
		require.NoError(t, err)
		require.Equal(t, full, string(raw))
	}
}

func TestLoadExecuteResultMembershipAndCancellation(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })

	loop := &looper{promptExpansion: promptExpansionEnvironment{root: root}, spillRel: defaultSpillRel}
	loop.restoreTurnExecuteResults("turn")
	_, err = loop.saveExecuteResult(strings.Repeat("x", 2*1024*1024)+"\nend", nil)
	require.NoError(t, err)

	for id, path := range loop.spillResults {
		for _, invalid := range []string{"", "unknown", path, "../../etc/passwd", root.Name() + "/" + path} {
			_, err := loop.loadExecuteResult(t.Context(), "", loadExecuteResultParams{ResultID: invalid})
			require.EqualError(t, err, "unknown or expired execute result")
		}

		sibling := &looper{promptExpansion: loop.promptExpansion, spillRel: loop.spillRel}
		sibling.restoreTurnExecuteResults("sibling")
		_, err := sibling.loadExecuteResult(t.Context(), "", loadExecuteResultParams{ResultID: id})
		require.Error(t, err)
		sibling.deleteTurnExecuteResults()

		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		_, err = loop.loadExecuteResult(ctx, "", loadExecuteResultParams{ResultID: id, StartLine: 2})
		require.ErrorIs(t, err, context.Canceled)
		loop.restoreTurnExecuteResults("new-turn")

		_, err = root.Stat(path)
		require.NoError(t, err)
		_, err = loop.loadExecuteResult(t.Context(), "", loadExecuteResultParams{ResultID: id})
		require.Error(t, err)
		loop.deleteTurnExecuteResults()
		require.NoError(t, root.RemoveAll(defaultSpillRel+"/turn"))
	}
}

func TestLoadExecuteResultRejectsSymlinksAndDirectories(t *testing.T) {
	for _, target := range []string{"file", "turn", "storage", "directory"} {
		t.Run(target, func(t *testing.T) {
			root, err := os.OpenRoot(t.TempDir())
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, root.Close()) })

			loop := &looper{promptExpansion: promptExpansionEnvironment{root: root}, spillRel: defaultSpillRel}
			loop.restoreTurnExecuteResults("turn")
			t.Cleanup(loop.deleteTurnExecuteResults)
			_, err = loop.saveExecuteResult(strings.Repeat("line\n", 2100), nil)
			require.NoError(t, err)

			for id, path := range loop.spillResults {
				switch target {
				case "file":
					require.NoError(t, root.WriteFile("secret.txt", []byte("secret"), 0o600))
					require.NoError(t, root.Remove(path))
					require.NoError(t, root.Symlink("../../../secret.txt", path))
				case "directory":
					require.NoError(t, root.Remove(path))
					require.NoError(t, root.Mkdir(path, 0o700))
				default:
					dir := loop.spillRel
					if target == "turn" {
						dir += "/turn"
					}

					require.NoError(t, root.Rename(dir, dir+"-real"))
					require.NoError(t, root.Symlink(filepath.Base(dir)+"-real", dir))
				}

				page, err := loop.loadExecuteResult(t.Context(), "", loadExecuteResultParams{ResultID: id})
				require.Error(t, err)
				require.Empty(t, page.Output)
				loop.restoreTurnExecuteResults("turn")
				page, err = loop.loadExecuteResult(t.Context(), "", loadExecuteResultParams{ResultID: id})
				require.Error(t, err)
				require.Empty(t, page.Output)
			}
		})
	}
}

func TestSaveExecuteResultWriteFailure(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })

	loop := &looper{
		Journal:         InertJournal{},
		observations:    &turnObservations{journal: InertJournal{}},
		promptExpansion: promptExpansionEnvironment{root: root},
		spillRel:        "blocked/spill",
	}
	loop.restoreTurnExecuteResults("turn-1")
	require.NoError(t, root.WriteFile("blocked", []byte("not a dir"), 0o644))

	_, err = loop.saveExecuteResult(strings.Repeat("line\n", 2100), nil)
	require.Error(t, err)
	require.NotContains(t, err.Error(), strings.Repeat("line\n", 10))
	require.Empty(t, loop.spillResults)
}

func TestExecuteResultParallelAssociation(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })

	loop := &looper{promptExpansion: promptExpansionEnvironment{root: root}, spillRel: defaultSpillRel}
	loop.restoreTurnExecuteResults("turn")
	t.Cleanup(loop.deleteTurnExecuteResults)

	var group errgroup.Group
	for i := range 8 {
		group.Go(func() error {
			full := strings.Repeat(fmt.Sprintf("%d\n", i), 2100)

			out, err := loop.saveExecuteResult(full, nil)
			if err != nil {
				return err
			}

			_, footer, _ := strings.Cut(out.Output, "result_id=\"")
			id, _, _ := strings.Cut(footer, "\"")

			page, err := loop.loadExecuteResult(t.Context(), "", loadExecuteResultParams{ResultID: id, StartLine: 2001, Limit: 1})
			if err != nil {
				return err
			}

			if want := fmt.Sprintf("%d\n\n[next_start_line=2002]\n", i); page.Output != want {
				return fmt.Errorf("page = %q, want %q", page.Output, want)
			}

			return nil
		})
	}

	require.NoError(t, group.Wait())
	require.Len(t, loop.spillResults, 8)
}

func testFile(t *testing.T, name, mimeType, content string) *codemode.File {
	t.Helper()

	if mimeType == "image/png" && content == "" {
		var buf bytes.Buffer
		require.NoError(t, png.Encode(&buf, image.NewGray(image.Rect(0, 0, 2, 1))))
		content = buf.String()
	}

	return codemode.NewFile(codemode.FileSpec{Name: name, Mime: mimeType, Content: content, Source: "test"})
}

func TestConvertExecuteReturn(t *testing.T) {
	t.Parallel()

	chart := testFile(t, "chart.png", "image/png", "")
	scan := testFile(t, "scan.pdf", "application/pdf", "%PDF-1.4")
	audio := testFile(t, "a.wav", "audio/wav", "RIFF")
	chartJSON := fmt.Sprintf(`{"attachment":1,"height":1,"mime":"image/png","name":"chart.png","size":%d,"width":2}`, chart.Size())
	scanJSON := `{"attachment":2,"mime":"application/pdf","name":"scan.pdf","size":8}`

	dict := func(items ...starlark.Value) *starlark.Dict {
		d := starlark.NewDict(len(items) / 2)
		for i := 0; i < len(items); i += 2 {
			require.NoError(t, d.SetKey(items[i], items[i+1]))
		}

		return d
	}

	glob := newGlobResult([]globMatch{{Path: "/w/b.go", MTime: 1}, {Path: "/w/a.go", MTime: 2}}, false, globToolParams{})
	grep := newGrepResult([]grepMatch{{Path: "/w/a.go", Line: 3, Text: "x"}}, false, grepToolParams{})
	page := webFetchValue(&webFetchPage{ContentType: "text/plain", Output: "page", URL: "https://example.com/"})
	tool, err := newMCPResult("figma.get", "flat", &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "flat"}}})
	require.NoError(t, err)

	failedBash := codemode.HostResult("bash", newBashStarlarkResult("out", "1"), nil)

	for _, tc := range []struct {
		name  string
		value starlark.Value
		want  string
		files []string
	}{
		{"string", starlark.String("hi"), "hi", nil},
		{"text is raw", codemode.NewText("text", "1: not a line number\nsecond", nil, nil), "1: not a line number\nsecond", nil},
		{"error", codemode.NewFailure("read", "File not found: x", nil), "read: File not found: x", nil},
		{"bash result", newBashStarlarkResult("out", ""), "out", nil},
		{"failed bash result", failedBash, "bash: exit status 1\nout", nil},
		{"other result", codemode.NewMatch("a.go", 3, "x"), "a.go:3: x", nil},
		{"glob result", glob, "/w/a.go\n/w/b.go", nil},
		{"grep result", grep, "Found 1 matches\n/w/a.go:\n  Line 3: x", nil},
		{"webfetch result", page, "page", nil},
		{"mcp result", tool, "flat", nil},
		{
			"typed results inside a dict",
			dict(starlark.String("glob"), glob, starlark.String("grep"), grep, starlark.String("page"), page, starlark.String("tool"), tool, starlark.String("bash"), failedBash),
			`{"bash":{"error":"bash: exit status 1","error_code":"1","ok":false,"output":"out"},"glob":["/w/a.go","/w/b.go"],"grep":[{"line":3,"path":"/w/a.go","text":"x"}],"page":{"url":"https://example.com/","status":0,"status_text":"","content_type":"text/plain","headers":{},"redirects":[],"request":{"method":"","url":"","headers":{}},"timing_ms":{"total":0,"first_byte":0},"output":"page"},"tool":"flat"}`,
			nil,
		},
		{"dict result", codemode.HostResult("decision", dict(starlark.String("b"), starlark.MakeInt(1)), nil), `{"b":1}`, nil},
		{"int", starlark.MakeInt(3), "3", nil},
		{"bool", starlark.True, "true", nil},
		{"float", starlark.Float(1.5), "1.5", nil},
		{"file", chart, fmt.Sprintf("Attached file 1: chart.png (image/png, 2 × 1, %d B)", chart.Size()), []string{"chart.png"}},
		{"file the model cannot take", audio, "Not attached: a.wav (audio/wav, 4 B): the model cannot take audio/wav", nil},
		{"dict with a file", dict(starlark.String("summary"), starlark.String("Chart attached"), starlark.String("chart"), chart), `{"chart":` + chartJSON + `,"summary":"Chart attached"}`, []string{"chart.png"}},
		{
			"numbering follows sorted keys and dedupes",
			dict(
				starlark.String("d"), starlark.Tuple{scan},
				starlark.String("b"), scan,
				starlark.String("c"), starlark.NewList([]starlark.Value{chart, audio, codemode.NewText("text", "raw", nil, nil), codemode.NewFailure("read", "boom", nil)}),
				starlark.String("a"), chart,
			),
			`{"a":` + chartJSON + `,"b":` + scanJSON + `,"c":[` + chartJSON + `,{"mime":"audio/wav","name":"a.wav","not_attached":true,"size":4},"raw",{"error":"read: boom","ok":false}],"d":[` + scanJSON + `]}`,
			[]string{"chart.png", "scan.pdf"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			text, files, _, err := convertExecuteReturn(tc.value, "")
			require.NoError(t, err)
			require.Equal(t, tc.want, text)

			names := make([]string, len(files))
			for i, file := range files {
				require.False(t, file.deferred)

				names[i] = file.file.Name()
			}

			require.Equal(t, strings.Join(tc.files, ","), strings.Join(names, ","), "attached files in number order")
		})
	}

	gathered, err := codemode.Run(t.Context(), "def main():\n    return try_gather([chart, scan])\n", nil, nil, nil, []codemode.HostTool{
		{Details: codemode.NoDetails{}, Name: "chart", CallValue: func(context.Context, map[string]any) (starlark.Value, error) { return chart, nil }},
		{Details: codemode.NoDetails{}, Name: "scan", CallValue: func(context.Context, map[string]any) (starlark.Value, error) { return scan, nil }},
	})
	require.NoError(t, err)

	text, files, _, err := convertExecuteReturn(gathered, "")
	require.NoError(t, err)
	require.Equal(t, `[`+chartJSON+`,`+scanJSON+`]`, text, "a try_gather list returns like a list")
	require.Len(t, files, 2)

	for value, want := range map[starlark.Value]bool{codemode.NewFailure("read", "x", nil): true, failedBash: true, newBashStarlarkResult("out", ""): false, starlark.NewList([]starlark.Value{failedBash}): false} {
		_, _, failed, err := convertExecuteReturn(value, "")
		require.NoError(t, err)
		require.Equal(t, want, failed, "only a returned value whose .ok is False is a failure")
	}

	cycle := starlark.NewList(nil)
	require.NoError(t, cycle.Append(cycle))

	for value, want := range map[starlark.Value]string{starlark.Bytes("x"): "encode main result: json.encode: in field .elems: cannot encode builtin_function_or_method as JSON", cycle: "cycle in JSON structure"} {
		_, _, _, err := convertExecuteReturn(value, "")
		require.ErrorContains(t, err, want)
	}
}

func TestExecuteReturnBudget(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })

	loop := &looper{promptExpansion: promptExpansionEnvironment{root: root}, spillRel: defaultSpillRel}
	loop.restoreTurnExecuteResults("turn")
	t.Cleanup(loop.deleteTurnExecuteResults)

	pdf := func(name string, size int) *codemode.File {
		return testFile(t, name, "application/pdf", "%PDF-"+strings.Repeat(name[:1], size-5))
	}
	placeholder := func(n int, file *codemode.File, deferred bool) string {
		flag := ""
		if deferred {
			flag = `"deferred":true,`
		}

		return fmt.Sprintf(`{"attachment":%d,%s"mime":"application/pdf","name":%q,"size":%d}`, n, flag, file.Name(), file.Size())
	}
	resultID := func(result ToolResult) string {
		_, tail, _ := strings.Cut(result.Output, `result_id="`)
		id, _, _ := strings.Cut(tail, `"`)

		return id
	}
	dataURL := func(file *codemode.File) string {
		return "data:" + file.Mime() + ";base64," + base64.StdEncoding.EncodeToString([]byte(file.Content()))
	}

	chart := testFile(t, "a.png", "image/png", "")
	big, mid, small, huge := pdf("big.pdf", 15<<20), pdf("mid.pdf", 10<<20), pdf("small.pdf", 4<<20), pdf("huge.pdf", 21<<20)
	text, files, _, err := convertExecuteReturn(starlark.NewList([]starlark.Value{chart, big, mid, small, huge}), "")
	require.NoError(t, err)

	result, err := loop.saveExecuteResult(text, files)
	require.NoError(t, err)

	id := resultID(result)
	want := fmt.Sprintf(`[{"attachment":1,"height":1,"mime":"image/png","name":"a.png","size":%d,"width":2},`, chart.Size()) + placeholder(2, big, false) + "," + placeholder(3, mid, true) + "," + placeholder(4, small, false) + "," + placeholder(5, huge, true) + "]\n\n" +
		`Not attached (over this call's 20 MiB budget): 3 mid.pdf (10 MiB), 5 huge.pdf (21 MiB). Fetch each with load_execute_result(result_id="` + id + `", attachment=n).`
	require.Equal(t, want, result.Output, "a file that does not fit is held back and a later smaller one is still attached")
	require.Equal(t, []Attachment{
		{MIME: "image/png", Filename: "a.png", URL: dataURL(chart)},
		{MIME: "application/pdf", Filename: "big.pdf", URL: dataURL(big)},
		{MIME: "application/pdf", Filename: "small.pdf", URL: dataURL(small)},
	}, result.Attachments)
	require.Nil(t, result.Data, "a foreground result keeps no files for a note")

	for _, looper := range []*looper{loop, {promptExpansion: loop.promptExpansion, spillRel: loop.spillRel}} {
		looper.restoreTurnExecuteResults("turn")

		for n, file := range map[int]*codemode.File{3: mid, 5: huge} {
			page, err := looper.loadExecuteResult(t.Context(), "", loadExecuteResultParams{ResultID: id, Attachment: n})
			require.NoError(t, err)
			require.Equal(t, ToolResult{Output: fmt.Sprintf("Attachment %d: %s", n, file), Attachments: []Attachment{{MIME: "application/pdf", Filename: file.Name(), URL: dataURL(file)}}}, page)
		}
	}

	_, err = loop.loadExecuteResult(t.Context(), "", loadExecuteResultParams{ResultID: id, Attachment: 2})
	require.EqualError(t, err, "this execute result has no stored attachment 2")
	_, err = loop.loadExecuteResult(t.Context(), "", loadExecuteResultParams{ResultID: id, Attachment: -1})
	require.EqualError(t, err, "start_line, limit and attachment must not be negative")
	_, err = loop.loadExecuteResult(t.Context(), "", loadExecuteResultParams{ResultID: id})
	require.EqualError(t, err, "stored execute result unavailable", "only the files were stored")

	lines := slices.Repeat([]starlark.Value{starlark.String("line")}, 8000)
	text, files, _, err = convertExecuteReturn(starlark.NewList(append(lines, huge)), "")
	require.NoError(t, err)
	result, err = loop.saveExecuteResult(text, files)
	require.NoError(t, err)

	id = resultID(result)
	require.Contains(t, result.Output, "...output truncated...")
	require.True(t, strings.HasSuffix(result.Output, "Fetch the rest with the top-level load_execute_result tool, using this result_id, start_line, limit and line_numbers.\n\nNot attached (over this call's 20 MiB budget): 1 huge.pdf (21 MiB). Fetch with load_execute_result(result_id=\""+id+"\", attachment=1)."), "the files trailer survives clipping")

	spilled, err := root.ReadFile(loop.spillResults[id])
	require.NoError(t, err)
	require.True(t, strings.HasSuffix(string(spilled), `"line",`+placeholder(1, huge, true)+"]"))

	loop.deleteTurnExecuteResults()
	loop.restoreTurnExecuteResults("turn")
	_, err = loop.loadExecuteResult(t.Context(), "", loadExecuteResultParams{ResultID: id, Attachment: 1})
	require.EqualError(t, err, "unknown or expired execute result", "foreground files expire with the turn")
}

func TestRetainedExecuteResultKeepsFilesForTheNote(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })

	chart := testFile(t, "a.png", "image/png", "")
	huge := testFile(t, "huge.pdf", "application/pdf", "%PDF-"+strings.Repeat("h", 21<<20))
	text, files, _, err := convertExecuteReturn(starlark.Tuple{chart, huge}, "")
	require.NoError(t, err)

	small, err := retainExecuteResult(root, "retained", "ok", nil)
	require.NoError(t, err)
	require.Equal(t, TextToolResult("ok"), small)

	result, err := retainExecuteResult(root, "retained", text, files)
	require.NoError(t, err)

	id := result.Data.(RetainedFiles).ResultID
	require.Contains(t, result.Output, `load_execute_result(result_id="`+id+`", attachment=2)`)
	require.Equal(t, []Attachment{fileAttachment(chart)}, result.Attachments)

	kept, err := LoadRetainedFiles(root, "retained", id)
	require.NoError(t, err)
	require.Len(t, kept, 1, "the note carries the attached files only")
	require.Equal(t, fileAttachment(chart), fileAttachment(kept[0]))

	loop := &looper{promptExpansion: promptExpansionEnvironment{root: root}, spillRel: defaultSpillRel}
	loop.restoreTurnExecuteResults("later")
	t.Cleanup(loop.deleteTurnExecuteResults)

	for n, file := range map[int]*codemode.File{1: chart, 2: huge} {
		page, err := loop.loadExecuteResult(t.Context(), "retained", loadExecuteResultParams{ResultID: id, Attachment: n})
		require.NoError(t, err)
		require.Equal(t, []Attachment{fileAttachment(file)}, page.Attachments)
	}

	old := time.Now().Add(-retainedResultTTL - time.Hour)
	require.NoError(t, root.Chtimes("retained/"+id+".files.json", old, old))
	_, err = loop.loadExecuteResult(t.Context(), "retained", loadExecuteResultParams{ResultID: id, Attachment: 2})
	require.EqualError(t, err, "unknown or expired execute result")
	_, err = LoadRetainedFiles(root, "retained", id)
	require.EqualError(t, err, "unknown or expired execute result")
}
