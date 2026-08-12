package quickbench

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing/fstest"

	"github.com/Rocketable/platform/internal/rocketcode"
)

const agentsRoot = "agents"

// AgentOverlay overrides one agent inside a variation.
type AgentOverlay struct {
	Model  string // optional model selector string
	System string // optional prompt body (markdown body after frontmatter)
}

// buildAgents materializes BAR agents with variation and matrix overrides into rocketcode.Agents.
func buildAgents(bar *BAR, variation Variation, matrixAgents map[string]MatrixAgent) (rocketcode.Agents, string, error) {
	if len(bar.Agents) == 0 {
		return rocketcode.Agents{}, "", errors.New("BAR has no agents/")
	}

	root := strings.TrimSpace(bar.Meta.Root)
	if root == "" {
		root = "main"
	}

	fsys := fstest.MapFS{}
	for name, data := range bar.Agents {
		fsys[name+".md"] = &fstest.MapFile{Data: data}
	}

	loaded := rocketcode.LoadAgents(fsys, func(model string) (string, error) { return model, nil })
	if len(loaded.Errors) > 0 {
		return rocketcode.Agents{}, "", fmt.Errorf("load agents: %v", loaded.Errors[0])
	}

	if _, ok := loaded.Agents.Items[root]; !ok {
		return rocketcode.Agents{}, "", fmt.Errorf("root agent %q missing from agents/", root)
	}

	items := map[string]rocketcode.Agent{}
	maps.Copy(items, loaded.Agents.Items)

	for name, overlay := range variation.AgentOverlays {
		agent, ok := items[name]
		if !ok {
			return rocketcode.Agents{}, "", fmt.Errorf("variation %q overlays unknown agent %q", variation.ID, name)
		}

		if m := strings.TrimSpace(overlay.Model); m != "" {
			agent.Model = m
		}

		if s := strings.TrimSpace(overlay.System); s != "" {
			agent.Prompt = s
		}

		items[name] = agent
	}
	// Legacy variation.System applies to root when no root overlay system set.
	if s := strings.TrimSpace(variation.System); s != "" {
		if overlay, ok := variation.AgentOverlays[root]; !ok || strings.TrimSpace(overlay.System) == "" {
			agent := items[root]
			agent.Prompt = s
			items[root] = agent
		}
	}

	for name, ov := range matrixAgents {
		agent, ok := items[name]
		if !ok {
			return rocketcode.Agents{}, "", fmt.Errorf("unknown agent %q in matrix", name)
		}

		if ov.Model.Raw != "" {
			agent.Model = ov.Model.Model
			if ov.Model.ReasoningEffort != "" {
				agent.ReasoningEffort = ov.Model.ReasoningEffort
			}

			if ov.Model.Verbosity != "" {
				agent.Verbosity = ov.Model.Verbosity
			}
		}

		if s := strings.TrimSpace(ov.System); s != "" {
			agent.Prompt = s
		}

		items[name] = agent
	}

	built := rocketcode.Agents{Items: items}
	if err := validateAgentTaskTargets(built); err != nil {
		return rocketcode.Agents{}, "", err
	}

	return built, root, nil
}

// copyWorkspaceAgents reads agents/*.md from a workspace agents directory into a name→content map.
func copyWorkspaceAgents(agentsDir string) (map[string][]byte, error) {
	entries, err := os.ReadDir(agentsDir)
	if err != nil {
		return nil, fmt.Errorf("read agents dir: %w", err)
	}

	out := map[string][]byte{}

	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".md" {
			continue
		}

		name := strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))

		data, err := os.ReadFile(filepath.Join(agentsDir, entry.Name()))
		if err != nil {
			return nil, err
		}

		out[name] = data
	}

	if len(out) == 0 {
		return nil, fmt.Errorf("no agent markdown files in %s", agentsDir)
	}

	return out, nil
}

func defaultMainAgentMarkdown(model, prompt string) []byte {
	// Test/sample helper only — production capture copies workspace agents verbatim.
	if strings.TrimSpace(model) == "" {
		model = "gpt-5.4"
	}

	if strings.TrimSpace(prompt) == "" {
		prompt = "You are a helpful assistant."
	}

	return fmt.Appendf(nil, "---\ndescription: quickbench sample root agent\nmodel: %s\npermission:\n  task:\n    \"*\": allow\n  bash:\n    \"*\": allow\n  tools:\n    \"*\": allow\n---\n\n%s\n", model, strings.TrimSpace(prompt))
}

// validateAgentTaskTargets ensures exact task allow targets exist in the BAR tree.
func validateAgentTaskTargets(agents rocketcode.Agents) error {
	names := map[string]struct{}{}
	for name := range agents.Items {
		names[name] = struct{}{}
	}

	for name, agent := range agents.Items {
		for _, bucket := range agent.Permission.Buckets {
			if bucket.Name != "task" {
				continue
			}

			for _, rule := range bucket.Rules {
				if rule.Action != rocketcode.PermissionAllow && rule.Action != rocketcode.PermissionAuto {
					continue
				}

				pat := strings.TrimSpace(rule.Pattern)
				if pat == "" || pat == "*" || strings.ContainsAny(pat, "*?[") {
					continue
				}

				if _, ok := names[pat]; !ok {
					return fmt.Errorf("agent %q permission task %q references missing agents/%s.md", name, pat, pat)
				}
			}
		}
	}

	return nil
}
