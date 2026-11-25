package backend

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/gorilla/websocket"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/harness"
)

const runWithSecretsToolName = "rocketclaw_run_with_secrets"

const openCodeRunScript = `read -r _ || exit; printf '\036'; { status=$({ { sh -c "$1" </dev/null 2>&1 3>&- 4>&-; echo "$?" >&4; } | cat >&3; } 4>&1); } 3>&1; exit "$status"`

func (s *SessionService) openCodeSecretStore(conversation string) *harness.TurnSecretStore {
	s.secretsMu.Lock()
	defer s.secretsMu.Unlock()

	if _, ok := s.openCodeSecrets[conversation]; !ok {
		s.openCodeSecrets[conversation] = harness.NewTurnSecretStore()
	}

	return s.openCodeSecrets[conversation]
}

func (s *SessionService) endOpenCodeRun(conversation string) {
	s.secretsMu.Lock()
	defer s.secretsMu.Unlock()

	if secrets, ok := s.openCodeSecrets[conversation]; ok {
		secrets.Erase()
		delete(s.openCodeSecrets, conversation)
	}

	for key, ask := range s.secrets {
		if key.conversationID == conversation {
			delete(s.secrets, key)
			close(ask.answered)
		}
	}
}

func openCodeRunTool(name string, server config.OpenCodeConfig, session string, secrets *harness.TurnSecretStore) harness.Tool {
	return harness.Tool{Name: runWithSecretsToolName, Description: "Run command with sh in this session's folder on the OpenCode server, with every secret ask_secret saved in this run set as its environment variable, and return the exit code and the output, where each secret's value shows as [secret:NAME]. It has no terminal: stdin is empty, and pagers and prompts are off. Use it for every command that needs a secret; your own shell tool never gets them.", Parameters: map[string]any{"properties": map[string]any{"command": map[string]any{"type": "string"}}, "required": []string{"command"}}, Call: func(ctx context.Context, raw json.RawMessage, _ chan<- harness.ChatResponse) (harness.ToolResult, error) {
		var input struct {
			Command string `json:"command"`
		}
		if err := json.Unmarshal(raw, &input); err != nil {
			return harness.ToolResult{}, fmt.Errorf("parse run_with_secrets: %w", err)
		}

		env, given, names := map[string]string{}, harness.NewTurnSecretStore(), []string{}
		defer given.Erase()

		for _, entry := range secrets.Env() {
			secret, value, _ := strings.Cut(entry, "=")
			env[secret], names = value, append(names, "$"+secret)
			_ = given.Save(secret, []byte(value))
		}

		if len(names) == 0 {
			return harness.ToolResult{}, errors.New("no secrets held; call ask_secret first")
		}

		var found struct {
			Data openCodeSession `json:"data"`
		}
		if err := openCodeCall(ctx, name, server, http.MethodGet, "/api/session/"+url.PathEscape(session), nil, &found); err != nil {
			return harness.ToolResult{}, err
		}

		location := "?" + url.Values{"location[directory]": {found.Data.Location.Directory}}.Encode()

		var terminal struct {
			Data struct {
				ID       string `json:"id"`
				ExitCode int    `json:"exitCode"`
			} `json:"data"`
		}

		create := struct {
			Command string            `json:"command"`
			Args    []string          `json:"args"`
			Env     map[string]string `json:"env"`
		}{"/usr/bin/env", []string{"TERM=dumb", "PAGER=cat", "GIT_PAGER=cat", "GIT_TERMINAL_PROMPT=0", "GH_PROMPT_DISABLED=1", "sh", "-c", openCodeRunScript, "sh", input.Command}, env}
		if err := openCodeCall(ctx, name, server, http.MethodPost, "/api/pty"+location, create, &terminal); err != nil {
			return harness.ToolResult{}, err
		}

		path := "/api/pty/" + url.PathEscape(terminal.Data.ID)
		defer func() {
			_ = openCodeCall(context.WithoutCancel(ctx), name, server, http.MethodDelete, path+location, nil, nil)
		}()

		conn, response, err := websocket.DefaultDialer.DialContext(ctx, "ws"+strings.TrimPrefix(strings.TrimSuffix(server.URL, "/"), "http")+path+"/connect"+location, http.Header{"Authorization": {"Basic " + base64.StdEncoding.EncodeToString([]byte("opencode:"+server.Password))}})
		if err != nil {
			return harness.ToolResult{}, fmt.Errorf("attach to OpenCode terminal: %w", err)
		}
		defer func() { _ = response.Body.Close() }()
		defer func() { _ = conn.Close() }()
		defer context.AfterFunc(ctx, func() { _ = conn.Close() })()

		var output bytes.Buffer

		err = conn.WriteMessage(websocket.TextMessage, []byte("\n"))
		for err == nil {
			var frame []byte
			if _, frame, err = conn.ReadMessage(); err == nil && !bytes.HasPrefix(frame, []byte{0}) {
				output.Write(frame)
			}
		}

		if !websocket.IsCloseError(err, websocket.CloseNormalClosure) {
			return harness.ToolResult{}, fmt.Errorf("run in OpenCode terminal: %w", err)
		}

		if err := openCodeCall(ctx, name, server, http.MethodGet, path+location, nil, &terminal); err != nil {
			return harness.ToolResult{}, err
		}

		_, ran, _ := strings.Cut(strings.ReplaceAll(output.String(), "\r\n", "\n"), "\036")

		return harness.TextToolResult(fmt.Sprintf("Ran with %s set; exit code %d.\n%s", strings.Join(names, ", "), terminal.Data.ExitCode, secrets.Redact(given.Redact(ran)))), nil
	}}
}
