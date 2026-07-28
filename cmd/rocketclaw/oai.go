package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/oai"
	"golang.org/x/sys/unix"
)

const oaiHelpText = `rocketclaw oai

Usage:
  rocketclaw oai login [provider] [--headless]
  rocketclaw oai list
  rocketclaw oai logout [provider]

Commands:
  login   Authenticate with ChatGPT for a RocketCode model provider.
  list    List configured providers and local credential presence.
  logout  Remove a provider's local ChatGPT credential.
`

func runOAI(args []string) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		return printStdout(oaiHelpText, "oai help")
	}

	switch args[0] {
	case "login":
		return runOAILogin(args[1:])
	case "list":
		return runOAIList(args[1:])
	case "logout":
		return runOAILogout(args[1:])
	default:
		return fmt.Errorf("unknown oai command %q", args[0])
	}
}

func runOAILogin(args []string) error {
	providerID := "openai"
	headless, providerSet, help := false, false, false
	for _, arg := range args {
		switch {
		case arg == "help" || arg == "-h" || arg == "--help":
			help = true
		case arg == "--headless":
			if headless {
				return errors.New("oai login accepts --headless at most once")
			}
			headless = true
		case strings.HasPrefix(arg, "-"):
			return fmt.Errorf("unknown oai login flag %q", arg)
		case providerSet:
			return fmt.Errorf("oai login accepts at most one provider; unexpected argument %q", arg)
		default:
			providerID, providerSet = arg, true
		}
	}
	if help {
		return printStdout(oaiHelpText, "oai help")
	}

	_, cfg, err := loadRuntimeConfig()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	provider, ok := cfg.Provider(providerID)
	if !ok {
		return fmt.Errorf("unknown provider %q", providerID)
	}

	login := oai.LoginBrowser
	if headless {
		login = oai.LoginDevice
	}
	token, err := login(context.Background(), os.Stdout)
	if err != nil {
		return fmt.Errorf("login with ChatGPT OAuth: %w", err)
	}

	cfg, err = commitOAILogin(providerID, token)
	if err != nil {
		return err
	}

	authPath := filepath.Join(cfg.Workspace, cfg.RuntimeDirName(), "auth.json")
	if _, err := fmt.Fprintf(os.Stdout, "Saved %s ChatGPT credential to %s\n", providerID, authPath); err != nil {
		return fmt.Errorf("write oai login result: %w", err)
	}
	if provider.RocketCodeAuth != "chatgpt" {
		return printStdout("Restart RocketClaw to use the new authentication mode.\n", "oai login restart notice")
	}

	return nil
}

func commitOAILogin(providerID string, token oai.Token) (*config.Config, error) {
	selected, err := selectRuntimeConfigFile()
	if err != nil {
		return nil, fmt.Errorf("stat config path: %w", err)
	}
	if !selected.Found {
		return nil, os.ErrNotExist
	}

	lock, err := os.OpenFile(selected.Path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open config lock: %w", err)
	}
	defer func() { _ = lock.Close() }()
	if err := lock.Chmod(0o600); err != nil {
		return nil, fmt.Errorf("set config lock permissions: %w", err)
	}
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX); err != nil {
		return nil, fmt.Errorf("lock config: %w", err)
	}
	defer func() { _ = unix.Flock(int(lock.Fd()), unix.LOCK_UN) }()

	cfg, err := config.Load(selected.Path)
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	cfg.WorkDir = selected.WorkDir
	if _, ok := cfg.Provider(providerID); !ok {
		return nil, fmt.Errorf("unknown provider %q", providerID)
	}

	original, mode, err := rewriteProviderAuth(selected.Path, providerID)
	if err != nil {
		return nil, err
	}
	if _, errReplace := oai.ReplaceTokenIn(cfg.Workspace, cfg.RuntimeDirName(), providerID, token); errReplace != nil {
		errReplace = fmt.Errorf("save %s ChatGPT credential: %w", providerID, errReplace)
		_, errRollback := writeAtomic(selected.Path, original, mode)
		if errRollback != nil {
			return nil, errors.Join(errReplace, fmt.Errorf("restore config after credential write failure: %w", errRollback))
		}
		return nil, errReplace
	}

	return cfg, nil
}

func runOAIList(args []string) error {
	if len(args) == 1 && (args[0] == "help" || args[0] == "-h" || args[0] == "--help") {
		return printStdout(oaiHelpText, "oai help")
	}
	if len(args) != 0 {
		return fmt.Errorf("oai list accepts no arguments; unexpected argument %q", args[0])
	}

	_, cfg, err := loadRuntimeConfig()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	ids := append([]string{"openai"}, slices.Sorted(maps.Keys(cfg.Providers))...)
	for _, providerID := range ids {
		provider, _ := cfg.Provider(providerID)
		present := strings.TrimSpace(provider.APIKey) != ""
		if provider.RocketCodeAuth == "chatgpt" {
			present, err = oai.HasTokenIn(cfg.Workspace, cfg.RuntimeDirName(), providerID)
			if err != nil {
				return fmt.Errorf("check %s ChatGPT credential: %w", providerID, err)
			}
		}
		label := providerID
		if providerID == "openai" {
			label += " (default)"
		}
		status := "missing"
		if present {
			status = "present"
		}
		if _, err := fmt.Fprintf(os.Stdout, "%s %s %s\n", label, provider.RocketCodeAuth, status); err != nil {
			return fmt.Errorf("write oai list result: %w", err)
		}
	}

	return nil
}

func runOAILogout(args []string) error {
	providerID := "openai"
	if len(args) == 1 && (args[0] == "help" || args[0] == "-h" || args[0] == "--help") {
		return printStdout(oaiHelpText, "oai help")
	}
	if len(args) > 1 {
		return fmt.Errorf("oai logout accepts at most one provider; unexpected argument %q", args[1])
	}
	if len(args) == 1 {
		if strings.HasPrefix(args[0], "-") {
			return fmt.Errorf("unknown oai logout flag %q", args[0])
		}
		providerID = args[0]
	}

	_, cfg, err := loadRuntimeConfig()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if _, ok := cfg.Provider(providerID); !ok {
		return fmt.Errorf("unknown provider %q", providerID)
	}
	if err := oai.RemoveTokenIn(cfg.Workspace, cfg.RuntimeDirName(), providerID); err != nil {
		return fmt.Errorf("remove %s ChatGPT credential: %w", providerID, err)
	}
	if _, err := fmt.Fprintf(os.Stdout, "Removed %s local ChatGPT credential.\n", providerID); err != nil {
		return fmt.Errorf("write oai logout result: %w", err)
	}

	return nil
}

func rewriteProviderAuth(path, providerID string) ([]byte, os.FileMode, error) {
	original, err := os.ReadFile(path)
	if err != nil {
		return nil, 0, fmt.Errorf("read config for login: %w", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, 0, fmt.Errorf("stat config for login: %w", err)
	}

	var root map[string]json.RawMessage
	if err := json.Unmarshal(original, &root); err != nil {
		return nil, 0, fmt.Errorf("parse config for login: %w", err)
	}
	providerRaw := root["openai"]
	var providers map[string]json.RawMessage
	if providerID != "openai" {
		if err := json.Unmarshal(root["providers"], &providers); err != nil {
			return nil, 0, fmt.Errorf("parse providers for login: %w", err)
		}
		providerRaw = providers[providerID]
	}
	var provider map[string]json.RawMessage
	if err := json.Unmarshal(providerRaw, &provider); err != nil {
		return nil, 0, fmt.Errorf("parse provider %q for login: %w", providerID, err)
	}
	provider["rocketcode_auth"] = json.RawMessage(`"chatgpt"`)
	providerRaw, err = json.Marshal(provider)
	if err != nil {
		return nil, 0, fmt.Errorf("marshal provider %q for login: %w", providerID, err)
	}
	if providerID == "openai" {
		root["openai"] = providerRaw
	} else {
		providers[providerID] = providerRaw
		root["providers"], err = json.Marshal(providers)
		if err != nil {
			return nil, 0, fmt.Errorf("marshal providers for login: %w", err)
		}
	}
	updated, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return nil, 0, fmt.Errorf("marshal config for login: %w", err)
	}
	updated = append(updated, '\n')
	mode := info.Mode() & (os.ModePerm | os.ModeSetuid | os.ModeSetgid | os.ModeSticky)
	committed, err := writeAtomic(path, updated, mode)
	if err == nil {
		return original, mode, nil
	}
	errRewrite := fmt.Errorf("rewrite config for login: %w", err)
	if !committed {
		return nil, 0, errRewrite
	}
	if _, errRestore := writeAtomic(path, original, mode); errRestore != nil {
		return nil, 0, errors.Join(errRewrite, fmt.Errorf("restore config after rewrite failure: %w", errRestore))
	}
	return nil, 0, errRewrite
}

func writeAtomic(path string, data []byte, mode os.FileMode) (bool, error) {
	dir := filepath.Dir(path)
	temp, err := os.CreateTemp(dir, ".rocketclaw-config.tmp-*")
	if err != nil {
		return false, fmt.Errorf("create temporary config: %w", err)
	}
	tempPath := temp.Name()
	defer func() { _ = temp.Close() }()
	defer func() { _ = os.Remove(tempPath) }()

	if err := temp.Chmod(mode); err != nil {
		return false, fmt.Errorf("set temporary config permissions: %w", err)
	}
	if _, err := temp.Write(data); err != nil {
		return false, fmt.Errorf("write temporary config: %w", err)
	}
	if err := temp.Chmod(mode); err != nil {
		return false, fmt.Errorf("restore temporary config permissions: %w", err)
	}
	errSync, errClose := temp.Sync(), temp.Close()
	if err := errors.Join(errSync, errClose); err != nil {
		return false, fmt.Errorf("sync and close temporary config: %w", err)
	}
	if err := os.Rename(tempPath, path); err != nil {
		return false, fmt.Errorf("replace config: %w", err)
	}
	directory, err := os.Open(dir)
	if err != nil {
		return true, fmt.Errorf("open config directory: %w", err)
	}
	errSync, errClose = directory.Sync(), directory.Close()
	if err := errors.Join(errSync, errClose); err != nil {
		return true, fmt.Errorf("sync and close config directory: %w", err)
	}

	return true, nil
}
