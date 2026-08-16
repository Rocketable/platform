package rocketcode

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"

	"github.com/gorilla/websocket"
	openai "github.com/openai/openai-go/v3"
)

type responsesWebsocketDoer struct {
	http *http.Client
	mu   sync.Mutex
	conn *websocket.Conn
}

func newResponsesAPI(client *openai.Client) responseServiceClient {
	return responseServiceClient{service: &client.Responses, doer: &responsesWebsocketDoer{http: http.DefaultClient}}
}

func (d *responsesWebsocketDoer) Do(req *http.Request) (*http.Response, error) {
	if req.URL.Scheme != "ws" && req.URL.Scheme != "wss" {
		resp, errDo := d.http.Do(req)
		if errDo != nil {
			return nil, fmt.Errorf("do responses http request: %w", errDo)
		}

		return resp, nil
	}

	if strings.Contains(req.URL.Path, "/compact") {
		httpReq := req.Clone(req.Context())
		if req.URL.Scheme == "wss" {
			httpReq.URL.Scheme = "https"
		} else {
			httpReq.URL.Scheme = "http"
		}

		resp, errDo := d.http.Do(httpReq)
		if errDo != nil {
			return nil, fmt.Errorf("do responses compact request: %w", errDo)
		}

		return resp, nil
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	return d.doCreate(req)
}

func (d *responsesWebsocketDoer) doCreate(req *http.Request) (*http.Response, error) {
	body, errRead := io.ReadAll(req.Body)
	_ = req.Body.Close()

	if errRead != nil {
		return nil, fmt.Errorf("read responses websocket body: %w", errRead)
	}

	var payload map[string]any
	if errUnmarshal := json.Unmarshal(body, &payload); errUnmarshal != nil {
		return nil, fmt.Errorf("decode responses websocket body: %w", errUnmarshal)
	}

	payload["type"] = "response.create"
	delete(payload, "stream")
	delete(payload, "background")

	create, errMarshal := json.Marshal(payload)
	if errMarshal != nil {
		return nil, fmt.Errorf("encode responses websocket create: %w", errMarshal)
	}

	if errWrite := d.writeCreate(req, create); errWrite != nil {
		d.resetConn()
		return nil, errWrite
	}

	return d.readCreate(req)
}

func (d *responsesWebsocketDoer) writeCreate(req *http.Request, payload []byte) error {
	if d.conn == nil {
		header := make(http.Header)

		for _, key := range []string{"Authorization", "OpenAI-Organization", "OpenAI-Project", "OpenAI-Beta"} {
			if values := req.Header.Values(key); len(values) > 0 {
				header[key] = slices.Clone(values)
			}
		}

		conn, resp, errDial := websocket.DefaultDialer.DialContext(req.Context(), req.URL.String(), header)
		if resp != nil {
			_ = resp.Body.Close()
		}

		if errDial != nil {
			return fmt.Errorf("dial responses websocket: %w", errDial)
		}

		d.conn = conn
	}

	deadline, _ := req.Context().Deadline()
	if errDeadline := d.conn.SetWriteDeadline(deadline); errDeadline != nil {
		return fmt.Errorf("set responses websocket write deadline: %w", errDeadline)
	}

	if errWrite := d.conn.WriteMessage(websocket.TextMessage, payload); errWrite != nil {
		return fmt.Errorf("write responses websocket create: %w", errWrite)
	}

	return nil
}

func (d *responsesWebsocketDoer) readCreate(req *http.Request) (*http.Response, error) {
	deadline, _ := req.Context().Deadline()
	if errDeadline := d.conn.SetReadDeadline(deadline); errDeadline != nil {
		return nil, fmt.Errorf("set responses websocket read deadline: %w", errDeadline)
	}

	for {
		_, message, errRead := d.conn.ReadMessage()
		if errRead != nil {
			d.resetConn()
			return nil, fmt.Errorf("read responses websocket event: %w", errRead)
		}

		var event struct {
			Type     string          `json:"type"`
			Status   int             `json:"status"`
			Error    json.RawMessage `json:"error"`
			Response json.RawMessage `json:"response"`
		}
		if errUnmarshal := json.Unmarshal(message, &event); errUnmarshal != nil {
			return nil, fmt.Errorf("decode responses websocket event: %w", errUnmarshal)
		}

		switch event.Type {
		case "response.completed", "response.failed", "response.incomplete":
			return jsonHTTPResponse(req, http.StatusOK, event.Response)
		case "error":
			body := event.Error
			if len(body) == 0 {
				body = message
			} else {
				wrapped, errMarshal := json.Marshal(map[string]json.RawMessage{"error": body})
				if errMarshal != nil {
					return nil, fmt.Errorf("encode responses websocket error: %w", errMarshal)
				}

				body = wrapped
			}

			return jsonHTTPResponse(req, event.Status, body)
		}
	}
}

func (d *responsesWebsocketDoer) resetConn() {
	if d.conn == nil {
		return
	}

	_ = d.conn.Close()
	d.conn = nil
}

func jsonHTTPResponse(req *http.Request, status int, body []byte) (*http.Response, error) {
	if len(body) == 0 {
		return nil, errors.New("missing responses websocket payload")
	}

	return &http.Response{
		StatusCode:    status,
		Status:        fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Header:        http.Header{"Content-Type": []string{"application/json"}},
		Body:          io.NopCloser(bytes.NewReader(body)),
		ContentLength: int64(len(body)),
		Request:       req,
	}, nil
}
