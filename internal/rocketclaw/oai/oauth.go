// Package oai provides ChatGPT OAuth-backed OpenAI clients for rocketclaw.
//
//nolint:exhaustruct // HTTP and decoded JSON structs intentionally use sparse literals.
package oai

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
)

const (
	clientID         = "app_EMoamEEZ73f0CkXaXp7hrann"
	issuer           = "https://auth.openai.com"
	codexBaseURL     = "https://chatgpt.com/backend-api/codex"
	dummyAPIKey      = "rocketclaw-oauth-dummy-key"
	defaultLoginPort = 1455
	originator       = "rocketable"
)

// Token is the persisted ChatGPT OAuth credential used for Codex requests.
type Token struct {
	Refresh   string `json:"refresh"`
	Access    string `json:"access"`
	Expires   int64  `json:"expires"`
	AccountID string `json:"account_id,omitempty"`
}

type pkceCodes struct {
	verifier  string
	challenge string
}

type tokenResponse struct {
	IDToken      string `json:"id_token"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
}

type claims struct {
	ChatGPTAccountID string `json:"chatgpt_account_id"`
	Organizations    []struct {
		ID string `json:"id"`
	} `json:"organizations"`
	OpenAIAuth struct {
		ChatGPTAccountID string `json:"chatgpt_account_id"`
	} `json:"https://api.openai.com/auth"`
}

// AuthFilePath returns the workspace-local token file path.
func AuthFilePath(workspace string) (string, error) {
	return AuthFilePathIn(workspace, config.DefaultWorkDir)
}

// AuthFilePathIn returns the workspace-local token file path in workDir.
func AuthFilePathIn(workspace, workDir string) (string, error) {
	if strings.TrimSpace(workspace) == "" {
		wd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("resolve current directory: %w", err)
		}

		workspace = wd
	}

	workspace, err := filepath.Abs(workspace)
	if err != nil {
		return "", fmt.Errorf("resolve workspace path: %w", err)
	}

	return filepath.Join(workspace, workDir, "auth.json"), nil
}

// LoadToken reads the persisted ChatGPT OAuth token.
func LoadToken(workspace string) (Token, error) {
	return LoadTokenIn(workspace, config.DefaultWorkDir)
}

// LoadTokenIn reads the persisted ChatGPT OAuth token from workDir.
func LoadTokenIn(workspace, workDir string) (Token, error) {
	path, err := AuthFilePathIn(workspace, workDir)
	if err != nil {
		return Token{}, err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return Token{}, fmt.Errorf("read OpenAI OAuth token: %w", err)
	}

	var token Token
	if err := json.Unmarshal(data, &token); err != nil {
		return Token{}, fmt.Errorf("parse OpenAI OAuth token: %w", err)
	}

	if strings.TrimSpace(token.Refresh) == "" {
		return Token{}, errors.New("OpenAI OAuth token is missing refresh token")
	}

	return token, nil
}

// SaveToken writes the ChatGPT OAuth token with owner-only permissions.
func SaveToken(workspace string, token Token) error {
	return SaveTokenIn(workspace, config.DefaultWorkDir, token)
}

// SaveTokenIn writes the ChatGPT OAuth token to workDir with owner-only permissions.
func SaveTokenIn(workspace, workDir string, token Token) error {
	path, err := AuthFilePathIn(workspace, workDir)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create OpenAI OAuth token dir: %w", err)
	}

	data, err := json.MarshalIndent(token, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal OpenAI OAuth token: %w", err)
	}

	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("write OpenAI OAuth token: %w", err)
	}

	return nil
}

// LoginBrowser completes the local browser OAuth flow and saves the resulting token.
func LoginBrowser(ctx context.Context, workspace string, out io.Writer) (string, error) {
	return LoginBrowserIn(ctx, workspace, config.DefaultWorkDir, out)
}

// LoginBrowserIn completes the local browser OAuth flow and saves the resulting token in workDir.
func LoginBrowserIn(ctx context.Context, workspace, workDir string, out io.Writer) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	pkce, err := generatePKCE()
	if err != nil {
		return "", err
	}

	state, err := randomString(32)
	if err != nil {
		return "", err
	}

	codeCh := make(chan string, 1)
	errCh := make(chan error, 1)
	server := &http.Server{Addr: fmt.Sprintf("127.0.0.1:%d", defaultLoginPort)}
	redirectURI := fmt.Sprintf("http://localhost:%d/auth/callback", defaultLoginPort)
	server.Handler = http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/auth/callback" {
			http.NotFound(w, req)
			return
		}

		if value := req.URL.Query().Get("error"); value != "" {
			description := req.URL.Query().Get("error_description")
			if description == "" {
				description = value
			}

			errCh <- errors.New(description)

			_, _ = io.WriteString(w, "Authorization failed. You can close this window.")

			return
		}

		if req.URL.Query().Get("state") != state {
			errCh <- errors.New("invalid OAuth state")

			_, _ = io.WriteString(w, "Invalid authorization state. You can close this window.")

			return
		}

		code := req.URL.Query().Get("code")
		if code == "" {
			errCh <- errors.New("missing OAuth authorization code")

			_, _ = io.WriteString(w, "Missing authorization code. You can close this window.")

			return
		}

		codeCh <- code

		_, _ = io.WriteString(w, "Authorization successful. You can close this window.")
	})

	go func() {
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		_ = server.Shutdown(shutdownCtx)
	}()

	authURL := authorizeURL(redirectURI, pkce, state)
	if out != nil {
		_, _ = fmt.Fprintf(out, "Open this URL to authorize rocketclaw with ChatGPT:\n%s\n", authURL)
	}

	var code string
	select {
	case <-ctx.Done():
		return "", fmt.Errorf("wait for OAuth callback: %w", ctx.Err())
	case err := <-errCh:
		return "", fmt.Errorf("OAuth callback: %w", err)
	case code = <-codeCh:
	}

	response, err := exchangeCode(ctx, code, redirectURI, pkce.verifier)
	if err != nil {
		return "", err
	}

	return saveTokenResponseIn(workspace, workDir, response)
}

// LoginDevice completes the headless device OAuth flow and saves the resulting token.
func LoginDevice(ctx context.Context, workspace string, out io.Writer) (string, error) {
	return LoginDeviceIn(ctx, workspace, config.DefaultWorkDir, out)
}

// LoginDeviceIn completes the headless device OAuth flow and saves the resulting token in workDir.
func LoginDeviceIn(ctx context.Context, workspace, workDir string, out io.Writer) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	body, err := json.Marshal(map[string]string{"client_id": clientID})
	if err != nil {
		return "", fmt.Errorf("marshal device authorization request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, issuer+"/api/accounts/deviceauth/usercode", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("create device authorization request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "rocketclaw")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("send device authorization request: %w", err)
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("device authorization failed (%d): %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}

	var device struct {
		DeviceAuthID string `json:"device_auth_id"`
		UserCode     string `json:"user_code"`
		Interval     string `json:"interval"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&device); err != nil {
		return "", fmt.Errorf("decode device authorization response: %w", err)
	}

	if out != nil {
		_, _ = fmt.Fprintf(out, "Open %s/codex/device and enter code: %s\n", issuer, device.UserCode)
	}

	interval, err := time.ParseDuration(device.Interval + "s")
	if err != nil || interval <= 0 {
		interval = 5 * time.Second
	}

	for {
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("wait for device authorization: %w", ctx.Err())
		case <-time.After(interval + 3*time.Second):
		}

		path, done, err := pollDeviceIn(ctx, workspace, workDir, device.DeviceAuthID, device.UserCode)
		if err != nil {
			return "", err
		}

		if done {
			return path, nil
		}
	}
}

// NewChatGPTClient creates an OpenAI client that sends Responses API requests to ChatGPT Codex.
func NewChatGPTClient(workspace string, opts ...option.RequestOption) (*openai.Client, error) {
	return NewChatGPTClientIn(workspace, config.DefaultWorkDir, opts...)
}

// NewChatGPTClientIn creates an OpenAI client that sends Responses API requests to ChatGPT Codex using workDir auth.
func NewChatGPTClientIn(workspace, workDir string, opts ...option.RequestOption) (*openai.Client, error) {
	if _, err := LoadTokenIn(workspace, workDir); err != nil {
		return nil, err
	}

	client := openai.NewClient(append([]option.RequestOption{
		option.WithAPIKey(dummyAPIKey),
		option.WithBaseURL(codexBaseURL),
		option.WithHTTPClient(&http.Client{Transport: &transport{base: http.DefaultTransport, workspace: workspace, workDir: workDir}}),
		option.WithHeader("originator", originator),
	}, opts...)...)

	return &client, nil
}

type transport struct {
	base      http.RoundTripper
	workspace string
	workDir   string
	mu        sync.Mutex
}

type codexRequestMetadata struct {
	compactThreshold float64
	hasCompact       bool
}

func (t *transport) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.base == nil {
		t.base = http.DefaultTransport
	}

	token, err := t.token(req.Context())
	if err != nil {
		return nil, err
	}

	cloned := req.Clone(req.Context())
	setAuthHeaders(cloned, token)

	codexPath := strings.TrimRight(cloned.URL.Path, "/")

	codexResponse := strings.HasSuffix(codexPath, "/responses")

	codexCompact := strings.HasSuffix(codexPath, "/responses/compact")

	var metadata codexRequestMetadata

	if codexResponse || codexCompact {
		var err error

		metadata, err = cleanCodexRequest(cloned, codexResponse)
		if err != nil {
			return nil, err
		}
	}

	activeReq := cloned

	resp, err := t.base.RoundTrip(cloned)
	if err != nil {
		return nil, fmt.Errorf("send OpenAI request: %w", err)
	}

	if resp.StatusCode == http.StatusUnauthorized {
		var retryBody io.ReadCloser

		if cloned.Body != nil {
			if cloned.GetBody == nil {
				return resp, nil
			}

			retryBody, err = cloned.GetBody()
			if err != nil {
				return resp, nil
			}
		}

		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()

		token, err = t.recoveryToken(req.Context(), token)
		if err != nil {
			if retryBody != nil {
				_ = retryBody.Close()
			}

			return nil, err
		}

		retry := cloned.Clone(req.Context())
		retry.Body = retryBody
		setAuthHeaders(retry, token)

		resp, err = t.base.RoundTrip(retry)
		if err != nil {
			return nil, fmt.Errorf("send OpenAI request: %w", err)
		}

		activeReq = retry

		if resp.StatusCode == http.StatusUnauthorized {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()

			return nil, errors.New("ChatGPT OAuth authorization failed after Codex 401 recovery; run `rocketclaw oai login`")
		}
	}

	if codexResponse && resp.StatusCode == http.StatusOK {
		resp, err = codexStreamingResponse(resp)
		if err != nil {
			return nil, fmt.Errorf("adapt Codex stream response: %w", err)
		}

		resp, err = t.codexCompaction(req.Context(), activeReq, resp, &metadata)
		if err != nil {
			return nil, fmt.Errorf("compact Codex response: %w", err)
		}
	} else if codexCompact && resp.StatusCode == http.StatusOK && resp.Header.Get("Content-Type") == "" {
		resp.Header.Set("Content-Type", "application/json")
	}

	return resp, nil
}

func setAuthHeaders(req *http.Request, token Token) {
	req.Header.Del("Authorization")
	req.Header.Set("Authorization", "Bearer "+token.Access)

	if token.AccountID != "" {
		req.Header.Set("Chatgpt-Account-Id", token.AccountID)
	}
}

func (t *transport) token(ctx context.Context) (Token, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	token, err := LoadTokenIn(t.workspace, t.workDir)
	if err != nil {
		return Token{}, err
	}

	if token.Access != "" && token.Expires > time.Now().Add(30*time.Second).UnixMilli() {
		return token, nil
	}

	response, err := refreshToken(ctx, token.Refresh)
	if err != nil {
		return Token{}, err
	}

	next := tokenFromRefreshResponse(response, token)

	if err := SaveTokenIn(t.workspace, t.workDir, next); err != nil {
		return Token{}, err
	}

	return next, nil
}

func (t *transport) recoveryToken(ctx context.Context, failed Token) (Token, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	token, err := LoadTokenIn(t.workspace, t.workDir)
	if err != nil {
		return Token{}, err
	}

	if token.Access != "" && token.Access != failed.Access && token.AccountID == failed.AccountID {
		return token, nil
	}

	response, err := refreshToken(ctx, token.Refresh)
	if err != nil {
		return Token{}, fmt.Errorf("refresh ChatGPT OAuth token after Codex 401; run `rocketclaw oai login`: %w", err)
	}

	next := tokenFromRefreshResponse(response, token)
	if err := SaveTokenIn(t.workspace, t.workDir, next); err != nil {
		return Token{}, err
	}

	return next, nil
}

func cleanCodexRequest(req *http.Request, streaming bool) (codexRequestMetadata, error) {
	if req.Method != http.MethodPost || req.Body == nil {
		return codexRequestMetadata{}, nil
	}

	contentType := req.Header.Get("Content-Type")
	if contentType != "" && !strings.Contains(contentType, "application/json") {
		return codexRequestMetadata{}, nil
	}

	data, err := io.ReadAll(req.Body)
	if err != nil {
		return codexRequestMetadata{}, fmt.Errorf("read OpenAI request body: %w", err)
	}

	_ = req.Body.Close()

	var body map[string]any
	if err := json.Unmarshal(data, &body); err != nil {
		req.Body = io.NopCloser(bytes.NewReader(data))
		return codexRequestMetadata{}, nil
	}

	var metadata codexRequestMetadata

	if contextManagement, ok := body["context_management"].([]any); ok {
		for _, item := range contextManagement {
			object, ok := item.(map[string]any)
			if !ok {
				continue
			}

			threshold, ok := object["compact_threshold"].(float64)
			if ok {
				metadata.compactThreshold = threshold
				metadata.hasCompact = true

				break
			}
		}
	}

	changed := false

	for _, key := range [...]string{"context_management", "max_output_tokens"} {
		if _, ok := body[key]; ok {
			delete(body, key)

			changed = true
		}
	}

	if streaming {
		if _, ok := body["instructions"]; !ok {
			body["instructions"] = ""
			changed = true
		}

		if body["stream"] != true {
			body["stream"] = true
			changed = true
		}
	}

	if body["store"] != true {
		if items, ok := body["input"].([]any); ok {
			for _, item := range items {
				object, ok := item.(map[string]any)
				if !ok {
					continue
				}

				if _, ok := object["id"]; ok {
					delete(object, "id")

					changed = true
				}
			}
		}
	}

	if changed {
		data, err = json.Marshal(body)
		if err != nil {
			return codexRequestMetadata{}, fmt.Errorf("marshal OpenAI request body: %w", err)
		}
	}

	req.Body = io.NopCloser(bytes.NewReader(data))
	req.ContentLength = int64(len(data))
	req.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(data)), nil }

	return metadata, nil
}

func (t *transport) codexCompaction(ctx context.Context, req *http.Request, resp *http.Response, metadata *codexRequestMetadata) (*http.Response, error) {
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read Codex response: %w", err)
	}

	_ = resp.Body.Close()
	resp.Body = io.NopCloser(bytes.NewReader(data))
	resp.ContentLength = int64(len(data))

	if !metadata.hasCompact {
		return resp, nil
	}

	var body map[string]json.RawMessage
	if err := json.Unmarshal(data, &body); err != nil {
		return nil, fmt.Errorf("parse Codex response: %w", err)
	}

	var usage struct {
		TotalTokens float64 `json:"total_tokens"`
	}
	if err := json.Unmarshal(body["usage"], &usage); err != nil {
		return resp, nil
	}

	if usage.TotalTokens < metadata.compactThreshold {
		return resp, nil
	}

	requestBody, err := req.GetBody()
	if err != nil {
		return nil, fmt.Errorf("read Codex compact source request: %w", err)
	}

	data, err = io.ReadAll(requestBody)
	_ = requestBody.Close()

	if err != nil {
		return nil, fmt.Errorf("read Codex compact source request: %w", err)
	}

	compactRequest := map[string]json.RawMessage{}
	if err := json.Unmarshal(data, &compactRequest); err != nil {
		return nil, fmt.Errorf("parse Codex compact source request: %w", err)
	}

	if codexInputHasUnansweredFunctionCall(compactRequest["input"]) {
		return resp, nil
	}

	compactSource := compactRequest

	compactRequest = map[string]json.RawMessage{
		"input": compactSource["input"],
		"model": compactSource["model"],
	}
	if instructions := compactSource["instructions"]; len(instructions) > 0 {
		compactRequest["instructions"] = instructions
	}

	var output []json.RawMessage
	if err := json.Unmarshal(body["output"], &output); err != nil {
		return nil, fmt.Errorf("parse Codex output: %w", err)
	}

	for _, raw := range output {
		var item struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(raw, &item); err != nil {
			continue
		}

		if item.Type == "compaction" {
			return resp, nil
		}
	}

	data, err = json.Marshal(compactRequest)
	if err != nil {
		return nil, fmt.Errorf("marshal Codex compact request: %w", err)
	}

	compactReq := req.Clone(ctx)
	compactURL := *req.URL
	compactReq.URL = &compactURL
	compactReq.URL.Path = strings.TrimRight(compactReq.URL.Path, "/") + "/compact"
	compactReq.Body = io.NopCloser(bytes.NewReader(data))
	compactReq.ContentLength = int64(len(data))
	compactReq.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(data)), nil }

	compactResp, err := t.base.RoundTrip(compactReq)
	if err != nil {
		return nil, fmt.Errorf("send Codex compact request: %w", err)
	}

	defer func() { _ = compactResp.Body.Close() }()

	data, err = io.ReadAll(compactResp.Body)
	if err != nil {
		return nil, fmt.Errorf("read Codex compact response: %w", err)
	}

	if compactResp.StatusCode != http.StatusOK {
		return resp, nil
	}

	var compactBody struct {
		Output []map[string]json.RawMessage `json:"output"`
	}
	if err := json.Unmarshal(data, &compactBody); err != nil {
		return nil, fmt.Errorf("parse Codex compact response: %w", err)
	}

	for _, item := range compactBody.Output {
		var itemType string
		if err := json.Unmarshal(item["type"], &itemType); err != nil {
			continue
		}

		if itemType != "compaction" && itemType != "compaction_summary" {
			continue
		}

		item["type"] = json.RawMessage(`"compaction"`)

		data, err = json.Marshal(item)
		if err != nil {
			return nil, fmt.Errorf("marshal Codex compact item: %w", err)
		}

		body["output"], err = json.Marshal(append([]json.RawMessage{data}, output...))
		if err != nil {
			return nil, fmt.Errorf("marshal Codex output: %w", err)
		}

		data, err = json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshal Codex response: %w", err)
		}

		resp.Body = io.NopCloser(bytes.NewReader(data))
		resp.ContentLength = int64(len(data))

		return resp, nil
	}

	return resp, nil
}

func codexInputHasUnansweredFunctionCall(data json.RawMessage) bool {
	var items []struct {
		Type   string `json:"type"`
		CallID string `json:"call_id"`
	}
	if err := json.Unmarshal(data, &items); err != nil {
		return false
	}

	pending := make(map[string]int)

	for _, item := range items {
		switch item.Type {
		case "function_call":
			pending[item.CallID]++
		case "function_call_output":
			if pending[item.CallID] > 0 {
				pending[item.CallID]--
			}
		}
	}

	for _, count := range pending {
		if count > 0 {
			return true
		}
	}

	return false
}

func codexStreamingResponse(resp *http.Response) (*http.Response, error) {
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read Codex stream response: %w", err)
	}

	_ = resp.Body.Close()
	outputs := make([]json.RawMessage, 0)

	for line := range strings.SplitSeq(string(data), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data: ") {
			continue
		}

		payload := []byte(strings.TrimPrefix(line, "data: "))

		var event struct {
			Type     string          `json:"type"`
			Item     json.RawMessage `json:"item"`
			Response json.RawMessage `json:"response"`
		}
		if err := json.Unmarshal(payload, &event); err != nil {
			return nil, fmt.Errorf("parse Codex stream response: %w", err)
		}

		if event.Type == "response.output_item.done" && len(event.Item) > 0 {
			outputs = append(outputs, event.Item)
			continue
		}

		if event.Type != "response.completed" {
			continue
		}

		var response map[string]json.RawMessage
		if err := json.Unmarshal(event.Response, &response); err != nil {
			return nil, fmt.Errorf("parse Codex completed response: %w", err)
		}

		if len(outputs) > 0 {
			outputData, err := json.Marshal(outputs)
			if err != nil {
				return nil, fmt.Errorf("marshal Codex output items: %w", err)
			}

			response["output"] = outputData

			event.Response, err = json.Marshal(response)
			if err != nil {
				return nil, fmt.Errorf("marshal Codex completed response: %w", err)
			}
		}

		resp.Body = io.NopCloser(bytes.NewReader(event.Response))
		resp.ContentLength = int64(len(event.Response))
		resp.Header.Set("Content-Type", "application/json")

		return resp, nil
	}

	return nil, errors.New("codex stream response missing completion event")
}

func authorizeURL(redirectURI string, pkce pkceCodes, state string) string {
	params := url.Values{}
	params.Set("response_type", "code")
	params.Set("client_id", clientID)
	params.Set("redirect_uri", redirectURI)
	params.Set("scope", "openid profile email offline_access")
	params.Set("code_challenge", pkce.challenge)
	params.Set("code_challenge_method", "S256")
	params.Set("id_token_add_organizations", "true")
	params.Set("codex_cli_simplified_flow", "true")
	params.Set("state", state)
	params.Set("originator", originator)

	return issuer + "/oauth/authorize?" + params.Encode()
}

func generatePKCE() (pkceCodes, error) {
	verifier, err := randomString(43)
	if err != nil {
		return pkceCodes{}, err
	}

	sum := sha256.Sum256([]byte(verifier))

	return pkceCodes{verifier: verifier, challenge: base64.RawURLEncoding.EncodeToString(sum[:])}, nil
}

func randomString(length int) (string, error) {
	buf := make([]byte, length)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate random string: %w", err)
	}

	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func exchangeCode(ctx context.Context, code, redirectURI, verifier string) (tokenResponse, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", redirectURI)
	form.Set("client_id", clientID)
	form.Set("code_verifier", verifier)

	return postToken(ctx, form)
}

func refreshToken(ctx context.Context, refresh string) (tokenResponse, error) {
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", refresh)
	form.Set("client_id", clientID)

	return postToken(ctx, form)
}

func postToken(ctx context.Context, form url.Values) (tokenResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, issuer+"/oauth/token", strings.NewReader(form.Encode()))
	if err != nil {
		return tokenResponse{}, fmt.Errorf("create token request: %w", err)
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return tokenResponse{}, fmt.Errorf("send token request: %w", err)
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(resp.Body)
		return tokenResponse{}, fmt.Errorf("token request failed (%d): %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}

	var response tokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return tokenResponse{}, fmt.Errorf("decode token response: %w", err)
	}

	return response, nil
}

func pollDeviceIn(ctx context.Context, workspace, workDir, deviceAuthID, userCode string) (path string, done bool, err error) {
	body, err := json.Marshal(map[string]string{"device_auth_id": deviceAuthID, "user_code": userCode})
	if err != nil {
		return "", false, fmt.Errorf("marshal device token request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, issuer+"/api/accounts/deviceauth/token", bytes.NewReader(body))
	if err != nil {
		return "", false, fmt.Errorf("create device token request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "rocketclaw")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", false, fmt.Errorf("send device token request: %w", err)
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusNotFound {
		return "", false, nil
	}

	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(resp.Body)
		return "", false, fmt.Errorf("device token request failed (%d): %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}

	var device struct {
		AuthorizationCode string `json:"authorization_code"`
		CodeVerifier      string `json:"code_verifier"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&device); err != nil {
		return "", false, fmt.Errorf("decode device token response: %w", err)
	}

	response, err := exchangeCode(ctx, device.AuthorizationCode, issuer+"/deviceauth/callback", device.CodeVerifier)
	if err != nil {
		return "", false, err
	}

	path, err = saveTokenResponseIn(workspace, workDir, response)
	if err != nil {
		return "", false, err
	}

	return path, true, nil
}

func saveTokenResponse(workspace string, response tokenResponse) (string, error) {
	return saveTokenResponseIn(workspace, config.DefaultWorkDir, response)
}

func saveTokenResponseIn(workspace, workDir string, response tokenResponse) (string, error) {
	token := tokenFromResponse(response)
	if err := SaveTokenIn(workspace, workDir, token); err != nil {
		return "", err
	}

	path, err := AuthFilePathIn(workspace, workDir)
	if err != nil {
		return "", err
	}

	return path, nil
}

func tokenFromResponse(response tokenResponse) Token {
	expiresIn := response.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = 3600
	}

	return Token{Refresh: response.RefreshToken, Access: response.AccessToken, Expires: time.Now().Add(time.Duration(expiresIn) * time.Second).UnixMilli(), AccountID: extractAccountID(response)}
}

func tokenFromRefreshResponse(response tokenResponse, previous Token) Token {
	next := tokenFromResponse(response)
	if next.Refresh == "" {
		next.Refresh = previous.Refresh
	}

	if next.AccountID == "" {
		next.AccountID = previous.AccountID
	}

	return next
}

func extractAccountID(response tokenResponse) string {
	for _, token := range []string{response.IDToken, response.AccessToken} {
		claims, ok := parseClaims(token)
		if !ok {
			continue
		}

		if claims.ChatGPTAccountID != "" {
			return claims.ChatGPTAccountID
		}

		if claims.OpenAIAuth.ChatGPTAccountID != "" {
			return claims.OpenAIAuth.ChatGPTAccountID
		}

		if len(claims.Organizations) > 0 {
			return claims.Organizations[0].ID
		}
	}

	return ""
}

func parseClaims(token string) (claims, bool) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return claims{}, false
	}

	data, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return claims{}, false
	}

	var parsed claims
	if err := json.Unmarshal(data, &parsed); err != nil {
		return claims{}, false
	}

	return parsed, true
}
