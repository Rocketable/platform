package backend

import (
	"cmp"
	"context"
	"encoding/json/v2"
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

func (b *Bridge) setEasyShell(ctx context.Context, cfg *config.Config, agents harness.Agents, skills harness.Skills, name string, turnEnv map[string]string, self string) error {
	agent := agents.Items[name]
	if !agent.CustomShell.Emulated && agent.Mode != harness.AgentModeEasy {
		return nil
	}

	selfEnv := maps.Collect(maps.All(turnEnv))
	selfEnv[rocketclawConversationIDEnv] = self

	folder, err := resolveWorkingDirectory(cfg, agent.WorkingDirectory, selfEnv)
	if err != nil {
		return err
	}

	uploads := self
	if b.shadow || b.underShadow {
		if uploads, err = b.config.SessionService.ShadowParent(ctx, self); err != nil {
			return fmt.Errorf("uploads of the shadowed chat: %w", err)
		}
	}

	uploadDir := filepath.Join(cfg.Workspace, EasyUploadDir(cfg, uploads))
	if err := os.MkdirAll(uploadDir, 0o700); err != nil {
		return fmt.Errorf("uploads folder: %w", err)
	}

	shelled := []*harness.Agent{&agent}

	if agent.Approver != nil {
		approver := *agent.Approver
		agent.Approver = &approver
		shelled = append(shelled, &approver)
	}

	for _, target := range shelled {
		secrets, err := config.SecretEnv(ctx, b.config.Secrets, target.SecretsARN)
		if err != nil {
			return fmt.Errorf("secretsARN: %w", err)
		}

		dirs := []string{uploadDir}
		for _, skill := range skills.Available(target) {
			dirs = append(dirs, filepath.Join(skills.Root, filepath.FromSlash(path.Dir(skill.Location))))
		}

		var readOnly []string

		slices.Sort(dirs)

		for _, dir := range dirs {
			if !slices.ContainsFunc(readOnly, func(outer string) bool { return within(dir, outer) }) {
				readOnly = append(readOnly, dir)
			}
		}

		shell := harness.CustomShell{Emulated: target.CustomShell.Emulated, Path: target.CustomShell.Path, Dir: folder, ReadOnly: readOnly, Commands: target.Commands, NetworkTargets: target.NetworkTargets, Secrets: secrets}
		if shell.Path != "" {
			environment, _ := json.Marshal(struct {
				SecretsARN       []string          `json:"secretsARN"`
				Commands         map[string]string `json:"commands"`
				NetworkTargets   []string          `json:"allowedNetworkTargets"`
				WorkingDirectory string            `json:"workingDirectory"`
			}{target.SecretsARN, target.Commands, target.NetworkTargets, folder}, json.Deterministic(true), json.FormatNilSliceAsNull(true), json.FormatNilMapAsNull(true))
			shell.Env = []string{"ROCKETCLAW_ENVIRONMENT=" + string(environment)}
		}

		target.CustomShell = shell
	}

	agents.Items[name] = agent

	return nil
}

func EasyUploadDir(cfg *config.Config, conversationID string) string {
	return filepath.Join(cfg.RuntimeDirName(), "uploads", pathSegment(conversationID))
}

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

	refused := []string{cfg.RuntimeDirName(), "agents", "skills", "cron", "scripts"}
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

func pathSegment(value string) string {
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
