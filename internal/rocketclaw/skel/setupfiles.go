package skel

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

var errUnknownSetupFile = errors.New("unknown embedded setup file")

// ListSetupFiles returns every workspace-relative file path that `rocketclaw setup` can materialize.
func ListSetupFiles() ([]string, error) {
	entries, err := fs.ReadDir(payload, ".")
	if err != nil {
		return nil, fmt.Errorf("read embedded root setup files: %w", err)
	}

	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		names = append(names, entry.Name())
	}

	for _, root := range [...]string{payloadRoot, agentsRoot, workspaceCron} {
		if err := fs.WalkDir(payload, root, func(name string, d fs.DirEntry, err error) error {
			if err != nil {
				return fmt.Errorf("walk embedded setup files under %s: %w", root, err)
			}

			if d.IsDir() {
				return nil
			}

			rel := strings.TrimPrefix(name, root+"/")
			names = append(names, path.Join(root, rel))

			return nil
		}); err != nil {
			return nil, fmt.Errorf("list embedded setup files under %s: %w", root, err)
		}
	}

	sort.Strings(names)

	return names, nil
}

// ReadSetupFile returns the bytes for one workspace-relative embedded setup file.
func ReadSetupFile(name string) ([]byte, error) {
	embeddedPath, err := resolveSetupFilePath(name)
	if err != nil {
		return nil, err
	}

	data, err := fs.ReadFile(payload, embeddedPath)
	if err != nil {
		return nil, fmt.Errorf("read embedded setup file %s: %w", name, err)
	}

	return data, nil
}

func resolveSetupFilePath(name string) (string, error) {
	cleaned := filepath.ToSlash(filepath.Clean(name))
	if cleaned == "." || strings.HasPrefix(cleaned, "../") || strings.HasPrefix(cleaned, "/") {
		return "", fmt.Errorf("%w: %s", errUnknownSetupFile, name)
	}

	if !strings.Contains(cleaned, "/") {
		if info, err := fs.Stat(payload, cleaned); err == nil && !info.IsDir() {
			return cleaned, nil
		}
	}

	for _, root := range [...]string{payloadRoot, agentsRoot, workspaceCron} {
		if after, ok := strings.CutPrefix(cleaned, root+"/"); ok && after != "" {
			embedded := path.Join(root, after)
			if info, err := fs.Stat(payload, embedded); err == nil && !info.IsDir() {
				return embedded, nil
			}
		}
	}

	return "", fmt.Errorf("%w: %s", errUnknownSetupFile, name)
}
