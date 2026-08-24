package rocketcode

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

const (
	executeHeadMaxLines = 2000
	executeHeadMaxBytes = 50 * 1024
	defaultSpillRel     = ".rocketcode/spill"
)

func clipExecuteHead(text string) (string, bool) {
	if text == "" {
		return text, false
	}

	var out strings.Builder

	reader := bufio.NewReader(strings.NewReader(text))

	lines := 0
	for lines < executeHeadMaxLines {
		line, err := reader.ReadString('\n')
		if out.Len()+len(line) > executeHeadMaxBytes {
			remain := executeHeadMaxBytes - out.Len()
			if remain > 0 {
				out.WriteString(line[:remain])
			}

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

func executeSpillFooter(path string) string {
	return "\n\n...output truncated...\n\n" +
		"Full output is at " + path + ".\n" +
		"Open it in a later execute with read(filePath=\"" + path + "\").\n" +
		"Extract or summarize in the script. Do not return the whole file from main() or it will spill again.\n" +
		"This file is deleted when this turn ends.\n"
}

func (l *looper) beginTurnSpills(turnID string) {
	l.spillMu.Lock()
	l.spillTurnID = turnID
	l.spillSeq = 0
	l.spillPaths = nil
	l.spillMu.Unlock()
}

func (l *looper) endTurnSpills() {
	l.spillMu.Lock()
	turnID := l.spillTurnID
	l.spillTurnID = ""
	l.spillMu.Unlock()

	if turnID == "" || l.spillRel == "" || l.promptExpansion.root == nil {
		return
	}

	_ = l.promptExpansion.root.RemoveAll(filepath.ToSlash(filepath.Join(l.spillRel, turnID)))
}

func (l *looper) spillExecuteOutput(out string) (string, error) {
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

	if existing := l.existingTurnSpillPath(readResultPath(out)); existing != "" {
		return strings.TrimRight(head, "\n") + executeSpillFooter(existing), nil
	}

	l.spillSeq++

	rel := filepath.ToSlash(filepath.Join(l.spillRel, l.spillTurnID, fmt.Sprintf("output-%d.txt", l.spillSeq)))
	if err := l.promptExpansion.root.MkdirAll(filepath.Dir(rel), 0o700); err != nil {
		return "", fmt.Errorf("create execute spill dir: %w", err)
	}

	if err := l.promptExpansion.root.WriteFile(rel, []byte(out), 0o600); err != nil {
		return "", fmt.Errorf("write execute spill: %w", err)
	}

	l.spillPaths = append(l.spillPaths, rel)

	if err := l.Permissions.Allow("read", rel); err != nil {
		return "", fmt.Errorf("grant execute spill read: %w", err)
	}

	if _, ok := l.CodeModeHosts["read"]; !ok && l.sandboxRead.Call != nil {
		if l.CodeModeHosts == nil {
			l.CodeModeHosts = map[string]looperTool{}
		}

		l.CodeModeHosts["read"] = l.sandboxRead
	}

	return strings.TrimRight(head, "\n") + executeSpillFooter(rel), nil
}

func readResultPath(out string) string {
	const prefix = "<path>"
	if !strings.HasPrefix(out, prefix) {
		return ""
	}

	end := strings.Index(out, "</path>")
	if end < 0 {
		return ""
	}

	return out[len(prefix):end]
}

func (l *looper) existingTurnSpillPath(path string) string {
	rel := filepath.ToSlash(filepath.Clean(path))
	if slices.Contains(l.spillPaths, rel) {
		return rel
	}

	return ""
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
