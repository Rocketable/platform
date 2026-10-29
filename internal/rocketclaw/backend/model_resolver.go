package backend

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"strings"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/harness"
	"github.com/Rocketable/platform/internal/rocketclaw/oai"
	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
)

type modelResolver struct {
	providers             map[string]config.OpenAIConfig
	workspace, runtimeDir string
	log                   *slog.Logger
}

func newModelResolver(cfg *config.Config, logger *slog.Logger) *modelResolver {
	providers := make(map[string]config.OpenAIConfig, len(cfg.Providers)+1)
	maps.Copy(providers, cfg.Providers)

	providers["openai"] = cfg.OpenAI

	return &modelResolver{providers: providers, workspace: cfg.Workspace, runtimeDir: cfg.RuntimeDirName(), log: logger}
}

func (r *modelResolver) Resolve(model string) (*openai.Client, harness.ProviderOrigin, error) {
	rawModel := model

	model = strings.TrimSpace(model)
	if model == "" {
		return nil, harness.ProviderOrigin{}, errors.New("model is required")
	}

	provider := "openai"

	apiModel := model
	if before, after, ok := strings.Cut(rawModel, "/"); ok {
		provider, apiModel = before, after
		if provider != strings.TrimSpace(provider) || apiModel != strings.TrimSpace(apiModel) {
			return nil, harness.ProviderOrigin{}, fmt.Errorf("invalid model %q: provider and model components must not contain whitespace", rawModel)
		}

		if provider == "" {
			return nil, harness.ProviderOrigin{}, fmt.Errorf("invalid model %q: provider is required", model)
		}

		if apiModel == "" {
			return nil, harness.ProviderOrigin{}, fmt.Errorf("invalid model %q: model is required", model)
		}

		if strings.Contains(model, "//") || strings.HasSuffix(apiModel, "/") {
			return nil, harness.ProviderOrigin{}, fmt.Errorf("invalid model %q: expected provider/model", model)
		}
	}

	providerConfig, ok := r.providers[provider]
	if !ok {
		return nil, harness.ProviderOrigin{}, fmt.Errorf("unknown provider %q", provider)
	}

	origin := harness.ProviderOrigin{Provider: provider, Model: apiModel, CompactThreshold: providerConfig.AutocompactionThreshold, PromptCacheMarkersRelativePosition: providerConfig.PromptCacheMarkersRelativePosition}

	options := r.options(origin)
	if providerConfig.Auth == "chatgpt" {
		client, err := oai.NewChatGPTClientIn(r.workspace, r.runtimeDir, provider, r.log.With("provider", provider, "model", apiModel), options...)
		if err != nil {
			return nil, harness.ProviderOrigin{}, fmt.Errorf("create ChatGPT OAuth client for provider %q: %w", provider, err)
		}

		return client, origin, nil
	}

	options = append(options, option.WithAPIKey(providerConfig.APIKey))
	if strings.TrimSpace(providerConfig.APIBaseURL) != "" {
		// WithUnsafeAllowHTTP keeps plain-HTTP base URLs working, local or remote.
		options = append(options, option.WithBaseURL(providerConfig.APIBaseURL), option.WithUnsafeAllowHTTP())
	}

	client := openai.NewClient(options...)

	return &client, origin, nil
}

func (r *modelResolver) options(origin harness.ProviderOrigin) []option.RequestOption {
	if !r.log.Enabled(context.Background(), slog.LevelInfo) && !r.log.Enabled(context.Background(), slog.LevelError) {
		return nil
	}

	return []option.RequestOption{option.WithMiddleware(func(req *http.Request, next option.MiddlewareNext) (*http.Response, error) {
		startedAt := time.Now()
		resp, err := next(req)

		status := 0
		if resp != nil {
			status = resp.StatusCode
		}

		boundary := "http_headers"
		if r.providers[origin.Provider].Auth == "chatgpt" {
			// Codex adapts streams and performs physical retries below SDK middleware.
			boundary = "sdk_return"
		}

		attrs := append(providerLogAttrs(req, resp, status, time.Since(startedAt), err, boundary), "provider", origin.Provider, "model", origin.Model)
		if status < 200 || status >= 300 || err != nil {
			r.log.Error("provider request failed", attrs...)
		} else {
			r.log.Info("provider request completed", attrs...)
		}

		return resp, err
	})}
}
