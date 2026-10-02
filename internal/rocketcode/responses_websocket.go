package rocketcode

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/gorilla/websocket"
	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/responses"
)

type responseObserver interface {
	beginResponse(context.Context) error
	observeResponse(context.Context, *responses.ResponseStreamEventUnion) error
}

type inertResponseObserver struct{}

func (inertResponseObserver) beginResponse(context.Context) error {
	return nil
}

func (inertResponseObserver) observeResponse(context.Context, *responses.ResponseStreamEventUnion) error {
	return nil
}

// responseObservations buffers only public text, never provider replay or arguments.
// One provider request owns it across SDK, looper, and compaction retries.
type responseObservations struct {
	owner      *turnObservations
	agent      string
	model      string
	responseID string
	text       []PublicProgress
}

func (o *responseObservations) beginResponse(ctx context.Context) error {
	if len(o.text) > 0 {
		if err := o.owner.replaceResponse(ctx, o.text[0].ParentID, nil); err != nil {
			return err
		}
	}

	o.responseID, o.text = "", nil

	return nil
}

func (o *responseObservations) observeResponse(ctx context.Context, event *responses.ResponseStreamEventUnion) error {
	parentID := o.owner.turnID + "/" + o.responseID

	switch event.Type {
	case "response.created":
		o.responseID = event.Response.ID
		return nil
	case "response.output_text.delta", "response.output_text.done":
		id := event.ItemID + "/" + strconv.FormatInt(event.ContentIndex, 10)

		index := slices.IndexFunc(o.text, func(item PublicProgress) bool { return item.ID == id })
		if index < 0 {
			index = len(o.text)
			o.text = append(o.text, PublicProgress{ID: id, ParentID: parentID, Kind: PublicProgressText, State: PublicProgressWorking, Agent: o.agent, Model: o.model})
		}

		if event.Type == "response.output_text.done" {
			o.text[index].Text = event.Text
		} else {
			o.text[index].Text += event.Delta
		}
	case "response.completed":
		parentID = o.owner.turnID + "/" + event.Response.ID
		o.text = nil

		for i := range event.Response.Output {
			item := &event.Response.Output[i]
			if item.Type != "message" || item.Role != "assistant" {
				continue
			}

			for index := range item.Content {
				content := &item.Content[index]
				if content.Type == "output_text" {
					o.text = append(o.text, PublicProgress{ID: item.ID + "/" + strconv.Itoa(index), ParentID: parentID, Kind: PublicProgressText, State: PublicProgressCompleted, Text: content.Text, Agent: o.agent, Model: o.model})
				}
			}
		}
	case "response.failed", "response.incomplete":
		for index := range o.text {
			o.text[index].State = PublicProgressFailed
			if event.Type == "response.incomplete" {
				o.text[index].State = PublicProgressStopped
			}
		}
	default:
		return nil
	}

	if err := o.owner.replaceResponse(ctx, parentID, o.text); err != nil {
		return err
	}

	if event.Type == "response.completed" {
		o.text = nil // The next successful request must retain completed display fallback.
	}

	return nil
}

type responsesWebsocketDoer struct {
	mu   sync.Mutex
	conn *websocket.Conn
}

func newResponsesAPI(client *openai.Client) responseServiceClient {
	return responseServiceClient{service: &client.Responses, doer: &responsesWebsocketDoer{}}
}

func (d *responsesWebsocketDoer) middleware(req *http.Request, next option.MiddlewareNext, observer responseObserver) (*http.Response, error) {
	if req.URL.Scheme != "ws" && req.URL.Scheme != "wss" {
		return next(req)
	}

	if strings.Contains(req.URL.Path, "/compact") {
		// openai-go origin checks reject scheme rewrites against a ws base URL, so
		// compact must leave the middleware chain and dial HTTP itself.
		return d.doCompactHTTP(req)
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	resp, err := d.doCreate(req, observer)
	if err != nil {
		d.resetConn()
	}

	return resp, err
}

func (d *responsesWebsocketDoer) doCompactHTTP(req *http.Request) (*http.Response, error) {
	httpReq := req.Clone(req.Context())
	if req.URL.Scheme == "wss" {
		httpReq.URL.Scheme = "https"
	} else {
		httpReq.URL.Scheme = "http"
	}

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("compact responses over http: %w", err)
	}

	return resp, nil
}

func (d *responsesWebsocketDoer) doCreate(req *http.Request, observer responseObserver) (*http.Response, error) {
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

	if err := observer.beginResponse(req.Context()); err != nil {
		return nil, err
	}

	if errWrite := d.writeCreate(req, create); errWrite != nil {
		return nil, errWrite
	}

	return d.readCreate(req, observer)
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

func (d *responsesWebsocketDoer) readCreate(req *http.Request, observer responseObserver) (*http.Response, error) {
	conn := d.conn

	stop := context.AfterFunc(req.Context(), func() { _ = conn.Close() })
	defer stop()

	deadline, _ := req.Context().Deadline()
	if errDeadline := d.conn.SetReadDeadline(deadline); errDeadline != nil {
		return nil, fmt.Errorf("set responses websocket read deadline: %w", errDeadline)
	}

	for {
		_, message, errRead := d.conn.ReadMessage()
		if errRead != nil {
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
		case "response.created", "response.output_text.delta", "response.output_text.done", "response.completed", "response.failed", "response.incomplete":
			var typed responses.ResponseStreamEventUnion
			if err := json.Unmarshal(message, &typed); err != nil {
				return nil, fmt.Errorf("decode typed responses websocket event: %w", err)
			}

			if err := observer.observeResponse(req.Context(), &typed); err != nil {
				return nil, err
			}
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
