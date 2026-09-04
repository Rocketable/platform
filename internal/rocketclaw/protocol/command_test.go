package protocol

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseDollarCommand(t *testing.T) {
	command, args, ok := ParseDollarCommand("$ goal ship")
	require.True(t, ok)
	require.Equal(t, "goal", command)
	require.Equal(t, "ship", args)
}
