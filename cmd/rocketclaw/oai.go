package main

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/Rocketable/platform/internal/rocketclaw/oai"
	"golang.org/x/sys/unix"
)

const oaiHelpText = `rocketclaw oai

Usage:
  rocketclaw oai login [provider] [--headless]
  rocketclaw oai list
  rocketclaw oai logout [provider]

Commands:
  login   Authenticate a provider with ChatGPT for RocketCode model requests.
  list    List provider auth modes and local credential presence.
  logout  Remove one provider's local ChatGPT OAuth token.
`

func runOAI(args []string) error {
	if len(args) == 0 {
		return printStdout(oaiHelpText, "oai help")
	}
	switch args[0] {
	case "login":
		return runOAILogin(args[1:])
	case "list":
		return runOAIList(args[1:])
	case "logout":
		return runOAILogout(args[1:])
	case "help", "-h", "--help":
		return printStdout(oaiHelpText, "oai help")
	default:
		return fmt.Errorf("unknown oai command %q", args[0])
	}
}

func runOAILogin(args []string) error {
	provider, headless, secretsARN, help, err := parseOAILogin(args)
	if err != nil {
		return err
	}
	if help {
		return printStdout(oaiHelpText, "oai help")
	}
	selected, cfg, err := loadRuntimeConfig(secretsARN)
	if err != nil {
		return fmt.Errorf("load runtime config: %w", err)
	}
	if _, ok := cfg.Provider(provider); !ok {
		return fmt.Errorf("unknown provider %q", provider)
	}

	acquire := oai.AcquireBrowserToken
	if headless {
		acquire = oai.AcquireDeviceToken
	}
	token, err := acquire(context.Background(), os.Stdout)
	if err != nil {
		return fmt.Errorf("login with ChatGPT OAuth: %w", err)
	}
	previous, path, err := saveOAILogin(selected.Path, cfg.Workspace, cfg.RuntimeDirName(), provider, token)
	if err != nil {
		return err
	}
	result := fmt.Sprintf("Saved %s ChatGPT OAuth token to %s\n", provider, path)
	if previous != "chatgpt" {
		result += fmt.Sprintf("Restart RocketClaw to use ChatGPT OAuth for %s.\n", provider)
	}
	return printStdout(result, "oai login result")
}

func parseOAILogin(args []string) (string, bool, string, bool, error) {
	provider, headless, secretsARN := "", false, ""
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--headless" && headless:
			return "", false, "", false, errors.New("duplicate --headless")
		case arg == "--headless":
			headless = true
		case arg == "-h" || arg == "--help":
			return cmp.Or(provider, "openai"), headless, secretsARN, true, nil
		case arg == "--aws-secrets-manager-arn" && secretsARN != "":
			return "", false, "", false, errors.New("duplicate --aws-secrets-manager-arn")
		case arg == "--aws-secrets-manager-arn":
			if i+1 >= len(args) {
				return "", false, "", false, errors.New("flag needs an argument: -aws-secrets-manager-arn")
			}
			i++
			secretsARN = args[i]
		case strings.HasPrefix(arg, "--aws-secrets-manager-arn="):
			if secretsARN != "" {
				return "", false, "", false, errors.New("duplicate --aws-secrets-manager-arn")
			}
			secretsARN = strings.TrimPrefix(arg, "--aws-secrets-manager-arn=")
		case strings.HasPrefix(arg, "-"):
			return "", false, "", false, fmt.Errorf("unknown flag %q", arg)
		case provider != "":
			return "", false, "", false, errors.New("oai login accepts no more than one provider")
		default:
			provider = arg
		}
	}
	return cmp.Or(provider, "openai"), headless, secretsARN, false, nil
}

func runOAIList(args []string) error {
	flagSet := flag.NewFlagSet("oai list", flag.ContinueOnError)
	flagSet.SetOutput(io.Discard)
	secretsARN := flagSet.String(secretsARNFlag, "", secretsARNUsage)
	if err := flagSet.Parse(args); err != nil {
		return fmt.Errorf("parse oai list flags: %w", err)
	}
	if len(flagSet.Args()) != 0 {
		return errors.New("oai list takes no arguments")
	}
	_, cfg, err := loadRuntimeConfig(*secretsARN)
	if err != nil {
		return fmt.Errorf("load runtime config: %w", err)
	}
	providers := maps.Collect(maps.All(cfg.Providers))
	providers["openai"] = cfg.OpenAI
	var output strings.Builder
	for _, name := range slices.Sorted(maps.Keys(providers)) {
		present, err := oai.HasTokenIn(cfg.Workspace, cfg.RuntimeDirName(), name)
		if err != nil {
			return err
		}
		marker := map[string]string{"openai": " (default)"}[name]
		credentials := map[bool]string{false: "missing", true: "present"}[present]
		fmt.Fprintf(&output, "%s%s\t%s\t%s\n", name, marker, providers[name].RocketCodeAuth, credentials)
	}
	return printStdout(output.String(), "oai list")
}

func runOAILogout(args []string) error {
	flagSet := flag.NewFlagSet("oai logout", flag.ContinueOnError)
	flagSet.SetOutput(io.Discard)
	secretsARN := flagSet.String(secretsARNFlag, "", secretsARNUsage)
	if err := flagSet.Parse(args); err != nil {
		return fmt.Errorf("parse oai logout flags: %w", err)
	}
	rest := flagSet.Args()
	if len(rest) > 1 {
		return errors.New("oai logout accepts no more than one provider")
	}
	provider := cmp.Or(strings.Join(rest, ""), "openai")
	_, cfg, err := loadRuntimeConfig(*secretsARN)
	if err != nil {
		return fmt.Errorf("load runtime config: %w", err)
	}
	if _, ok := cfg.Provider(provider); !ok {
		return fmt.Errorf("unknown provider %q", provider)
	}
	if err := oai.RemoveTokenIn(cfg.Workspace, cfg.RuntimeDirName(), provider); err != nil {
		return err
	}
	return printStdout(fmt.Sprintf("Removed local ChatGPT OAuth token for %s; no remote token was revoked.\n", provider), "oai logout result")
}

func saveOAILogin(configPath, workspace, runtimeDir, provider string, token oai.Token) (string, string, error) {
	lock, err := os.OpenFile(configPath+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return "", "", fmt.Errorf("open config lock: %w", err)
	}
	defer func() { _ = lock.Close() }()
	if err := lock.Chmod(0o600); err != nil {
		return "", "", fmt.Errorf("chmod config lock: %w", err)
	}
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX); err != nil {
		return "", "", fmt.Errorf("lock config: %w", err)
	}

	original, err := os.ReadFile(configPath)
	if err != nil {
		return "", "", fmt.Errorf("read config: %w", err)
	}
	info, err := os.Stat(configPath)
	if err != nil {
		return "", "", fmt.Errorf("stat config: %w", err)
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(original, &root); err != nil {
		return "", "", fmt.Errorf("parse config JSON: %w", err)
	}
	container, key := root, "openai"
	if provider != "openai" {
		container, key = nil, provider
		if err := json.Unmarshal(root["providers"], &container); err != nil || container == nil {
			return "", "", fmt.Errorf("parse providers object for provider %q", provider)
		}
	}
	section, ok := container[key]
	if !ok {
		return "", "", fmt.Errorf("provider %q is no longer configured", provider)
	}
	var target map[string]json.RawMessage
	if err := json.Unmarshal(section, &target); err != nil || target == nil {
		return "", "", fmt.Errorf("parse provider %q object", provider)
	}
	var previous string
	_ = json.Unmarshal(target["rocketcode_auth"], &previous)
	target["rocketcode_auth"] = json.RawMessage(`"chatgpt"`)
	section, err = json.Marshal(target)
	container[key] = section
	if provider != "openai" {
		root["providers"], err = json.Marshal(container)
	}
	if err != nil {
		return "", "", fmt.Errorf("marshal provider %q: %w", provider, err)
	}
	updated, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return "", "", fmt.Errorf("marshal config: %w", err)
	}
	committed, errConfig := replaceConfig(configPath, append(updated, '\n'), info.Mode())
	if errConfig != nil {
		errConfig = fmt.Errorf("commit config: %w", errConfig)
	}
	if !committed {
		return "", "", errConfig
	}
	errToken := oai.SaveTokenIn(workspace, runtimeDir, provider, token)
	if errToken != nil {
		if stored, _ := oai.LoadTokenIn(workspace, runtimeDir, provider); stored != token {
			_, errRestore := replaceConfig(configPath, original, info.Mode())
			if errRestore != nil {
				errRestore = fmt.Errorf("restore config: %w", errRestore)
			}
			return "", "", errors.Join(errConfig, errToken, errRestore)
		}
	}
	return previous, filepath.Join(workspace, runtimeDir, "auth.json"), errors.Join(errConfig, errToken)
}

func replaceConfig(path string, data []byte, mode os.FileMode) (bool, error) {
	temp, err := os.CreateTemp(filepath.Dir(path), ".rocketclaw-config-*")
	if err != nil {
		return false, err
	}
	tempPath := temp.Name()
	defer func() { _ = os.Remove(tempPath) }()
	_, err = temp.Write(data)
	err = errors.Join(err, temp.Chmod(mode), temp.Sync(), temp.Close())
	if err != nil {
		return false, err
	}
	if err := os.Rename(tempPath, path); err != nil {
		return false, err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return true, err
	}
	defer func() { _ = dir.Close() }()
	return true, dir.Sync()
}
