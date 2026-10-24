package backend

import (
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Rocketable/platform/internal/rocketcode"
)

type pageToolInput struct {
	Path          string          `json:"path"`
	Key           string          `json:"key"`
	Content       string          `json:"content"`
	ContentBase64 string          `json:"content_base64"`
	Source        string          `json:"source"`
	ContentType   string          `json:"content_type"`
	Value         json.RawMessage `json:"value"`
}

const pageStoreDescription = `Store a file as a Hosted Page of this agent, replacing any file at path, and return its shareable URL: <web origin>/pages/<agent space or ->/<agent>/<path>. Give path plus exactly one of content (text), content_base64 (binary), or source (a workspace file); content_type is optional and inferred from path's extension. Each file is capped at 10 MiB. Only people the RocketClaw Web admits can open the URL; a path ending in / serves its index.html.
Pages run sandboxed with an opaque origin: they cannot fetch their own files or use localStorage or cookies. Start HTML with <meta charset="utf-8">. Load your own scripts and styles by relative URL as classic <script src> and <link> tags, not modules. CDN scripts, styles, and images load; pin exact versions. Any website can load script files, so keep secrets and data out of files: store data with rocketclaw_kv_store and read it in the page:
const token = new URLSearchParams(location.hash.slice(1)).get("rc");
function readData(key) {
  return new Promise((resolve, reject) => {
    const id = Math.random().toString(36).slice(2);
    addEventListener("message", function on(event) {
      if (event.data?.type !== "rocketclaw.read" || event.data.id !== id) return;
      removeEventListener("message", on);
      "error" in event.data ? reject(new Error(event.data.error)) : resolve(event.data.value);
    });
    parent.postMessage({ type: "rocketclaw.read", id, key, token }, "*");
  });
}
Pages only read data; they cannot write it or read another agent's.`

// pageTools builds the Hosted Page tools acting on agent's files and Page Data, linked under its space.
// Pages they store belong to conversation when it is set, so they go with that session.
func pageTools(sessions *SessionService, root *os.Root, threads *threadBridgeManager, agent, space, conversation string) []rocketcode.Tool {
	pageURL := func(ctx context.Context, name string) (string, error) {
		origin, err := threads.webOrigin(ctx)
		if err != nil {
			return "", err
		}

		segments := strings.Split(name, "/")
		for i := range segments {
			segments[i] = url.PathEscape(segments[i])
		}

		// The Web escapes the ":" of a session owner too, so both links open one tab.
		return origin + "/pages/" + url.PathEscape(cmp.Or(space, "-")) + "/" + strings.ReplaceAll(url.PathEscape(agent), ":", "%3A") + "/" + strings.Join(segments, "/"), nil
	}
	put := func(ctx context.Context, kind PageKind, name, contentType string, body []byte) error {
		if !fs.ValidPath(name) || name == "." {
			return fmt.Errorf("invalid page %s name %q: use a relative slash-separated path without empty, . or .. segments", kind, name)
		}

		if len(body) > 10<<20 {
			return fmt.Errorf("page %s %q is %d bytes, over the 10 MiB limit", kind, name, len(body))
		}

		return sessions.PutPage(ctx, &Page{Agent: agent, Kind: kind, Name: name, ContentType: contentType, Body: body, Conversation: conversation})
	}
	get := func(ctx context.Context, kind PageKind, name string) (Page, error) {
		page, ok, err := sessions.Page(ctx, agent, kind, name)
		if err == nil && !ok {
			err = fmt.Errorf("page %s %q not found", kind, name)
		}

		return page, err
	}
	str := map[string]any{"type": "string"}
	tool := func(name, description string, properties map[string]any, call func(context.Context, *pageToolInput) (string, error)) rocketcode.Tool {
		return rocketcode.Tool{Name: name, Description: description, Permission: "rocketclaw", VisibilitySubjects: []string{name}, Subjects: func(json.RawMessage) ([]string, error) { return []string{name}, nil }, Parameters: map[string]any{"properties": properties}, Call: func(ctx context.Context, raw json.RawMessage, _ chan<- rocketcode.ChatResponse) (rocketcode.ToolResult, error) {
			var input pageToolInput
			if err := json.Unmarshal(raw, &input); err != nil {
				return rocketcode.ToolResult{}, fmt.Errorf("parse %s: %w", name, err)
			}

			out, err := call(ctx, &input)
			if err != nil {
				return rocketcode.ToolResult{}, err
			}

			return rocketcode.TextToolResult(out), nil
		}}
	}

	return []rocketcode.Tool{
		tool("rocketclaw_store", pageStoreDescription, map[string]any{"path": str, "content": str, "content_base64": str, "source": str, "content_type": str}, func(ctx context.Context, input *pageToolInput) (string, error) {
			file, err := outboundAttachment(root, &outboundAttachmentInput{Path: input.Source, Name: input.Path, MIMEType: input.ContentType, Content: input.Content, ContentBase64: input.ContentBase64})
			if err != nil {
				return "", err
			}

			if err := put(ctx, PageFile, input.Path, file.MIMEType, file.Data); err != nil {
				return "", err
			}

			return pageURL(ctx, input.Path)
		}),
		tool("rocketclaw_read", "Return this agent's Hosted Page file at path as JSON with content_type and content, or content_base64 when it is not UTF-8 text.", map[string]any{"path": str}, func(ctx context.Context, input *pageToolInput) (string, error) {
			page, err := get(ctx, PageFile, input.Path)
			if err != nil {
				return "", err
			}

			out := struct {
				ContentType   string `json:"content_type"`
				Content       string `json:"content,omitempty"`
				ContentBase64 string `json:"content_base64,omitempty"`
			}{ContentType: page.ContentType}
			if utf8.Valid(page.Body) {
				out.Content = string(page.Body)
			} else {
				out.ContentBase64 = base64.StdEncoding.EncodeToString(page.Body)
			}

			data, err := json.Marshal(out)

			return string(data), err
		}),
		tool("rocketclaw_kv_store", "Store value, any JSON value up to 10 MiB, as this agent's Page Data at key, replacing it; its pages read it as shown in rocketclaw_store.", map[string]any{"key": str, "value": map[string]any{"description": "any JSON value"}}, func(ctx context.Context, input *pageToolInput) (string, error) {
			if !json.Valid(input.Value) {
				return "", errors.New("value must be a JSON value")
			}

			return "stored " + input.Key, put(ctx, PageData, input.Key, "application/json", input.Value)
		}),
		tool("rocketclaw_kv_read", "Return the JSON value this agent stored as Page Data at key.", map[string]any{"key": str}, func(ctx context.Context, input *pageToolInput) (string, error) {
			page, err := get(ctx, PageData, input.Key)
			return string(page.Body), err
		}),
		tool("rocketclaw_delete", "Delete this agent's Hosted Page file at path, or its Page Data at key; give exactly one, leaving the other empty.", map[string]any{"path": str, "key": str}, func(ctx context.Context, input *pageToolInput) (string, error) {
			if (input.Path == "") == (input.Key == "") {
				return "", errors.New("give exactly one of path or key")
			}

			kind, name := PageFile, input.Path
			if input.Key != "" {
				kind, name = PageData, input.Key
			}

			found, err := sessions.DeletePage(ctx, agent, kind, name)
			if err == nil && !found {
				err = fmt.Errorf("page %s %q not found", kind, name)
			}

			return "deleted " + name, err
		}),
		tool("rocketclaw_list", "List this agent's Hosted Page files, with URLs, and Page Data keys as JSON.", map[string]any{}, func(ctx context.Context, _ *pageToolInput) (string, error) {
			pages, err := sessions.Pages(ctx, agent)
			if err != nil {
				return "", err
			}

			type file struct {
				Path        string    `json:"path"`
				ContentType string    `json:"content_type"`
				Size        int       `json:"size"`
				UpdatedAt   time.Time `json:"updated_at"`
				URL         string    `json:"url"`
			}

			type key struct {
				Key       string    `json:"key"`
				Size      int       `json:"size"`
				UpdatedAt time.Time `json:"updated_at"`
			}

			out := struct {
				Files []file `json:"files"`
				Keys  []key  `json:"keys"`
			}{Files: []file{}, Keys: []key{}}

			for i := range pages {
				page := &pages[i]
				if page.Kind == PageData {
					out.Keys = append(out.Keys, key{Key: page.Name, Size: page.Size, UpdatedAt: page.UpdatedAt})
					continue
				}

				link, err := pageURL(ctx, page.Name)
				if err != nil {
					return "", err
				}

				out.Files = append(out.Files, file{Path: page.Name, ContentType: page.ContentType, Size: page.Size, UpdatedAt: page.UpdatedAt, URL: link})
			}

			data, err := json.Marshal(out)

			return string(data), err
		}),
	}
}
