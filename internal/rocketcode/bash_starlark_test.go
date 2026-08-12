package rocketcode

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.starlark.net/starlark"
)

func TestBashStarlarkResult(t *testing.T) {
	value := newBashStarlarkResult(BashResult{
		HeadOutput: "head",
		FullOutput: "full",
		ErrorCode:  "7",
		Success:    false,
	})

	require.Equal(t, "bash_result", value.Type())
	require.Equal(t, "head", value.String())
	require.Equal(t, starlark.False, value.Truth())

	head, err := value.Attr("head_output")
	require.NoError(t, err)
	require.Equal(t, starlark.String("head"), head)

	full, err := value.Attr("full_output")
	require.NoError(t, err)
	require.Equal(t, starlark.String("full"), full)

	code, err := value.Attr("error_code")
	require.NoError(t, err)
	require.Equal(t, starlark.String("7"), code)

	require.Equal(t, []string{"error_code", "full_output", "head_output"}, value.AttrNames())
}
