package rocketcode

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"golang.org/x/sys/unix"
)

const (
	executeHeadMaxLines = 2000
	executeHeadMaxBytes = 50 * 1024
	defaultSpillRel     = ".rocketcode/spill"
	deniedSpillAccess   = "execute output storage is private; use load_execute_result"
	retainedResultTTL   = 7 * 24 * time.Hour
)

func isExecuteSpillPath(root *os.Root, spillRel, path string) bool {
	name, err := normalizeRootName(root, path)
	if err == nil && spillRel != "" && pathWithinRoot(spillRel, name) {
		return true
	}

	spill, err := root.Stat(spillRel)

	for ancestor := filepath.Clean(path); ancestor != "." && ancestor != "/"; ancestor = filepath.Dir(ancestor) {
		info, errStat := root.Stat(ancestor)
		if filepath.IsAbs(ancestor) {
			// Host stat checks identity only, including case aliases of the root.
			info, errStat = os.Stat(ancestor)
		}

		if err == nil && errStat == nil && os.SameFile(spill, info) {
			return true
		}
	}

	return false
}

func clipExecuteHead(text string) (string, bool) {
	var out strings.Builder

	reader := bufio.NewReader(strings.NewReader(text))

	lines := 0
	for lines < executeHeadMaxLines {
		line, err := reader.ReadString('\n')
		if out.Len()+len(line) > executeHeadMaxBytes {
			remain := executeHeadMaxBytes - out.Len()
			out.WriteString(line[:remain])

			return out.String(), true
		}

		out.WriteString(line)

		lines++

		if err != nil {
			return out.String(), false
		}
	}

	if _, err := reader.ReadByte(); err == nil {
		return out.String(), true
	}

	return out.String(), false
}

var errUnknownExecuteResult = errors.New("unknown or expired execute result")

func executeSpillFooter(id, expires string) string {
	return "\n\n...output truncated...\n\n" +
		"Full output: result_id=\"" + id + "\".\n" +
		"Call the top-level load_execute_result tool with this result_id, start_line, limit, and line_numbers.\n" +
		"This result expires " + expires + ".\n"
}

// retainExecuteResult files a detached script's oversized output in dir, which outlives turns,
// and sweeps results there older than retainedResultTTL.
func retainExecuteResult(root *os.Root, dir, out string) (string, error) {
	head, oversized := clipExecuteHead(out)
	if !oversized {
		return out, nil
	}

	sweepRetainedDir(root, dir)

	if err := root.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create retained execute result dir: %w", err)
	}

	id := uuid.NewString()
	if err := root.WriteFile(filepath.Join(dir, id+".txt"), []byte(out), 0o600); err != nil {
		return "", fmt.Errorf("write retained execute result: %w", err)
	}

	return strings.TrimRight(head, "\n") + executeSpillFooter(id, "in 7 days"), nil
}

// sweepRetainedDir removes the results in dir older than retainedResultTTL.
func sweepRetainedDir(root *os.Root, dir string) {
	entries, _ := fs.ReadDir(root.FS(), dir)
	for _, entry := range entries {
		if info, err := entry.Info(); err == nil && time.Since(info.ModTime()) > retainedResultTTL {
			_ = root.Remove(filepath.Join(dir, entry.Name()))
		}
	}
}

// SweepRetainedResults sweeps every retained result directory in dir, such as the
// RetainedResultDir of each conversation, and removes those it empties. Nothing may retain
// a result in dir meanwhile, as an emptied directory goes away under it.
func SweepRetainedResults(root *os.Root, dir string) {
	entries, _ := fs.ReadDir(root.FS(), dir)
	for _, entry := range entries {
		if entry.IsDir() {
			retained := filepath.Join(dir, entry.Name())
			sweepRetainedDir(root, retained)
			_ = root.Remove(retained) // Fails, keeping the directory, unless it is empty.
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
		if id, ok := strings.CutSuffix(entry.Name(), ".txt"); ok && entry.Type().IsRegular() {
			l.spillResults[id] = dir + "/" + entry.Name()
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

func (l *looper) saveExecuteResult(out string) (string, error) {
	head, oversized := clipExecuteHead(out)
	if !oversized {
		return out, nil
	}

	if l.promptExpansion.root == nil || l.spillRel == "" {
		return "", fmt.Errorf("execute output exceeds %d lines or %d bytes and no spill directory is configured", executeHeadMaxLines, executeHeadMaxBytes)
	}

	l.spillMu.Lock()
	defer l.spillMu.Unlock()

	if l.spillTurnID == "" {
		return "", fmt.Errorf("execute output exceeds %d lines or %d bytes outside a turn", executeHeadMaxLines, executeHeadMaxBytes)
	}

	id := uuid.NewString()

	rel := filepath.ToSlash(filepath.Join(l.spillRel, l.spillTurnID, id+".txt"))
	if err := l.promptExpansion.root.MkdirAll(filepath.Dir(rel), 0o700); err != nil {
		return "", fmt.Errorf("create execute spill dir: %w", err)
	}

	if err := l.promptExpansion.root.WriteFile(rel, []byte(out), 0o600); err != nil {
		return "", fmt.Errorf("write execute spill: %w", err)
	}

	l.spillResults[id] = rel

	return strings.TrimRight(head, "\n") + executeSpillFooter(id, "when this turn ends"), nil
}

type loadExecuteResultParams struct {
	ResultID    string `json:"result_id"`
	StartLine   int    `json:"start_line"`
	Limit       int    `json:"limit"`
	LineNumbers bool   `json:"line_numbers"`
}

// loadExecuteResult reads only registered current-turn files and this conversation's retained
// results under retainedRel, never caller paths.
func (l *looper) loadExecuteResult(ctx context.Context, retainedRel string, params loadExecuteResultParams) (result ToolResult, err error) {
	if params.StartLine < 0 || params.Limit < 0 {
		return ToolResult{}, errors.New("start_line and limit must not be negative")
	}

	start := max(params.StartLine, 1)

	limit := params.Limit
	if limit == 0 {
		limit = executeHeadMaxLines
	}

	limit = min(limit, executeHeadMaxLines)

	// Ponytail: pages serialize under the lifetime lock and scan the prefix.
	// Add indexing or shorten lock scope only if measured demand requires it.
	l.spillMu.Lock()
	defer l.spillMu.Unlock()

	file, err := l.openStoredExecuteResult(retainedRel, params.ResultID)
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

// openExecuteResult opens each registered path component without following links.
// O_NOFOLLOW on a full path would still follow replaced parent directories.
// openStoredExecuteResult opens a live turn spill, or else a retained Background Job result
// in retainedRel, which expires after retainedResultTTL. The caller holds spillMu.
func (l *looper) openStoredExecuteResult(retainedRel, resultID string) (_ *os.File, err error) {
	path, live := l.spillResults[resultID]
	if !live {
		// A retained ID is a UUID, so a valid one names a file directly in retainedRel.
		id, errParse := uuid.Parse(resultID)
		if errParse != nil {
			return nil, errUnknownExecuteResult
		}

		path = filepath.Join(retainedRel, id.String()+".txt")
	}

	file, err := openExecuteResult(l.promptExpansion.root, path)
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
		_ = l.promptExpansion.root.Remove(path)
		return nil, errors.New("execute result expired")
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
