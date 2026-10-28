package justbash

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

const defaultWorkingDirectory = "justbash/$ROCKETCLAW_CONVERSATION_ID"

// Settings is an agent's just-bash configuration, read from its frontmatter.
type Settings struct {
	// WorkingDirectory is unexpanded; Setup expands it against the turn's environment.
	WorkingDirectory string
	// Commands maps each approved host command's name to the bash command prefix it runs.
	Commands   map[string]string
	SecretsARN []string
	network    networkRules
}

// SettingsError names the frontmatter key whose value is wrong.
type SettingsError struct {
	Key string
	err error
}

func (e *SettingsError) Error() string { return e.Key + ": " + e.err.Error() }

func (e *SettingsError) Unwrap() error { return e.err }

// ParseSettings reads the just-bash keys of an agent's frontmatter. It reports false when the agent
// does not set shell: just-bash.
func ParseSettings(frontmatter map[string]any) (Settings, bool, error) {
	shell, hasShell := frontmatter["shell"]
	if !hasShell {
		for _, key := range []string{"workingDirectory", "commands", "secretsARN", "allowedNetworkTargets"} {
			if _, ok := frontmatter[key]; ok {
				return Settings{}, false, &SettingsError{Key: key, err: errors.New("requires shell: just-bash")}
			}
		}

		return Settings{}, false, nil
	}

	if shell != "just-bash" {
		return Settings{}, false, &SettingsError{Key: "shell", err: errors.New("must be just-bash")}
	}

	if _, ok := frontmatter["customShell"]; ok {
		return Settings{}, false, &SettingsError{Key: "customShell", err: errors.New("cannot be combined with shell: just-bash")}
	}

	settings := Settings{WorkingDirectory: defaultWorkingDirectory, Commands: map[string]string{}, network: networkRules{auto: true}}

	if v, ok := frontmatter["workingDirectory"]; ok {
		s, ok := v.(string)
		if !ok || strings.TrimSpace(s) == "" {
			return Settings{}, false, &SettingsError{Key: "workingDirectory", err: errors.New("must be a non-empty string")}
		}

		settings.WorkingDirectory = s
	}

	if v, ok := frontmatter["commands"]; ok {
		commands, ok := v.(map[string]any)
		if !ok {
			return Settings{}, false, &SettingsError{Key: "commands", err: errors.New("must map command names to bash commands")}
		}

		for name, prefix := range commands {
			s, ok := prefix.(string)
			if name == "" || strings.ContainsAny(name, "/ \t\n") || !ok || strings.TrimSpace(s) == "" {
				return Settings{}, false, &SettingsError{Key: "commands", err: fmt.Errorf("%q must name a command without slashes or spaces and map to a non-empty bash command", name)}
			}

			settings.Commands[name] = s
		}
	}

	if v, ok := frontmatter["secretsARN"]; ok {
		arns, err := stringList(v)
		if err != nil {
			return Settings{}, false, &SettingsError{Key: "secretsARN", err: err}
		}

		settings.SecretsARN = arns
	}

	if v, ok := frontmatter["allowedNetworkTargets"]; ok {
		rules, err := parseNetworkRules(v)
		if err != nil {
			return Settings{}, false, &SettingsError{Key: "allowedNetworkTargets", err: err}
		}

		settings.network = rules
	}

	return settings, true, nil
}

func stringList(v any) ([]string, error) {
	items, ok := v.([]any)
	if !ok {
		return nil, errors.New("must be a list of strings")
	}

	out := make([]string, 0, len(items))
	for _, item := range items {
		s, ok := item.(string)
		if !ok || strings.TrimSpace(s) == "" {
			return nil, errors.New("must be a list of non-empty strings")
		}

		out = append(out, s)
	}

	return out, nil
}

func parseNetworkRules(v any) (networkRules, error) {
	if v == "auto" {
		return networkRules{auto: true}, nil
	}

	items, ok := v.([]any)
	if !ok {
		return networkRules{}, errors.New("must be auto or a list of targets")
	}

	rules := networkRules{targets: make([]target, 0, len(items))}

	for _, item := range items {
		var (
			entry   string
			methods []string
		)

		switch item := item.(type) {
		case string:
			entry = item
		case map[string]any:
			s, ok := item["target"].(string)
			if !ok {
				return networkRules{}, errors.New("an entry object needs a target string")
			}

			entry = s

			if m, ok := item["methods"]; ok {
				list, err := stringList(m)
				if err != nil {
					return networkRules{}, fmt.Errorf("methods of %q %w", entry, err)
				}

				for _, method := range list {
					method = strings.ToUpper(method)
					if !slices.Contains([]string{"GET", "HEAD", "POST", "PUT", "DELETE", "PATCH", "OPTIONS"}, method) {
						return networkRules{}, fmt.Errorf("methods of %q: unknown method %q", entry, method)
					}

					methods = append(methods, method)
				}
			}
		default:
			return networkRules{}, errors.New("entries must be strings or objects with a target")
		}

		t, err := parseTarget(entry, methods)
		if err != nil {
			return networkRules{}, err
		}

		rules.targets = append(rules.targets, t)
	}

	return rules, nil
}
