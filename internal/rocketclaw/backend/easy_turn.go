package backend

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/harness"
)

// setEasyShell sets up the emulated shell of agents' agent name, when it is easy or sets shell: emulated,
// for a turn of the top-level session self whose variables are turnEnv: the sandbox in the folder its
// workingDirectory names for self, so its subagents and workers share it, with its secrets for host commands
// only. It shows read-only the uploads to self, or to the chat a shadow shadows, and the folders of the
// skills in skills it may use.
func (b *Bridge) setEasyShell(ctx context.Context, cfg *config.Config, agents harness.Agents, skills harness.Skills, name string, turnEnv map[string]string, self string) error {
	agent := agents.Items[name]
	if !agent.CustomShell.Emulated {
		return nil
	}

	selfEnv := maps.Collect(maps.All(turnEnv))
	selfEnv[rocketclawConversationIDEnv] = self

	folder, err := resolveWorkingDirectory(cfg, agent.WorkingDirectory, selfEnv)
	if err != nil {
		return err
	}

	secrets, err := config.SecretEnv(ctx, b.config.Secrets, agent.SecretsARN)
	if err != nil {
		return fmt.Errorf("secretsARN: %w", err)
	}

	uploads := self
	if b.shadow || b.underShadow {
		if uploads, err = b.config.SessionService.ShadowParent(ctx, self); err != nil {
			return fmt.Errorf("uploads of the shadowed chat: %w", err)
		}
	}

	dirs := []string{filepath.Join(cfg.Workspace, EasyUploadDir(cfg, uploads))}
	if err := os.MkdirAll(dirs[0], 0o700); err != nil {
		return fmt.Errorf("uploads folder: %w", err)
	}

	for _, skill := range skills.Available(&agent) {
		dirs = append(dirs, filepath.Join(skills.Root, filepath.FromSlash(path.Dir(skill.Location))))
	}

	// A folder inside another shows through it anyway, so only the outer one is mounted. A folder sorts before
	// those in it.
	var readOnly []string

	slices.Sort(dirs)

	for _, dir := range dirs {
		if !slices.ContainsFunc(readOnly, func(outer string) bool { return within(dir, outer) }) {
			readOnly = append(readOnly, dir)
		}
	}

	agent.CustomShell = harness.CustomShell{Emulated: true, Dir: folder, ReadOnly: readOnly, Commands: agent.Commands, NetworkTargets: agent.NetworkTargets, Secrets: secrets}
	agents.Items[name] = agent

	return nil
}

// EasyUploadDir is the folder, relative to the workspace, that keeps the uploads to an easy agent's chat
// conversationID: in the runtime directory, which reload keeps, named as a turn variable is in a
// workingDirectory.
func EasyUploadDir(cfg *config.Config, conversationID string) string {
	return filepath.Join(cfg.RuntimeDirName(), "uploads", pathSegment(conversationID))
}

// resolveWorkingDirectory creates and returns the real folder that workingDirectory dir names, relative to
// RocketClaw's working directory. A $VAR comes from turnEnv, escaped into one path segment since callers
// choose those values, or else from RocketClaw's environment as written; an unset one fails. The folder
// may not be or contain the workspace, nor be or lie inside the runtime directory or a folder that defines
// agents, skills, cron jobs, scripts or workflows: the agent could rewrite its own rights there.
func resolveWorkingDirectory(cfg *config.Config, dir string, turnEnv map[string]string) (string, error) {
	unset := ""
	expanded := os.Expand(dir, func(name string) string {
		if value, ok := turnEnv[name]; ok {
			return pathSegment(value)
		}

		value, ok := os.LookupEnv(name)
		if !ok {
			unset = cmp.Or(unset, name)
		}

		return value
	})

	if unset != "" {
		return "", fmt.Errorf("workingDirectory: $%s is not set", unset)
	}

	wanted, err := filepath.Abs(expanded)
	if err != nil {
		return "", fmt.Errorf("workingDirectory: %w", err)
	}

	if err := os.MkdirAll(wanted, 0o755); err != nil {
		return "", fmt.Errorf("workingDirectory: %w", err)
	}

	folder, err := filepath.EvalSymlinks(wanted)
	if err != nil {
		return "", fmt.Errorf("workingDirectory: %w", err)
	}

	workspace, err := filepath.EvalSymlinks(cfg.Workspace)
	if err != nil {
		return "", fmt.Errorf("workingDirectory: workspace: %w", err)
	}

	// A definitions folder counts where it really is, which a link may put out of the workspace; a missing
	// one, where it would be made.
	refused := []string{cfg.RuntimeDirName(), "agents", "skills", "cron", "scripts", "workflows"}
	for _, name := range refused {
		dir := filepath.Join(workspace, name)
		if _, errStat := os.Lstat(dir); !errors.Is(errStat, fs.ErrNotExist) {
			if dir, err = filepath.EvalSymlinks(dir); err != nil {
				return "", fmt.Errorf("workingDirectory: %w", err)
			}
		}

		if within(workspace, folder) || within(folder, dir) || within(dir, folder) {
			return "", fmt.Errorf("workingDirectory %s must not be or contain the workspace, nor be, contain or lie inside its %s", folder, strings.Join(refused, ", "))
		}
	}

	return folder, nil
}

// pathSegment escapes value, which a caller chooses, into one path segment no other value escapes to.
func pathSegment(value string) string {
	// "", "." and ".." would not stay one segment, so they get a "%" in front; PathEscape never writes a
	// "%" before a dot or at the end, so no two values meet.
	segment := url.PathEscape(value)
	if strings.Trim(segment, ".") == "" {
		segment = "%" + segment
	}

	return segment
}

func within(name, dir string) bool {
	rel, err := filepath.Rel(dir, name)
	return err == nil && filepath.IsLocal(rel)
}
