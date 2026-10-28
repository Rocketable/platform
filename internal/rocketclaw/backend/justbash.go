package backend

import (
	"fmt"
	"maps"
	"path/filepath"

	"github.com/Rocketable/platform/internal/justbash"
	"github.com/Rocketable/platform/internal/rocketcode"
)

// startJustBash registers the agents that set shell: just-bash for one turn, points their customShell at
// their launchers, and returns env with the variables their bash calls need. A turn without such agents
// keeps env and gets no session. $ROCKETCLAW_CONVERSATION_ID in a workingDirectory always names
// conversationID, even where env lacks it.
func (b *Bridge) startJustBash(agents rocketcode.Agents, env map[string]string, conversationID string) (map[string]string, *justbash.Session, error) {
	settings := map[string]justbash.Settings{}

	for name := range agents.Items {
		// The loader already rejected invalid just-bash settings.
		if s, ok, _ := justbash.ParseSettings(agents.Items[name].Frontmatter); ok {
			settings[name] = s
		}
	}

	if len(settings) == 0 {
		return env, nil, nil
	}

	runtimeCfg := b.runtime.Clone()
	expandEnv := map[string]string{rocketclawConversationIDEnv: conversationID}
	maps.Copy(expandEnv, env)

	session, err := b.justbash.Register(justbash.Turn{Workspace: runtimeCfg.Workspace, RuntimeDir: filepath.Join(runtimeCfg.Workspace, runtimeCfg.RuntimeDirName()), Agents: settings, Env: expandEnv})
	if err != nil {
		return nil, nil, fmt.Errorf("start just-bash: %w", err)
	}

	if err := b.setJustBashLaunchers(agents); err != nil {
		session.Release()

		return nil, nil, err
	}

	runEnv := session.Env()
	maps.Copy(runEnv, env)

	return runEnv, session, nil
}

// setJustBashLaunchers points the customShell of each agent that sets shell: just-bash at its launcher.
func (b *Bridge) setJustBashLaunchers(agents rocketcode.Agents) error {
	for name := range agents.Items {
		agent := agents.Items[name]
		if _, ok, _ := justbash.ParseSettings(agent.Frontmatter); !ok {
			continue
		}

		launcher, err := b.justbash.Launcher(name)
		if err != nil {
			return fmt.Errorf("%s: %w", agent.Location, err)
		}

		agent.CustomShell = launcher
		agents.Items[name] = agent
	}

	return nil
}
