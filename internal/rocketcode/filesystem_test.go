package rocketcode

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type oracleInput struct {
	Worktree  string `json:"worktree"`
	PatchText string `json:"patchText"`
}

func TestIsDeniedEnvPath(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{path: ".env", want: true},
		{path: ".env.local", want: true},
		{path: ".env.production", want: true},
		{path: ".env.development.local", want: true},
		{path: "nested/.env", want: true},
		{path: "service.env", want: true},
		{path: ".env.example", want: false},
		{path: "nested/.env.example", want: false},
		{path: ".envrc", want: false},
		{path: "environment.ts", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			require.Equal(t, tt.want, isDeniedEnvPath(tt.path))
		})
	}
}

type oracleTreeEntry struct {
	Path          string `json:"path"`
	Type          string `json:"type"`
	ContentBase64 string `json:"contentBase64,omitempty"`
}

type oracleResult struct {
	OK     bool                 `json:"ok"`
	Output string               `json:"output,omitempty"`
	Error  string               `json:"error,omitempty"`
	Diff   string               `json:"diff,omitempty"`
	Files  []applyPatchFileMeta `json:"files,omitempty"`
	Tree   []oracleTreeEntry    `json:"tree"`
}

func seedRoot(t *testing.T, root *os.Root, seed map[string][]byte, setup func(*testing.T, *os.Root)) {
	t.Helper()

	for name, content := range seed {
		require.NoError(t, root.MkdirAll(filepath.Dir(name), 0o755))
		require.NoError(t, root.WriteFile(name, content, 0o644))
	}

	if setup != nil {
		setup(t, root)
	}
}

func requireRootSymlink(t *testing.T, root *os.Root, oldname, newname string) {
	t.Helper()

	if err := root.Symlink(oldname, newname); err != nil {
		t.Skipf("symlink not available: %v", err)
	}
}

func runOpenCodeOracle(t *testing.T, dir, patchText string) oracleResult {
	t.Helper()

	payload, err := json.Marshal(oracleInput{Worktree: dir, PatchText: patchText})
	require.NoError(t, err)

	cmd := exec.Command("node", filepath.Join("testdata", "opencode", "oracle", "run-apply-patch.mjs"))
	cmd.Dir, err = os.Getwd()
	require.NoError(t, err)

	cmd.Stdin = bytes.NewReader(payload)
	out, err := cmd.Output()
	require.NoError(t, err)

	var result oracleResult
	require.NoError(t, json.Unmarshal(out, &result))

	return result
}

func snapshotTree(t *testing.T, rootDir string) []oracleTreeEntry {
	t.Helper()

	entries := []oracleTreeEntry{}
	err := filepath.WalkDir(rootDir, func(path string, d fs.DirEntry, err error) error {
		require.NoError(t, err)

		if path == rootDir {
			return nil
		}

		rel, err := filepath.Rel(rootDir, path)
		require.NoError(t, err)

		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			entries = append(entries, oracleTreeEntry{Path: rel + "/", Type: "dir", ContentBase64: ""})
			return nil
		}

		data, err := os.ReadFile(path)
		require.NoError(t, err)

		entries = append(entries, oracleTreeEntry{Path: rel, Type: "file", ContentBase64: base64.StdEncoding.EncodeToString(data)})

		return nil
	})
	require.NoError(t, err)

	return entries
}

func runGoApplyPatch(t *testing.T, dir, patchText string) oracleResult {
	t.Helper()

	root, err := os.OpenRoot(dir)
	require.NoError(t, err)

	defer func() { require.NoError(t, root.Close()) }()

	sfs := &sandboxedFileSystem{mu: sync.Mutex{}, root: root}
	preview, previewMessage := previewApplyPatch(sfs, patchText)
	output := sfs.ApplyPatch(patchText)

	result := oracleResult{OK: false, Output: "", Error: "", Diff: "", Files: nil, Tree: snapshotTree(t, dir)}
	if strings.HasPrefix(output, "Success. Updated the following files:") {
		require.Empty(t, previewMessage)

		result.OK = true
		result.Output = output
		result.Diff = preview.diff
		result.Files = preview.files

		return result
	}

	result.Error = output

	return result
}

func requireParity(t *testing.T, patchText string, seed map[string][]byte) {
	t.Helper()
	_ = runParity(t, patchText, seed, nil)
}

func runParity(t *testing.T, patchText string, seed map[string][]byte, setup func(*testing.T, *os.Root)) oracleResult {
	t.Helper()
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	seedRoot(t, root, seed, setup)
	require.NoError(t, root.Close())
	oracle := runOpenCodeOracle(t, dir, patchText)
	require.NoError(t, os.RemoveAll(dir))
	require.NoError(t, os.MkdirAll(dir, 0o755))
	root, err = os.OpenRoot(dir)
	require.NoError(t, err)
	seedRoot(t, root, seed, setup)
	require.NoError(t, root.Close())
	local := runGoApplyPatch(t, dir, patchText)
	require.Equal(t, oracle, local)

	return local
}

func requireParityWithSetup(t *testing.T, patchText string, seed map[string][]byte, setup func(*testing.T, *os.Root)) {
	t.Helper()
	_ = runParity(t, patchText, seed, setup)
}

func TestTSandboxedFileSystem(t *testing.T) {
	dir := t.TempDir()

	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })
	require.NoError(t, root.WriteFile("small.txt", []byte("hello world"), 0o644))
	require.NoError(t, root.WriteFile("offset.txt", []byte("line1\nline2\nline3"), 0o644))
	require.NoError(t, root.WriteFile(".env", []byte("SECRET=value"), 0o644))
	require.NoError(t, root.WriteFile(".env.example", []byte("SECRET=example"), 0o644))
	requireRootSymlink(t, root, ".env", "safe-env-link.txt")
	require.NoError(t, root.WriteFile("image.png", []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}, 0o644))
	require.NoError(t, root.WriteFile("doc.pdf", []byte("%PDF-1.7\n"), 0o644))

	var manyLines strings.Builder
	for i := 1; i <= 2100; i++ {
		fmt.Fprintf(&manyLines, "line%d", i)

		if i < 2100 {
			manyLines.WriteString("\n")
		}
	}

	require.NoError(t, root.WriteFile("many-lines.txt", []byte(manyLines.String()), 0o644))

	sfs := &sandboxedFileSystem{
		mu:   sync.Mutex{},
		root: root,
	}

	{
		file := sfs.ReadResult("small.txt", 1).Output
		require.Contains(t, file, "<path>small.txt</path>")
		require.Contains(t, file, "<type>file</type>")
		require.Contains(t, file, "1: hello world")
		require.Contains(t, file, "(End of file - total 1 lines)")
	}

	{
		path := filepath.Join(root.Name(), "small.txt")
		file := sfs.ReadResult(path, 1).Output
		require.Contains(t, file, "<path>"+path+"</path>")
		require.Contains(t, file, "1: hello world")
		require.Equal(t, "small.txt", sfs.readPermissionSubject(path))
	}

	{
		path := filepath.Join(t.TempDir(), "outside.txt")
		file := sfs.ReadResult(path, 1).Output
		require.Equal(t, "path escapes root: "+path, file)
	}

	{
		file := sfs.ReadResult("offset.txt", 2).Output
		require.Contains(t, file, "2: line2")
		require.Contains(t, file, "3: line3")
		require.Contains(t, file, "(End of file - total 3 lines)")
	}

	{
		file := sfs.ReadResult("many-lines.txt", 1).Output
		require.Contains(t, file, "1: line1")
		require.Contains(t, file, "2000: line2000")
		require.Contains(t, file, "(Showing lines 1-2000 of 2100. Use offset=2001 to continue.)")
		require.NotContains(t, file, "2001: line2001")
	}

	{
		file := sfs.ReadResult("offset.txt", 4).Output
		require.Equal(t, "Offset 4 is out of range for this file (3 lines)", file)
	}

	{
		file := sfs.ReadResult("file_b.txt", 30).Output
		require.Equal(t, "File not found: file_b.txt", file)
	}

	{
		file := sfs.ReadResult(".env", 1).Output
		require.Equal(t, deniedEnvAccessMessage(".env"), file)
	}

	{
		file := sfs.ReadResult(".env.example", 1).Output
		require.Contains(t, file, "1: SECRET=example")
	}

	{
		file := sfs.ReadResult("safe-env-link.txt", 1).Output
		require.Equal(t, "symlink access denied: safe-env-link.txt", file)
	}

	{
		result := sfs.ReadResult("image.png", 1)
		require.Equal(t, "Image read successfully", result.Output)
		require.Len(t, result.Attachments, 1)
		require.Equal(t, "image/png", result.Attachments[0].MIME)
		require.Contains(t, result.Attachments[0].URL, "data:image/png;base64,")
	}

	{
		result := sfs.ReadResult("doc.pdf", 1)
		require.Equal(t, "PDF read successfully", result.Output)
		require.Len(t, result.Attachments, 1)
		require.Equal(t, "application/pdf", result.Attachments[0].MIME)
		require.Contains(t, result.Attachments[0].URL, "data:application/pdf;base64,")
	}
}

func TestSandboxedFileSystemGlob(t *testing.T) {
	dir := t.TempDir()

	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })

	require.NoError(t, root.MkdirAll("glob/nested", 0o755))
	require.NoError(t, root.WriteFile("glob/old.txt", []byte("old"), 0o644))
	require.NoError(t, root.WriteFile("glob/new.txt", []byte("new"), 0o644))
	require.NoError(t, root.WriteFile("glob/nested/inside.txt", []byte("inside"), 0o644))
	require.NoError(t, root.WriteFile("glob/skip.md", []byte("skip"), 0o644))
	require.NoError(t, root.WriteFile("glob/.env", []byte("secret"), 0o644))
	require.NoError(t, root.WriteFile("glob/.env.local", []byte("secret"), 0o644))
	require.NoError(t, root.WriteFile("glob/.env.example", []byte("example"), 0o644))
	requireRootSymlink(t, root, "new.txt", "glob/link.txt")
	requireRootSymlink(t, root, "nested", "glob/linkdir")
	require.NoError(t, root.WriteFile("ripgreprc", []byte("--bad-flag\n"), 0o644))

	older := time.Unix(1_700_000_000, 0)
	newer := older.Add(time.Hour)
	inside := newer.Add(time.Hour)

	require.NoError(t, root.Chtimes("glob/old.txt", older, older))
	require.NoError(t, root.Chtimes("glob/new.txt", newer, newer))
	require.NoError(t, root.Chtimes("glob/nested/inside.txt", inside, inside))

	sfs := &sandboxedFileSystem{mu: sync.Mutex{}, root: root}

	t.Run("no matches", func(t *testing.T) {
		require.Equal(t, "No files found", sfs.Glob(context.Background(), "**/*.pdf", "glob"))
	})

	t.Run("sorts by newest first within search path", func(t *testing.T) {
		got := sfs.Glob(context.Background(), "*.txt", "glob")
		require.Equal(t, strings.Join([]string{
			filepath.Join(dir, "glob", "nested", "inside.txt"),
			filepath.Join(dir, "glob", "new.txt"),
			filepath.Join(dir, "glob", "old.txt"),
		}, "\n"), got)
	})

	t.Run("searches nested path from sandbox root", func(t *testing.T) {
		got := sfs.Glob(context.Background(), "**/*.txt", "glob")
		require.Equal(t, strings.Join([]string{
			filepath.Join(dir, "glob", "nested", "inside.txt"),
			filepath.Join(dir, "glob", "new.txt"),
			filepath.Join(dir, "glob", "old.txt"),
		}, "\n"), got)
	})

	t.Run("searches absolute in root path", func(t *testing.T) {
		got := sfs.Glob(context.Background(), "*.txt", filepath.Join(root.Name(), "glob"))
		require.Equal(t, strings.Join([]string{
			filepath.Join(dir, "glob", "nested", "inside.txt"),
			filepath.Join(dir, "glob", "new.txt"),
			filepath.Join(dir, "glob", "old.txt"),
		}, "\n"), got)
	})

	t.Run("rejects absolute outside root path", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "glob")
		require.Equal(t, "path escapes root: "+path, sfs.Glob(context.Background(), "*.txt", path))
	})

	t.Run("does not follow or return symlinks", func(t *testing.T) {
		got := sfs.Glob(context.Background(), "**/*.txt", "glob")
		require.NotContains(t, got, filepath.Join(dir, "glob", "link.txt"))
		require.NotContains(t, got, filepath.Join(dir, "glob", "linkdir"))
	})

	t.Run("ignores ripgrep config", func(t *testing.T) {
		t.Setenv("RIPGREP_CONFIG_PATH", filepath.Join(dir, "ripgreprc"))

		got := sfs.Glob(context.Background(), "*.txt", "glob")
		require.Contains(t, got, filepath.Join(dir, "glob", "new.txt"))
	})

	t.Run("reports invalid pattern", func(t *testing.T) {
		got := sfs.Glob(context.Background(), "[", "glob")
		require.Contains(t, got, "error parsing glob")
	})

	t.Run("reports invalid search path", func(t *testing.T) {
		got := sfs.Glob(context.Background(), "*.txt", "missing")
		require.Contains(t, got, "missing")
		require.Contains(t, got, "no such file or directory")
	})

	t.Run("filters denied env files", func(t *testing.T) {
		got := sfs.Glob(context.Background(), ".env*", "glob")
		require.Equal(t, filepath.Join(dir, "glob", ".env.example"), got)
	})

	t.Run("truncates after 100 results", func(t *testing.T) {
		require.NoError(t, root.MkdirAll("many", 0o755))

		base := time.Unix(1_800_000_000, 0)

		for i := range 101 {
			name := filepath.ToSlash(filepath.Join("many", fmt.Sprintf("file-%03d.txt", i)))
			require.NoError(t, root.WriteFile(name, []byte(strconv.Itoa(i)), 0o644))
			mtime := base.Add(time.Duration(i) * time.Minute)
			require.NoError(t, root.Chtimes(name, mtime, mtime))
		}

		got := sfs.Glob(context.Background(), "*.txt", "many")
		lines := strings.Split(got, "\n")
		require.Len(t, lines, 102)
		require.Equal(t, filepath.Join(dir, "many", "file-100.txt"), lines[0])
		require.Equal(t, filepath.Join(dir, "many", "file-001.txt"), lines[99])
		require.Empty(t, lines[100])
		require.Equal(t, "(Results are truncated: showing first 100 results. Consider using a more specific path or pattern.)", lines[101])
		require.NotContains(t, got, filepath.Join(dir, "many", "file-000.txt"))
	})
}

func TestSandboxedFileSystemGrep(t *testing.T) {
	dir := t.TempDir()

	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })

	require.NoError(t, root.MkdirAll("grep/nested", 0o755))
	require.NoError(t, root.WriteFile("grep/old.txt", []byte("needle old\nshared"), 0o644))
	require.NoError(t, root.WriteFile("grep/new.txt", []byte("needle new\nshared"), 0o644))
	require.NoError(t, root.WriteFile("grep/nested/inside.txt", []byte("needle inside\nshared"), 0o644))
	require.NoError(t, root.WriteFile("grep/skip.md", []byte("needle markdown"), 0o644))
	require.NoError(t, root.WriteFile("grep/.env", []byte("secret needle"), 0o644))
	require.NoError(t, root.WriteFile("grep/.env.example", []byte("example needle"), 0o644))
	requireRootSymlink(t, root, ".env", "grep/link.txt")
	require.NoError(t, root.WriteFile("grep-ripgreprc", []byte("--bad-flag\n"), 0o644))

	older := time.Unix(1_700_100_000, 0)
	newer := older.Add(time.Hour)
	inside := newer.Add(time.Hour)

	require.NoError(t, root.Chtimes("grep/old.txt", older, older))
	require.NoError(t, root.Chtimes("grep/new.txt", newer, newer))
	require.NoError(t, root.Chtimes("grep/nested/inside.txt", inside, inside))
	require.NoError(t, root.Chtimes("grep/skip.md", inside.Add(time.Hour), inside.Add(time.Hour)))

	sfs := &sandboxedFileSystem{mu: sync.Mutex{}, root: root}

	t.Run("no matches", func(t *testing.T) {
		require.Equal(t, "No files found", sfs.Grep(context.Background(), "missing", "grep", ""))
	})

	t.Run("searches directory sorted by newest file first", func(t *testing.T) {
		got := sfs.Grep(context.Background(), "needle", "grep", "*.txt")
		require.Equal(t, strings.Join([]string{
			"Found 3 matches",
			filepath.Join(dir, "grep", "nested", "inside.txt") + ":",
			"  Line 1: needle inside",
			"",
			filepath.Join(dir, "grep", "new.txt") + ":",
			"  Line 1: needle new",
			"",
			filepath.Join(dir, "grep", "old.txt") + ":",
			"  Line 1: needle old",
		}, "\n"), got)
	})

	t.Run("searches exact file path", func(t *testing.T) {
		got := sfs.Grep(context.Background(), "shared", "grep/new.txt", "")
		require.Equal(t, strings.Join([]string{
			"Found 1 matches",
			filepath.Join(dir, "grep", "new.txt") + ":",
			"  Line 2: shared",
		}, "\n"), got)
	})

	t.Run("searches absolute in root directory", func(t *testing.T) {
		got := sfs.Grep(context.Background(), "needle", filepath.Join(root.Name(), "grep"), "*.txt")
		require.Contains(t, got, filepath.Join(dir, "grep", "new.txt")+":")
		require.Contains(t, got, "  Line 1: needle new")
	})

	t.Run("searches absolute in root file", func(t *testing.T) {
		got := sfs.Grep(context.Background(), "shared", filepath.Join(root.Name(), "grep", "new.txt"), "")
		require.Equal(t, strings.Join([]string{
			"Found 1 matches",
			filepath.Join(dir, "grep", "new.txt") + ":",
			"  Line 2: shared",
		}, "\n"), got)
	})

	t.Run("rejects absolute outside root path", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "grep")
		require.Equal(t, "path escapes root: "+path, sfs.Grep(context.Background(), "needle", path, ""))
	})

	t.Run("include filters files", func(t *testing.T) {
		got := sfs.Grep(context.Background(), "needle", "grep", "*.md")
		require.Equal(t, strings.Join([]string{
			"Found 1 matches",
			filepath.Join(dir, "grep", "skip.md") + ":",
			"  Line 1: needle markdown",
		}, "\n"), got)
	})

	t.Run("truncates after one hundred matches", func(t *testing.T) {
		require.NoError(t, root.MkdirAll("grep/many", 0o755))

		base := time.Unix(1_800_100_000, 0)

		for i := range 101 {
			name := filepath.ToSlash(filepath.Join("grep", "many", fmt.Sprintf("file-%03d.txt", i)))
			content := fmt.Sprintf("needle %03d", i)
			require.NoError(t, root.WriteFile(name, []byte(content), 0o644))

			mtime := base.Add(time.Duration(i) * time.Minute)
			require.NoError(t, root.Chtimes(name, mtime, mtime))
		}

		got := sfs.Grep(context.Background(), "needle", "grep/many", "*.txt")
		lines := strings.Split(got, "\n")
		require.Len(t, lines, 302)
		require.Equal(t, "Found 101 matches (showing first 100)", lines[0])
		require.Equal(t, filepath.Join(dir, "grep", "many", "file-100.txt")+":", lines[1])
		require.Equal(t, "  Line 1: needle 100", lines[2])
		require.Equal(t, filepath.Join(dir, "grep", "many", "file-001.txt")+":", lines[298])
		require.Equal(t, "  Line 1: needle 001", lines[299])
		require.Empty(t, lines[300])
		require.Equal(t, "(Results truncated: showing 100 of 101 matches (1 hidden). Consider using a more specific path or pattern.)", lines[301])
		require.NotContains(t, got, filepath.Join(dir, "grep", "many", "file-000.txt"))
	})

	t.Run("truncates long matching lines", func(t *testing.T) {
		longLine := strings.Repeat("a", maxLineLength+5)
		require.NoError(t, root.WriteFile("grep/long.txt", []byte(longLine), 0o644))

		mtime := inside.Add(2 * time.Hour)
		require.NoError(t, root.Chtimes("grep/long.txt", mtime, mtime))

		got := sfs.Grep(context.Background(), "a+", "grep/long.txt", "")
		require.Contains(t, got, "  Line 1: "+strings.Repeat("a", maxLineLength)+"...")
	})

	t.Run("reports invalid regex", func(t *testing.T) {
		got := sfs.Grep(context.Background(), "(", "grep", "")
		require.Contains(t, got, "regex parse error")
	})

	t.Run("reports invalid search path", func(t *testing.T) {
		got := sfs.Grep(context.Background(), "needle", "missing", "")
		require.Contains(t, got, "missing")
		require.Contains(t, got, "no such file or directory")
	})

	t.Run("denies exact symlink target", func(t *testing.T) {
		require.Equal(t, "symlink access denied: grep/link.txt", sfs.Grep(context.Background(), "needle", "grep/link.txt", ""))
	})

	t.Run("does not follow symlink entries", func(t *testing.T) {
		got := sfs.Grep(context.Background(), "secret", "grep", "*.txt")
		require.Equal(t, "No files found", got)
	})

	t.Run("ignores ripgrep config", func(t *testing.T) {
		t.Setenv("RIPGREP_CONFIG_PATH", filepath.Join(dir, "grep-ripgreprc"))

		got := sfs.Grep(context.Background(), "needle", "grep/new.txt", "")
		require.Contains(t, got, "Found 1 matches")
	})

	t.Run("denies exact env file target", func(t *testing.T) {
		require.Equal(t, deniedEnvAccessMessage("grep/.env"), sfs.Grep(context.Background(), "needle", "grep/.env", ""))
	})

	t.Run("excludes denied env files but allows env example", func(t *testing.T) {
		got := sfs.Grep(context.Background(), "needle", "grep", ".env*")
		require.Contains(t, got, filepath.Join(dir, "grep", ".env.example")+":")
		require.NotContains(t, got, filepath.Join(dir, "grep", ".env")+":")
		require.NotContains(t, got, "secret needle")
	})
}

func TestApplyPatchSymlinkDenied(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	require.NoError(t, root.WriteFile(".env", []byte("SECRET=old\n"), 0o644))
	requireRootSymlink(t, root, ".env", "safe.txt")
	require.NoError(t, root.Close())

	result := runGoApplyPatch(t, dir, "*** Begin Patch\n*** Update File: safe.txt\n@@\n-SECRET=old\n+SECRET=new\n*** End Patch")
	require.False(t, result.OK)
}

func TestApplyPatchEnvFileHardDeny(t *testing.T) {
	tests := []struct {
		name  string
		seed  map[string][]byte
		patch string
	}{
		{
			name:  "add denied env file",
			seed:  nil,
			patch: "*** Begin Patch\n*** Add File: .env\n+SECRET=value\n*** End Patch",
		},
		{
			name:  "update denied env file",
			seed:  map[string][]byte{".env": []byte("SECRET=old\n")},
			patch: "*** Begin Patch\n*** Update File: .env\n@@\n-SECRET=old\n+SECRET=new\n*** End Patch",
		},
		{
			name:  "delete denied env file",
			seed:  map[string][]byte{".env.local": []byte("SECRET=old\n")},
			patch: "*** Begin Patch\n*** Delete File: .env.local\n*** End Patch",
		},
		{
			name:  "move to denied env file",
			seed:  map[string][]byte{"safe.txt": []byte("SECRET=old\n")},
			patch: "*** Begin Patch\n*** Update File: safe.txt\n*** Move to: .env.production\n@@\n-SECRET=old\n+SECRET=new\n*** End Patch",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			root, err := os.OpenRoot(dir)
			require.NoError(t, err)
			seedRoot(t, root, tt.seed, nil)
			require.NoError(t, root.Close())

			result := runGoApplyPatch(t, dir, tt.patch)
			require.False(t, result.OK)
			require.Contains(t, result.Error, "access denied: .env files are blocked")
		})
	}

	t.Run("allows env example", func(t *testing.T) {
		dir := t.TempDir()
		result := runGoApplyPatch(t, dir, "*** Begin Patch\n*** Add File: .env.example\n+SECRET=example\n*** End Patch")
		require.True(t, result.OK)
		require.Equal(t, "Success. Updated the following files:\nA .env.example", result.Output)
	})
}

func TestApplyPatchAbsolutePaths(t *testing.T) {
	dir := t.TempDir()
	patch := strings.Join([]string{
		"*** Begin Patch",
		"*** Add File: " + filepath.Join(dir, "added.txt"),
		"+added",
		"*** Update File: " + filepath.Join(dir, "update.txt"),
		"@@",
		"-old",
		"+new",
		"*** Delete File: " + filepath.Join(dir, "delete.txt"),
		"*** Update File: " + filepath.Join(dir, "move.txt"),
		"*** Move to: " + filepath.Join(dir, "moved.txt"),
		"@@",
		"-move old",
		"+move new",
		"*** End Patch",
	}, "\n")
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	seedRoot(t, root, map[string][]byte{
		"update.txt": []byte("old\n"),
		"delete.txt": []byte("obsolete\n"),
		"move.txt":   []byte("move old\n"),
	}, nil)
	require.NoError(t, root.Close())

	local := runGoApplyPatch(t, dir, patch)
	require.True(t, local.OK)
	require.Equal(t, strings.Join([]string{
		"Success. Updated the following files:",
		"A added.txt",
		"M update.txt",
		"D delete.txt",
		"M moved.txt",
	}, "\n"), local.Output)
	require.Equal(t, []oracleTreeEntry{
		{Path: "added.txt", Type: "file", ContentBase64: base64.StdEncoding.EncodeToString([]byte("added\n"))},
		{Path: "moved.txt", Type: "file", ContentBase64: base64.StdEncoding.EncodeToString([]byte("move new\n"))},
		{Path: "update.txt", Type: "file", ContentBase64: base64.StdEncoding.EncodeToString([]byte("new\n"))},
	}, local.Tree)

	outside := t.TempDir()
	result := runGoApplyPatch(t, dir, "*** Begin Patch\n*** Add File: "+filepath.Join(outside, "nope.txt")+"\n+nope\n*** End Patch")
	require.False(t, result.OK)
	require.Contains(t, result.Error, "path escapes root")

	result = runGoApplyPatch(t, dir, "*** Begin Patch\n*** Update File: update.txt\n*** Move to: "+filepath.Join(outside, "nope.txt")+"\n@@\n-new\n+newer\n*** End Patch")
	require.False(t, result.OK)
	require.Contains(t, result.Error, "path escapes root")
}

func TestApplyPatchParity(t *testing.T) {
	t.Run("requires patchText", func(t *testing.T) {
		requireParity(t, "", nil)
	})

	t.Run("rejects invalid patch format", func(t *testing.T) {
		requireParity(t, "invalid patch", nil)
	})

	t.Run("rejects empty patch", func(t *testing.T) {
		requireParity(t, "*** Begin Patch\n*** End Patch", nil)
	})

	t.Run("applies add update delete in one patch", func(t *testing.T) {
		seed := map[string][]byte{
			"modify.txt": []byte("line1\nline2\n"),
			"delete.txt": []byte("obsolete\n"),
		}
		patch := "*** Begin Patch\n*** Add File: nested/new.txt\n+created\n*** Delete File: delete.txt\n*** Update File: modify.txt\n@@\n-line2\n+changed\n*** End Patch"
		local := runParity(t, patch, seed, nil)
		require.Len(t, local.Files, 3)
		require.Equal(t, "add", local.Files[0].Type)
		require.Equal(t, "nested/new.txt", local.Files[0].RelativePath)
		require.Contains(t, local.Files[0].Patch, "+created")
		require.Equal(t, "delete", local.Files[1].Type)
		require.Equal(t, "update", local.Files[2].Type)
		require.Contains(t, local.Files[2].Patch, "-line2")
		require.Contains(t, local.Files[2].Patch, "+changed")
	})

	t.Run("multiple hunks", func(t *testing.T) {
		seed := map[string][]byte{"multi.txt": []byte("line1\nline2\nline3\nline4\n")}
		patch := "*** Begin Patch\n*** Update File: multi.txt\n@@\n-line2\n+changed2\n@@\n-line4\n+changed4\n*** End Patch"
		requireParity(t, patch, seed)
	})

	t.Run("insert only hunk", func(t *testing.T) {
		seed := map[string][]byte{"insert_only.txt": []byte("alpha\nomega\n")}
		patch := "*** Begin Patch\n*** Update File: insert_only.txt\n@@\n alpha\n+beta\n omega\n*** End Patch"
		requireParity(t, patch, seed)
	})

	t.Run("appends trailing newline on update", func(t *testing.T) {
		seed := map[string][]byte{"no_newline.txt": []byte("no newline at end")}
		patch := "*** Begin Patch\n*** Update File: no_newline.txt\n@@\n-no newline at end\n+first line\n+second line\n*** End Patch"
		requireParity(t, patch, seed)
	})

	t.Run("moves file", func(t *testing.T) {
		seed := map[string][]byte{"old/name.txt": []byte("old content\n")}
		patch := "*** Begin Patch\n*** Update File: old/name.txt\n*** Move to: renamed/dir/name.txt\n@@\n-old content\n+new content\n*** End Patch"
		local := runParity(t, patch, seed, nil)
		require.Len(t, local.Files, 1)
		require.Equal(t, "move", local.Files[0].Type)
		require.Equal(t, "renamed/dir/name.txt", local.Files[0].RelativePath)
		require.Contains(t, local.Files[0].MovePath, filepath.ToSlash("renamed/dir/name.txt"))
		require.Contains(t, local.Files[0].Patch, "-old content")
		require.Contains(t, local.Files[0].Patch, "+new content")
	})

	t.Run("moves file overwriting existing destination", func(t *testing.T) {
		seed := map[string][]byte{
			"old/name.txt":         []byte("from\n"),
			"renamed/dir/name.txt": []byte("existing\n"),
		}
		patch := "*** Begin Patch\n*** Update File: old/name.txt\n*** Move to: renamed/dir/name.txt\n@@\n-from\n+new\n*** End Patch"
		requireParity(t, patch, seed)
	})

	t.Run("adds file overwriting existing file", func(t *testing.T) {
		seed := map[string][]byte{"duplicate.txt": []byte("old content\n")}
		patch := "*** Begin Patch\n*** Add File: duplicate.txt\n+new content\n*** End Patch"
		requireParity(t, patch, seed)
	})

	t.Run("rejects update when target file is missing", func(t *testing.T) {
		patch := "*** Begin Patch\n*** Update File: missing.txt\n@@\n-nope\n+better\n*** End Patch"
		requireParity(t, patch, nil)
	})

	t.Run("rejects delete when file is missing", func(t *testing.T) {
		patch := "*** Begin Patch\n*** Delete File: missing.txt\n*** End Patch"
		requireParity(t, patch, nil)
	})

	t.Run("rejects delete when target is a directory", func(t *testing.T) {
		patch := "*** Begin Patch\n*** Delete File: dir\n*** End Patch"
		requireParityWithSetup(t, patch, nil, func(t *testing.T, root *os.Root) {
			t.Helper()
			require.NoError(t, root.Mkdir("dir", 0o755))
		})
	})

	t.Run("rejects invalid hunk header", func(t *testing.T) {
		patch := "*** Begin Patch\n*** Frobnicate File: foo\n*** End Patch"
		requireParity(t, patch, nil)
	})

	t.Run("rejects update with missing context", func(t *testing.T) {
		seed := map[string][]byte{"modify.txt": []byte("line1\nline2\n")}
		patch := "*** Begin Patch\n*** Update File: modify.txt\n@@\n-missing\n+changed\n*** End Patch"
		requireParity(t, patch, seed)
	})

	t.Run("verification failure leaves no side effects", func(t *testing.T) {
		patch := "*** Begin Patch\n*** Add File: created.txt\n+hello\n*** Update File: missing.txt\n@@\n-old\n+new\n*** End Patch"
		requireParity(t, patch, nil)
	})

	t.Run("supports end of file anchor", func(t *testing.T) {
		seed := map[string][]byte{"tail.txt": []byte("alpha\nlast\n")}
		patch := "*** Begin Patch\n*** Update File: tail.txt\n@@\n-last\n+end\n*** End of File\n*** End Patch"
		requireParity(t, patch, seed)
	})

	t.Run("heredoc wrapped patch", func(t *testing.T) {
		patch := "cat <<'EOF'\n*** Begin Patch\n*** Add File: heredoc_test.txt\n+heredoc content\n*** End Patch\nEOF"
		requireParity(t, patch, nil)
	})

	t.Run("rejects missing second chunk context", func(t *testing.T) {
		seed := map[string][]byte{"two_chunks.txt": []byte("a\nb\nc\nd\n")}
		patch := "*** Begin Patch\n*** Update File: two_chunks.txt\n@@\n-b\n+B\n\n-d\n+D\n*** End Patch"
		requireParity(t, patch, seed)
	})

	t.Run("disambiguates change context with header", func(t *testing.T) {
		seed := map[string][]byte{"multi_ctx.txt": []byte("fn a\nx=10\ny=2\nfn b\nx=10\ny=20\n")}
		patch := "*** Begin Patch\n*** Update File: multi_ctx.txt\n@@ fn b\n-x=10\n+x=11\n*** End Patch"
		requireParity(t, patch, seed)
	})

	t.Run("EOF anchor matches from end of file first", func(t *testing.T) {
		seed := map[string][]byte{"eof_anchor.txt": []byte("start\nmarker\nmiddle\nmarker\nend\n")}
		patch := "*** Begin Patch\n*** Update File: eof_anchor.txt\n@@\n-marker\n-end\n+marker-changed\n+end\n*** End of File\n*** End Patch"
		requireParity(t, patch, seed)
	})

	t.Run("heredoc wrapped patch without cat", func(t *testing.T) {
		patch := "<<EOF\n*** Begin Patch\n*** Add File: heredoc_no_cat.txt\n+no cat prefix\n*** End Patch\nEOF"
		requireParity(t, patch, nil)
	})

	t.Run("trailing whitespace differences", func(t *testing.T) {
		seed := map[string][]byte{"trailing_ws.txt": []byte("line1  \nline2\nline3   \n")}
		patch := "*** Begin Patch\n*** Update File: trailing_ws.txt\n@@\n-line2\n+changed\n*** End Patch"
		requireParity(t, patch, seed)
	})

	t.Run("leading whitespace differences", func(t *testing.T) {
		seed := map[string][]byte{"leading_ws.txt": []byte("  line1\nline2\n  line3\n")}
		patch := "*** Begin Patch\n*** Update File: leading_ws.txt\n@@\n-line2\n+changed\n*** End Patch"
		requireParity(t, patch, seed)
	})

	t.Run("unicode punctuation differences", func(t *testing.T) {
		seed := map[string][]byte{"unicode.txt": []byte("He said “hello”\nsome—dash\nend\n")}
		patch := "*** Begin Patch\n*** Update File: unicode.txt\n@@\n-He said \"hello\"\n+He said \"hi\"\n*** End Patch"
		requireParity(t, patch, seed)
	})

	t.Run("BOM file update parity", func(t *testing.T) {
		seed := map[string][]byte{"example.cs": []byte("\ufeffusing System;\n\nclass Test {}\n")}
		patch := "*** Begin Patch\n*** Update File: example.cs\n@@\n class Test {}\n+class Next {}\n*** End Patch"
		local := runParity(t, patch, seed, nil)
		require.Len(t, local.Files, 1)
		require.NotContains(t, local.Files[0].Patch, "\ufeff")
		require.NotContains(t, local.Files[0].Patch, "-using System;")
		require.NotContains(t, local.Files[0].Patch, "+using System;")
	})
}
