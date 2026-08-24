package rocketcode

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.starlark.net/starlark"
)

func TestBashStarlarkResult(t *testing.T) {
	value := newBashStarlarkResult(BashResult{
		Output:    "full",
		ErrorCode: "7",
		Success:   false,
	})

	require.Equal(t, "bash_result", value.Type())
	require.Equal(t, "full", value.String())
	require.Equal(t, starlark.False, value.Truth())

	missing, err := value.Attr("full_output")
	require.NoError(t, err)
	require.Nil(t, missing)

	code, err := value.Attr("error_code")
	require.NoError(t, err)
	require.Equal(t, starlark.String("7"), code)

	require.Equal(t, []string{"error_code"}, value.AttrNames())
}
