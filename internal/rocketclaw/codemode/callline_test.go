package codemode

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	"go.starlark.net/starlark"
)

func TestCallLine(t *testing.T) {
	probe := HostTool{Details: NoDetails{}, Name: "probe", CallValue: func(ctx context.Context, _ map[string]any) (starlark.Value, error) {
		number, code := CallLine(ctx)

		return starlark.String(fmt.Sprintf("%d %s", number, code)), nil
	}}

	got, err := Run(t.Context(), `def main():
    direct = probe()
    branches = gather([lambda: probe(), probe])
    return [str(direct)] + [str(b) for b in branches]
`, nil, nil, nil, []HostTool{probe})
	require.NoError(t, err)
	require.Equal(t, `["2 direct = probe()", "3 branches = gather([lambda: probe(), probe])", "0 "]`, got.String())
}
