package rocketcode

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"
)

const (
	defaultReadLimit = 2000
	maxLineLength    = 2000
	maxBytes         = 50 * 1024
	maxBytesLabel    = "50 KB"
	maxLineSuffix    = "... (line truncated to 2000 chars)"
)

type sandboxedFileSystem struct {
	mu   sync.Mutex
	root *os.Root
}

type readResult struct {
	raw   []string
	count int
	cut   bool
	more  bool
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
	typ        string
	path       string
	movePath   string
	oldContent string
	newContent string
	bom        bool
	diff       string
	additions  int
	deletions  int
}

type applyPatchFileMeta struct {
	FilePath     string
	RelativePath string
	Type         string
	Patch        string
	Additions    int
	Deletions    int
	MovePath     string
}

type applyPatchPreview struct {
	changes []fileChange
	files   []applyPatchFileMeta
	diff    string
	output  string
}

func (sfs *sandboxedFileSystem) Read(filename string, offset int) string {
	return sfs.ReadResult(filename, offset).Output
}

func (sfs *sandboxedFileSystem) ReadResult(filename string, offset int) ToolResult {
	sfs.mu.Lock()
	defer sfs.mu.Unlock()

	if offset < 1 {
		return textToolResult("offset must be greater than or equal to 1")
	}

	if isDeniedEnvPath(filename) {
		return textToolResult(deniedEnvAccessMessage(filename))
	}

	name, err := normalizeRootName(sfs.root, filename)
	if err != nil {
		return textToolResult(err.Error())
	}

	if err := sfs.rejectSymlink(name); err != nil && !errors.Is(err, os.ErrNotExist) {
		return textToolResult(err.Error())
	}

	file, err := sfs.root.Open(name)
	if err != nil {
		return textToolResult("File not found: " + filename)
	}

	content, err := io.ReadAll(file)
	errClose := file.Close()

	if err != nil {
		return textToolResult("failed to read file: " + filename)
	}

	if errClose != nil {
		return textToolResult("failed to read file: " + filename)
	}

	mimeType := sniffAttachmentMIME(content, mimeFromFilename(filename))
	if isSupportedAttachmentMIME(mimeType) {
		attachment, err := attachmentFromBytes(filepath.Base(filename), mimeType, content)
		if err != nil {
			return textToolResult(err.Error())
		}

		message := "Image read successfully"
		if mimeType == "application/pdf" {
			message = "PDF read successfully"
		}

		return ToolResult{Output: message, Attachments: []Attachment{attachment}}
	}

	result, errScan := readLines(content, offset, defaultReadLimit)
	if errScan != nil {
		return textToolResult("failed to read file: " + filename)
	}

	if result.count < offset && (result.count != 0 || offset != 1) {
		return textToolResult(fmt.Sprintf("Offset %d is out of range for this file (%d lines)", offset, result.count))
	}

	var output strings.Builder
	output.WriteString("<path>")
	output.WriteString(filename)
	output.WriteString("</path>\n<type>file</type>\n<content>\n")

	for i, line := range result.raw {
		if i > 0 {
			output.WriteByte('\n')
		}

		fmt.Fprintf(&output, "%d: %s", offset+i, line)
	}

	last := offset + len(result.raw) - 1
	next := last + 1

	switch {
	case result.cut:
		fmt.Fprintf(&output, "\n\n(Output capped at %s. Showing lines %d-%d. Use offset=%d to continue.)", maxBytesLabel, offset, last, next)
	case result.more:
		fmt.Fprintf(&output, "\n\n(Showing lines %d-%d of %d. Use offset=%d to continue.)", offset, last, result.count, next)
	default:
		fmt.Fprintf(&output, "\n\n(End of file - total %d lines)", result.count)
	}

	output.WriteString("\n</content>")

	return textToolResult(output.String())
}

func readLines(content []byte, offset, limit int) (readResult, error) {
	scanner := bufio.NewScanner(bytes.NewReader(content))
	scanner.Buffer(make([]byte, 0, 64*1024), max(len(content), 64*1024))

	start := offset - 1
	raw := []string{}
	bytesRead := 0
	count := 0
	cut := false
	more := false

	for scanner.Scan() {
		count++
		if count <= start {
			continue
		}

		if len(raw) >= limit {
			more = true
			continue
		}

		line := scanner.Text()
		if len(line) > maxLineLength {
			line = line[:maxLineLength] + maxLineSuffix
		}

		size := len([]byte(line))
		if len(raw) > 0 {
			size++
		}

		if bytesRead+size > maxBytes {
			cut = true
			more = true

			break
		}

		raw = append(raw, line)
		bytesRead += size
	}

	if err := scanner.Err(); err != nil {
		return readResult{}, fmt.Errorf("scan file lines: %w", err)
	}

	return readResult{raw: raw, count: count, cut: cut, more: more}, nil
}

func (sfs *sandboxedFileSystem) ApplyPatch(patchText string) string {
	sfs.mu.Lock()
	defer sfs.mu.Unlock()

	preview, errText := sfs.previewApplyPatchLocked(patchText)
	if errText != "" {
		return errText
	}

	for _, change := range preview.changes {
		switch change.typ {
		case "add", "update":
			if err := sfs.writeTextFile(change.path, change.newContent, change.bom); err != nil {
				return "apply_patch verification failed: " + err.Error()
			}
		case "move":
			if err := sfs.writeTextFile(change.movePath, change.newContent, change.bom); err != nil {
				return "apply_patch verification failed: " + err.Error()
			}

			if err := sfs.root.Remove(change.path); err != nil {
				return "apply_patch verification failed: " + err.Error()
			}
		case "delete":
			if err := sfs.root.Remove(change.path); err != nil {
				return "apply_patch verification failed: " + err.Error()
			}
		}
	}

	return preview.output
}

func emptyApplyPatchPreview() applyPatchPreview {
	return applyPatchPreview{changes: nil, files: nil, diff: "", output: ""}
}

//nolint:funcorder // Patch helpers are grouped with the ApplyPatch implementation they support.
func (sfs *sandboxedFileSystem) previewApplyPatch(patchText string) (preview applyPatchPreview, errText string) {
	sfs.mu.Lock()
	defer sfs.mu.Unlock()

	return sfs.previewApplyPatchLocked(patchText)
}

//nolint:funcorder // Locked implementation stays next to the public preview wrapper to avoid split patch logic.
func (sfs *sandboxedFileSystem) previewApplyPatchLocked(patchText string) (preview applyPatchPreview, errText string) {
	if patchText == "" {
		return emptyApplyPatchPreview(), "patchText is required"
	}

	hunks, err := parsePatch(patchText)
	if err != nil {
		return emptyApplyPatchPreview(), "apply_patch verification failed: Error: " + err.Error()
	}

	if len(hunks) == 0 {
		normalized := strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(patchText, "\r\n", "\n"), "\r", "\n"))
		if normalized == "*** Begin Patch\n*** End Patch" {
			return emptyApplyPatchPreview(), "patch rejected: empty patch"
		}

		return emptyApplyPatchPreview(), "apply_patch verification failed: no hunks found"
	}

	changes := make([]fileChange, 0, len(hunks))
	for _, hunk := range hunks {
		cleanPath, err := cleanPatchPath(sfs.root, hunk.path)
		if err != nil {
			return emptyApplyPatchPreview(), "apply_patch verification failed: " + err.Error()
		}

		if isDeniedEnvPath(cleanPath) {
			return emptyApplyPatchPreview(), "apply_patch verification failed: " + deniedEnvAccessMessage(cleanPath)
		}

		switch hunk.typ {
		case "add":
			oldContent := ""

			nextContent := hunk.contents
			if nextContent == "" || !strings.HasSuffix(nextContent, "\n") {
				nextContent += "\n"
			}

			text, bom := splitBOM(nextContent)
			diff := generateUnifiedDiff(oldContent, text)
			additions, deletions := countDiffLines(oldContent, text)
			changes = append(changes, fileChange{typ: "add", path: cleanPath, movePath: "", oldContent: oldContent, newContent: text, bom: bom, diff: diff, additions: additions, deletions: deletions})
		case "update":
			info, err := sfs.root.Stat(cleanPath)
			if err != nil || info.IsDir() {
				return emptyApplyPatchPreview(), "apply_patch verification failed: Failed to read file to update: " + filepath.Join(sfs.root.Name(), cleanPath)
			}

			oldContent, sourceBOM, err := sfs.readTextFile(cleanPath)
			if err != nil {
				return emptyApplyPatchPreview(), "apply_patch verification failed: " + err.Error()
			}

			text, bom, err := sfs.deriveNewContentsFromChunks(cleanPath, hunk.chunks)
			if err != nil {
				return emptyApplyPatchPreview(), "apply_patch verification failed: Error: " + err.Error()
			}

			if sourceBOM {
				bom = true
			}

			movePath := ""
			if hunk.movePath != "" {
				movePath, err = cleanPatchPath(sfs.root, hunk.movePath)
				if err != nil {
					return emptyApplyPatchPreview(), "apply_patch verification failed: " + err.Error()
				}

				if isDeniedEnvPath(movePath) {
					return emptyApplyPatchPreview(), "apply_patch verification failed: " + deniedEnvAccessMessage(movePath)
				}
			}

			changeType := "update"
			if movePath != "" {
				changeType = "move"
			}

			diff := generateUnifiedDiff(oldContent, text)
			additions, deletions := countDiffLines(oldContent, text)
			changes = append(changes, fileChange{typ: changeType, path: cleanPath, movePath: movePath, oldContent: oldContent, newContent: text, bom: bom, diff: diff, additions: additions, deletions: deletions})
		case "delete":
			if err := sfs.verifyDeleteTarget(cleanPath); err != nil {
				return emptyApplyPatchPreview(), "apply_patch verification failed: " + err.Error()
			}

			oldContent, bom, err := sfs.readTextFile(cleanPath)
			if err != nil {
				return emptyApplyPatchPreview(), "apply_patch verification failed: " + err.Error()
			}

			diff := generateUnifiedDiff(oldContent, "")
			_, deletions := countDiffLines(oldContent, "")
			changes = append(changes, fileChange{typ: "delete", path: cleanPath, movePath: "", oldContent: oldContent, newContent: "", bom: bom, diff: diff, additions: 0, deletions: deletions})
		}
	}

	files := make([]applyPatchFileMeta, 0, len(changes))

	var totalDiff strings.Builder

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

		lines = append(lines, prefix+filepath.ToSlash(target))
		files = append(files, applyPatchFileMeta{
			FilePath:     filepath.Join(sfs.root.Name(), change.path),
			RelativePath: filepath.ToSlash(target),
			Type:         change.typ,
			Patch:        change.diff,
			Additions:    change.additions,
			Deletions:    change.deletions,
			MovePath:     patchMovePath(sfs.root.Name(), change.movePath),
		})
		totalDiff.WriteString(change.diff)
		totalDiff.WriteByte('\n')
	}

	return applyPatchPreview{changes: changes, files: files, diff: totalDiff.String(), output: strings.Join(lines, "\n")}, ""
}

//nolint:funcorder // Patch helpers are grouped with the ApplyPatch implementation they support.
func (sfs *sandboxedFileSystem) verifyDeleteTarget(name string) error {
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
		return nil, errors.New("Invalid patch format: missing Begin/End markers") //nolint:staticcheck // Exact OpenCode parse error text is capitalized.
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

func stripHeredoc(input string) string {
	trimmed := strings.TrimSpace(input)

	lines := strings.Split(trimmed, "\n")
	if len(lines) < 3 {
		return input
	}

	first := strings.TrimSpace(lines[0])

	first = strings.TrimPrefix(first, "cat ")
	if !strings.HasPrefix(first, "<<") {
		return input
	}

	marker := strings.TrimSpace(strings.TrimPrefix(first, "<<"))

	marker = strings.Trim(marker, `"'`)
	if marker == "" {
		return input
	}

	if strings.TrimSpace(lines[len(lines)-1]) != marker {
		return input
	}

	return strings.Join(lines[1:len(lines)-1], "\n")
}

func splitBOM(text string) (string, bool) {
	r, _ := utf8.DecodeRuneInString(text)
	if text == "" || r != '\ufeff' {
		return text, false
	}

	return strings.TrimPrefix(text, "\ufeff"), true
}

func joinBOM(text string, bom bool) string {
	text, _ = splitBOM(text)
	if !bom {
		return text
	}

	return "\ufeff" + text
}

//nolint:funcorder // Patch helpers are grouped with the ApplyPatch implementation they support.
func (sfs *sandboxedFileSystem) readTextFile(name string) (text string, bom bool, err error) {
	name, err = normalizeRootName(sfs.root, name)
	if err != nil {
		return "", false, err
	}

	if err := sfs.rejectSymlink(name); err != nil {
		return "", false, err
	}

	data, err := sfs.root.ReadFile(name)
	if err != nil {
		return "", false, fmt.Errorf("read text file %q: %w", name, err)
	}

	text, bom = splitBOM(string(data))

	return text, bom, nil
}

//nolint:funcorder // Patch helpers are grouped with the ApplyPatch implementation they support.
func (sfs *sandboxedFileSystem) writeTextFile(name, text string, bom bool) error {
	if err := sfs.rejectSymlink(name); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}

	dir := filepath.Dir(name)
	if dir != "." {
		if err := sfs.root.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("mkdir parents for %q: %w", name, err)
		}
	}

	if err := sfs.root.WriteFile(name, []byte(joinBOM(text, bom)), 0o644); err != nil {
		return fmt.Errorf("write text file %q: %w", name, err)
	}

	return nil
}

//nolint:funcorder // Patch helpers are grouped with the ApplyPatch implementation they support.
func (sfs *sandboxedFileSystem) rejectSymlink(name string) error {
	info, err := sfs.root.Lstat(name)
	if err != nil {
		return fmt.Errorf("lstat %q: %w", name, err)
	}

	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("symlink access denied: %s", name)
	}

	return nil
}

//nolint:funcorder // Patch helpers are grouped with the ApplyPatch implementation they support.
func (sfs *sandboxedFileSystem) deriveNewContentsFromChunks(name string, chunks []updateFileChunk) (text string, bom bool, err error) {
	originalText, originalBOM, err := sfs.readTextFile(name)
	if err != nil {
		return "", false, fmt.Errorf("Failed to read file %s: %w", filepath.Join(sfs.root.Name(), name), err) //nolint:staticcheck // Exact OpenCode compute error text is capitalized.
	}

	originalLines := strings.Split(originalText, "\n")
	if len(originalLines) > 0 && originalLines[len(originalLines)-1] == "" {
		originalLines = originalLines[:len(originalLines)-1]
	}

	replacements, err := computeReplacements(originalLines, filepath.Join(sfs.root.Name(), name), chunks)
	if err != nil {
		return "", false, err
	}

	newLines := applyReplacements(originalLines, replacements)
	if len(newLines) == 0 || newLines[len(newLines)-1] != "" {
		newLines = append(newLines, "")
	}

	text, bom = splitBOM(strings.Join(newLines, "\n"))

	return text, originalBOM || bom, nil
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
				return nil, fmt.Errorf("Failed to find context '%s' in %s", chunk.changeContext, filePath) //nolint:staticcheck // Exact OpenCode match error text is capitalized.
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

		pattern := append([]string(nil), chunk.oldLines...)
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
			return nil, fmt.Errorf("Failed to find expected lines in %s:\n%s", filePath, strings.Join(chunk.oldLines, "\n")) //nolint:staticcheck // Exact OpenCode match error text is capitalized.
		}

		replacements = append(replacements, replacement{start: found, oldLen: len(pattern), newLines: newSlice})
		lineIndex = found + len(pattern)
	}

	sort.Slice(replacements, func(i, j int) bool { return replacements[i].start < replacements[j].start })

	return replacements, nil
}

func applyReplacements(lines []string, replacements []replacement) []string {
	result := append([]string(nil), lines...)

	for _, v := range slices.Backward(replacements) {
		repl := v
		result = append(result[:repl.start], append(repl.newLines, result[repl.start+repl.oldLen:]...)...)
	}

	return result
}

func seekSequence(lines, pattern []string, startIndex int, eof bool) int {
	if len(pattern) == 0 {
		return -1
	}

	comparators := []func(string, string) bool{
		func(a, b string) bool { return a == b },
		func(a, b string) bool { return strings.TrimRight(a, " \t") == strings.TrimRight(b, " \t") },
		func(a, b string) bool { return strings.TrimSpace(a) == strings.TrimSpace(b) },
		func(a, b string) bool {
			return normalizeUnicode(strings.TrimSpace(a)) == normalizeUnicode(strings.TrimSpace(b))
		},
	}
	for _, compare := range comparators {
		if match := tryMatch(lines, pattern, startIndex, compare, eof); match != -1 {
			return match
		}
	}

	return -1
}

func tryMatch(lines, pattern []string, startIndex int, compare func(string, string) bool, eof bool) int {
	if eof {
		fromEnd := len(lines) - len(pattern)
		if fromEnd >= startIndex && sequenceMatches(lines[fromEnd:], pattern, compare) {
			return fromEnd
		}
	}

	for i := startIndex; i <= len(lines)-len(pattern); i++ {
		if sequenceMatches(lines[i:], pattern, compare) {
			return i
		}
	}

	return -1
}

func sequenceMatches(lines, pattern []string, compare func(string, string) bool) bool {
	for i := range pattern {
		if !compare(lines[i], pattern[i]) {
			return false
		}
	}

	return true
}

func normalizeUnicode(text string) string {
	replacer := strings.NewReplacer(
		"\u2018", "'", "\u2019", "'", "\u201A", "'", "\u201B", "'",
		"\u201C", `"`, "\u201D", `"`, "\u201E", `"`, "\u201F", `"`,
		"\u2010", "-", "\u2011", "-", "\u2012", "-", "\u2013", "-", "\u2014", "-", "\u2015", "-",
		"\u2026", "...",
		"\u00A0", " ",
	)

	return replacer.Replace(text)
}

func generateUnifiedDiff(oldContent, newContent string) string {
	oldLines := strings.Split(oldContent, "\n")
	newLines := strings.Split(newContent, "\n")
	diff := "@@ -1 +1 @@\n"
	maxLen := max(len(oldLines), len(newLines))
	hasChanges := false

	var diffSb743 strings.Builder

	for i := range maxLen {
		oldLine := ""
		newLine := ""

		if i < len(oldLines) {
			oldLine = oldLines[i]
		}

		if i < len(newLines) {
			newLine = newLines[i]
		}

		if oldLine != newLine {
			if oldLine != "" {
				diffSb743.WriteString("-" + oldLine + "\n")
			}

			if newLine != "" {
				diffSb743.WriteString("+" + newLine + "\n")
			}

			hasChanges = true

			continue
		}

		if oldLine != "" {
			diffSb743.WriteString(" " + oldLine + "\n")
		}
	}

	diff += diffSb743.String()

	if !hasChanges {
		return ""
	}

	return diff
}

func countDiffLines(oldContent, newContent string) (additions, deletions int) {
	oldLines := strings.Split(oldContent, "\n")
	newLines := strings.Split(newContent, "\n")

	maxLen := max(len(oldLines), len(newLines))
	for i := range maxLen {
		oldLine := ""
		newLine := ""

		if i < len(oldLines) {
			oldLine = oldLines[i]
		}

		if i < len(newLines) {
			newLine = newLines[i]
		}

		if oldLine == newLine {
			continue
		}

		if oldLine != "" {
			deletions++
		}

		if newLine != "" {
			additions++
		}
	}

	return additions, deletions
}

func patchMovePath(rootName, movePath string) string {
	if movePath == "" {
		return ""
	}

	return filepath.Join(rootName, movePath)
}

func (sfs *sandboxedFileSystem) Glob(ctx context.Context, pattern, path string) string {
	sfs.mu.Lock()
	defer sfs.mu.Unlock()

	const limit = 100

	type globMatch struct {
		path  string
		mtime int64
	}

	searchPath := path
	searchRoot := sfs.root

	hostRoot := sfs.root.Name()

	if searchPath != "" {
		var err error

		searchPath, err = normalizeRootName(sfs.root, searchPath)
		if err != nil {
			return err.Error()
		}

		if err := sfs.rejectSymlink(searchPath); err != nil {
			return err.Error()
		}

		root, err := sfs.root.OpenRoot(searchPath)
		if err != nil {
			return err.Error()
		}

		defer func() { _ = root.Close() }()

		searchRoot = root
		hostRoot = filepath.Join(hostRoot, searchPath)
	}

	cmd := exec.CommandContext(context.WithoutCancel(ctx), "rg", "--no-config", "--files", "--hidden", "--no-follow", "--path-separator=/", "--glob=!.git/*", "-g", pattern)
	cmd.Dir = hostRoot

	var stdoutBuf, stderr bytes.Buffer

	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		if errExit, ok := errors.AsType[*exec.ExitError](err); ok && errExit.ExitCode() == 1 {
			return "No files found"
		}

		message := strings.TrimSpace(stderr.String())
		if message != "" {
			return message
		}

		return err.Error()
	}

	outputText := strings.TrimSpace(stdoutBuf.String())
	if outputText == "" {
		return "No files found"
	}

	matches := strings.Split(outputText, "\n")
	if len(matches) == 0 {
		return "No files found"
	}

	results := make([]globMatch, 0, len(matches))
	for _, match := range matches {
		if isDeniedEnvPath(match) {
			continue
		}

		info, err := searchRoot.Lstat(match)
		if err != nil {
			continue
		}

		if info.Mode()&os.ModeSymlink != 0 {
			continue
		}

		results = append(results, globMatch{path: filepath.Join(hostRoot, match), mtime: info.ModTime().UnixMilli()})
	}

	if len(results) == 0 {
		return "No files found"
	}

	sort.Slice(results, func(i, j int) bool {
		if results[i].mtime == results[j].mtime {
			return results[i].path < results[j].path
		}

		return results[i].mtime > results[j].mtime
	})

	truncated := false
	if len(results) > limit {
		truncated = true
		results = results[:limit]
	}

	output := make([]string, 0, len(results)+2)
	for _, result := range results {
		output = append(output, result.path)
	}

	if truncated {
		output = append(output, "", "(Results are truncated: showing first 100 results. Consider using a more specific path or pattern.)")
	}

	return strings.Join(output, "\n")
}

type grepMatch struct {
	path  string
	line  int
	text  string
	mtime int64
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
	close    func()
}

func (sfs *sandboxedFileSystem) Grep(ctx context.Context, pattern, searchPath, include string) string {
	sfs.mu.Lock()
	defer sfs.mu.Unlock()

	if pattern == "" {
		return "pattern is required"
	}

	target, err := sfs.resolveGrepTarget(searchPath)
	if err != nil {
		return err.Error()
	}
	defer target.close()

	files := target.files
	if len(files) == 1 && files[0] == "." {
		var errText string

		files, errText = allowedRipgrepFiles(ctx, target.hostRoot, include)
		if errText != "" {
			return errText
		}

		if len(files) == 0 {
			return "No files found"
		}
	}

	stdout, partial, errText := runRipgrep(ctx, target.hostRoot, pattern, files)
	if errText != "" {
		return errText
	}

	matches, err := parseGrepMatches(stdout, target.root, target.hostRoot)
	if err != nil {
		return err.Error()
	}

	if len(matches) == 0 {
		return "No files found"
	}

	return formatGrepOutput(matches, partial)
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

	targetAbs, err := filepath.Abs(clean)
	if err != nil {
		return "", fmt.Errorf("resolve path %q: %w", name, err)
	}

	rel, err := filepath.Rel(rootAbs, targetAbs)
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
	target := grepTarget{root: sfs.root, hostRoot: sfs.root.Name(), files: []string{"."}, close: func() {}}
	if searchPath == "" {
		return target, nil
	}

	var err error

	searchPath, err = normalizeRootName(sfs.root, searchPath)
	if err != nil {
		return grepTarget{}, err
	}

	if isDeniedEnvPath(searchPath) {
		return grepTarget{}, errors.New(deniedEnvAccessMessage(searchPath))
	}

	if err := sfs.rejectSymlink(searchPath); err != nil {
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
		target.close = func() { _ = root.Close() }

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
	target.close = func() { _ = root.Close() }

	return target, nil
}

func allowedRipgrepFiles(ctx context.Context, hostRoot, include string) (files []string, errText string) {
	args := []string{"--no-config", "--files", "--hidden", "--no-follow", "--path-separator=/", "--glob=!.git/*"}
	if include != "" {
		args = append(args, "--glob="+include)
	}

	cmd := exec.CommandContext(context.WithoutCancel(ctx), "rg", args...)
	cmd.Dir = hostRoot

	var stdoutBuf, stderr bytes.Buffer

	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderr

	err := cmd.Run()
	message := strings.TrimSpace(stderr.String())

	if err != nil {
		errExit, ok := errors.AsType[*exec.ExitError](err)
		if !ok || errExit.ExitCode() != 1 {
			if message != "" {
				return nil, message
			}

			return nil, err.Error()
		}
	}

	outputText := strings.TrimSpace(stdoutBuf.String())
	if outputText == "" {
		return nil, ""
	}

	listed := strings.Split(outputText, "\n")

	allowed := make([]string, 0, len(listed))
	for _, file := range listed {
		if isDeniedEnvPath(file) {
			continue
		}

		allowed = append(allowed, file)
	}

	return allowed, ""
}

func runRipgrep(ctx context.Context, hostRoot, pattern string, files []string) (stdout []byte, partial bool, errText string) {
	args := make([]string, 0, 7+len(files))
	args = append(args, "--no-config", "--json", "--hidden", "--no-follow", "--path-separator=/", "--glob=!.git/*", "--no-messages", "--", pattern)
	args = append(args, files...)
	cmd := exec.CommandContext(context.WithoutCancel(ctx), "rg", args...)
	cmd.Dir = hostRoot

	var stdoutBuf, stderr bytes.Buffer

	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderr
	err := cmd.Run()
	message := strings.TrimSpace(stderr.String())

	if err == nil {
		return stdoutBuf.Bytes(), false, ""
	}

	errExit, ok := errors.AsType[*exec.ExitError](err)
	if !ok {
		if message != "" {
			return nil, false, message
		}

		return nil, false, err.Error()
	}

	switch errExit.ExitCode() {
	case 1:
		return nil, false, "No files found"
	case 2:
		if message != "" {
			return nil, false, message
		}

		return stdoutBuf.Bytes(), true, ""
	default:
		if message != "" {
			return nil, false, message
		}

		return nil, false, err.Error()
	}
}

func parseGrepMatches(stdout []byte, searchRoot *os.Root, hostRoot string) ([]grepMatch, error) {
	scanner := bufio.NewScanner(bytes.NewReader(stdout))
	scanner.Buffer(make([]byte, 0, 64*1024), max(len(stdout), 64*1024))

	var matches []grepMatch

	for scanner.Scan() {
		line := scanner.Bytes()
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
		if len(text) > maxLineLength {
			text = text[:maxLineLength] + "..."
		}

		matches = append(matches, grepMatch{path: filepath.Join(hostRoot, item.Data.Path.Text), line: item.Data.LineNumber, text: text, mtime: info.ModTime().UnixMilli()})
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan ripgrep output: %w", err)
	}

	return matches, nil
}

func formatGrepOutput(matches []grepMatch, partial bool) string {
	const limit = 100

	sort.Slice(matches, func(i, j int) bool {
		if matches[i].mtime == matches[j].mtime {
			if matches[i].path == matches[j].path {
				return matches[i].line < matches[j].line
			}

			return matches[i].path < matches[j].path
		}

		return matches[i].mtime > matches[j].mtime
	})
	total := len(matches)

	truncated := total > limit
	if truncated {
		matches = matches[:limit]
	}

	output := []string{fmt.Sprintf("Found %d matches", total)}
	if truncated {
		output[0] = fmt.Sprintf("Found %d matches (showing first %d)", total, limit)
	}

	current := ""
	for _, match := range matches {
		if current != match.path {
			if current != "" {
				output = append(output, "")
			}

			current = match.path
			output = append(output, match.path+":")
		}

		output = append(output, fmt.Sprintf("  Line %d: %s", match.line, match.text))
	}

	if truncated {
		output = append(output, "", fmt.Sprintf("(Results truncated: showing %d of %d matches (%d hidden). Consider using a more specific path or pattern.)", limit, total, total-limit))
	}

	if partial {
		output = append(output, "", "(Some paths were inaccessible and skipped)")
	}

	return strings.Join(output, "\n")
}
