package harness

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/openai/openai-go/v3/responses"
	"github.com/stretchr/testify/require"

	"github.com/Rocketable/platform/internal/rocketclaw/codemode"
	"github.com/Rocketable/platform/internal/rocketclaw/mcpclient"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"github.com/Rocketable/platform/internal/rocketclaw/sandbox"
)

type resultModelEnv struct {
	root     *os.Root
	web      string
	tools    map[string]looperTool
	registry mcpclient.Registry
	decider  *mockDecider
	files    map[string]codemode.FileSpec

	mu        sync.Mutex
	ran       []string
	mcpCalls  []string
	webHits   []string
	cancelled int
}

type resultModelRun struct {
	text, raw string
	answer    responses.ResponseInputItemFunctionCallOutputParam
	files     []Attachment
	failed    bool
	ran       []string
	mcpCalls  []string
	webHits   []string
	decided   int
	fetched   []Attachment
}

type resultModelCase struct {
	name, expr, want string
	failed           bool
	files            []string
	ran, mcpCalls    []string
	webHits          []string
	decided          int
	fetch            int
	fetched          []string
}

func paddedPNG(t *testing.T, width, height, size int) string {
	t.Helper()

	var encoded bytes.Buffer
	require.NoError(t, png.Encode(&encoded, image.NewGray(image.Rect(0, 0, width, height))))

	return encoded.String() + strings.Repeat("\x00", size-encoded.Len())
}

func newResultModelEnv(t *testing.T) *resultModelEnv {
	t.Helper()

	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })

	env := &resultModelEnv{root: root, decider: &mockDecider{DecideFunc: func(context.Context, *DecisionRequest) ([]DecisionAnswer, DecisionUsage, error) {
		return []DecisionAnswer{{Name: "tone", Type: DecisionChoice, Choice: "calm"}}, DecisionUsage{}, nil
	}}}

	pano := exifJPEG(t, 6)
	sof := bytes.Index(pano, []byte{0xff, 0xc0})
	binary.BigEndian.PutUint16(pano[sof+5:], 6000)
	binary.BigEndian.PutUint16(pano[sof+7:], 8000)

	photoMIME, photo, err := protocol.StraightenImage("image/jpeg", string(exifJPEG(t, 6)))
	require.NoError(t, err)

	env.files = map[string]codemode.FileSpec{
		"shot.png":  {Name: "shot.png", Mime: "image/png", Content: paddedPNG(t, 1280, 800, 186412)},
		"phone.jpg": {Name: "phone.jpg", Mime: photoMIME, Content: photo},
		"spec.pdf":  {Name: "spec.pdf", Mime: "application/pdf", Content: "%PDF-1.4\nspec"},
		"a.png":     {Name: "a.png", Mime: "image/png", Content: paddedPNG(t, 1, 1, 8<<20)},
		"big.pdf":   {Name: "big.pdf", Mime: "application/pdf", Content: "%PDF-" + strings.Repeat("b", 15<<20-5)},
		"c.png":     {Name: "c.png", Mime: "image/png", Content: paddedPNG(t, 1, 1, 2<<20)},
	}

	at := time.UnixMilli(1_700_000_000_000)

	for name, content := range map[string]string{
		"notes.md": "ready", "a.md": "A", "b.md": "B", "rows.csv": "1\n2\n",
		"out/shot.png": env.files["shot.png"].Content, "phone.jpg": string(exifJPEG(t, 6)), "spec.pdf": env.files["spec.pdf"].Content,
		"pano.jpg": string(pano), "data.zip": "PK\x03\x04\x00\x00",
		"a.png": env.files["a.png"].Content, "big.pdf": env.files["big.pdf"].Content, "c.png": env.files["c.png"].Content,
	} {
		require.NoError(t, root.MkdirAll(filepath.Dir(name), 0o755))
		require.NoError(t, root.WriteFile(name, []byte(content), 0o644))
		require.NoError(t, root.Chtimes(name, at, at))
	}

	require.NoError(t, root.MkdirAll(".tmp/shell", 0o700))

	web := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		env.mu.Lock()
		env.webHits = append(env.webHits, r.URL.Path)
		env.mu.Unlock()

		w.Header()["Date"] = nil
		w.Header().Set("Content-Type", "text/plain")

		switch r.URL.Path {
		case "/missing":
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte("Not here"))
		case "/slow":
			time.Sleep(300 * time.Millisecond)

			_, _ = w.Write([]byte("slow page"))
		case "/hang":
			<-r.Context().Done()
			env.mu.Lock()
			env.cancelled++
			env.mu.Unlock()
		default:
			_, _ = w.Write([]byte("Hello"))
		}
	}))
	t.Cleanup(web.Close)
	env.web = web.URL

	url := startRocketCodeMCPHTTPServer(t, func(server *mcp.Server) {
		add := func(name string, answer func(map[string]any) *mcp.CallToolResult) {
			server.AddTool(&mcp.Tool{Name: name, InputSchema: map[string]any{"type": "object"}}, func(_ context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				env.mu.Lock()
				env.mcpCalls = append(env.mcpCalls, name)
				env.mu.Unlock()

				var args map[string]any

				_ = json.Unmarshal(request.Params.Arguments, &args)

				return answer(args), nil
			})
		}
		text := func(s string) *mcp.CallToolResult {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: s}}}
		}

		add("say", func(map[string]any) *mcp.CallToolResult { return text("said") })
		add("echo", func(args map[string]any) *mcp.CallToolResult {
			encoded, _ := json.Marshal(args["value"])

			return text(string(encoded))
		})
		add("upload", func(args map[string]any) *mcp.CallToolResult {
			image, _ := args["image"].(string)

			return text(fmt.Sprintf("uploaded %d", len(image)))
		})
		add("clip", func(map[string]any) *mcp.CallToolResult {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.AudioContent{Data: []byte("RIFF" + strings.Repeat("\x00", 48<<10-4)), MIMEType: "audio/wav"}}}
		})
		add("snap", func(map[string]any) *mcp.CallToolResult {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.ImageContent{Data: []byte(paddedPNG(t, 1, 1, 128)), MIMEType: "image/png"}}}
		})
		add("fail", func(map[string]any) *mcp.CallToolResult {
			return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "nope"}}}
		})
	})
	env.registry = mcpclient.New(t.TempDir(), map[string]mcpclient.ServerConfig{"uploader": {URL: url}}, nil)

	shellTemp := testShellTempConfig(t, root, filepath.Join(dir, ".tmp", "shell"))
	env.tools = makeSandboxedTools(&sandboxedFileSystem{mu: sync.Mutex{}, root: root}, newSandboxedShellSystem(root, &shellTemp, nil, &sandbox.Runtime{}))
	bash := env.tools["bash"]
	runBash := bash.Call
	bash.Call = func(ctx context.Context, raw json.RawMessage, output chan<- ChatResponse, metadata toolCallMetadata) (ToolResult, error) {
		var params bashParams
		require.NoError(t, json.Unmarshal(raw, &params))
		env.mu.Lock()
		env.ran = append(env.ran, params.Command)
		env.mu.Unlock()

		return runBash(ctx, raw, output, metadata)
	}
	env.tools["bash"] = bash
	env.tools["decision"] = decisionTool(map[string]Decider{"openai": env.decider}, ObservabilityConfig{})

	return env
}

func (e *resultModelEnv) script(expr string) string {
	values := map[string]string{
		"ok_txt": `read(filePath="notes.md")`, "bad_txt": `read(filePath="missing.md")`,
		"ok_sh": `bash(command="printf hi")`, "bad_sh": `bash(command="printf 'FAIL: TestLogin'; exit 2")`,
		"page": `webfetch(url=OK_URL)`, "gone": `webfetch(url=MISSING)`,
		"shot": `read(filePath="out/shot.png")`, "photo": `read(filePath="phone.jpg")`, "doc": `read(filePath="spec.pdf")`,
		"nope": `read(filePath="out/missing.png")`, "huge": `read(filePath="pano.jpg")`, "zipf": `read(filePath="data.zip")`,
		"md_path": `glob(pattern="notes.md")[0]`, "found": `glob(pattern="*.md")`,
		"hit": `grep(pattern="ready", include="notes.md")`, "hit_line": `grep(pattern="ready", include="notes.md")[0]`,
		"said": `uploader_say()`, "mcp_bad": `uploader_fail()`, "snap": `uploader_snap()`,
		"quiet":       `bash(command="grep -q ready notes.md")`,
		"bad_args":    `bash(command="true", timeout_ms="soon")`,
		"positional":  `read("notes.md")`,
		"bad_url":     `webfetch(url="ftp://example.com/x")`,
		"bad_glob":    `glob(pattern="*.md", path="../outside")`,
		"bad_grep":    `grep(pattern="", path="docs", include="*.md")`,
		"bad_verdict": `decision(model="other/x", state={}, questions=QUESTIONS)`,
		"verdict":     `decision(model="openai/gpt-6-luna", state={}, questions=QUESTIONS)`,
		"both":        `try_gather([lambda: read(filePath="a.md"), lambda: read(filePath="b.md")])`,
		"data":        `json.decode('{"name": "orchard", "tags": ["a"]}')`,
		"rows":        `json.decode("[1, 2]")`,
		"bad_json":    `json.decode("{oops")`,
	}

	var script strings.Builder

	fmt.Fprintf(&script, "OK_URL = %q\nMISSING = %q\nSLOW = %q\nHANG = %q\n", e.web+"/ok", e.web+"/missing", e.web+"/slow", e.web+"/hang")
	script.WriteString(`QUESTIONS = {"tone": {"type": "choice", "instructions": "Tone?", "criteria": {"calm": "", "angry": ""}}}` + "\n")
	fmt.Fprintf(&script, "MD = %q\n", filepath.Join(e.root.Name(), "notes.md"))
	script.WriteString("def main():\n")

	code := regexp.MustCompile(`"[^"]*"`).ReplaceAllString(expr, `""`)

	for _, name := range slices.Sorted(maps.Keys(values)) {
		if regexp.MustCompile(`\b` + name + `\b`).MatchString(code) {
			fmt.Fprintf(&script, "    %s = %s\n", name, values[name])
		}
	}

	fmt.Fprintf(&script, "    return %s\n", expr)

	return script.String()
}

func (e *resultModelEnv) normalize(text string) string {
	text = strings.NewReplacer(e.root.Name()+"/", "/w/", e.web, "http://web").Replace(text)
	text = regexp.MustCompile(`result_id="[^"]+"`).ReplaceAllString(text, `result_id="…"`)

	return regexp.MustCompile(`("timing_ms":\{"\w+":)\d+(,"\w+":)\d+`).ReplaceAllString(text, "${1}0${2}0")
}

func (e *resultModelEnv) attachments(names []string) []Attachment {
	if len(names) == 0 {
		return nil
	}

	attachments := make([]Attachment, len(names))
	for i, name := range names {
		attachments[i] = fileAttachment(codemode.NewFile(e.files[name]))
	}

	return attachments
}

func (e *resultModelEnv) run(t *testing.T, expr string, background bool, fetch int) resultModelRun {
	t.Helper()

	var (
		finished ToolResult
		errWork  error
	)

	jobs := BackgroundJobs(InertBackgroundJobs{})

	if background {
		detached := enabledBackgroundJobs()
		detached.RunFunc = func(ctx context.Context, _ *BackgroundJob, w BackgroundWork) (BackgroundResult, error) {
			finished, errWork = w.RunBackground(context.WithoutCancel(ctx))

			return BackgroundResult{Moved: true}, nil
		}
		jobs = detached
	}

	permissions := parsePermissionYAML(t, `{rocketclaw: {allow_background: allow}}`)
	for bucket, pattern := range map[string]string{"read": "*", "bash": "*", "webfetch": "*", "glob": "*", "grep": "*", "decision": "openai/*", "mcp": "uploader.*"} {
		require.NoError(t, permissions.Set(bucket, pattern, PermissionAllow))
	}

	factory := testTaskFactory(mockResponses(), Agents{Items: map[string]Agent{}})
	factory.baseTools = maps.Clone(e.tools)
	factory.backgroundJobs = jobs
	factory.retainedRel = "retained"
	factory.mcpRegistry = e.registry
	agent := Agent{Name: "main", Permission: permissions}
	loop := testLooper(mockResponses())
	loop.agent = agent
	loop.Permissions = permissions
	loop.observations = &turnObservations{journal: InertJournal{}, turnID: "turn"}
	loop.promptExpansion = promptExpansionEnvironment{root: e.root}
	loop.spillRel = defaultSpillRel
	loop.Tools, loop.CodeModeHosts = factory.assembleTools(&agent)
	loop.restoreTurnExecuteResults("turn")
	t.Cleanup(loop.deleteTurnExecuteResults)

	e.mu.Lock()
	e.ran, e.mcpCalls, e.webHits = nil, nil, nil
	e.mu.Unlock()

	decided := len(e.decider.DecideCalls())
	raw, err := json.Marshal(map[string]any{"code": e.script(expr), "description": "Result model", "background": background})
	require.NoError(t, err)

	outputs, err := loop.dispatchToolCalls(t.Context(), responseWithFunctionCalls("resp", []responses.ResponseFunctionToolCall{testFunctionCall("item", "call-1", executeToolName, string(raw))}), nil, nil, make(chan ChatResponse, 20))
	require.NoError(t, err)

	run := resultModelRun{raw: finished.Output, files: finished.Attachments, failed: errWork != nil}

	if !background {
		run.answer = outputs[0].Param

		run.raw = run.answer.Output.OfString.Value
		if items := run.answer.Output.OfResponseFunctionCallOutputItemArray; len(items) > 0 {
			run.raw = items[0].OfInputText.Text
		}

		run.failed = PublicProgressFromTrace(loop.observations.trace)[0].State == PublicProgressFailed
	}

	run.text = run.raw

	if fetch > 0 {
		page, err := loop.loadExecuteResult(t.Context(), "retained", loadExecuteResultParams{ResultID: regexp.MustCompile(`result_id="([^"]+)"`).FindStringSubmatch(run.text)[1], Attachment: fetch})
		require.NoError(t, err)

		run.fetched = page.Attachments
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	run.text = e.normalize(run.text)
	run.ran, run.mcpCalls, run.webHits, run.decided = e.ran, e.mcpCalls, slices.Sorted(slices.Values(e.webHits)), len(e.decider.DecideCalls())-decided

	return run
}

func (e *resultModelEnv) check(t *testing.T, tc *resultModelCase) {
	t.Helper()

	fg := e.run(t, tc.expr, false, tc.fetch)
	require.Equal(t, tc.want, fg.text)
	require.Equal(t, toolCallOutput("call-1", ToolResult{Output: fg.raw, Attachments: e.attachments(tc.files)}), fg.answer, "the answer's text, then its files")
	require.Equal(t, tc.failed, fg.failed, "the call is failed exactly when it returned a failure")
	require.Equal(t, []any{tc.ran, tc.mcpCalls, tc.webHits, tc.decided, e.attachments(tc.fetched)}, []any{fg.ran, fg.mcpCalls, fg.webHits, fg.decided, fg.fetched}, "calls that ran")

	bg := e.run(t, tc.expr, true, tc.fetch)
	require.Equal(t, []any{fg.text, e.attachments(tc.files), fg.failed}, []any{bg.text, bg.files, bg.failed}, "a background run delivers the same answer")
}

func TestResultModelApprovedExamples(t *testing.T) {
	env := newResultModelEnv(t)
	crash := func(message string) string {
		return "tool call failed: execute: run code mode: call main: " + message + ". Choose a different action."
	}
	okSh, badSh := []string{"printf hi"}, []string{"printf 'FAIL: TestLogin'; exit 2"}
	missing, nope := "read: File not found: missing.md", "read: File not found: out/missing.png"
	shot := func(n int) string {
		return fmt.Sprintf(`{"attachment":%d,"height":800,"mime":"image/png","name":"shot.png","size":186412,"width":1280}`, n)
	}
	photo := fmt.Sprintf(`{"attachment":1,"height":4,"mime":"image/jpeg","name":"phone.jpg","size":%d,"width":2}`, len(env.files["phone.jpg"].Content))
	gone := `{"content_type":"text/plain","error":"webfetch: HTTP 404","headers":{"content-length":"8","content-type":"text/plain"},"ok":false,"output":"Not here","redirects":[],"request":{"headers":{"accept":"text/markdown;q=1.0, text/x-markdown;q=0.9, text/plain;q=0.8, text/html;q=0.7, */*;q=0.1","accept-language":"en-US,en;q=0.9","user-agent":"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/143.0.0.0 Safari/537.36"},"method":"GET","url":"http://web/missing"},"status":404,"status_text":"Not Found","timing_ms":{"first_byte":0,"total":0},"url":"http://web/missing"}`

	for _, tc := range []resultModelCase{
		{name: "success equals its value", expr: `ok_txt == "ready"`, want: "true"},
		{name: "success contains like its value", expr: `"rea" in ok_txt`, want: "true"},
		{name: "success has its value's methods", expr: `ok_txt.upper()`, want: "READY"},
		{name: "success is truthy like its value", expr: `"taken" if ok_txt else "skipped"`, want: "taken"},
		{name: "bash success has string methods", expr: `ok_sh.strip() == "hi"`, want: "true", ran: okSh},
		{name: "webfetch success has its status", expr: `page.status`, want: "200", webHits: []string{"/ok"}},
		{name: "success unwraps to its value", expr: `ok_txt.unwrap()`, want: "ready"},
		{name: "failure is falsy", expr: `"taken" if bad_txt else "skipped"`, want: "skipped"},
		{name: "failure never equals text", expr: `bad_txt == "read: File not found"`, want: "false"},
		{name: "failure is never in a list", expr: `bad_txt in ["x", "y"]`, want: "false"},
		{name: "failures with the same error are equal", expr: `bad_txt == read(filePath="missing.md")`, want: "true"},
		{name: "ordering a failure raises", expr: `bad_txt < "z"`, want: crash("error < string not implemented"), failed: true},
		{name: "a method on a failure returns it", expr: `bad_txt.upper()`, want: missing, failed: true},
		{name: "json.decode returns a failure", expr: `json.decode(bad_txt)`, want: missing, failed: true},
		{name: "json.decode success is a result", expr: `[data.ok, rows.ok, json.decode('"s"').ok, data["name"], rows[1], len(rows)]`, want: `[true,true,true,"orchard",2,2]`},
		{name: "json.decode of a number is the plain number", expr: `json.decode("3") + 1`, want: "4"},
		{name: "json.decode success can be changed", expr: `[data.update(v=1), rows.append(3), data["v"], rows]`, want: `[null,null,1,[1,2,3]]`},
		{name: "json.decode of text that is not JSON is a failure", expr: `bad_json`, want: "json.decode: at offset 1, unexpected character 'o'", failed: true},
		{name: "json.decode of a page that is not JSON is a failure", expr: `json.decode(page).error`, want: "json.decode: at offset 0, unexpected character 'H'", webHits: []string{"/ok"}},
		{name: "bash success has len, +, slicing and indexing", expr: `[len(ok_sh), ok_sh + "!", "!" + ok_sh, ok_sh[1:], ok_sh[0]]`, want: `[2,"hi!","!hi","i","h"]`, ran: okSh},
		{name: "unwrap stops with the error", expr: `bad_txt.unwrap()`, want: crash(missing), failed: true},
		{name: "or_else gives the fallback", expr: `bad_txt.or_else("default")`, want: "default"},
		{name: "a failure as bash stdin skips bash", expr: `bash(command="wc -l", stdin=bad_txt)`, want: missing, failed: true},
		{name: "a failure as a webfetch url skips webfetch", expr: `webfetch(url=bad_txt)`, want: missing, failed: true},
		{name: "a chain carries the read that failed first", expr: `bash(command="wc -l", stdin=read(filePath="data.csv")).error`, want: "read: File not found: data.csv"},
		{name: "a chain carries the bash that failed first", expr: `bash(command="wc -l; exit 3", stdin=read(filePath="rows.csv")).error`, want: "bash: exit status 3", ran: []string{"wc -l; exit 3"}},
		{name: "text form of a success", expr: `str(ok_txt)`, want: "ready"},
		{name: "text form of an error", expr: `str(bad_txt)`, want: missing},
		{name: "text form of a failed bash", expr: `str(bad_sh)`, want: "bash: exit status 2\nFAIL: TestLogin", ran: badSh},
		{name: "text form of a failed webfetch", expr: `str(gone)`, want: "webfetch: HTTP 404\nNot here", webHits: []string{"/missing"}},
		{name: "a returned failed bash reads as its text form", expr: `bad_sh`, want: "bash: exit status 2\nFAIL: TestLogin", failed: true, ran: badSh},
		{name: "data form", expr: `{"notes": ok_txt, "tests": bad_sh, "page": gone}`, want: `{"notes":"ready","page":` + gone + `,"tests":{"error":"bash: exit status 2","error_code":"2","ok":false,"output":"FAIL: TestLogin"}}`, ran: badSh, webHits: []string{"/missing"}},
		{name: "bash success output", expr: `ok_sh.output`, want: "hi", ran: okSh},
		{name: "failed bash output", expr: `bad_sh.output`, want: "FAIL: TestLogin", ran: badSh},
		{name: "failed bash error_code", expr: `bad_sh.error_code`, want: "2", ran: badSh},
		{name: "a method on a failed bash returns it", expr: `bad_sh.split("\n")`, want: "bash: exit status 2\nFAIL: TestLogin", failed: true, ran: badSh},
		{name: "failed bash output has string methods", expr: `bad_sh.output.split("\n")`, want: `["FAIL: TestLogin"]`, ran: badSh},
		{name: "failed webfetch status", expr: `gone.status`, want: "404", webHits: []string{"/missing"}},
		{name: "failed webfetch output", expr: `gone.output`, want: "Not here", webHits: []string{"/missing"}},
		{name: "gather returns every result", expr: `gather([lambda: read(filePath="a.md"), lambda: read(filePath="missing.md")])`, want: `["A",{"error":"read: File not found: missing.md","name":"missing.md","ok":false,"path":"missing.md"}]`},
		{name: "try_gather returns the failure", expr: `try_gather([lambda: read(filePath="a.md"), lambda: read(filePath="missing.md")])`, want: missing, failed: true},
		{name: "try_gather cancels the rest", expr: `try_gather([lambda: webfetch(url=HANG), lambda: bash(command="sleep 0.2; exit 1")])`, want: "bash: exit status 1", failed: true, ran: []string{"sleep 0.2; exit 1"}, webHits: []string{"/hang"}},
		{name: "try_gather success is a result list", expr: `{"list": both, "ok": both.ok}`, want: `{"list":["A","B"],"ok":true}`},
		{name: "race returns the first success", expr: `race([lambda: webfetch(url=SLOW), lambda: webfetch(url=MISSING)])`, want: "slow page", webHits: []string{"/missing", "/slow"}},
		{name: "a returned success completes", expr: `ok_sh`, want: "hi", ran: okSh},
		{name: "a successful command with no output is falsy", expr: `["taken" if quiet else "skipped", quiet.ok, quiet.output, str(quiet)]`, want: `["skipped",true,"",""]`, ran: []string{"grep -q ready notes.md"}},
		{name: "a failed read keeps its details", expr: `[nope.path, nope.name]`, want: `["out/missing.png","missing.png"]`},
		{name: "a positional call is a failure", expr: `read("notes.md")`, want: "read: only keyword arguments are allowed; call read(filePath=...)", failed: true},
		{name: "a positional call is falsy", expr: `"taken" if read("notes.md") else "skipped"`, want: "skipped"},
		{name: "a positional call names the keyword", expr: `read("notes.md").error`, want: "read: only keyword arguments are allowed; call read(filePath=...)"},
		{name: "a positional call skips the call it is passed to", expr: `bash(command="wc -l", stdin=read("notes.md"))`, want: "read: only keyword arguments are allowed; call read(filePath=...)", failed: true},
		{name: "a positional MCP call is a failure", expr: `uploader_upload("x")`, want: "uploader.upload: only keyword arguments are allowed; use keyword arguments", failed: true},
		{name: "file details", expr: `[shot.ok, shot.name, shot.mime, shot.width, shot.height, photo.width, "taken" if shot else "skipped", shot == read(filePath="out/shot.png")]`, want: `[true,"shot.png","image/png",1280,800,2,"taken",true]`},
		{name: "a returned file", expr: `shot`, want: "Attached file 1: shot.png (image/png, 1280 × 800, 182 KB)", files: []string{"shot.png"}},
		{name: "files numbered by sorted keys", expr: `{"screen": shot, "spec": doc, "photo": photo}`, want: `{"photo":` + photo + `,"screen":` + shot(2) + `,"spec":{"attachment":3,"mime":"application/pdf","name":"spec.pdf","size":13}}`, files: []string{"phone.jpg", "shot.png", "spec.pdf"}},
		{name: "failures inside a returned dict are data", expr: `{"screen": shot, "missing": nope, "pano": huge}`, want: `{"missing":{"error":"read: File not found: out/missing.png","name":"missing.png","ok":false,"path":"out/missing.png"},"pano":{"error":"read: image too large to straighten (48 MP; limit 10.24 MP)","name":"pano.jpg","ok":false,"path":"pano.jpg"},"screen":` + shot(1) + `}`, files: []string{"shot.png"}},
		{name: "a returned failed read", expr: `nope`, want: nope, failed: true},
		{name: "a failed file as stdin skips bash", expr: `bash(command="cat > copy.png", stdin=nope)`, want: nope, failed: true},
		{name: "a failed file among decision images skips decision", expr: `decision(model="openai/gpt-6-luna", state={}, questions=QUESTIONS, images=[shot, nope])`, want: nope, failed: true},
		{name: "a file as bash stdin", expr: `[bash(command="cat > out/copy.png", stdin=shot).ok, read(filePath="out/copy.png") == shot]`, want: "[true,true]", ran: []string{"cat > out/copy.png"}},
		{name: "files as decision images", expr: `decision(model="openai/gpt-6-luna", state={}, questions=QUESTIONS, images=[shot, photo]).ok`, want: "true", decided: 1},
		{name: "a file in an MCP argument", expr: `uploader_upload(image=shot)`, want: "uploader.upload: argument image got a file object; pass shot.base64(), a path, or text", failed: true},
		{name: "base64 in an MCP argument", expr: `uploader_upload(image=shot.base64())`, want: "uploaded 248552", mcpCalls: []string{"upload"}},
		{name: "return budget", expr: `[read(filePath="a.png"), read(filePath="big.pdf"), read(filePath="c.png")]`, want: `[{"attachment":1,"height":1,"mime":"image/png","name":"a.png","size":8388608,"width":1},{"attachment":2,"deferred":true,"mime":"application/pdf","name":"big.pdf","size":15728640},{"attachment":3,"height":1,"mime":"image/png","name":"c.png","size":2097152,"width":1}]` + "\n\n" + `Not attached (over this call's 20 MiB budget): 2 big.pdf (15 MiB). Fetch with load_execute_result(result_id="…", attachment=2).`, files: []string{"a.png", "c.png"}, fetch: 2, fetched: []string{"big.pdf"}},
		{name: "a file the model cannot take", expr: `uploader_clip().content[0]`, want: "Not attached: audio-1.wav (audio/wav, 48 KB): the model cannot take audio/wav", mcpCalls: []string{"clip"}},
		{name: "a dict with a file", expr: `{"screen": shot}`, want: `{"screen":` + shot(1) + `}`, files: []string{"shot.png"}},
		{name: "a returned read is raw text", expr: `read(filePath="notes.md")`, want: "ready"},
		{name: "numbered view", expr: `read(filePath="notes.md").numbered()`, want: "1: ready"},
	} {
		t.Run(tc.name, func(t *testing.T) { env.check(t, &tc) })
	}

	require.Eventually(t, func() bool {
		env.mu.Lock()
		defer env.mu.Unlock()

		return env.cancelled == 2
	}, 10*time.Second, 10*time.Millisecond, "try_gather cancelled the hanging fetch in both runs")
}

type resultModelKind struct {
	sequence                   bool
	name, value, plain, method string
	argument, returned         string
	data, details, detailsWant string
	argumentFails              bool
	files                      []string
	failure                    *resultModelFailure
}

type resultModelFailure struct {
	error, text, data string
}

func TestResultModelKindsAndOperations(t *testing.T) {
	env := newResultModelEnv(t)
	gone := `{"content_type":"text/plain","error":"webfetch: HTTP 404","headers":{"content-length":"8","content-type":"text/plain"},"ok":false,"output":"Not here","redirects":[],"request":{"headers":{"accept":"text/markdown;q=1.0, text/x-markdown;q=0.9, text/plain;q=0.8, text/html;q=0.7, */*;q=0.1","accept-language":"en-US,en;q=0.9","user-agent":"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/143.0.0.0 Safari/537.36"},"method":"GET","url":"http://web/missing"},"status":404,"status_text":"Not Found","timing_ms":{"first_byte":0,"total":0},"url":"http://web/missing"}`
	page := `{"content_type":"text/plain","headers":{"content-length":"5","content-type":"text/plain"},"output":"Hello","redirects":[],"request":{"headers":{"accept":"text/markdown;q=1.0, text/x-markdown;q=0.9, text/plain;q=0.8, text/html;q=0.7, */*;q=0.1","accept-language":"en-US,en;q=0.9","user-agent":"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/143.0.0.0 Safari/537.36"},"method":"GET","url":"http://web/ok"},"status":200,"status_text":"OK","timing_ms":{"first_byte":0,"total":0},"url":"http://web/ok"}`
	verdict := `{"answers":{"tone":{"choice":"calm","confidence":0,"type":"choice"}},"model":"openai/gpt-6-luna","stop_reason":"stop","usage":{"input_tokens":0,"output_tokens":0}}`
	match := `{"line":1,"path":"/w/notes.md","text":"ready"}`

	kinds := []resultModelKind{
		{name: "text", sequence: true, details: `[X.path, X.name]`, detailsWant: `["notes.md","notes.md"]`, value: "ok_txt", plain: `"ready"`, method: `X.upper()`, argument: `"ready"`, returned: "ready"},
		{name: "file", details: `[X.name, X.mime]`, detailsWant: `["shot.png","image/png"]`, value: "shot", plain: `read(filePath="out/shot.png")`, method: `X.base64()[:8]`, argument: "uploader.echo: argument value got a file object; pass shot.base64(), a path, or text", argumentFails: true, returned: "Attached file 1: shot.png (image/png, 1280 × 800, 182 KB)", files: []string{"shot.png"}},
		{name: "path", sequence: true, details: `[X.path, X.name]`, detailsWant: `["/w/notes.md","notes.md"]`, value: "md_path", plain: `MD`, method: `X.endswith(".md")`, argument: `"/w/notes.md"`, returned: "/w/notes.md"},
		{name: "match", details: `[X.path, X.line, X.text]`, detailsWant: `["/w/notes.md",1,"ready"]`, value: "hit_line", plain: `hit_line`, method: `X.text.upper()`, argument: match, returned: "/w/notes.md:1: ready"},
		{name: "glob_result", details: `[X.pattern, X.path, X.partial]`, detailsWant: `["*.md","",false]`, value: "found", plain: `found`, method: `X[0].name`, argument: `["/w/a.md","/w/b.md","/w/notes.md"]`, returned: "/w/a.md\n/w/b.md\n/w/notes.md"},
		{name: "grep_result", details: `[X.pattern, X.path, X.include, X.partial]`, detailsWant: `["ready","","notes.md",false]`, value: "hit", plain: `hit`, method: `X[0].text`, argument: "[" + match + "]", returned: "Found 1 matches\n/w/notes.md:\n  Line 1: ready"},
		{name: "bash_result", sequence: true, details: `[X.output, X.error_code]`, detailsWant: `["hi",""]`, value: "ok_sh", plain: `"hi"`, method: `X.upper()`, argument: `"hi"`, returned: "hi"},
		{name: "webfetch_result", sequence: true, details: `[X.url, X.status]`, detailsWant: `["http://web/ok",200]`, value: "page", plain: `"Hello"`, method: `X.upper()`, argument: page, returned: "Hello", data: `{"url":"http://web/ok","status":200,"status_text":"OK","content_type":"text/plain","headers":{"content-length":"5","content-type":"text/plain"},"redirects":[],"request":{"method":"GET","url":"http://web/ok","headers":{"accept":"text/markdown;q=1.0, text/x-markdown;q=0.9, text/plain;q=0.8, text/html;q=0.7, */*;q=0.1","accept-language":"en-US,en;q=0.9","user-agent":"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/143.0.0.0 Safari/537.36"}},"timing_ms":{"total":0,"first_byte":0},"output":"Hello"}`},
		{name: "mcp_result", sequence: true, details: `[X.server, X.tool, X.text]`, detailsWant: `["uploader","say","said"]`, value: "said", plain: `"said"`, method: `X.upper()`, argument: `"said"`, returned: "said"},
		{name: "mcp_result with only images", details: `[X.tool, len(X.content), X.content[0].mime]`, detailsWant: `["snap",1,"image/png"]`, value: "snap", plain: `""`, method: `X.upper()`, argument: `""`, returned: ""},
		{name: "dict_result", details: `X["model"]`, detailsWant: "openai/gpt-6-luna", value: "verdict", plain: `{"model": "openai/gpt-6-luna", "answers": {"tone": {"type": "choice", "choice": "calm", "confidence": 0}}, "usage": {"input_tokens": 0, "output_tokens": 0}, "stop_reason": "stop"}`, method: `X.keys()`, argument: verdict, returned: verdict},
		{name: "decoded json", details: `[X.ok, X.error]`, detailsWant: "[true,null]", value: "data", plain: `{"name": "orchard", "tags": ["a"]}`, method: `X.get("name")`, argument: `{"name":"orchard","tags":["a"]}`, returned: `{"name":"orchard","tags":["a"]}`},
		{name: "decoded json list", sequence: true, details: `[X.ok, X.error]`, detailsWant: "[true,null]", value: "rows", plain: `[1, 2]`, method: `X.index(2)`, argument: `[1,2]`, returned: `[1,2]`},
		{name: "gather_result", details: `[X.ok, X.error]`, detailsWant: "[true,null]", value: "both", plain: `[read(filePath="a.md"), read(filePath="b.md")]`, method: `X[1].upper()`, argument: `["A","B"]`, returned: `["A","B"]`},
		{name: "error", details: `[X.path, X.name]`, detailsWant: `["missing.md","missing.md"]`, value: "bad_txt", method: `X.upper()`, failure: &resultModelFailure{"read: File not found: missing.md", "read: File not found: missing.md", `{"error":"read: File not found: missing.md","name":"missing.md","ok":false,"path":"missing.md"}`}},
		{name: "failed bash_result", details: `[X.output, X.error_code]`, detailsWant: `["FAIL: TestLogin","2"]`, value: "bad_sh", method: `X.split("\n")`, failure: &resultModelFailure{"bash: exit status 2", "bash: exit status 2\nFAIL: TestLogin", `{"error":"bash: exit status 2","error_code":"2","ok":false,"output":"FAIL: TestLogin"}`}},
		{name: "failed webfetch_result", details: `[X.url, X.status, X.output]`, detailsWant: `["http://web/missing",404,"Not here"]`, value: "gone", method: `X.strip()`, failure: &resultModelFailure{"webfetch: HTTP 404", "webfetch: HTTP 404\nNot here", gone}},
		{name: "failed mcp call", details: `[X.server, X.tool]`, detailsWant: `["uploader","fail"]`, value: "mcp_bad", method: `X.lower()`, failure: &resultModelFailure{"uploader.fail: nope", "uploader.fail: nope", `{"error":"uploader.fail: nope","ok":false,"server":"uploader","tool":"fail"}`}},
		{name: "bash_result without output", details: `[X.output, X.error_code]`, detailsWant: `["",""]`, value: "quiet", plain: `""`, method: `X.upper()`, argument: `""`, returned: ""},
		{name: "bash refused before running", details: `[X.output, X.error_code]`, detailsWant: `["",""]`, value: "bad_args", method: `X.strip()`, failure: &resultModelFailure{"bash: invalid arguments: validate args: validating root: validating /properties/timeout_ms: type: soon has type \"string\", want \"integer\"", "bash: invalid arguments: validate args: validating root: validating /properties/timeout_ms: type: soon has type \"string\", want \"integer\"", `{"error":"bash: invalid arguments: validate args: validating root: validating /properties/timeout_ms: type: soon has type \"string\", want \"integer\"","error_code":"","ok":false,"output":""}`}},
		{name: "webfetch refused before a response", details: `X.url`, detailsWant: "ftp://example.com/x", value: "bad_url", method: `X.strip()`, failure: &resultModelFailure{"webfetch: URL must start with http:// or https://", "webfetch: URL must start with http:// or https://", `{"error":"webfetch: URL must start with http:// or https://","ok":false,"url":"ftp://example.com/x"}`}},
		{name: "failed glob", details: `[X.pattern, X.path]`, detailsWant: `["*.md","../outside"]`, value: "bad_glob", method: `X.strip()`, failure: &resultModelFailure{"glob: lstat \"../outside\": statat ../outside: path escapes from parent", "glob: lstat \"../outside\": statat ../outside: path escapes from parent", `{"error":"glob: lstat \"../outside\": statat ../outside: path escapes from parent","ok":false,"path":"../outside","pattern":"*.md"}`}},
		{name: "failed grep", details: `[X.pattern, X.path, X.include]`, detailsWant: `["","docs","*.md"]`, value: "bad_grep", method: `X.strip()`, failure: &resultModelFailure{"grep: pattern is required", "grep: pattern is required", `{"error":"grep: pattern is required","include":"*.md","ok":false,"path":"docs","pattern":""}`}},
		{name: "positional call", details: `[X.path, X.name]`, detailsWant: `["",""]`, value: "positional", method: `X.strip()`, failure: &resultModelFailure{"read: only keyword arguments are allowed; call read(filePath=...)", "read: only keyword arguments are allowed; call read(filePath=...)", `{"error":"read: only keyword arguments are allowed; call read(filePath=...)","name":"","ok":false,"path":""}`}},
		{name: "failed json.decode", details: `[X.ok, X.error]`, detailsWant: `[false,"json.decode: at offset 1, unexpected character 'o'"]`, value: "bad_json", method: `X.keys()`, failure: &resultModelFailure{"json.decode: at offset 1, unexpected character 'o'", "json.decode: at offset 1, unexpected character 'o'", `{"error":"json.decode: at offset 1, unexpected character 'o'","ok":false}`}},
		{name: "failed decision", details: `X.model`, detailsWant: "other/x", value: "bad_verdict", method: `X.strip()`, failure: &resultModelFailure{"decision: tool call denied: permission \"decision\" has no matching allow rule for subject \"other/x\". Choose a different action.", "decision: tool call denied: permission \"decision\" has no matching allow rule for subject \"other/x\". Choose a different action.", `{"error":"decision: tool call denied: permission \"decision\" has no matching allow rule for subject \"other/x\". Choose a different action.","model":"other/x","ok":false}`}},
	}
	operations := []struct{ name, template string }{
		{"if", `"taken" if X else "skipped"`},
		{"==", `X == P`},
		{"!=", `X != P`},
		{"in list", `X in [P]`},
		{"in a list holding itself", `X in [X]`},
		{"in tuple", `X in (P,)`},
		{"in dict", `X in {P: 1}`},
		{"in set", `X in set([P])`},
		{"ordering", `X < P`},
		{"str", `str(X)`},
		{"json in a returned dict", `{"v": X}`},
		{"method", "M"},
		{"unwrap", `X.unwrap()`},
		{"or_else", `X.or_else("default")`},
		{"json.decode", `json.decode(X)`},
		{"argument", `uploader_echo(value=X)`},
		{"return", `X`},
		{"details", "D"},
		{"len", `len(X)`},
		{"+", `[X + "!", "!" + X]`},
		{"slicing", `X[1:3]`},
		{"indexing", `X[0]`},
	}
	sequenceOperations := []string{"len", "+", "slicing", "indexing"}

	for _, kind := range kinds {
		for _, operation := range operations {
			if slices.Contains(sequenceOperations, operation.name) && !kind.sequence && kind.failure == nil {
				continue
			}

			t.Run(kind.name+"/"+operation.name, func(t *testing.T) {
				expr := func(value string) string {
					comparand := kind.plain
					if kind.failure != nil {
						comparand = fmt.Sprintf("%q", kind.failure.error)
					}

					return strings.NewReplacer("M", strings.ReplaceAll(kind.method, "X", value), "D", strings.ReplaceAll(kind.details, "X", value), "X", value, "P", comparand).Replace(operation.template)
				}
				got := env.run(t, expr(kind.value), false, 0)

				if operation.name == "details" {
					require.Equal(t, []any{kind.detailsWant, false}, []any{got.text, got.failed}, "details have the same names on success and failure")

					return
				}

				if kind.failure != nil {
					failure := kind.failure
					want := map[string]resultModelCase{
						"if": {want: "skipped"}, "==": {want: "false"}, "!=": {want: "true"},
						"in list": {want: "false"}, "in a list holding itself": {want: "false"}, "in tuple": {want: "false"}, "in dict": {want: "false"}, "in set": {want: "false"},
						"ordering":                {want: "tool call failed: execute: run code mode: call main: error < string not implemented. Choose a different action.", failed: true},
						"str":                     {want: failure.text},
						"json in a returned dict": {want: `{"v":` + failure.data + `}`},
						"method":                  {want: failure.text, failed: true},
						"unwrap":                  {want: "tool call failed: execute: run code mode: call main: " + failure.error + ". Choose a different action.", failed: true},
						"or_else":                 {want: "default"},
						"json.decode":             {want: failure.text, failed: true},
						"argument":                {want: failure.text, failed: true},
						"return":                  {want: failure.text, failed: true},
						"len":                     {want: "tool call failed: execute: run code mode: call main: len: value of type error has no len. Choose a different action.", failed: true},
						"+":                       {want: "tool call failed: execute: run code mode: call main: unknown binary op: error + string. Choose a different action.", failed: true},
						"slicing":                 {want: "tool call failed: execute: run code mode: call main: invalid slice operand error. Choose a different action.", failed: true},
						"indexing":                {want: "tool call failed: execute: run code mode: call main: unhandled index operation error[int]. Choose a different action.", failed: true},
					}[operation.name]
					require.Equal(t, []any{want.want, want.failed}, []any{got.text, got.failed})
					require.NotContains(t, got.mcpCalls, "echo", "a failure passed into a call skips the call")
				}

				switch {
				case kind.failure != nil:
				case operation.name == "json in a returned dict" && kind.data != "":
					require.Equal(t, []any{`{"v":` + kind.data + `}`, false}, []any{got.text, got.failed}, "a typed success uses its declared JSON")
				case operation.name == "argument":
					require.Equal(t, []any{kind.argument, kind.argumentFails}, []any{got.text, got.failed})
					require.Equal(t, !kind.argumentFails, slices.Contains(got.mcpCalls, "echo"))
				case operation.name == "return" || operation.name == "unwrap" || operation.name == "or_else":
					require.Equal(t, []any{kind.returned, false}, []any{got.text, got.failed})
					require.Equal(t, toolCallOutput("call-1", ToolResult{Output: got.raw, Attachments: env.attachments(kind.files)}), got.answer)
				default:
					plain := env.run(t, expr("("+kind.plain+")"), false, 0)
					require.Equal(t, []any{plain.text, plain.failed}, []any{got.text, got.failed}, "a success behaves like its value")

					if wanted, ok := map[string]string{"==": "true", "!=": "false", "in list": "true", "in a list holding itself": "true", "in tuple": "true"}[operation.name]; ok {
						require.Equal(t, wanted, got.text)
					}
				}

				if operation.name == "return" {
					background := env.run(t, expr(kind.value), true, 0)
					require.Equal(t, []any{got.text, got.failed}, []any{background.text, background.failed}, "a background run delivers the same answer")
				}
			})
		}
	}
}
