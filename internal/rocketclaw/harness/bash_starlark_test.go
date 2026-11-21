package harness

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.starlark.net/starlark"
	"go.starlark.net/syntax"
)

func TestBashStarlarkResult(t *testing.T) {
	env := starlark.StringDict{
		"ok":     &bashStarlarkResult{output: "a,b\n"},
		"failed": &bashStarlarkResult{output: "boom\n", errorCode: "7"},
	}

	for expr, want := range map[string]string{
		`type(failed)`:                       `"bash_result"`,
		`str(failed)`:                        `"boom\n"`,
		`[bool(ok), bool(failed)]`:           `[True, False]`,
		`[ok.ok, failed.ok]`:                 `[True, False]`,
		`[ok.error, failed.error]`:           `[None, "boom\n"]`,
		`[ok.error_code, failed.error_code]`: `["", "7"]`,
		`ok.strip().split(",")`:              `["a", "b"]`,
		`failed.upper()`:                     `"BOOM\n"`,
		`ok.unwrap().count(",")`:             `1`,
		`failed.or_else("fallback")`:         `"fallback"`,
	} {
		got, err := starlark.EvalOptions(&syntax.FileOptions{}, &starlark.Thread{}, "test", expr, env)
		require.NoError(t, err, expr)
		require.Equal(t, want, got.String(), expr)
	}

	_, err := starlark.EvalOptions(&syntax.FileOptions{}, &starlark.Thread{}, "test", `failed.unwrap()`, env)
	require.EqualError(t, err, "bash: boom\n")

	missing, err := env["ok"].(*bashStarlarkResult).Attr("full_output")
	require.NoError(t, err)
	require.Nil(t, missing)
	require.Subset(t, env["ok"].(*bashStarlarkResult).AttrNames(), []string{"error", "error_code", "ok", "or_else", "split", "unwrap"})
}
