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
	"github.com/Rocketable/platform/internal/rocketclaw/oai"
	"github.com/Rocketable/platform/internal/rocketcode"
	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
)

type modelResolver struct {
	providers             map[string]config.OpenAIConfig
	workspace, runtimeDir string
	log                   *slog.Logger
}

func newModelResolver(cfg *config.Config, logger *slog.Logger) *modelResolver {
	providers := maps.Clone(cfg.Providers)
	if providers == nil {
		providers = make(map[string]config.OpenAIConfig, 1)
	}

	providers["openai"] = cfg.OpenAI

	return &modelResolver{providers: providers, workspace: cfg.Workspace, runtimeDir: cfg.RuntimeDirName(), log: logger}
}

func (r *modelResolver) Resolve(model string) (*openai.Client, rocketcode.ProviderOrigin, error) {
	rawModel := model

	model = strings.TrimSpace(model)
	if model == "" {
		return nil, rocketcode.ProviderOrigin{}, errors.New("model is required")
	}

	provider := "openai"

	apiModel := model
	if before, after, ok := strings.Cut(rawModel, "/"); ok {
		provider, apiModel = before, after
		if provider != strings.TrimSpace(provider) || apiModel != strings.TrimSpace(apiModel) {
			return nil, rocketcode.ProviderOrigin{}, fmt.Errorf("invalid model %q: provider and model components must not contain whitespace", rawModel)
		}

		if provider == "" {
			return nil, rocketcode.ProviderOrigin{}, fmt.Errorf("invalid model %q: provider is required", model)
		}

		if apiModel == "" {
			return nil, rocketcode.ProviderOrigin{}, fmt.Errorf("invalid model %q: model is required", model)
		}

		if strings.Contains(apiModel, "/") {
			return nil, rocketcode.ProviderOrigin{}, fmt.Errorf("invalid model %q: expected provider/model", model)
		}
	}

	providerConfig, ok := r.providers[provider]
	if !ok {
		return nil, rocketcode.ProviderOrigin{}, fmt.Errorf("unknown provider %q", provider)
	}

	origin := rocketcode.ProviderOrigin{Provider: provider, Model: apiModel, CompactThreshold: providerConfig.AutocompactionThreshold}

	options := r.options(origin)
	if providerConfig.RocketCodeAuth == "chatgpt" {
		client, err := oai.NewChatGPTClientIn(r.workspace, r.runtimeDir, provider, options...)
		if err != nil {
			return nil, rocketcode.ProviderOrigin{}, fmt.Errorf("create ChatGPT OAuth client for provider %q: %w", provider, err)
		}

		return client, origin, nil
	}

	options = append(options, option.WithAPIKey(providerConfig.APIKey))
	if strings.TrimSpace(providerConfig.APIBaseURL) != "" {
		options = append(options, option.WithBaseURL(providerConfig.APIBaseURL))
	}

	client := openai.NewClient(options...)

	return &client, origin, nil
}

func (r *modelResolver) options(origin rocketcode.ProviderOrigin) []option.RequestOption {
	if !r.log.Enabled(context.Background(), slog.LevelError) {
		return nil
	}

	return []option.RequestOption{option.WithMiddleware(func(req *http.Request, next option.MiddlewareNext) (*http.Response, error) {
		startedAt := time.Now()
		resp, err := next(req)

		status := 0
		if resp != nil {
			status = resp.StatusCode
		}

		errLog := err
		if status != http.StatusOK || err != nil {
			if errLog == nil {
				errLog = fmt.Errorf("provider returned status %d", status)
			}

			attrs := append(providerLogAttrs(req, resp, status, time.Since(startedAt), errLog), "provider", origin.Provider, "model", origin.Model)
			r.log.Error("provider request failed", attrs...)
		} else if time.Since(startedAt) > time.Minute {
			attrs := append(providerLogAttrs(req, resp, status, time.Since(startedAt), errLog), "provider", origin.Provider, "model", origin.Model)
			r.log.Info("provider request completed", attrs...)
		}

		return resp, err
	})}
}
