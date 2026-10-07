package rocketcode

import (
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
)

func TestClipExecuteHead(t *testing.T) {
	t.Parallel()

	for _, text := range []string{"", "ok", strings.Repeat("a\n", 10), strings.Repeat("a\n", executeHeadMaxLines), strings.Repeat("x", executeHeadMaxBytes)} {
		got, oversized := clipExecuteHead(text)
		require.False(t, oversized)
		require.Equal(t, text, got)
	}

	head, oversized := clipExecuteHead(strings.Repeat("line\n", 2100))
	require.True(t, oversized)
	require.Equal(t, 2000, strings.Count(head, "\n"))
	require.NotContains(t, head, "output truncated")

	long := strings.Repeat("x", executeHeadMaxBytes+10)
	head, oversized = clipExecuteHead(long)
	require.True(t, oversized)
	require.LessOrEqual(t, len(head), executeHeadMaxBytes)
	head, oversized = clipExecuteHead(strings.Repeat("x", executeHeadMaxBytes-1) + "\nnext")
	require.True(t, oversized)
	require.Equal(t, strings.Repeat("x", executeHeadMaxBytes-1)+"\n", head)
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

	small, err := loop.saveExecuteResult("ok")
	require.NoError(t, err)
	require.Equal(t, "ok", small)
	require.Empty(t, loop.spillResults)

	full := strings.Repeat("line\n", 2100)
	got, err := loop.saveExecuteResult(full)
	require.NoError(t, err)
	require.Contains(t, got, "...output truncated...")
	require.Contains(t, got, "load_execute_result")
	require.Contains(t, got, "This result expires when this turn ends.")
	require.NotContains(t, got, defaultSpillRel)
	require.NotContains(t, got, "read(")

	_, err = loop.saveExecuteResult(full)
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

	_, err = resumed.saveExecuteResult(full + "more\n")
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

func TestLoadExecuteResultPages(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })

	loop := &looper{promptExpansion: promptExpansionEnvironment{root: root}, spillRel: defaultSpillRel}
	loop.restoreTurnExecuteResults("turn")
	t.Cleanup(loop.deleteTurnExecuteResults)
	_, err = loop.saveExecuteResult(strings.Repeat("line\n", 2100))
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
		out, err := loop.saveExecuteResult(full)
		require.NoError(t, err)

		_, footer, _ := strings.Cut(out, "result_id=\"")
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
	_, err = loop.saveExecuteResult(strings.Repeat("x", 2*1024*1024) + "\nend")
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
		loop.restoreTurnExecuteResults("new-turn") // Deliberately retain old bytes to test membership, not deletion.

		_, err = root.Stat(path)
		require.NoError(t, err)
		_, err = loop.loadExecuteResult(t.Context(), "", loadExecuteResultParams{ResultID: id})
		require.Error(t, err)
		loop.deleteTurnExecuteResults() // A canceled loader must release the lifetime lock.
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
			_, err = loop.saveExecuteResult(strings.Repeat("line\n", 2100))
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

	_, err = loop.saveExecuteResult(strings.Repeat("line\n", 2100))
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

			out, err := loop.saveExecuteResult(full)
			if err != nil {
				return err
			}

			_, footer, _ := strings.Cut(out, "result_id=\"")
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

func TestMakeSandboxedToolsHasRead(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })

	tmp := filepath.Join(dir, "tmp")

	require.NoError(t, root.Mkdir("tmp", 0o755))
	tools := newSandboxedTools(root, defaultSpillRel, testShellTempConfig(t, root, tmp), nil, DefaultShellCommand)
	require.NotNil(t, tools["read"].Call)
}
