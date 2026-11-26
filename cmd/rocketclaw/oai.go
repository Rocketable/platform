package main

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
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
  rocketclaw oai login [provider] [--headless] [--aws-secrets-manager-arn ARN]
  rocketclaw oai list [--aws-secrets-manager-arn ARN]
  rocketclaw oai logout [provider] [--aws-secrets-manager-arn ARN]

Commands:
  login   Authenticate a provider with ChatGPT for model requests, and set its
          auth to chatgpt in the config file unless the secret already does.
  list    List provider auth modes and local credential presence.
  logout  Remove one provider's local ChatGPT OAuth token.

Options:
  --aws-secrets-manager-arn
          Merge this JSON secret over rocketclaw.json, as rocketclaw run does.
          login refuses a provider whose auth the secret sets to anything but chatgpt.
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
	flagSet := flag.NewFlagSet("oai login", flag.ContinueOnError)
	flagSet.SetOutput(io.Discard)
	headless := flagSet.Bool("headless", false, "")
	secretsARN := flagSet.String(secretsARNFlag, "", secretsARNUsage)
	err := flagSet.Parse(args)
	provider := cmp.Or(flagSet.Arg(0), "openai")
	if err == nil && flagSet.NArg() > 0 {
		err = flagSet.Parse(flagSet.Args()[1:])
	}
	switch {
	case errors.Is(err, flag.ErrHelp):
		return printStdout(oaiHelpText, "oai help")
	case err != nil:
		return fmt.Errorf("parse oai login flags: %w", err)
	case flagSet.NArg() > 0:
		return errors.New("oai login accepts no more than one provider")
	}
	selected, cfg, err := loadRuntimeConfig(*secretsARN, "")
	if err != nil {
		return fmt.Errorf("load runtime config: %w", err)
	}
	current, ok := cfg.Provider(provider)
	if !ok {
		return fmt.Errorf("unknown provider %q", provider)
	}
	var secret config.Config
	if *secretsARN != "" {
		body, err := secretFetcher.SecretString(context.Background(), *secretsARN)
		if err != nil {
			return fmt.Errorf("load secret: %w", err)
		}
		_ = json.Unmarshal([]byte(body), &secret)
	}
	fromSecret, createSection := secret.Provider(provider)
	if fromSecret.Auth != "" && fromSecret.AuthMode() != "chatgpt" {
		return fmt.Errorf(`the AWS secret sets provider %q auth to %q, which overrides the config file; set it to "chatgpt" in the secret`, provider, fromSecret.AuthMode())
	}

	acquire := oai.AcquireBrowserToken
	if *headless {
		acquire = oai.AcquireDeviceToken
	}
	token, err := acquire(context.Background(), os.Stdout)
	if err != nil {
		return fmt.Errorf("login with ChatGPT OAuth: %w", err)
	}
	if fromSecret.Auth != "" {
		err = oai.SaveTokenIn(cfg.Workspace, cfg.RuntimeDirName(), provider, token)
	} else {
		err = saveOAILogin(selected.Path, cfg.Workspace, cfg.RuntimeDirName(), provider, createSection, token)
	}
	if err != nil {
		return err
	}
	result := fmt.Sprintf("Saved %s ChatGPT OAuth token to %s\n", provider, filepath.Join(cfg.Workspace, cfg.RuntimeDirName(), "auth.json"))
	if current.AuthMode() != "chatgpt" {
		result += fmt.Sprintf("Restart RocketClaw to use ChatGPT OAuth for %s.\n", provider)
	}
	return printStdout(result, "oai login result")
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
	_, cfg, err := loadRuntimeConfig(*secretsARN, "")
	if err != nil {
		return fmt.Errorf("load runtime config: %w", err)
	}
	names := cfg.ProviderNames()
	slices.Sort(names)
	var output strings.Builder
	for _, name := range names {
		present, err := oai.HasTokenIn(cfg.Workspace, cfg.RuntimeDirName(), name)
		if err != nil {
			return err
		}
		marker := map[string]string{"openai": " (default)"}[name]
		credentials := map[bool]string{false: "missing", true: "present"}[present]
		provider, _ := cfg.Provider(name)
		fmt.Fprintf(&output, "%s%s\t%s\t%s\n", name, marker, provider.AuthMode(), credentials)
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
	_, cfg, err := loadRuntimeConfig(*secretsARN, "")
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

func saveOAILogin(configPath, workspace, runtimeDir, provider string, createSection bool, token oai.Token) error {
	lock, err := os.OpenFile(configPath+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("open config lock: %w", err)
	}
	defer func() { _ = lock.Close() }()
	if err := lock.Chmod(0o600); err != nil {
		return fmt.Errorf("chmod config lock: %w", err)
	}
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX); err != nil {
		return fmt.Errorf("lock config: %w", err)
	}

	mode, original := os.FileMode(0o600), []byte(nil)
	info, err := os.Stat(configPath)
	if err == nil {
		mode = info.Mode()
		original, err = os.ReadFile(configPath)
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read config: %w", err)
	}
	root := map[string]json.RawMessage{}
	if original != nil {
		if err := json.Unmarshal(original, &root); err != nil || root == nil {
			return fmt.Errorf("parse config JSON: %w", cmp.Or(err, errors.New("must be an object")))
		}
	}
	container, key := root, "openai"
	if provider != "openai" {
		container, key = map[string]json.RawMessage{}, provider
		if raw, ok := root["providers"]; ok {
			if err := json.Unmarshal(raw, &container); err != nil || container == nil {
				return fmt.Errorf("parse providers object for provider %q", provider)
			}
		}
	}
	target := map[string]json.RawMessage{}
	if section, ok := container[key]; ok {
		if err := json.Unmarshal(section, &target); err != nil || target == nil {
			return fmt.Errorf("parse provider %q object", provider)
		}
	} else if !createSection {
		return fmt.Errorf("provider %q is no longer configured", provider)
	}
	target["auth"] = json.RawMessage(`"chatgpt"`)
	container[key], _ = json.Marshal(target)
	if provider != "openai" {
		root["providers"], _ = json.Marshal(container)
	}
	updated, _ := json.MarshalIndent(root, "", "  ")
	committed, errConfig := replaceConfig(configPath, append(updated, '\n'), mode)
	if errConfig != nil {
		errConfig = fmt.Errorf("commit config: %w", errConfig)
	}
	if !committed {
		return errConfig
	}
	errToken := oai.SaveTokenIn(workspace, runtimeDir, provider, token)
	if errToken != nil {
		if stored, _ := oai.LoadTokenIn(workspace, runtimeDir, provider); stored != token {
			var errRestore error
			if original == nil {
				errRestore = os.Remove(configPath)
			} else {
				_, errRestore = replaceConfig(configPath, original, mode)
			}
			if errRestore != nil {
				errRestore = fmt.Errorf("restore config: %w", errRestore)
			}
			return errors.Join(errConfig, errToken, errRestore)
		}
	}
	return errors.Join(errConfig, errToken)
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
