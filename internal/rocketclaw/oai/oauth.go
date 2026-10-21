// Package oai provides ChatGPT OAuth-backed OpenAI clients for rocketclaw.
package oai

import (
	"bytes"
	"cmp"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/responses"
	"golang.org/x/sys/unix"
)

const (
	clientID, issuer, codexBaseURL, dummyAPIKey = "app_EMoamEEZ73f0CkXaXp7hrann", "https://auth.openai.com", "https://chatgpt.com/backend-api/codex", "rocketclaw-oauth-dummy-key"
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

	committed, err := writeAuthFile(path, file, localDirectorySyncer{})
	if err != nil {
		err = fmt.Errorf("write OpenAI OAuth token: %w", err)
	}

	return committed, err
}

type directorySyncer interface {
	syncDirectory(path string) error
}

type localDirectorySyncer struct{}

func writeAuthFile(path string, file authFile, syncDir directorySyncer) (bool, error) {
	data, _ := json.MarshalIndent(file, "", "  ")

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

	if err := syncDir.syncDirectory(filepath.Dir(path)); err != nil {
		return true, fmt.Errorf("sync OpenAI OAuth token dir: %w", err)
	}

	return true, nil
}

func (localDirectorySyncer) syncDirectory(path string) error {
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
// The OAuth client's registered redirect URI fixes the callback port.
func AcquireBrowserToken(ctx context.Context, out io.Writer) (Token, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:1455")
	if err != nil {
		return Token{}, fmt.Errorf("OAuth callback: %w", err)
	}

	return acquireBrowserToken(ctx, out, listener)
}

// acquireBrowserToken serves the OAuth callback on listener and closes it before returning.
func acquireBrowserToken(ctx context.Context, out io.Writer, listener net.Listener) (Token, error) {
	defer func() { _ = listener.Close() }()

	pkce := generatePKCE()
	state := randomString(32)

	codeCh := make(chan string, 1)
	errCh := make(chan error, 1)
	server := new(http.Server)
	redirectURI := fmt.Sprintf("http://localhost:%d/auth/callback", listener.Addr().(*net.TCPAddr).Port)
	server.Handler = http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/auth/callback" {
			http.NotFound(w, req)
			return
		}

		if value := req.URL.Query().Get("error"); value != "" {
			errCh <- errors.New(cmp.Or(req.URL.Query().Get("error_description"), value))

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
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
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

// AcquireDeviceToken completes the headless device OAuth flow without persisting the token.
func AcquireDeviceToken(ctx context.Context, out io.Writer) (Token, error) {
	body, _ := json.Marshal(map[string]string{"client_id": clientID})

	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, issuer+"/api/accounts/deviceauth/usercode", bytes.NewReader(body))

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

// NewChatGPTClientIn creates an OpenAI client that sends Responses API requests to ChatGPT Codex using runtimeDir auth.
func NewChatGPTClientIn(workspace, runtimeDir, provider string, logger *slog.Logger, opts ...option.RequestOption) (*openai.Client, error) {
	if _, err := LoadTokenIn(workspace, runtimeDir, provider); err != nil {
		return nil, err
	}

	sessionID := uuid.NewString()

	client := openai.NewClient(append([]option.RequestOption{
		option.WithAPIKey(dummyAPIKey),
		option.WithBaseURL(codexBaseURL),
		option.WithHTTPClient(&http.Client{Transport: &transport{base: http.DefaultTransport, log: logger, workspace: workspace, runtimeDir: runtimeDir, provider: provider, sessionID: sessionID}}),
		option.WithHeader("originator", originator),
		option.WithHeader("User-Agent", codexUserAgent),
	}, opts...)...)

	return &client, nil
}

type transport struct {
	base                            http.RoundTripper
	log                             *slog.Logger
	workspace, runtimeDir, provider string
	sessionID                       string
}

type codexStreamError struct {
	Code, Message string
}

func (e *codexStreamError) Error() string {
	message := cmp.Or(strings.TrimSpace(e.Message), "response.failed event received")

	if code := strings.TrimSpace(e.Code); code != "" && message != "response.failed event received" {
		message = code + ": " + message
	}

	return "codex stream response failed: " + message
}

func (t *transport) RoundTrip(req *http.Request) (*http.Response, error) {
	token, err := FreshTokenIn(req.Context(), t.workspace, t.runtimeDir, t.provider, Token{})
	if err != nil {
		return nil, err
	}

	cloned := req.Clone(req.Context())

	setCodexHeaders(cloned, token, t.sessionID)

	codexPath := strings.TrimRight(cloned.URL.Path, "/")

	codexResponse := strings.HasSuffix(codexPath, "/responses")

	codexCompact := strings.HasSuffix(codexPath, "/responses/compact")

	var threshold *float64

	if codexResponse || codexCompact {
		threshold, err = cleanCodexRequest(cloned, codexResponse)
		if err != nil {
			return nil, err
		}
	}

	activeReq := cloned

	resp, err := t.roundTrip(cloned, "initial")
	if err != nil {
		return nil, err
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

		token, err = FreshTokenIn(req.Context(), t.workspace, t.runtimeDir, t.provider, token)
		if err != nil {
			if retryBody != nil {
				_ = retryBody.Close()
			}

			return nil, err
		}

		retry := cloned.Clone(req.Context())
		retry.Body = retryBody
		setCodexHeaders(retry, token, t.sessionID)

		resp, err = t.roundTrip(retry, "authorization_retry")
		if err != nil {
			return nil, err
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

				resp, retried, err = t.retryCodexAfterCompaction(req.Context(), activeReq, threshold)
				if retried {
					if err != nil {
						return nil, fmt.Errorf("adapt Codex stream response after compaction retry: %w", err)
					}

					return resp, nil
				}
			}

			return nil, fmt.Errorf("adapt Codex stream response: %w", errStream)
		}

		resp, err = t.codexCompaction(req.Context(), activeReq, resp, threshold)
		if err != nil {
			return nil, fmt.Errorf("compact Codex response: %w", err)
		}
	} else if codexCompact && resp.StatusCode == http.StatusOK && resp.Header.Get("Content-Type") == "" {
		resp.Header.Set("Content-Type", "application/json")
	}

	return resp, nil
}

// roundTrip measures a physical HTTP attempt, before stream conversion or compaction.
func (t *transport) roundTrip(req *http.Request, reason string) (*http.Response, error) {
	startedAt := time.Now()
	resp, err := t.base.RoundTrip(req)
	duration := time.Since(startedAt)

	status := 0
	if resp != nil {
		status = resp.StatusCode
	}

	outcome := "success"
	if err != nil {
		outcome = "transport_error"
	} else if status < 200 || status >= 300 {
		outcome = "http_error"
	}

	retryCount, _ := strconv.Atoi(req.Header.Get("X-Stainless-Retry-Count"))
	t.log.Info("Codex HTTP attempt returned", "event", "provider_http_attempt", "boundary", "http_headers", "reason", reason, "retry_count", retryCount, "status", status, "outcome", outcome, "duration_ms", duration.Milliseconds(), "error_type", fmt.Sprintf("%T", err))

	if err != nil {
		if reason == "compaction" {
			return resp, fmt.Errorf("send Codex compact request: %w", err)
		}

		return resp, fmt.Errorf("send OpenAI request: %w", err)
	}

	return resp, nil
}

func setCodexHeaders(req *http.Request, token Token, sessionID string) {
	req.Header.Set("Authorization", "Bearer "+token.Access)
	req.Header.Set("Originator", originator)
	req.Header.Set("User-Agent", codexUserAgent)
	req.Header.Set("Session_id", sessionID)
	req.Header.Set("X-Client-Request-Id", sessionID)

	if token.AccountID != "" {
		req.Header.Set("Chatgpt-Account-Id", token.AccountID)
	}
}

// MissingTokenError reports that no ChatGPT OAuth token is stored for Provider.
type MissingTokenError struct {
	Provider string
}

func (e *MissingTokenError) Error() string {
	return fmt.Sprintf("OpenAI OAuth token for provider %q is missing refresh token", e.Provider)
}

// Unwrap reports a missing token as os.ErrNotExist.
func (e *MissingTokenError) Unwrap() error {
	return os.ErrNotExist
}

// FreshTokenIn returns the provider's ChatGPT OAuth token from runtimeDir,
// refreshing and persisting it under the auth file lock when needed.
// With a zero rejected token, the stored token is reused while it expires
// more than 120 s out. With the token a server just rejected, the stored token
// is reused only if another writer already replaced it for the same account.
// It returns a *MissingTokenError when the auth file has no token for the
// provider; a missing auth file also matches os.ErrNotExist.
func FreshTokenIn(ctx context.Context, workspace, runtimeDir, provider string, rejected Token) (Token, error) {
	var selected Token

	_, err := updateAuthFileIn(workspace, runtimeDir, false, func(file *authFile) (bool, error) {
		token := file.Providers[provider]
		if strings.TrimSpace(token.Refresh) == "" {
			return false, &MissingTokenError{Provider: provider}
		}

		reuse := token.Access != "" && token.Expires > time.Now().Add(refreshSkew).UnixMilli()
		if rejected.Access != "" {
			reuse = token.Access != "" && token.Access != rejected.Access && token.AccountID == rejected.AccountID
		}

		if reuse {
			selected = token
			return false, nil
		}

		form := url.Values{}
		form.Set("grant_type", "refresh_token")
		form.Set("refresh_token", token.Refresh)
		form.Set("client_id", clientID)

		response, err := postToken(ctx, form)
		if err != nil {
			if rejected.Access != "" {
				return false, fmt.Errorf("refresh ChatGPT OAuth token after Codex 401 for provider %q; run `%s`: %w", provider, loginCommand(provider), err)
			}

			return false, fmt.Errorf("refresh ChatGPT OAuth token for provider %q; run `%s`: %w", provider, loginCommand(provider), err)
		}

		selected = tokenFromResponse(response)
		selected.Refresh = cmp.Or(selected.Refresh, token.Refresh)
		selected.AccountID = cmp.Or(selected.AccountID, token.AccountID)
		file.Providers[provider] = selected

		return true, nil
	})
	if err != nil {
		return Token{}, err
	}

	return selected, nil
}

// cleanCodexRequest returns the first decoded compaction threshold; nil denotes absence, not zero.
func cleanCodexRequest(req *http.Request, streaming bool) (*float64, error) {
	if req.Method != http.MethodPost || req.Body == nil {
		return nil, nil
	}

	contentType := req.Header.Get("Content-Type")
	if contentType != "" && !strings.Contains(contentType, "application/json") {
		return nil, nil
	}

	data, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, fmt.Errorf("read OpenAI request body: %w", err)
	}

	_ = req.Body.Close()

	var body map[string]json.RawMessage
	if err := json.Unmarshal(data, &body); err != nil {
		req.Body = io.NopCloser(bytes.NewReader(data))
		return nil, nil
	}

	var threshold *float64

	if raw, ok := body["context_management"]; ok {
		var contextManagement []struct {
			CompactThreshold *float64 `json:"compact_threshold"`
		}
		if err := json.Unmarshal(raw, &contextManagement); err == nil {
			for i := range contextManagement {
				if contextManagement[i].CompactThreshold != nil {
					threshold = contextManagement[i].CompactThreshold
					break
				}
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
			body["instructions"] = json.RawMessage(`""`)
			changed = true
		}

		var stream bool
		if err := json.Unmarshal(body["stream"], &stream); err != nil || !stream {
			body["stream"] = json.RawMessage("true")
			changed = true
		}
	}

	var store bool

	_ = json.Unmarshal(body["store"], &store)

	var items []json.RawMessage
	if err := json.Unmarshal(body["input"], &items); err == nil {
		inputChanged := false

		for i := range items {
			if item, ok := cleanCodexInputItem(items[i], !store); ok {
				items[i] = item
				inputChanged = true
			}
		}

		if inputChanged {
			body["input"], _ = json.Marshal(items)
			changed = true
		}
	}

	if changed {
		data, _ = json.Marshal(body)
	}

	setRequestBody(req, data)

	return threshold, nil
}

// setRequestBody replaces req's body with data, which GetBody replays.
func setRequestBody(req *http.Request, data []byte) {
	req.Body = io.NopCloser(bytes.NewReader(data))
	req.ContentLength = int64(len(data))
	req.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(data)), nil }
}

// requestFields decodes the JSON object that cleanCodexRequest buffered behind req's byte-backed GetBody.
func requestFields(req *http.Request) map[string]json.RawMessage {
	requestBody, _ := req.GetBody()
	data, _ := io.ReadAll(requestBody)
	_ = requestBody.Close()

	var fields map[string]json.RawMessage

	_ = json.Unmarshal(data, &fields)

	return fields
}

func cleanCodexInputItem(raw json.RawMessage, stripIDs bool) (json.RawMessage, bool) {
	var item struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &item); err != nil {
		return nil, false
	}

	if item.Type == "compaction" {
		var compaction responses.ResponseCompactionItemParam
		if err := json.Unmarshal(raw, &compaction); err != nil {
			return nil, false
		}

		projected := responses.ResponseCompactionItemParam{EncryptedContent: compaction.EncryptedContent, Type: compaction.Type}
		if !stripIDs {
			projected.ID = compaction.ID
		}

		data, _ := json.Marshal(projected)

		return data, true
	}

	if !stripIDs {
		return nil, false
	}

	var object map[string]json.RawMessage
	// The type decode above already established a valid JSON object or null.
	_ = json.Unmarshal(raw, &object)

	if _, ok := object["id"]; !ok {
		return nil, false
	}

	delete(object, "id")

	data, _ := json.Marshal(object)

	return data, true
}

func (t *transport) codexCompaction(ctx context.Context, req *http.Request, resp *http.Response, threshold *float64) (*http.Response, error) {
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read Codex response: %w", err)
	}

	_ = resp.Body.Close()
	resp.Body = io.NopCloser(bytes.NewReader(data))
	resp.ContentLength = int64(len(data))

	if threshold == nil {
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

	if usage.TotalTokens < *threshold {
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

	body["output"], _ = json.Marshal(append([]json.RawMessage{data}, output...))

	data, _ = json.Marshal(body)

	resp.Body = io.NopCloser(bytes.NewReader(data))
	resp.ContentLength = int64(len(data))

	return resp, nil
}

func (t *transport) retryCodexAfterCompaction(ctx context.Context, req *http.Request, threshold *float64) (*http.Response, bool, error) {
	if threshold == nil {
		return nil, false, nil
	}

	compactItem, ok, err := t.codexCompactItem(ctx, req)
	if err != nil || !ok {
		return nil, false, nil
	}

	// A compaction threshold comes only from cleanCodexRequest's buffered JSON.
	retryBody := requestFields(req)

	retryItem := compactItem
	if item, ok := cleanCodexInputItem(compactItem, false); ok {
		retryItem = item
	}

	input, _ := json.Marshal([]json.RawMessage{retryItem})

	retryBody["input"] = input

	data, _ := json.Marshal(retryBody)

	retry := req.Clone(ctx)
	setRequestBody(retry, data)

	resp, err := t.roundTrip(retry, "compacted_retry")
	if err != nil {
		return nil, true, err
	}

	if resp.StatusCode != http.StatusOK {
		return resp, true, nil
	}

	resp, err = codexStreamingResponse(resp)

	return resp, true, err
}

func (t *transport) codexCompactItem(ctx context.Context, req *http.Request) (item json.RawMessage, ok bool, err error) {
	compactSource := requestFields(req)
	if codexInputHasUnansweredFunctionCall(compactSource["input"]) {
		return nil, false, nil
	}

	// Streaming request preparation always supplies instructions, even when empty.
	compactRequest := map[string]json.RawMessage{
		"input":        compactSource["input"],
		"model":        compactSource["model"],
		"instructions": compactSource["instructions"],
	}

	data, _ := json.Marshal(compactRequest)

	compactReq := req.Clone(ctx)
	compactReq.URL.Path = strings.TrimRight(compactReq.URL.Path, "/") + "/compact"
	setRequestBody(compactReq, data)

	compactResp, err := t.roundTrip(compactReq, "compaction")
	if err != nil {
		return nil, false, err
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
		// Types are valid JSON; non-string values leave "", rejected by the discriminator.
		_ = json.Unmarshal(item["type"], &itemType)

		if itemType != "compaction" && itemType != "compaction_summary" {
			continue
		}

		item["type"] = json.RawMessage(`"compaction"`)
		delete(item, "recent")

		data, _ = json.Marshal(item)

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
			// Only calls still awaiting an output keep an entry.
			pending[item.CallID]--
			if pending[item.CallID] <= 0 {
				delete(pending, item.CallID)
			}
		}
	}

	return len(pending) > 0
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
			var response struct {
				Error struct {
					Code    string `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			}

			if len(event.Response) > 0 {
				if err := json.Unmarshal(event.Response, &response); err != nil {
					return nil, fmt.Errorf("parse Codex failed response: %w", err)
				}
			}

			errResponse = &codexStreamError{Code: response.Error.Code, Message: response.Error.Message}

			continue
		}

		if event.Type == "response.incomplete" {
			var response struct {
				IncompleteDetails struct {
					Reason string `json:"reason"`
				} `json:"incomplete_details"`
			}

			if len(event.Response) > 0 {
				if err := json.Unmarshal(event.Response, &response); err != nil {
					return nil, fmt.Errorf("parse Codex incomplete response: %w", err)
				}
			}

			errResponse = fmt.Errorf("codex stream response incomplete: %s", cmp.Or(strings.TrimSpace(response.IncompleteDetails.Reason), "unknown"))

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
			outputData, _ := json.Marshal(outputs)

			response["output"] = outputData

			event.Response, _ = json.Marshal(response)
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

func generatePKCE() pkceCodes {
	verifier := randomString(43)

	sum := sha256.Sum256([]byte(verifier))

	return pkceCodes{verifier: verifier, challenge: base64.RawURLEncoding.EncodeToString(sum[:])}
}

func randomString(length int) string {
	buf := make([]byte, length)
	_, _ = rand.Read(buf)

	return base64.RawURLEncoding.EncodeToString(buf)
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

func loginCommand(provider string) string {
	if provider == "openai" {
		return "rocketclaw oai login"
	}

	return "rocketclaw oai login " + provider
}

func postToken(ctx context.Context, form url.Values) (tokenResponse, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, issuer+"/oauth/token", strings.NewReader(form.Encode()))

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
	body, _ := json.Marshal(map[string]string{"device_auth_id": deviceAuthID, "user_code": userCode})

	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, issuer+"/api/accounts/deviceauth/token", bytes.NewReader(body))

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

func tokenFromResponse(response tokenResponse) Token {
	expiresIn := response.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = 3600
	}

	return Token{Refresh: response.RefreshToken, Access: response.AccessToken, Expires: time.Now().Add(time.Duration(expiresIn) * time.Second).UnixMilli(), AccountID: extractAccountID(response)}
}

func extractAccountID(response tokenResponse) string {
	for _, token := range []string{response.IDToken, response.AccessToken} {
		claims, ok := parseClaims(token)
		if !ok {
			continue
		}

		if id := cmp.Or(claims.ChatGPTAccountID, claims.OpenAIAuth.ChatGPTAccountID); id != "" {
			return id
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

	var parsed claims

	return parsed, err == nil && json.Unmarshal(data, &parsed) == nil
}
