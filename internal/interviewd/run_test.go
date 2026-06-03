package interviewd

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"testing"
)

func TestQuestionLifecycle(t *testing.T) {
	store := store{root: filepath.Join(t.TempDir(), ".interviewd")}

	iv := &interview{ID: "interview-test"}
	if err := store.saveInterview(iv); err != nil {
		t.Fatal(err)
	}

	if err := addQuestion(store, iv.ID, []string{"first", "radio", "a", "b", "--with-textarea"}); err != nil {
		t.Fatal(err)
	}

	if err := addQuestion(store, iv.ID, []string{"second", "checkbox", "x", "y"}); err != nil {
		t.Fatal(err)
	}

	if err := addQuestion(store, iv.ID, []string{"third", "text"}); err != nil {
		t.Fatal(err)
	}

	if err := reorderQuestions(store, iv.ID, []string{"3", "1", "2"}); err != nil {
		t.Fatal(err)
	}

	loaded, err := store.loadInterview(iv.ID)
	if err != nil {
		t.Fatal(err)
	}

	if got := loaded.Questions[0].Body; got != "third" {
		t.Fatalf("first reordered question = %q, want third", got)
	}

	if err := deleteQuestion(store, iv.ID, []string{"2"}); err != nil {
		t.Fatal(err)
	}

	loaded, err = store.loadInterview(iv.ID)
	if err != nil {
		t.Fatal(err)
	}

	if len(loaded.Questions) != 2 || loaded.Questions[1].Body != "second" {
		t.Fatalf("unexpected questions after delete: %#v", loaded.Questions)
	}
}

func TestRunWithoutArgsShowsHelpSuccessfully(t *testing.T) {
	if err := Run(context.Background(), "interviewd", nil); err != nil {
		t.Fatal(err)
	}

	help := helpText("interviewd")
	for _, want := range []string{"Successful flow:", "interviewd new", "add-question", "prepare-to-serve", "wait-for-user"} {
		if !strings.Contains(help, want) {
			t.Fatalf("help missing %q:\n%s", want, help)
		}
	}
}

func TestInvocationCommand(t *testing.T) {
	goRunPath := filepath.Join(os.TempDir(), "go-build123", "b001", "exe", "interviewd")

	info, ok := debug.ReadBuildInfo()
	if !ok || info.Path == "" {
		t.Fatal("build info path is unavailable")
	}

	want := "go run " + info.Path
	if got := invocationCommand(goRunPath); got != want {
		t.Fatalf("go run invocation = %q, want %q", got, want)
	}

	if got := invocationCommand("./interviewd"); got != "./interviewd" {
		t.Fatalf("binary invocation = %q, want ./interviewd", got)
	}
}

func TestPreviewUsesSourceMarkdown(t *testing.T) {
	iv := &interview{Questions: []question{{Body: "**pick one**", Kind: "radio", Options: []string{"a", "b"}, WithTextarea: true}}}

	preview := renderPreview(iv)
	for _, want := range []string{"## Question 1", "**pick one**", "( ) a", "[ with-textarea ]", "{{ SUBMIT }}"} {
		if !strings.Contains(preview, want) {
			t.Fatalf("preview missing %q:\n%s", want, preview)
		}
	}
}

func TestMarkdownClientRender(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", r.Method)
		}

		if got := r.Header.Get("X-Github-Api-Version"); got != "2026-03-10" {
			t.Fatalf("api version = %q", got)
		}

		_, _ = w.Write([]byte("<p>Hello</p>"))
	}))
	defer server.Close()

	html, err := (markdownClient{endpoint: server.URL, client: server.Client()}).render(context.Background(), "Hello")
	if err != nil {
		t.Fatal(err)
	}

	if html != "<p>Hello</p>" {
		t.Fatalf("html = %q", html)
	}
}

func TestRenderAnswers(t *testing.T) {
	answers := []answer{
		{Question: question{Body: "Pick", Kind: "checkbox"}, Values: []string{"a", "b"}, Comment: "note"},
		{Question: question{Body: "Explain", Kind: "text"}, Text: "details"},
	}

	got := renderAnswers(answers)
	for _, want := range []string{"## Question 1", "Pick", "- a", "- b", "### Comment", "note", "## Question 2", "details"} {
		if !strings.Contains(got, want) {
			t.Fatalf("answers missing %q:\n%s", want, got)
		}
	}
}

func TestRunCreatesProjectLocalState(t *testing.T) {
	dir := t.TempDir()

	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	defer func() {
		if err := os.Chdir(old); err != nil {
			t.Fatal(err)
		}
	}()

	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}

	if err := Run(context.Background(), "interviewd", []string{"new"}); err != nil {
		t.Fatal(err)
	}

	entries, err := os.ReadDir(filepath.Join(dir, ".interviewd"))
	if err != nil {
		t.Fatal(err)
	}

	if len(entries) != 1 || !strings.HasPrefix(entries[0].Name(), "interview-") {
		t.Fatalf("unexpected state entries: %#v", entries)
	}
}
