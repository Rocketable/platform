// Package oai provides ChatGPT OAuth-backed OpenAI clients for rocketclaw.
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

	"github.com/google/uuid"
	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/responses"
	"golang.org/x/sys/unix"
)

const (
	clientID, issuer, codexBaseURL, dummyAPIKey = "app_EMoamEEZ73f0CkXaXp7hrann", "https://auth.openai.com", "https://chatgpt.com/backend-api/codex", "rocketclaw-oauth-dummy-key"
	defaultLoginPort                            = 1455
	originator, codexUserAgent                  = "codex_cli_rs", "codex_cli_rs/0.0.0 (RocketClaw)"
	refreshSkew                                 = 120 * time.Second
)

// Token is the persisted ChatGPT OAuth credential used for Codex requests.
type Token struct {
	Refresh   string `json:"refresh"`
	Access    string `json:"access"`
	Expires   int64  `json:"expires"`
	AccountID string `json:"account_id,omitempty"`
}

type authFile struct {
	Providers map[string]Token `json:"providers"`
}

type pkceCodes struct {
	verifier, challenge string
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

// AuthFilePathIn returns the workspace-local token file path in runtimeDir.
func AuthFilePathIn(workspace, runtimeDir string) (string, error) {
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

	return filepath.Join(workspace, runtimeDir, "auth.json"), nil
}

// LoadTokenIn reads the provider's persisted ChatGPT OAuth token from runtimeDir.
func LoadTokenIn(workspace, runtimeDir, provider string) (Token, error) {
	path, err := AuthFilePathIn(workspace, runtimeDir)
	if err != nil {
		return Token{}, err
	}

	file, err := readAuthFile(path)
	if err != nil {
		return Token{}, err
	}

	token := file.Providers[provider]
	if strings.TrimSpace(token.Refresh) == "" {
		return Token{}, fmt.Errorf("OpenAI OAuth token for provider %q is missing refresh token: %w", provider, os.ErrNotExist)
	}

	return token, nil
}

// HasTokenIn reports whether the provider has a persisted ChatGPT OAuth token.
func HasTokenIn(workspace, runtimeDir, provider string) (bool, error) {
	_, err := LoadTokenIn(workspace, runtimeDir, provider)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}

	return err == nil, err
}

// SaveTokenIn writes the provider's ChatGPT OAuth token.
func SaveTokenIn(workspace, runtimeDir, provider string, token Token) error {
	committed, err := updateAuthFileIn(workspace, runtimeDir, true, func(file *authFile) (bool, error) {
		file.Providers[provider] = token
		return true, nil
	})
	if committed && err != nil {
		return fmt.Errorf("committed OpenAI OAuth token: %w", err)
	}

	return err
}

// RemoveTokenIn removes only the provider's persisted ChatGPT OAuth token.
func RemoveTokenIn(workspace, runtimeDir, provider string) error {
	_, err := updateAuthFileIn(workspace, runtimeDir, true, func(file *authFile) (bool, error) {
		_, changed := file.Providers[provider]
		delete(file.Providers, provider)

		return changed, nil
	})

	return err
}

func readAuthFile(path string) (authFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return authFile{}, fmt.Errorf("read OpenAI OAuth token: %w", err)
	}

	var file authFile
	if err := json.Unmarshal(data, &file); err != nil {
		return authFile{}, fmt.Errorf("parse OpenAI OAuth token: %w", err)
	}

	if file.Providers != nil {
		return file, nil
	}

	var fields map[string]json.RawMessage

	_ = json.Unmarshal(data, &fields)
	if _, ok := fields["providers"]; ok {
		return authFile{}, errors.New("parse OpenAI OAuth providers: providers must be an object")
	}

	var token Token
	if err := json.Unmarshal(data, &token); err != nil {
		return authFile{}, fmt.Errorf("parse OpenAI OAuth token: %w", err)
	}

	return authFile{Providers: map[string]Token{"openai": token}}, nil
}

func updateAuthFileIn(workspace, runtimeDir string, create bool, update func(*authFile) (bool, error)) (bool, error) {
	path, err := AuthFilePathIn(workspace, runtimeDir)
	if err != nil {
		return false, err
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return false, fmt.Errorf("create OpenAI OAuth token dir: %w", err)
	}

	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return false, fmt.Errorf("open OpenAI OAuth token lock: %w", err)
	}
	defer func() { _ = lock.Close() }()

	if err := lock.Chmod(0o600); err != nil {
		return false, fmt.Errorf("chmod OpenAI OAuth token lock: %w", err)
	}

	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX); err != nil {
		return false, fmt.Errorf("lock OpenAI OAuth token: %w", err)
	}

	file, err := readAuthFile(path)
	if err != nil {
		if !create || !errors.Is(err, os.ErrNotExist) {
			return false, fmt.Errorf("write OpenAI OAuth token: %w", err)
		}

		file = authFile{Providers: make(map[string]Token)}
	}

	changed, err := update(&file)
	if err != nil || !changed {
		return false, err
	}

	committed, err := writeAuthFile(path, file, syncDirectory)
	if err != nil {
		err = fmt.Errorf("write OpenAI OAuth token: %w", err)
	}

	return committed, err
}

func writeAuthFile(path string, file authFile, syncDir func(string) error) (bool, error) {
	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return false, fmt.Errorf("marshal OpenAI OAuth token: %w", err)
	}

	data = append(data, '\n')

	temp, err := os.CreateTemp(filepath.Dir(path), ".auth.json-*")
	if err != nil {
		return false, fmt.Errorf("create temporary OpenAI OAuth token: %w", err)
	}

	tempPath := temp.Name()
	defer func() { _ = os.Remove(tempPath) }()

	_, err = temp.Write(data)

	err = errors.Join(err, temp.Sync(), temp.Close())
	if err != nil {
		return false, fmt.Errorf("replace OpenAI OAuth token: %w", err)
	}

	if err := os.Rename(tempPath, path); err != nil {
		return false, fmt.Errorf("replace OpenAI OAuth token: %w", err)
	}

	if err := syncDir(filepath.Dir(path)); err != nil {
		return true, fmt.Errorf("sync OpenAI OAuth token dir: %w", err)
	}

	return true, nil
}

func syncDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open directory: %w", err)
	}
	defer func() { _ = dir.Close() }()

	if err := dir.Sync(); err != nil {
		return fmt.Errorf("sync directory: %w", err)
	}

	return nil
}

// AcquireBrowserToken completes the local browser OAuth flow without persisting the token.
func AcquireBrowserToken(ctx context.Context, out io.Writer) (Token, error) {
	pkce, err := generatePKCE()
	if err != nil {
		return Token{}, err
	}

	state, err := randomString(32)
	if err != nil {
		return Token{}, err
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
	if _, err := fmt.Fprintf(out, "Open this URL to authorize rocketclaw with ChatGPT:\n%s\n", authURL); err != nil {
		return Token{}, fmt.Errorf("write OAuth authorization URL: %w", err)
	}

	var code string
	select {
	case <-ctx.Done():
		return Token{}, fmt.Errorf("wait for OAuth callback: %w", ctx.Err())
	case err := <-errCh:
		return Token{}, fmt.Errorf("OAuth callback: %w", err)
	case code = <-codeCh:
	}

	response, err := exchangeCode(ctx, code, redirectURI, pkce.verifier)
	if err != nil {
		return Token{}, err
	}

	return tokenFromResponse(response), nil
}

// LoginBrowserIn completes the local browser OAuth flow and saves the resulting token in runtimeDir.
func LoginBrowserIn(ctx context.Context, workspace, runtimeDir, provider string, out io.Writer) (string, error) {
	token, err := AcquireBrowserToken(ctx, out)
	if err != nil {
		return "", err
	}

	return saveTokenIn(workspace, runtimeDir, provider, token)
}

// AcquireDeviceToken completes the headless device OAuth flow without persisting the token.
func AcquireDeviceToken(ctx context.Context, out io.Writer) (Token, error) {
	body, err := json.Marshal(map[string]string{"client_id": clientID})
	if err != nil {
		return Token{}, fmt.Errorf("marshal device authorization request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, issuer+"/api/accounts/deviceauth/usercode", bytes.NewReader(body))
	if err != nil {
		return Token{}, fmt.Errorf("create device authorization request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "rocketclaw")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return Token{}, fmt.Errorf("send device authorization request: %w", err)
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return Token{}, oauthHTTPError("device authorization", resp.StatusCode, resp.Body)
	}

	var device struct {
		DeviceAuthID string `json:"device_auth_id"`
		UserCode     string `json:"user_code"`
		Interval     string `json:"interval"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&device); err != nil {
		return Token{}, fmt.Errorf("decode device authorization response: %w", err)
	}

	if _, err := fmt.Fprintf(out, "Open %s/codex/device and enter code: %s\n", issuer, device.UserCode); err != nil {
		return Token{}, fmt.Errorf("write OAuth device code: %w", err)
	}

	interval, err := time.ParseDuration(device.Interval + "s")
	if err != nil || interval <= 0 {
		interval = 5 * time.Second
	}

	for {
		select {
		case <-ctx.Done():
			return Token{}, fmt.Errorf("wait for device authorization: %w", ctx.Err())
		case <-time.After(interval + 3*time.Second):
		}

		token, done, err := pollDeviceToken(ctx, device.DeviceAuthID, device.UserCode)
		if err != nil {
			return Token{}, err
		}

		if done {
			return token, nil
		}
	}
}

// LoginDeviceIn completes the headless device OAuth flow and saves the resulting token in runtimeDir.
func LoginDeviceIn(ctx context.Context, workspace, runtimeDir, provider string, out io.Writer) (string, error) {
	token, err := AcquireDeviceToken(ctx, out)
	if err != nil {
		return "", err
	}

	return saveTokenIn(workspace, runtimeDir, provider, token)
}

// NewChatGPTClientIn creates an OpenAI client that sends Responses API requests to ChatGPT Codex using runtimeDir auth.
func NewChatGPTClientIn(workspace, runtimeDir, provider string, opts ...option.RequestOption) (*openai.Client, error) {
	if _, err := LoadTokenIn(workspace, runtimeDir, provider); err != nil {
		return nil, err
	}

	sessionID := uuid.NewString()

	client := openai.NewClient(append([]option.RequestOption{
		option.WithAPIKey(dummyAPIKey),
		option.WithBaseURL(codexBaseURL),
		option.WithHTTPClient(&http.Client{Transport: &transport{base: http.DefaultTransport, workspace: workspace, runtimeDir: runtimeDir, provider: provider, sessionID: sessionID}}),
		option.WithHeader("originator", originator),
		option.WithHeader("User-Agent", codexUserAgent),
	}, opts...)...)

	return &client, nil
}

type transport struct {
	base                            http.RoundTripper
	workspace, runtimeDir, provider string
	mu                              sync.Mutex
	sessionID                       string
}

type codexRequestMetadata struct {
	compactThreshold float64
	hasCompact       bool
}

type codexStreamError struct {
	Code, Message string
}

func (e *codexStreamError) Error() string {
	message := "response.failed event received"
	if text := strings.TrimSpace(e.Message); text != "" {
		message = text
	}

	if code := strings.TrimSpace(e.Code); code != "" && message != "response.failed event received" {
		message = code + ": " + message
	}

	return "codex stream response failed: " + message
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

	sessionID, err := t.codexSessionID()
	if err != nil {
		return nil, err
	}

	setCodexHeaders(cloned, token, sessionID)

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
		setCodexHeaders(retry, token, sessionID)

		resp, err = t.base.RoundTrip(retry)
		if err != nil {
			return nil, fmt.Errorf("send OpenAI request: %w", err)
		}

		activeReq = retry

		if resp.StatusCode == http.StatusUnauthorized {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()

			return nil, fmt.Errorf("ChatGPT OAuth authorization failed after Codex 401 recovery for provider %q; run `%s`", t.provider, loginCommand(t.provider))
		}
	}

	if codexResponse && resp.StatusCode == http.StatusOK {
		resp, err = codexStreamingResponse(resp)
		if err != nil {
			errStream := err
			if streamErr, ok := errors.AsType[*codexStreamError](errStream); ok && strings.TrimSpace(streamErr.Code) == "context_length_exceeded" {
				var retried bool

				resp, retried, err = t.retryCodexAfterCompaction(req.Context(), activeReq, &metadata)
				if retried {
					if err != nil {
						return nil, fmt.Errorf("adapt Codex stream response after compaction retry: %w", err)
					}

					return resp, nil
				}
			}

			return nil, fmt.Errorf("adapt Codex stream response: %w", errStream)
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

func setCodexHeaders(req *http.Request, token Token, sessionID string) {
	req.Header.Del("Authorization")
	req.Header.Set("Authorization", "Bearer "+token.Access)
	req.Header.Set("Originator", originator)
	req.Header.Set("User-Agent", codexUserAgent)
	req.Header.Set("Session_id", sessionID)
	req.Header.Set("X-Client-Request-Id", sessionID)

	if token.AccountID != "" {
		req.Header.Set("Chatgpt-Account-Id", token.AccountID)
	}
}

func (t *transport) codexSessionID() (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.sessionID != "" {
		return t.sessionID, nil
	}

	t.sessionID = uuid.NewString()

	return t.sessionID, nil
}

func (t *transport) token(ctx context.Context) (Token, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	var selected Token

	_, err := updateAuthFileIn(t.workspace, t.runtimeDir, false, func(file *authFile) (bool, error) {
		token := file.Providers[t.provider]
		if strings.TrimSpace(token.Refresh) == "" {
			return false, fmt.Errorf("OpenAI OAuth token for provider %q is missing refresh token", t.provider)
		}

		if token.Access != "" && token.Expires > time.Now().Add(refreshSkew).UnixMilli() {
			selected = token
			return false, nil
		}

		response, err := refreshToken(ctx, token.Refresh)
		if err != nil {
			return false, fmt.Errorf("refresh ChatGPT OAuth token for provider %q; run `%s`: %w", t.provider, loginCommand(t.provider), err)
		}

		selected = tokenFromRefreshResponse(response, token)
		file.Providers[t.provider] = selected

		return true, nil
	})
	if err != nil {
		return Token{}, err
	}

	return selected, nil
}

func (t *transport) recoveryToken(ctx context.Context, failed Token) (Token, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	var selected Token

	_, err := updateAuthFileIn(t.workspace, t.runtimeDir, false, func(file *authFile) (bool, error) {
		token := file.Providers[t.provider]
		if strings.TrimSpace(token.Refresh) == "" {
			return false, fmt.Errorf("OpenAI OAuth token for provider %q is missing refresh token", t.provider)
		}

		if token.Access != "" && token.Access != failed.Access && token.AccountID == failed.AccountID {
			selected = token
			return false, nil
		}

		response, err := refreshToken(ctx, token.Refresh)
		if err != nil {
			return false, fmt.Errorf("refresh ChatGPT OAuth token after Codex 401 for provider %q; run `%s`: %w", t.provider, loginCommand(t.provider), err)
		}

		selected = tokenFromRefreshResponse(response, token)
		file.Providers[t.provider] = selected

		return true, nil
	})
	if err != nil {
		return Token{}, err
	}

	return selected, nil
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

	var body map[string]json.RawMessage
	if err := json.Unmarshal(data, &body); err != nil {
		req.Body = io.NopCloser(bytes.NewReader(data))
		return codexRequestMetadata{}, nil
	}

	var metadata codexRequestMetadata

	if raw, ok := body["context_management"]; ok {
		if threshold, ok := codexCompactionThreshold(raw); ok {
			metadata.compactThreshold = threshold
			metadata.hasCompact = true
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
			body["instructions"] = json.RawMessage(`""`)
			changed = true
		}

		var stream bool
		if err := json.Unmarshal(body["stream"], &stream); err != nil || !stream {
			body["stream"] = json.RawMessage("true")
			changed = true
		}
	}

	stripIDs := true

	if raw, ok := body["store"]; ok {
		var store bool
		if err := json.Unmarshal(raw, &store); err == nil && store {
			stripIDs = false
		}
	}

	if raw, ok := body["input"]; ok {
		input, ok, err := cleanCodexInput(raw, stripIDs)
		if err != nil {
			return codexRequestMetadata{}, err
		}

		if ok {
			body["input"] = input
			changed = true
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

func codexCompactionThreshold(raw json.RawMessage) (float64, bool) {
	var contextManagement []struct {
		CompactThreshold *float64 `json:"compact_threshold"`
	}
	if err := json.Unmarshal(raw, &contextManagement); err != nil {
		return 0, false
	}

	for i := range contextManagement {
		if contextManagement[i].CompactThreshold != nil {
			return *contextManagement[i].CompactThreshold, true
		}
	}

	return 0, false
}

func cleanCodexInput(raw json.RawMessage, stripIDs bool) (json.RawMessage, bool, error) {
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, false, nil
	}

	changed := false

	for i := range items {
		item, ok, err := cleanCodexInputItem(items[i], stripIDs)
		if err != nil {
			return nil, false, err
		}

		if ok {
			items[i] = item
			changed = true
		}
	}

	if !changed {
		return nil, false, nil
	}

	data, err := json.Marshal(items)
	if err != nil {
		return nil, false, fmt.Errorf("marshal Codex input items: %w", err)
	}

	return data, true, nil
}

func cleanCodexInputItem(raw json.RawMessage, stripIDs bool) (json.RawMessage, bool, error) {
	var item struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &item); err != nil {
		return nil, false, nil
	}

	if item.Type == "compaction" {
		var compaction responses.ResponseCompactionItemParam
		if err := json.Unmarshal(raw, &compaction); err != nil {
			return nil, false, nil
		}

		projected := responses.ResponseCompactionItemParam{EncryptedContent: compaction.EncryptedContent, Type: compaction.Type}
		if !stripIDs {
			projected.ID = compaction.ID
		}

		data, err := json.Marshal(projected)
		if err != nil {
			return nil, false, fmt.Errorf("marshal Codex compaction input: %w", err)
		}

		return data, true, nil
	}

	if !stripIDs {
		return nil, false, nil
	}

	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return nil, false, nil
	}

	if _, ok := object["id"]; !ok {
		return nil, false, nil
	}

	delete(object, "id")

	data, err := json.Marshal(object)
	if err != nil {
		return nil, false, fmt.Errorf("marshal Codex input item: %w", err)
	}

	return data, true, nil
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

	data, ok, err := t.codexCompactItem(ctx, req)
	if err != nil {
		return nil, err
	}

	if !ok {
		return resp, nil
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

func (t *transport) retryCodexAfterCompaction(ctx context.Context, req *http.Request, metadata *codexRequestMetadata) (*http.Response, bool, error) {
	if !metadata.hasCompact {
		return nil, false, nil
	}

	compactItem, ok, err := t.codexCompactItem(ctx, req)
	if err != nil || !ok {
		return nil, false, nil
	}

	requestBody, err := req.GetBody()
	if err != nil {
		return nil, false, nil
	}

	data, err := io.ReadAll(requestBody)
	_ = requestBody.Close()

	if err != nil {
		return nil, false, nil
	}

	var retryBody map[string]json.RawMessage
	if err := json.Unmarshal(data, &retryBody); err != nil {
		return nil, false, nil
	}

	retryItem := compactItem
	if item, ok, err := cleanCodexInputItem(compactItem, false); err != nil {
		return nil, false, err
	} else if ok {
		retryItem = item
	}

	input, err := json.Marshal([]json.RawMessage{retryItem})
	if err != nil {
		return nil, false, fmt.Errorf("marshal Codex compaction retry input: %w", err)
	}

	retryBody["input"] = input

	data, err = json.Marshal(retryBody)
	if err != nil {
		return nil, false, fmt.Errorf("marshal Codex compaction retry request: %w", err)
	}

	retry := req.Clone(ctx)
	retry.Body = io.NopCloser(bytes.NewReader(data))
	retry.ContentLength = int64(len(data))
	retry.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(data)), nil }

	resp, err := t.base.RoundTrip(retry)
	if err != nil {
		return nil, true, fmt.Errorf("send OpenAI request: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return resp, true, nil
	}

	resp, err = codexStreamingResponse(resp)
	if err != nil {
		return nil, true, err
	}

	return resp, true, nil
}

func (t *transport) codexCompactItem(ctx context.Context, req *http.Request) (json.RawMessage, bool, error) {
	requestBody, err := req.GetBody()
	if err != nil {
		return nil, false, fmt.Errorf("read Codex compact source request: %w", err)
	}

	data, err := io.ReadAll(requestBody)
	_ = requestBody.Close()

	if err != nil {
		return nil, false, fmt.Errorf("read Codex compact source request: %w", err)
	}

	compactSource := map[string]json.RawMessage{}
	if err := json.Unmarshal(data, &compactSource); err != nil {
		return nil, false, fmt.Errorf("parse Codex compact source request: %w", err)
	}

	if codexInputHasUnansweredFunctionCall(compactSource["input"]) {
		return nil, false, nil
	}

	compactRequest := map[string]json.RawMessage{
		"input": compactSource["input"],
		"model": compactSource["model"],
	}
	if instructions := compactSource["instructions"]; len(instructions) > 0 {
		compactRequest["instructions"] = instructions
	}

	data, err = json.Marshal(compactRequest)
	if err != nil {
		return nil, false, fmt.Errorf("marshal Codex compact request: %w", err)
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
		return nil, false, fmt.Errorf("send Codex compact request: %w", err)
	}

	defer func() { _ = compactResp.Body.Close() }()

	data, err = io.ReadAll(compactResp.Body)
	if err != nil {
		return nil, false, fmt.Errorf("read Codex compact response: %w", err)
	}

	if compactResp.StatusCode != http.StatusOK {
		return nil, false, nil
	}

	var compactBody struct {
		Output []map[string]json.RawMessage `json:"output"`
	}
	if err := json.Unmarshal(data, &compactBody); err != nil {
		return nil, false, fmt.Errorf("parse Codex compact response: %w", err)
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
		delete(item, "recent")

		data, err = json.Marshal(item)
		if err != nil {
			return nil, false, fmt.Errorf("marshal Codex compact item: %w", err)
		}

		return data, true, nil
	}

	return nil, false, nil
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

	var errResponse error

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

		if event.Type == "response.failed" {
			errResponse = &codexStreamError{}

			if len(event.Response) > 0 {
				var response struct {
					Error struct {
						Code    string `json:"code"`
						Message string `json:"message"`
					} `json:"error"`
				}

				if err := json.Unmarshal(event.Response, &response); err != nil {
					return nil, fmt.Errorf("parse Codex failed response: %w", err)
				}

				errResponse = &codexStreamError{Code: response.Error.Code, Message: response.Error.Message}
			}

			continue
		}

		if event.Type == "response.incomplete" {
			reason := "unknown"

			if len(event.Response) > 0 {
				var response struct {
					IncompleteDetails struct {
						Reason string `json:"reason"`
					} `json:"incomplete_details"`
				}

				if err := json.Unmarshal(event.Response, &response); err != nil {
					return nil, fmt.Errorf("parse Codex incomplete response: %w", err)
				}

				if text := strings.TrimSpace(response.IncompleteDetails.Reason); text != "" {
					reason = text
				}
			}

			errResponse = fmt.Errorf("codex stream response incomplete: %s", reason)

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

	if errResponse != nil {
		return nil, errResponse
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

func loginCommand(provider string) string {
	if provider == "openai" {
		return "rocketclaw oai login"
	}

	return "rocketclaw oai login " + provider
}

func postToken(ctx context.Context, form url.Values) (tokenResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, issuer+"/oauth/token", strings.NewReader(form.Encode()))
	if err != nil {
		return tokenResponse{}, fmt.Errorf("create token request: %w", err)
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return tokenResponse{}, fmt.Errorf("send token request: %w", err)
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return tokenResponse{}, oauthHTTPError("token request", resp.StatusCode, resp.Body)
	}

	var response tokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return tokenResponse{}, fmt.Errorf("decode token response: %w", err)
	}

	return response, nil
}

func pollDeviceToken(ctx context.Context, deviceAuthID, userCode string) (Token, bool, error) {
	body, err := json.Marshal(map[string]string{"device_auth_id": deviceAuthID, "user_code": userCode})
	if err != nil {
		return Token{}, false, fmt.Errorf("marshal device token request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, issuer+"/api/accounts/deviceauth/token", bytes.NewReader(body))
	if err != nil {
		return Token{}, false, fmt.Errorf("create device token request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "rocketclaw")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return Token{}, false, fmt.Errorf("send device token request: %w", err)
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusNotFound {
		return Token{}, false, nil
	}

	if resp.StatusCode != http.StatusOK {
		return Token{}, false, oauthHTTPError("device token request", resp.StatusCode, resp.Body)
	}

	var device struct {
		AuthorizationCode string `json:"authorization_code"`
		CodeVerifier      string `json:"code_verifier"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&device); err != nil {
		return Token{}, false, fmt.Errorf("decode device token response: %w", err)
	}

	response, err := exchangeCode(ctx, device.AuthorizationCode, issuer+"/deviceauth/callback", device.CodeVerifier)
	if err != nil {
		return Token{}, false, err
	}

	return tokenFromResponse(response), true, nil
}

func oauthHTTPError(operation string, status int, body io.Reader) error {
	var response struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.NewDecoder(body).Decode(&response); err == nil && response.Error.Code != "" {
		return fmt.Errorf("%s failed (%d): %s", operation, status, response.Error.Code)
	}

	return fmt.Errorf("%s failed (%d)", operation, status)
}

func saveTokenIn(workspace, runtimeDir, provider string, token Token) (string, error) {
	if err := SaveTokenIn(workspace, runtimeDir, provider, token); err != nil {
		return "", err
	}

	return AuthFilePathIn(workspace, runtimeDir)
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
