package harness

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"

	"go.starlark.net/starlark"

	"github.com/Rocketable/platform/internal/rocketclaw/codemode"
)

type sandboxedFileSystem struct {
	mu        sync.Mutex
	root      *os.Root
	spillRel  string
	hostPaths bool
}

type patchHunk struct {
	typ      string
	path     string
	movePath string
	contents string
	chunks   []updateFileChunk
}

type updateFileChunk struct {
	oldLines      []string
	newLines      []string
	changeContext string
	endOfFile     bool
}

type fileChange struct {
	typ, path, movePath string
	newContent          string
	bom                 bool
}

type applyPatchPreview struct {
	changes []fileChange
	output  string
}

type openCodePatchError struct {
	message string
}

func (e openCodePatchError) Error() string {
	return e.message
}

func (sfs *sandboxedFileSystem) Read(ctx context.Context, filename string, offset int) (starlark.Value, error) {
	return readTyped(ctx, &sfs.mu, rootedReadAttempt{sfs: sfs, path: filename}, filename, offset)
}

func (sfs *sandboxedFileSystem) ApplyPatch(patchText string) (starlark.Value, error) {
	sfs.mu.Lock()
	defer sfs.mu.Unlock()

	preview, errText := previewApplyPatchLocked(sfs, patchText)
	if errText != "" {
		return nil, errors.New(errText)
	}

	for _, change := range preview.changes {
		switch change.typ {
		case "add", "update":
			if err := writeTextFile(sfs, change.path, change.newContent, change.bom); err != nil {
				return nil, errors.New("apply_patch verification failed: " + err.Error())
			}
		case "move":
			if err := writeTextFile(sfs, change.movePath, change.newContent, change.bom); err != nil {
				return nil, errors.New("apply_patch verification failed: " + err.Error())
			}

			if err := sfs.root.Remove(change.path); err != nil {
				return nil, errors.New("apply_patch verification failed: " + err.Error())
			}
		case "delete":
			if err := sfs.root.Remove(change.path); err != nil {
				return nil, errors.New("apply_patch verification failed: " + err.Error())
			}
		}
	}

	return codemode.NewText("text", preview.output, nil, nil), nil
}

func previewApplyPatchLocked(sfs *sandboxedFileSystem, patchText string) (preview applyPatchPreview, errText string) {
	if patchText == "" {
		return applyPatchPreview{}, "patchText is required"
	}

	hunks, err := parsePatch(patchText)
	if err != nil {
		return applyPatchPreview{}, "apply_patch verification failed: Error: " + err.Error()
	}

	if len(hunks) == 0 {
		normalized := strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(patchText, "\r\n", "\n"), "\r", "\n"))
		if normalized == "*** Begin Patch\n*** End Patch" {
			return applyPatchPreview{}, "patch rejected: empty patch"
		}

		return applyPatchPreview{}, "apply_patch verification failed: no hunks found"
	}

	changes := make([]fileChange, 0, len(hunks))
	for _, hunk := range hunks {
		cleanPath, err := cleanPatchPath(sfs.root, hunk.path)
		if err != nil {
			return applyPatchPreview{}, "apply_patch verification failed: " + err.Error()
		}

		if denied := sfs.deniedPath(cleanPath); denied != "" {
			return applyPatchPreview{}, "apply_patch verification failed: " + denied
		}

		switch hunk.typ {
		case "add":
			nextContent := hunk.contents
			if nextContent == "" || !strings.HasSuffix(nextContent, "\n") {
				nextContent += "\n"
			}

			text, bom := strings.CutPrefix(nextContent, "\ufeff")
			changes = append(changes, fileChange{typ: "add", path: cleanPath, newContent: text, bom: bom})
		case "update":
			info, err := sfs.root.Stat(cleanPath)
			if err != nil || info.IsDir() {
				return applyPatchPreview{}, "apply_patch verification failed: Failed to read file to update: " + filepath.Join(sfs.root.Name(), cleanPath)
			}

			source, sourceBOM, err := readTextFile(sfs, cleanPath)
			if err != nil {
				return applyPatchPreview{}, "apply_patch verification failed: " + err.Error()
			}

			text, bom, err := deriveNewContentsFromChunks(source, filepath.Join(sfs.root.Name(), cleanPath), hunk.chunks)
			if err != nil {
				return applyPatchPreview{}, "apply_patch verification failed: Error: " + err.Error()
			}

			if sourceBOM {
				bom = true
			}

			movePath := ""
			if hunk.movePath != "" {
				movePath, err = cleanPatchPath(sfs.root, hunk.movePath)
				if err != nil {
					return applyPatchPreview{}, "apply_patch verification failed: " + err.Error()
				}

				if denied := sfs.deniedPath(movePath); denied != "" {
					return applyPatchPreview{}, "apply_patch verification failed: " + denied
				}
			}

			changeType := "update"
			if movePath != "" {
				changeType = "move"
			}

			changes = append(changes, fileChange{typ: changeType, path: cleanPath, movePath: movePath, newContent: text, bom: bom})
		case "delete":
			if err := verifyDeleteTarget(sfs, cleanPath); err != nil {
				return applyPatchPreview{}, "apply_patch verification failed: " + err.Error()
			}

			_, _, err := readTextFile(sfs, cleanPath)
			if err != nil {
				return applyPatchPreview{}, "apply_patch verification failed: " + err.Error()
			}

			changes = append(changes, fileChange{typ: "delete", path: cleanPath})
		}
	}

	lines := []string{"Success. Updated the following files:"}

	for _, change := range changes {
		target := change.path

		prefix := "M "
		if change.typ == "add" {
			prefix = "A "
		}

		if change.typ == "delete" {
			prefix = "D "
		}

		if change.movePath != "" {
			target = change.movePath
		}

		if sfs.hostPaths {
			target = filepath.Join(sfs.root.Name(), target)
		}

		lines = append(lines, prefix+filepath.ToSlash(target))
	}

	return applyPatchPreview{changes: changes, output: strings.Join(lines, "\n")}, ""
}

func verifyDeleteTarget(sfs *sandboxedFileSystem, name string) error {
	info, err := sfs.root.Lstat(name)
	if err != nil {
		return fmt.Errorf("ENOENT: no such file or directory, open '%s'", filepath.Join(sfs.root.Name(), name))
	}

	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("symlink access denied: %s", name)
	}

	if info.IsDir() {
		return errors.New("EISDIR: illegal operation on a directory, read")
	}

	return nil
}

func cleanPatchPath(root *os.Root, name string) (string, error) {
	if name == "" {
		return "", errors.New("empty patch path")
	}

	clean, err := normalizeRootName(root, filepath.FromSlash(name))
	if err != nil {
		return "", err
	}

	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("invalid patch path: %s", name)
	}

	return clean, nil
}

func parsePatch(patchText string) ([]patchHunk, error) {
	cleaned := stripHeredoc(strings.TrimSpace(patchText))
	lines := strings.Split(cleaned, "\n")
	begin := -1
	end := -1

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "*** Begin Patch" && begin == -1 {
			begin = i
		}

		if trimmed == "*** End Patch" {
			end = i
		}
	}

	if begin == -1 || end == -1 || begin >= end {
		return nil, openCodePatchError{message: "Invalid patch format: missing Begin/End markers"}
	}

	var hunks []patchHunk

	for i := begin + 1; i < end; {
		line := lines[i]
		switch {
		case strings.HasPrefix(line, "*** Add File:"):
			filePath := strings.TrimSpace(strings.TrimPrefix(line, "*** Add File:"))
			content, next := parseAddFileContent(lines, i+1, end)
			hunks = append(hunks, patchHunk{typ: "add", path: filePath, movePath: "", contents: content, chunks: nil})
			i = next
		case strings.HasPrefix(line, "*** Delete File:"):
			filePath := strings.TrimSpace(strings.TrimPrefix(line, "*** Delete File:"))
			hunks = append(hunks, patchHunk{typ: "delete", path: filePath, movePath: "", contents: "", chunks: nil})
			i++
		case strings.HasPrefix(line, "*** Update File:"):
			filePath := strings.TrimSpace(strings.TrimPrefix(line, "*** Update File:"))
			movePath := ""

			next := i + 1
			if next < end && strings.HasPrefix(lines[next], "*** Move to:") {
				movePath = strings.TrimSpace(strings.TrimPrefix(lines[next], "*** Move to:"))
				next++
			}

			chunks, after := parseUpdateFileChunks(lines, next, end)
			hunks = append(hunks, patchHunk{typ: "update", path: filePath, movePath: movePath, contents: "", chunks: chunks})
			i = after
		default:
			i++
		}
	}

	return hunks, nil
}

func parseAddFileContent(lines []string, start, end int) (content string, next int) {
	var b strings.Builder

	i := start
	for i < end && !strings.HasPrefix(lines[i], "***") {
		if after, ok := strings.CutPrefix(lines[i], "+"); ok {
			b.WriteString(after)
			b.WriteByte('\n')
		}

		i++
	}

	content = strings.TrimSuffix(b.String(), "\n")

	return content, i
}

func parseUpdateFileChunks(lines []string, start, end int) (chunks []updateFileChunk, next int) {
	i := start
	for i < end && !strings.HasPrefix(lines[i], "***") {
		if !strings.HasPrefix(lines[i], "@@") {
			i++
			continue
		}

		changeContext := strings.TrimSpace(strings.TrimPrefix(lines[i], "@@"))
		i++
		chunk := updateFileChunk{oldLines: nil, newLines: nil, changeContext: changeContext, endOfFile: false}

		for i < end && !strings.HasPrefix(lines[i], "@@") && !strings.HasPrefix(lines[i], "***") {
			line := lines[i]
			if line == "*** End of File" {
				chunk.endOfFile = true
				i++

				break
			}

			switch {
			case strings.HasPrefix(line, " "):
				text := strings.TrimPrefix(line, " ")
				chunk.oldLines = append(chunk.oldLines, text)
				chunk.newLines = append(chunk.newLines, text)
			case strings.HasPrefix(line, "-"):
				chunk.oldLines = append(chunk.oldLines, strings.TrimPrefix(line, "-"))
			case strings.HasPrefix(line, "+"):
				chunk.newLines = append(chunk.newLines, strings.TrimPrefix(line, "+"))
			}

			i++
		}

		chunks = append(chunks, chunk)
	}

	return chunks, i
}

func stripHeredoc(trimmedPatch string) string {
	first, rest, _ := strings.Cut(trimmedPatch, "\n")

	marker, ok := strings.CutPrefix(strings.TrimPrefix(strings.TrimSpace(first), "cat "), "<<")
	if !ok {
		return trimmedPatch
	}

	marker = strings.Trim(strings.TrimSpace(marker), `"'`)

	body, last, _ := strings.CutLast(rest, "\n")
	if marker == "" || strings.TrimSpace(last) != marker {
		return trimmedPatch
	}

	if strings.Contains(body, "\n") {
		return strings.Clone(body)
	}

	return body
}

func readTextFile(sfs *sandboxedFileSystem, name string) (text string, bom bool, err error) {
	name, err = normalizeRootName(sfs.root, name)
	if err != nil {
		return "", false, err
	}

	if err := rejectSymlink(sfs, name); err != nil {
		return "", false, err
	}

	data, err := sfs.root.ReadFile(name)
	if err != nil {
		return "", false, fmt.Errorf("read text file %q: %w", name, err)
	}

	text, bom = strings.CutPrefix(string(data), "\ufeff")

	return text, bom, nil
}

func writeTextFile(sfs *sandboxedFileSystem, name, text string, bom bool) error {
	if err := rejectSymlink(sfs, name); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}

	dir := filepath.Dir(name)
	if dir != "." {
		if err := sfs.root.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("mkdir parents for %q: %w", name, err)
		}
	}

	text = strings.TrimPrefix(text, "\ufeff")
	if bom {
		text = "\ufeff" + text
	}

	if err := sfs.root.WriteFile(name, []byte(text), 0o644); err != nil {
		return fmt.Errorf("write text file %q: %w", name, err)
	}

	return nil
}

func rejectSymlink(sfs *sandboxedFileSystem, name string) error {
	for current := filepath.Clean(name); current != "."; current = filepath.Dir(current) {
		info, err := sfs.root.Lstat(current)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}

			return fmt.Errorf("lstat %q: %w", current, err)
		}

		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink access denied: %s", current)
		}
	}

	return nil
}

func deriveNewContentsFromChunks(originalText, filePath string, chunks []updateFileChunk) (text string, bom bool, err error) {
	lines := strings.Split(originalText, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}

	replacements, err := computeReplacements(lines, filePath, chunks)
	if err != nil {
		return "", false, err
	}

	for _, repl := range slices.Backward(replacements) {
		lines = append(lines[:repl.start], append(repl.newLines, lines[repl.start+repl.oldLen:]...)...)
	}

	if len(lines) == 0 || lines[len(lines)-1] != "" {
		lines = append(lines, "")
	}

	text, bom = strings.CutPrefix(strings.Join(lines, "\n"), "\ufeff")

	return text, bom, nil
}

type replacement struct {
	start    int
	oldLen   int
	newLines []string
}

func computeReplacements(originalLines []string, filePath string, chunks []updateFileChunk) ([]replacement, error) {
	replacements := make([]replacement, 0, len(chunks))
	lineIndex := 0

	for _, chunk := range chunks {
		if chunk.changeContext != "" {
			contextIdx := seekSequence(originalLines, []string{chunk.changeContext}, lineIndex, false)
			if contextIdx == -1 {
				return nil, openCodePatchError{message: fmt.Sprintf("Failed to find context '%s' in %s", chunk.changeContext, filePath)}
			}

			lineIndex = contextIdx + 1
		}

		if len(chunk.oldLines) == 0 {
			insertAt := len(originalLines)
			if insertAt > 0 && originalLines[insertAt-1] == "" {
				insertAt--
			}

			replacements = append(replacements, replacement{start: insertAt, oldLen: 0, newLines: chunk.newLines})

			continue
		}

		pattern := chunk.oldLines
		newSlice := append([]string(nil), chunk.newLines...)

		found := seekSequence(originalLines, pattern, lineIndex, chunk.endOfFile)
		if found == -1 && len(pattern) > 0 && pattern[len(pattern)-1] == "" {
			pattern = pattern[:len(pattern)-1]
			if len(newSlice) > 0 && newSlice[len(newSlice)-1] == "" {
				newSlice = newSlice[:len(newSlice)-1]
			}

			found = seekSequence(originalLines, pattern, lineIndex, chunk.endOfFile)
		}

		if found == -1 {
			return nil, openCodePatchError{message: fmt.Sprintf("Failed to find expected lines in %s:\n%s", filePath, strings.Join(chunk.oldLines, "\n"))}
		}

		replacements = append(replacements, replacement{start: found, oldLen: len(pattern), newLines: newSlice})
		lineIndex = found + len(pattern)
	}

	sort.Slice(replacements, func(i, j int) bool { return replacements[i].start < replacements[j].start })

	return replacements, nil
}

func seekSequence(lines, pattern []string, startIndex int, eof bool) int {
	if len(pattern) == 0 || len(pattern) > len(lines)-startIndex {
		return -1
	}

	comparators := []func(string, string) bool{
		func(a, b string) bool { return a == b },
		func(a, b string) bool { return strings.TrimRight(a, " \t") == strings.TrimRight(b, " \t") },
		func(a, b string) bool { return strings.TrimSpace(a) == strings.TrimSpace(b) },
	}
	for _, compare := range comparators {
		if match := tryMatch(lines, pattern, startIndex, compare, eof); match != -1 {
			return match
		}
	}

	replacer := strings.NewReplacer(
		"\u2018", "'", "\u2019", "'", "\u201A", "'", "\u201B", "'",
		"\u201C", `"`, "\u201D", `"`, "\u201E", `"`, "\u201F", `"`,
		"\u2010", "-", "\u2011", "-", "\u2012", "-", "\u2013", "-", "\u2014", "-", "\u2015", "-",
		"\u2026", "...",
		"\u00A0", " ",
	)

	return tryMatch(lines, pattern, startIndex, func(a, b string) bool {
		return replacer.Replace(strings.TrimSpace(a)) == replacer.Replace(strings.TrimSpace(b))
	}, eof)
}

func tryMatch(lines, pattern []string, startIndex int, compare func(string, string) bool, eof bool) int {
	if eof {
		fromEnd := len(lines) - len(pattern)
		if fromEnd >= startIndex && slices.EqualFunc(lines[fromEnd:], pattern, compare) {
			return fromEnd
		}
	}

	for i := startIndex; i <= len(lines)-len(pattern); i++ {
		if slices.EqualFunc(lines[i:i+len(pattern)], pattern, compare) {
			return i
		}
	}

	return -1
}

type globMatch struct {
	Path  string `json:"path"`
	MTime int64  `json:"mtime"`
}

func (sfs *sandboxedFileSystem) Glob(ctx context.Context, pattern, path string) (starlark.Value, error) {
	matches, partial, errText := sfs.globMatches(ctx, pattern, path)
	if errText != "" {
		return nil, errors.New(errText)
	}

	return newGlobResult(matches, partial, globToolParams{Pattern: pattern, Path: path}), nil
}

type grepMatch struct {
	Path  string `json:"path"`
	Line  int    `json:"line"`
	Text  string `json:"text"`
	MTime int64  `json:"mtime"`
	match *codemode.Match
}

type ripgrepJSON struct {
	Type string `json:"type"`
	Data struct {
		Path struct {
			Text string `json:"text"`
		} `json:"path"`
		Lines struct {
			Text string `json:"text"`
		} `json:"lines"`
		LineNumber int `json:"line_number"`
	} `json:"data"`
}

type grepTarget struct {
	root     *os.Root
	hostRoot string
	files    []string
}

func (sfs *sandboxedFileSystem) Grep(ctx context.Context, pattern, searchPath, include string) (starlark.Value, error) {
	matches, partial, errText := sfs.grepMatches(ctx, pattern, searchPath, include)
	if errText != "" {
		return nil, errors.New(errText)
	}

	return newGrepResult(matches, partial, grepToolParams{Pattern: pattern, Path: searchPath, Include: include}), nil
}

func (sfs *sandboxedFileSystem) grepMatches(ctx context.Context, pattern, searchPath, include string) (matches []grepMatch, partial bool, errText string) {
	sfs.mu.Lock()
	defer sfs.mu.Unlock()

	if pattern == "" {
		return nil, false, "pattern is required"
	}

	target, err := sfs.resolveGrepTarget(searchPath)
	if err != nil {
		return nil, false, err.Error()
	}

	if target.root != sfs.root {
		defer func() {
			_ = target.root.Close()
		}()
	}

	files := target.files
	if len(files) == 1 && files[0] == "." {
		if files, errText = sfs.allowedRipgrepFiles(ctx, target.hostRoot, include); len(files) == 0 {
			return nil, false, errText
		}
	}

	stdout, errSearch := ripgrep(ctx, target.hostRoot, append([]string{"--json", "--no-messages", "--", pattern}, files...)...)

	matches, err = parseGrepMatches(stdout, target.root, target.hostRoot)
	switch {
	case err != nil:
		return nil, false, err.Error()
	case len(matches) == 0:
		return nil, false, cmp.Or(errSearch, errText)
	}

	return matches, errSearch != "" || errText != "", ""
}

func (sfs *sandboxedFileSystem) previewApplyPatch(patchText string) (preview applyPatchPreview, errText string) {
	sfs.mu.Lock()
	defer sfs.mu.Unlock()

	return previewApplyPatchLocked(sfs, patchText)
}

func (sfs *sandboxedFileSystem) globMatches(ctx context.Context, pattern, path string) (results []globMatch, partial bool, errText string) {
	sfs.mu.Lock()
	defer sfs.mu.Unlock()

	searchPath := path
	searchRoot := sfs.root

	hostRoot := sfs.root.Name()
	if denied := sfs.deniedPath(searchPath); denied != "" {
		return nil, false, denied
	}

	if searchPath != "" {
		var err error

		searchPath, err = normalizeRootName(sfs.root, searchPath)
		if err != nil {
			return nil, false, err.Error()
		}

		if err := rejectSymlink(sfs, searchPath); err != nil {
			return nil, false, err.Error()
		}

		root, err := sfs.root.OpenRoot(searchPath)
		if err != nil {
			return nil, false, err.Error()
		}

		defer func() { _ = root.Close() }()

		searchRoot = root
		hostRoot = filepath.Join(hostRoot, searchPath)
	}

	stdout, errText := ripgrep(ctx, hostRoot, "--files", "-g", pattern)

	outputText := strings.TrimSpace(string(stdout))
	if outputText == "" {
		return nil, false, errText
	}

	matches := strings.Split(outputText, "\n")

	results = make([]globMatch, 0, len(matches))
	for _, match := range matches {
		full := filepath.Join(hostRoot, match)
		if sfs.deniedPath(full) != "" {
			continue
		}

		info, err := searchRoot.Lstat(match)
		if err != nil {
			continue
		}

		if info.Mode()&os.ModeSymlink != 0 {
			continue
		}

		results = append(results, globMatch{Path: full, MTime: info.ModTime().UnixMilli()})
	}

	return results, errText != "", ""
}

func (sfs *sandboxedFileSystem) deniedPath(name string) string {
	if isDeniedEnvPath(name) {
		return deniedEnvAccessMessage(name)
	}

	if isExecuteSpillPath(sfs.root, sfs.spillRel, name) {
		return deniedSpillAccess
	}

	return ""
}

func normalizeRootName(root *os.Root, name string) (string, error) {
	clean := filepath.Clean(name)
	if !filepath.IsAbs(clean) {
		return clean, nil
	}

	rootAbs, err := filepath.Abs(root.Name())
	if err != nil {
		return "", fmt.Errorf("resolve root %q: %w", root.Name(), err)
	}

	rel, err := filepath.Rel(rootAbs, clean)
	if err != nil {
		return "", fmt.Errorf("resolve path %q relative to root: %w", name, err)
	}

	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes root: %s", name)
	}

	return rel, nil
}

func (sfs *sandboxedFileSystem) readPermissionSubject(name string) string {
	if normalized, err := normalizeRootName(sfs.root, name); err == nil {
		return rootedPathSubject(normalized)
	}

	return rootedPathSubject(name)
}

func (sfs *sandboxedFileSystem) resolveGrepTarget(searchPath string) (grepTarget, error) {
	target := grepTarget{root: sfs.root, hostRoot: sfs.root.Name(), files: []string{"."}}
	if searchPath == "" {
		return target, nil
	}

	var err error

	searchPath, err = normalizeRootName(sfs.root, searchPath)
	if err != nil {
		return grepTarget{}, err
	}

	if denied := sfs.deniedPath(searchPath); denied != "" {
		return grepTarget{}, errors.New(denied)
	}

	if err := rejectSymlink(sfs, searchPath); err != nil {
		return grepTarget{}, err
	}

	info, err := sfs.root.Stat(searchPath)
	if err != nil {
		return grepTarget{}, fmt.Errorf("stat grep path %q: %w", searchPath, err)
	}

	if info.IsDir() {
		root, err := sfs.root.OpenRoot(searchPath)
		if err != nil {
			return grepTarget{}, fmt.Errorf("open grep root %q: %w", searchPath, err)
		}

		target.root = root
		target.hostRoot = filepath.Join(target.hostRoot, searchPath)

		return target, nil
	}

	dir := filepath.Dir(searchPath)
	base := filepath.Base(searchPath)

	target.files = []string{base}
	if dir == "." {
		return target, nil
	}

	root, err := sfs.root.OpenRoot(dir)
	if err != nil {
		return grepTarget{}, fmt.Errorf("open grep parent %q: %w", dir, err)
	}

	target.root = root
	target.hostRoot = filepath.Join(target.hostRoot, dir)

	return target, nil
}

func (sfs *sandboxedFileSystem) allowedRipgrepFiles(ctx context.Context, hostRoot, include string) (files []string, errText string) {
	args := []string{"--files"}
	if include != "" {
		args = append(args, "--glob="+include)
	}

	stdout, errText := ripgrep(ctx, hostRoot, args...)

	outputText := strings.TrimSpace(string(stdout))
	if outputText == "" {
		return nil, errText
	}

	return sfs.filterRipgrepFiles(hostRoot, strings.Split(outputText, "\n")), errText
}

func (sfs *sandboxedFileSystem) filterRipgrepFiles(hostRoot string, listed []string) []string {
	return slices.DeleteFunc(listed, func(file string) bool {
		return sfs.deniedPath(filepath.Join(hostRoot, file)) != ""
	})
}

func ripgrep(ctx context.Context, hostRoot string, args ...string) (stdout []byte, errText string) {
	cmd := exec.CommandContext(ctx, "rg", append([]string{"--no-config", "--hidden", "--no-follow", "--path-separator=/", "--glob=!.git/*"}, args...)...)
	cmd.Dir = hostRoot

	var stderr bytes.Buffer

	cmd.Stderr = &stderr
	stdout, err := cmd.Output()
	message := strings.TrimSpace(stderr.String())
	errExit, exited := errors.AsType[*exec.ExitError](err)

	switch {
	case err == nil || exited && errExit.ExitCode() == 1:
		return stdout, ""
	case exited && errExit.ExitCode() == 2:
		return stdout, cmp.Or(message, "some paths could not be searched and nothing was found")
	}

	return nil, cmp.Or(message, err.Error())
}

func parseGrepMatches(stdout []byte, searchRoot *os.Root, hostRoot string) ([]grepMatch, error) {
	var matches []grepMatch

	for line := range bytes.SplitSeq(stdout, []byte("\n")) {
		line = bytes.TrimSuffix(line, []byte("\r"))
		if len(line) == 0 {
			continue
		}

		var item ripgrepJSON
		if err := json.Unmarshal(line, &item); err != nil {
			return nil, errors.New("invalid ripgrep output")
		}

		if item.Type != "match" {
			continue
		}

		if isDeniedEnvPath(item.Data.Path.Text) {
			continue
		}

		info, err := searchRoot.Stat(item.Data.Path.Text)
		if err != nil || info.IsDir() {
			continue
		}

		text := strings.TrimRight(item.Data.Lines.Text, "\r\n")
		matches = append(matches, grepMatch{Path: filepath.Join(hostRoot, item.Data.Path.Text), Line: item.Data.LineNumber, Text: text, MTime: info.ModTime().UnixMilli()})
	}

	return matches, nil
}
