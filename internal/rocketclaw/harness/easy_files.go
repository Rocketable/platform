package harness

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"go.starlark.net/starlark"
)

type easyFiles struct {
	mu    sync.Mutex
	shell CustomShell
}

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

func (v *easyFiles) Read(ctx context.Context, filename string, offset int) (starlark.Value, error) {
	path := v.readPermissionSubject(filename)

	return readTyped(ctx, &v.mu, easyReadAttempt{files: v, path: path}, path, offset)
}

func (v *easyFiles) ApplyPatch(patchText string) (starlark.Value, error) {
	v.mu.Lock()
	defer v.mu.Unlock()

	sfs, errText := v.patchFolder(patchText)
	if errText != "" {
		return nil, errors.New(errText)
	}

	defer func() { _ = sfs.root.Close() }()

	return sfs.ApplyPatch(patchText)
}

func (v *easyFiles) Glob(ctx context.Context, pattern, path string) (starlark.Value, error) {
	var matches []globMatch

	if errText := v.search(path, func(sfs *sandboxedFileSystem, name string) string {
		found, errText := sfs.globMatches(ctx, pattern, name)
		matches = append(matches, found...)

		return errText
	}); errText != "" {
		return nil, errors.New(errText)
	}

	return newGlobResult(matches, globToolParams{Pattern: pattern, Path: path}), nil
}

func (v *easyFiles) Grep(ctx context.Context, pattern, searchPath, include string) (starlark.Value, error) {
	var (
		matches []grepMatch
		partial bool
	)

	if errText := v.search(searchPath, func(sfs *sandboxedFileSystem, name string) string {
		found, skipped, errText := sfs.grepMatches(ctx, pattern, name, include)
		matches, partial = append(matches, found...), partial || skipped

		return errText
	}); errText != "" {
		return nil, errors.New(errText)
	}

	return newGrepResult(matches, partial, grepToolParams{Pattern: pattern, Path: searchPath, Include: include}), nil
}

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

func (v *easyFiles) patchFolder(patchText string) (sfs *sandboxedFileSystem, errText string) {
	hunks, _ := parsePatch(patchText)
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
