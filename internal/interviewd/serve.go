package interviewd

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"
)

type submission struct {
	Answers []answer
}

type answer struct {
	Question question
	Values   []string
	Text     string
	Comment  string
}

func waitForUser(ctx context.Context, store store, id string, out io.Writer) error {
	iv, err := store.loadInterview(id)
	if err != nil {
		return err
	}

	prepared, err := store.loadPrepared(id)
	if err != nil {
		return err
	}

	if len(prepared.RenderedHTML) != len(iv.Questions) {
		return errors.New("prepared form is stale; run prepare-to-serve again")
	}

	ln, err := net.Listen("tcp4", fmt.Sprintf("0.0.0.0:%d", prepared.Port))
	if err != nil {
		return fmt.Errorf("cannot bind prepared port %d; rerun prepare-to-serve: %w", prepared.Port, err)
	}

	result := make(chan submission, 1)
	mux := http.NewServeMux()
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 15 * time.Second}
	state := &sessionState{}
	path := "/" + id
	mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		session, nonce, ok := state.claim(r)
		if !ok {
			http.Error(w, "this form is already open in another browser session", http.StatusConflict)
			return
		}

		setSessionCookie(w, session)
		renderForm(w, id, iv, prepared, nonce)
	})
	mux.HandleFunc(path+"/submit", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		answers, err := parseSubmission(r, iv, state)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		renderSuccess(w)

		select {
		case result <- submission{Answers: answers}:
			go func() {
				_ = server.Shutdown(context.Background())
			}()
		default:
		}
	})

	serveErr := make(chan error, 1)

	go func() {
		serveErr <- server.Serve(ln)
	}()

	for {
		select {
		case sub := <-result:
			if err := store.deleteInterview(id); err != nil {
				return err
			}

			if _, err := fmt.Fprint(out, renderAnswers(sub.Answers)); err != nil {
				return fmt.Errorf("write answers: %w", err)
			}

			return nil
		case err := <-serveErr:
			if err != nil && err != http.ErrServerClosed {
				return err
			}
		case <-ctx.Done():
			_ = server.Shutdown(context.Background())
			return fmt.Errorf("wait for user: %w", ctx.Err())
		}
	}
}

type sessionState struct {
	mu      sync.Mutex
	session string
	nonce   string
}

func (s *sessionState) claim(r *http.Request) (session, nonce string, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.session == "" {
		s.session = randomHex(16)
		s.nonce = randomHex(16)

		return s.session, s.nonce, true
	}

	cookie, err := r.Cookie("interviewd_session")
	if err != nil || cookie.Value != s.session {
		return "", "", false
	}

	return s.session, s.nonce, true
}

func (s *sessionState) validate(r *http.Request) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	cookie, err := r.Cookie("interviewd_session")
	if err != nil || cookie.Value != s.session {
		return false
	}

	return r.FormValue("nonce") == s.nonce
}

func setSessionCookie(w http.ResponseWriter, session string) {
	http.SetCookie(w, &http.Cookie{Name: "interviewd_session", Value: session, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode})
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}

	return hex.EncodeToString(b)
}

func renderForm(w http.ResponseWriter, id string, iv *interview, prepared *prepared, nonce string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = fmt.Fprint(w, "<!doctype html><html><head><meta charset=\"utf-8\"><meta name=\"viewport\" content=\"width=device-width, initial-scale=1\"><title>Interview</title><style>body{font-family:system-ui,sans-serif;max-width:820px;margin:2rem auto;padding:0 1rem;line-height:1.5}fieldset{border:1px solid #ddd;border-radius:8px;margin:1.5rem 0;padding:1rem}label{display:block;margin:.5rem 0}textarea{width:100%;min-height:9rem}button{font:inherit;padding:.7rem 1rem}</style></head><body>")
	_, _ = fmt.Fprintf(w, "<form method=\"post\" action=\"/%s/submit\">", html.EscapeString(id))
	_, _ = fmt.Fprintf(w, "<input type=\"hidden\" name=\"nonce\" value=\"%s\">", html.EscapeString(nonce))

	for i, q := range iv.Questions {
		_, _ = fmt.Fprintf(w, "<fieldset><legend>Question %d</legend><div>%s</div>", i+1, prepared.RenderedHTML[i])
		name := fmt.Sprintf("q%d", i)

		switch q.Kind {
		case "radio":
			for _, option := range q.Options {
				_, _ = fmt.Fprintf(w, "<label><input type=\"radio\" name=\"%s\" value=\"%s\" required> %s</label>", name, html.EscapeString(option), html.EscapeString(option))
			}
		case "checkbox":
			for _, option := range q.Options {
				_, _ = fmt.Fprintf(w, "<label><input type=\"checkbox\" name=\"%s\" value=\"%s\"> %s</label>", name, html.EscapeString(option), html.EscapeString(option))
			}
		case "text":
			_, _ = fmt.Fprintf(w, "<textarea name=\"%s\" required></textarea>", name)
		}

		if q.WithTextarea {
			_, _ = fmt.Fprintf(w, "<label>Additional notes <textarea name=\"%s_comment\"></textarea></label>", name)
		}

		_, _ = fmt.Fprint(w, "</fieldset>")
	}

	_, _ = fmt.Fprint(w, "<button type=\"submit\">Submit</button></form></body></html>")
}

func parseSubmission(r *http.Request, iv *interview, state *sessionState) ([]answer, error) {
	if err := r.ParseForm(); err != nil {
		return nil, fmt.Errorf("parse form: %w", err)
	}

	if !state.validate(r) {
		return nil, errors.New("invalid browser session")
	}

	answers := make([]answer, len(iv.Questions))
	for i, q := range iv.Questions {
		name := fmt.Sprintf("q%d", i)

		answer := answer{Question: q, Comment: strings.TrimSpace(r.FormValue(name + "_comment"))}
		switch q.Kind {
		case "radio":
			value := r.FormValue(name)
			if value == "" || !contains(q.Options, value) {
				return nil, fmt.Errorf("question %d requires one valid option", i+1)
			}

			answer.Values = []string{value}
		case "checkbox":
			values := r.Form[name]
			if len(values) == 0 {
				return nil, fmt.Errorf("question %d requires at least one option", i+1)
			}

			for _, value := range values {
				if !contains(q.Options, value) {
					return nil, fmt.Errorf("question %d contains an invalid option", i+1)
				}
			}

			answer.Values = values
		case "text":
			text := strings.TrimSpace(r.FormValue(name))
			if text == "" {
				return nil, fmt.Errorf("question %d requires text", i+1)
			}

			answer.Text = text
		}

		answers[i] = answer
	}

	return answers, nil
}

func renderSuccess(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = fmt.Fprint(w, "<!doctype html><html><head><meta charset=\"utf-8\"><title>Submitted</title></head><body><p>Submitted. You can close this tab.</p><script>window.close()</script></body></html>")
}

func renderAnswers(answers []answer) string {
	var b strings.Builder

	for i, answer := range answers {
		if i > 0 {
			b.WriteByte('\n')
		}

		fmt.Fprintf(&b, "## Question %d\n", i+1)
		b.WriteString(answer.Question.Body)
		b.WriteString("\n\n")
		b.WriteString("### Answer\n")

		if answer.Question.Kind == "text" {
			b.WriteString(answer.Text)
			b.WriteByte('\n')
		} else {
			for _, value := range answer.Values {
				fmt.Fprintf(&b, "- %s\n", value)
			}

			if answer.Comment != "" {
				b.WriteString("\n### Comment\n")
				b.WriteString(answer.Comment)
				b.WriteByte('\n')
			}
		}
	}

	return b.String()
}

func contains(values []string, want string) bool {
	return slices.Contains(values, want)
}
