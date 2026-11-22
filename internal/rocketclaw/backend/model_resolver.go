package backend

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"strings"
	"testing/iotest"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/harness"
	"github.com/Rocketable/platform/internal/rocketclaw/oai"
	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/responses"
	"github.com/openai/openai-go/v3/shared"
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

	if providerConfig.Auth == "chatgpt" {
		options := r.options(origin, "sdk_return")

		client, err := oai.NewChatGPTClientIn(r.workspace, r.runtimeDir, provider, r.log.With("provider", provider, "model", apiModel), options...)
		if err != nil {
			return nil, harness.ProviderOrigin{}, fmt.Errorf("create ChatGPT OAuth client for provider %q: %w", provider, err)
		}

		return client, origin, nil
	}

	options := append(r.options(origin, "http_headers"), option.WithAPIKey(providerConfig.APIKey))
	if strings.TrimSpace(providerConfig.APIBaseURL) != "" {
		options = append(options, option.WithBaseURL(providerConfig.APIBaseURL), option.WithUnsafeAllowHTTP())
	}

	client := openai.NewClient(options...)

	return &client, origin, nil
}

func TestModel(ctx context.Context, cfg *config.Config, ref string) (time.Duration, error) {
	model, effort := harness.ParseModelRef(ref)

	client, origin, err := newModelResolver(cfg, slog.Default()).Resolve(model)
	if err != nil {
		return 0, err
	}

	params := responses.ResponseNewParams{
		Model: origin.Model, Input: responses.ResponseNewParamsInputUnion{OfString: openai.String("Reply with OK.")},
		MaxOutputTokens: openai.Int(16), Store: openai.Bool(false), Reasoning: shared.ReasoningParam{Effort: shared.ReasoningEffort(effort)},
	}

	startedAt := time.Now()

	if _, err := client.Responses.New(ctx, params); err != nil {
		return 0, fmt.Errorf("test model %q: %w", ref, err)
	}

	return time.Since(startedAt), nil
}

func GenerateTitle(ctx context.Context, cfg *config.Config, request, recent string) (string, error) {
	title, err := harness.GenerateTitle(ctx, newModelResolver(cfg, slog.Default()), cfg.TitleModel, request, recent)
	if err != nil {
		return "", fmt.Errorf("generate title with %q: %w", cmp.Or(cfg.TitleModel, "default title model"), err)
	}

	return title, nil
}

func newDeciders(cfg *config.Config) map[string]harness.Decider {
	deciders := make(map[string]harness.Decider, len(cfg.Decision))
	for name, provider := range cfg.Decision {
		if name == config.DecisionTypeSafe {
			deciders[string(name)] = harness.TypeSafeDecider{HTTPClient: http.DefaultClient, APIKey: provider.APIKey, BaseURL: cmp.Or(provider.APIBaseURL, "https://api.typesafe.ai/v1")}
			continue
		}

		options := []option.RequestOption{option.WithAPIKey(provider.APIKey)}
		if provider.APIBaseURL != "" {
			options = append(options, option.WithBaseURL(provider.APIBaseURL), option.WithUnsafeAllowHTTP())
		}

		client := openai.NewClient(options...)
		deciders[string(name)] = harness.OpenAIDecider{Client: &client}
	}

	return deciders
}

func (r *modelResolver) options(origin harness.ProviderOrigin, boundary string) []option.RequestOption {
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

		attrs := append(providerLogAttrs(req, resp, status, time.Since(startedAt), err, boundary), "provider", origin.Provider, "model", origin.Model)
		if resp != nil && (status < http.StatusOK || status >= http.StatusMultipleChoices) {
			body, errRead := io.ReadAll(resp.Body)
			_ = resp.Body.Close()

			resp.Body = io.NopCloser(bytes.NewReader(body))
			if errRead != nil {
				resp.Body = io.NopCloser(io.MultiReader(bytes.NewReader(body), iotest.ErrReader(errRead)))
			}

			var payload struct {
				Error struct{ Type, Code, Message string }
			}
			if json.Unmarshal(body, &payload) == nil {
				attrs = append(attrs, "provider_error_type", payload.Error.Type, "provider_error_code", payload.Error.Code, "provider_error_message", payload.Error.Message)
			}
		}

		switch {
		case errors.Is(err, context.Canceled):
			r.log.Info("provider request cancelled", attrs...)
		case status < 200 || status >= 300 || err != nil:
			r.log.Error("provider request failed", attrs...)
		default:
			r.log.Info("provider request completed", attrs...)
		}

		return resp, err
	})}
}
