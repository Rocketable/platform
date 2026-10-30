package harness

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// easyFiles is an easy agent's file view, the one its shell has (R30): the shell's folder read-write and its
// read-only folders. Each call works in a sandbox on the folder it names, opened for the call. Paths are
// host paths; a relative one starts from the shell's folder.
type easyFiles struct {
	mu    sync.Mutex // Held for each call, as one sandbox's is.
	shell CustomShell
}

// EasyRoot opens the folder of an easy agent's shell that holds name, a host path or one relative to the
// shell's folder, and returns name relative to it.
func EasyRoot(shell *CustomShell, name string) (root *os.Root, rel string, err error) {
	if !filepath.IsAbs(name) {
		name = filepath.Join(shell.Dir, name)
	}

	for _, dir := range append([]string{shell.Dir}, shell.ReadOnly...) {
		if rel, err := filepath.Rel(dir, name); err == nil && filepath.IsLocal(rel) {
			root, err := os.OpenRoot(dir)
			if err != nil {
				return nil, "", fmt.Errorf("open agent folder: %w", err)
			}

			return root, rel, nil
		}
	}

	return nil, "", fmt.Errorf("%s is outside this agent's folders", filepath.Clean(name))
}

func (v *easyFiles) ReadResult(filename string, offset int) ToolResult {
	v.mu.Lock()
	defer v.mu.Unlock()

	sfs, name, err := v.open(filename)
	if err != nil {
		return TextToolResult(err.Error())
	}

	defer func() { _ = sfs.root.Close() }()

	return sfs.ReadResult(name, offset)
}

func (v *easyFiles) ApplyPatch(patchText string) string {
	v.mu.Lock()
	defer v.mu.Unlock()

	sfs, errText := v.patchFolder(patchText)
	if errText != "" {
		return errText
	}

	defer func() { _ = sfs.root.Close() }()

	return sfs.ApplyPatch(patchText)
}

func (v *easyFiles) Glob(ctx context.Context, pattern, path string) string {
	var matches []globMatch

	if errText := v.search(path, func(sfs *sandboxedFileSystem, name string) string {
		found, errText := sfs.globMatches(ctx, pattern, name)
		matches = append(matches, found...)

		return errText
	}); errText != "" {
		return errText
	}

	return formatGlobOutput(matches)
}

func (v *easyFiles) Grep(ctx context.Context, pattern, searchPath, include string) string {
	var (
		matches []grepMatch
		partial bool
	)

	if errText := v.search(searchPath, func(sfs *sandboxedFileSystem, name string) string {
		found, skipped, errText := sfs.grepMatches(ctx, pattern, name, include)
		matches, partial = append(matches, found...), partial || skipped

		return errText
	}); errText != "" {
		return errText
	}

	return formatGrepOutput(matches, partial)
}

// open returns the sandbox of the folder holding name and name's host path, which the sandbox's tools
// take and show.
func (v *easyFiles) open(name string) (*sandboxedFileSystem, string, error) {
	root, rel, err := EasyRoot(&v.shell, name)
	if err != nil {
		return nil, "", err
	}

	return &sandboxedFileSystem{root: root, hostPaths: true}, filepath.Join(root.Name(), rel), nil
}

func (v *easyFiles) readPermissionSubject(name string) string {
	if !filepath.IsAbs(name) {
		name = filepath.Join(v.shell.Dir, name)
	}

	return rootedPathSubject(name)
}

func (v *easyFiles) previewApplyPatch(patchText string) (preview applyPatchPreview, errText string) {
	v.mu.Lock()
	defer v.mu.Unlock()

	sfs, errText := v.patchFolder(patchText)
	if errText != "" {
		return preview, errText
	}

	defer func() { _ = sfs.root.Close() }()

	return sfs.previewApplyPatch(patchText)
}

// patchFolder opens the sandbox of the shell's folder for patchText, unless it names a file in a read-only
// folder.
func (v *easyFiles) patchFolder(patchText string) (sfs *sandboxedFileSystem, errText string) {
	hunks, _ := parsePatch(patchText) // The sandbox reports a patch it cannot parse.
	for _, hunk := range hunks {
		for _, name := range []string{hunk.path, hunk.movePath} {
			if root, rel, err := EasyRoot(&v.shell, name); err == nil {
				_ = root.Close()

				if root.Name() != v.shell.Dir {
					return nil, "apply_patch verification failed: " + filepath.Join(root.Name(), rel) + " is read-only"
				}
			}
		}
	}

	sfs, _, err := v.open(".")
	if err != nil {
		return nil, err.Error()
	}

	return sfs, ""
}

// search runs find in the sandbox of the folder holding path, or of every folder when path is empty, with
// the host path to search there, and returns the first error text find returns.
func (v *easyFiles) search(path string, find func(sfs *sandboxedFileSystem, path string) string) string {
	v.mu.Lock()
	defer v.mu.Unlock()

	paths := []string{path}
	if path == "" {
		paths = append([]string{v.shell.Dir}, v.shell.ReadOnly...)
	}

	for _, path := range paths {
		sfs, name, err := v.open(path)
		if err != nil {
			return err.Error()
		}

		errText := find(sfs, name)
		_ = sfs.root.Close()

		if errText != "" {
			return errText
		}
	}

	return ""
}
