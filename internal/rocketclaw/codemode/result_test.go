package codemode

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"image"
	"image/png"
	"runtime"
	"strings"
	"testing"

	"go.starlark.net/starlark"
)

func valueHosts(values map[string]starlark.Value) []HostTool {
	hosts := make([]HostTool, 0, 1+len(values))
	hosts = append(hosts, HostTool{
		Name: "box",
		CallValue: func(_ context.Context, args map[string]any) (starlark.Value, error) {
			return starlark.String(args["s"].(string)), nil
		},
	})

	for name, value := range values {
		hosts = append(hosts, HostTool{
			Name: name,
			CallValue: func(context.Context, map[string]any) (starlark.Value, error) {
				return value, nil
			},
		})
	}

	return hosts
}

type scriptCase struct {
	expression, want, wantErr string
}

func runScriptCases(t *testing.T, values map[string]starlark.Value, cases []scriptCase) {
	t.Helper()

	for _, tc := range cases {
		t.Run(tc.expression, func(t *testing.T) {
			got, err := runToString(t.Context(), "def main():\n    return "+tc.expression+"\n", nil, allowAll, nilCall, valueHosts(values))
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("runToString() = %q, %v, want error %q", got, err, tc.wantErr)
				}

				return
			}

			if err != nil || got != tc.want {
				t.Fatalf("runToString() = %q, %v, want %q", got, err, tc.want)
			}
		})
	}
}

func testPNG(t *testing.T, width, height int) string {
	t.Helper()

	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, width, height))); err != nil {
		t.Fatal(err)
	}

	return buf.String()
}

func TestErrorValue(t *testing.T) {
	runScriptCases(t, map[string]starlark.Value{"e": NewError("read", "File not found: a.txt")}, []scriptCase{
		{expression: `type(e())`, want: "error"},
		{expression: `str(e())`, want: "File not found: a.txt"},
		{expression: `e()`, want: "File not found: a.txt"},
		{expression: `bool(e())`, want: "false"},
		{expression: `e().ok`, want: "false"},
		{expression: `e().error`, want: "File not found: a.txt"},
		{expression: `e().or_else("fallback")`, want: "fallback"},
		{expression: `e().crop(x=0, y=0, width=1, height=1).resize(width=2).error`, want: "File not found: a.txt"},
		{expression: `e().split(",") == e()`, want: "true"},
		{expression: `e().width.height`, want: "File not found: a.txt"},
		{expression: `[e(), 1]`, want: `[{"error":"File not found: a.txt"},1]`},
		{expression: `e() == "File not found: a.txt"`, want: "true"},
		{expression: `e().unwrap()`, wantErr: "read: File not found: a.txt"},
		{expression: `e() + "x"`, wantErr: "unknown binary op: error + string"},
		{expression: `{e(): 1}`, wantErr: "unhashable type: error"},
	})
}

func TestTextValue(t *testing.T) {
	runScriptCases(t, map[string]starlark.Value{
		"t":     starlark.String("abc"),
		"empty": starlark.String(""),
		"multi": starlark.String("one\ntwo\r\nthree"),
		"fmt":   starlark.String("hi %s"),
		"j":     starlark.String(`{"name": "x", "n": [1, 2]}`),
	}, []scriptCase{
		{expression: `type(t())`, want: "text"},
		{expression: `t()`, want: "abc"},
		{expression: `[t()]`, want: `["abc"]`},
		{expression: `bool(t())`, want: "true"},
		{expression: `bool(empty())`, want: "false"},
		{expression: `empty().ok`, want: "true"},
		{expression: `[t().error]`, want: "[null]"},
		{expression: `type(t().unwrap())`, want: "text"},
		{expression: `type(t().or_else("x"))`, want: "text"},
		{expression: `[t().path, t().name]`, want: "[null,null]"},
		{expression: `len(t())`, want: "3"},
		{expression: `t()[1]`, want: "b"},
		{expression: `t()[1:]`, want: "bc"},
		{expression: `t() + "!"`, want: "abc!"},
		{expression: `"!" + t()`, want: "!abc"},
		{expression: `t() + t()`, want: "abcabc"},
		{expression: `t() * 2`, want: "abcabc"},
		{expression: `2 * t()`, want: "abcabc"},
		{expression: `fmt() % "you"`, want: "hi you"},
		{expression: `"<%s>" % t()`, want: "<abc>"},
		{expression: `t().upper()`, want: "ABC"},
		{expression: `t().split("b")`, want: `["a","c"]`},
		{expression: `type(t().replace("a", "z"))`, want: "string"},
		{expression: `multi().lines()`, want: `["one","two","three"]`},
		{expression: `empty().lines()`, want: `[]`},
		{expression: `[str(m) for m in multi().search("^t")]`, want: `["2: two","3: three"]`},
		{expression: `[m.line for m in multi().search("e$")]`, want: `[1,3]`},
		{expression: `multi().search("(")`, wantErr: "search: error parsing regexp"},
		{expression: `type(t().bytes())`, want: "bytes"},
		{expression: `len(t().bytes())`, want: "3"},
		{expression: `json.decode(j())["n"]`, want: "[1,2]"},
		{expression: `{t(): 1}[t()]`, want: "1"},
		{expression: `sorted([box(s="b"), box(s="a")])`, want: `["a","b"]`},
		{expression: `t() - 1`, wantErr: "unknown binary op: text - int"},
		{expression: `"".join([t()])`, wantErr: "want string, got text"},
	})
}

func allocatedBytes(t *testing.T, fn func()) uint64 {
	t.Helper()

	var before, after runtime.MemStats

	runtime.ReadMemStats(&before)
	fn()
	runtime.ReadMemStats(&after)

	return after.TotalAlloc - before.TotalAlloc
}

func TestValuesShareContent(t *testing.T) {
	content := strings.Repeat("x", 8<<20)
	text := NewText(content, PlainText{})
	file := NewFile(FileSpec{Name: "a.pdf", Mime: "application/pdf", Content: content}, NoFileMethods{})

	for _, tc := range []struct {
		name  string
		value starlark.HasAttrs
		attr  string
	}{
		{"text bytes", text, "bytes"},
		{"file bytes", file, "bytes"},
		{"file sha256", file, "sha256"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			allocated := allocatedBytes(t, func() {
				v, err := tc.value.Attr(tc.attr)
				if err != nil {
					t.Fatal(err)
				}

				if callable, ok := v.(starlark.Callable); ok {
					if _, err := starlark.Call(&starlark.Thread{}, callable, nil, nil); err != nil {
						t.Fatal(err)
					}
				}
			})
			if allocated >= uint64(len(content)) {
				t.Fatalf("%s allocated %d bytes for %d bytes of content", tc.name, allocated, len(content))
			}
		})
	}

	if allocated := allocatedBytes(t, func() { text.Slice(10, len(content), 1) }); allocated >= uint64(len(content)) {
		t.Fatalf("slicing allocated %d bytes", allocated)
	}
}

func TestDictResult(t *testing.T) {
	dict := starlark.NewDict(1)
	if err := dict.SetKey(starlark.String("a"), starlark.MakeInt(1)); err != nil {
		t.Fatal(err)
	}

	runScriptCases(t, map[string]starlark.Value{"d": dict, "empty": starlark.NewDict(0)}, []scriptCase{
		{expression: `type(d())`, want: "dict_result"},
		{expression: `d()`, want: `{"a":1}`},
		{expression: `d()["a"]`, want: "1"},
		{expression: `d().get("b", 2)`, want: "2"},
		{expression: `[d().keys(), d().values(), d().items()]`, want: `[["a"],[1],[["a",1]]]`},
		{expression: `[k for k in d()]`, want: `["a"]`},
		{expression: `len(d())`, want: "1"},
		{expression: `"a" in d()`, want: "true"},
		{expression: `d() == {"a": 1}`, want: "true"},
		{expression: `[bool(d()), bool(empty())]`, want: "[true,false]"},
		{expression: `d().ok`, want: "true"},
		{expression: `d()["b"]`, wantErr: "key \"b\" not in dict"},
	})
}

func TestMatchValue(t *testing.T) {
	runScriptCases(t, map[string]starlark.Value{"m": NewMatch("/w/a.go", 3, "x := 1"), "bare": NewMatch("", 2, "y")}, []scriptCase{
		{expression: `type(m())`, want: "match"},
		{expression: `str(m())`, want: "/w/a.go:3: x := 1"},
		{expression: `str(bare())`, want: "2: y"},
		{expression: `[m(), bare()]`, want: `[{"line":3,"path":"/w/a.go","text":"x := 1"},{"line":2,"path":null,"text":"y"}]`},
		{expression: `[m().path, m().line, m().text, m().ok]`, want: `["/w/a.go",3,"x := 1",true]`},
	})
}

func TestFileValue(t *testing.T) {
	content := testPNG(t, 3, 2)
	sum := sha256.Sum256([]byte(content))
	digest := hex.EncodeToString(sum[:])
	img := NewFile(FileSpec{Name: "chart.png", Mime: "image/png", Content: content, Source: "read out/chart.png"}, NoFileMethods{})
	same := NewFile(FileSpec{Name: "copy.png", Mime: "image/png", Content: content}, NoFileMethods{})
	pdf := NewFile(FileSpec{Name: "a.pdf", Mime: "application/pdf", Content: strings.Repeat("p", 186412)}, NoFileMethods{})
	size := len(content)

	runScriptCases(t, map[string]starlark.Value{"f": img, "g": same, "pdf": pdf}, []scriptCase{
		{expression: `type(f())`, want: "file"},
		{expression: `str(f())`, want: "chart.png (image/png, 3 × 2, " + humanSize(size) + ")"},
		{expression: `str(pdf())`, want: "a.pdf (application/pdf, 182 KB)"},
		{expression: `[f().name, f().mime, f().size, f().width, f().height, f().source]`, want: `["chart.png","image/png",` + starlark.MakeInt(size).String() + `,3,2,"read out/chart.png"]`},
		{expression: `[pdf().width, pdf().height]`, want: "[null,null]"},
		{expression: `f().sha256`, want: digest},
		{expression: `f().base64()`, want: base64.StdEncoding.EncodeToString([]byte(content))},
		{expression: `len(f().bytes())`, want: starlark.MakeInt(size).String()},
		{expression: `[bool(f()), f().ok]`, want: "[true,true]"},
		{expression: `f() == g()`, want: "true"},
		{expression: `f() != pdf()`, want: "true"},
		{expression: `{f(): 1}[g()]`, want: "1"},
		{expression: `f() in [g()]`, want: "true"},
		{expression: `[pdf()]`, want: `[{"file":"a.pdf","mime":"application/pdf","sha256":"` + pdf.SHA256() + `","size":186412,"source":"","summary":"a.pdf, application/pdf, 182 KB"}]`},
		{expression: `json.decode(json.encode(f()))["summary"]`, want: "chart.png, image/png, 3 × 2, " + humanSize(size) + ", from read out/chart.png"},
		{expression: `len(f())`, wantErr: "file has no len"},
		{expression: `f() < g()`, wantErr: "file < file not implemented"},
		{expression: `f().crop`, wantErr: "file has no .crop field or method"},
	})
}

func TestHumanSize(t *testing.T) {
	for n, want := range map[int]string{0: "0 B", 1023: "1023 B", 1024: "1 KB", 1536: "1.5 KB", 10422: "10 KB", 186412: "182 KB", 1258291: "1.2 MB", 32505856: "31 MB", 3 << 30: "3 GB"} {
		if got := humanSize(n); got != want {
			t.Errorf("humanSize(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestHostResult(t *testing.T) {
	dict := starlark.NewDict(0)
	file := NewFile(FileSpec{Name: "a.pdf", Mime: "application/pdf"}, NoFileMethods{})

	for _, tc := range []struct {
		name     string
		value    starlark.Value
		err      error
		wantType string
		wantStr  string
	}{
		{"go error", starlark.String("partial"), errors.New("boom"), "error", "boom"},
		{"string", starlark.String("hi"), nil, "text", "hi"},
		{"dict", dict, nil, "dict_result", "{}"},
		{"typed value", file, nil, "file", "a.pdf (application/pdf, 0 B)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := HostResult("read", tc.value, tc.err)
			if got.Type() != tc.wantType || got.String() != tc.wantStr {
				t.Fatalf("HostResult() = %s %q, want %s %q", got.Type(), got.String(), tc.wantType, tc.wantStr)
			}
		})
	}

	if dict.SetKey(starlark.String("k"), starlark.None) == nil {
		t.Fatal("dict_result left its dict mutable")
	}
}

func TestTextValueAttrsExtension(t *testing.T) {
	attrs := &textAttrsMock{
		AttrFunc: func(name string) (starlark.Value, error) {
			switch name {
			case "path":
				return starlark.String("/w/notes.txt"), nil
			case "start_line":
				return starlark.MakeInt(5), nil
			case "broken":
				return nil, errors.New("boom")
			default:
				return nil, nil
			}
		},
		AttrNamesFunc: func() []string { return []string{"path", "start_line"} },
	}

	runScriptCases(t, map[string]starlark.Value{"r": NewText("five\nsix\nseven", attrs)}, []scriptCase{
		{expression: `r().path`, want: "/w/notes.txt"},
		{expression: `[str(m) for m in r().search("^s")]`, want: `["/w/notes.txt:6: six","/w/notes.txt:7: seven"]`},
		{expression: `[n for n in dir(r()) if n in ["ok", "path", "start_line", "upper", "lines"]]`, want: `["lines","ok","path","start_line","upper"]`},
		{expression: `r().broken`, wantErr: "text.broken: boom"},
	})
}

func TestFileMethodsExtension(t *testing.T) {
	var receiver *File

	file := NewFile(FileSpec{Name: "a.png", Mime: "image/png"}, &fileMethodsMock{
		AttrFunc: func(f *File, name string) (starlark.Value, error) {
			receiver = f

			if name != "crop" {
				return nil, nil
			}

			return starlark.NewBuiltin("crop", func(*starlark.Thread, *starlark.Builtin, starlark.Tuple, []starlark.Tuple) (starlark.Value, error) {
				return NewError("crop", "image/png is not decodable"), nil
			}), nil
		},
		AttrNamesFunc: func() []string { return []string{"crop"} },
	})

	runScriptCases(t, map[string]starlark.Value{"f": file}, []scriptCase{
		{expression: `f().crop().error`, want: "image/png is not decodable"},
		{expression: `"crop" in dir(f())`, want: "true"},
		{expression: `f().rotate`, wantErr: "file has no .rotate field or method"},
	})

	if receiver != file {
		t.Fatal("FileMethods.Attr did not receive the file")
	}
}
