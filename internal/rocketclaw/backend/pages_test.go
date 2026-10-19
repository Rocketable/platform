package backend

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPages(t *testing.T) {
	store := newTestSessionService(t)
	ctx := t.Context()

	get := func(agent string, kind PageKind, name string) (Page, bool) {
		t.Helper()

		page, ok, err := store.Page(ctx, agent, kind, name)
		require.NoError(t, err)

		return page, ok
	}
	names := func(agent string) []string {
		t.Helper()

		pages, err := store.Pages(ctx, agent)
		require.NoError(t, err)

		out := make([]string, 0, len(pages))
		for _, page := range pages {
			out = append(out, page.Agent+" "+string(page.Kind)+" "+page.Name)
		}

		return out
	}

	require.NoError(t, store.PutPage(ctx, &Page{Agent: "main", Kind: PageFile, Name: "index.html", ContentType: "text/html", Body: []byte("<p>one</p>")}))
	require.NoError(t, store.PutPage(ctx, &Page{Agent: "main", Kind: PageFile, Name: "index.html", ContentType: "text/plain", Body: []byte("two")}))
	require.NoError(t, store.PutPage(ctx, &Page{Agent: "main", Kind: PageData, Name: "index.html", ContentType: "application/json", Body: []byte(`{"n":1}`)}))
	require.NoError(t, store.PutPage(ctx, &Page{Agent: "main/review", Kind: PageFile, Name: "a.html", ContentType: "text/html", Body: []byte("<p>review</p>")}))

	page, ok := get("main", PageFile, "index.html")
	require.True(t, ok)
	require.Equal(t, "text/plain", page.ContentType)
	require.Equal(t, []byte("two"), page.Body)
	require.Equal(t, 3, page.Size)

	data, ok := get("main", PageData, "index.html")
	require.True(t, ok)
	require.JSONEq(t, `{"n":1}`, string(data.Body))

	_, ok = get("main", PageFile, "a.html")
	require.False(t, ok)

	require.Equal(t, []string{"main data index.html", "main file index.html"}, names("main"))
	require.Equal(t, []string{"main/review file a.html"}, names("main/review"))
	require.Equal(t, []string{"main data index.html", "main file index.html", "main/review file a.html"}, names(""))

	deleted, err := store.DeletePage(ctx, "main", PageFile, "index.html")
	require.NoError(t, err)
	require.True(t, deleted)

	deleted, err = store.DeletePage(ctx, "main", PageFile, "index.html")
	require.NoError(t, err)
	require.False(t, deleted)

	require.Equal(t, []string{"main data index.html"}, names("main"))
}
