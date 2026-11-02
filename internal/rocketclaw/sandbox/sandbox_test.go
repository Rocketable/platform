package sandbox

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// A command name becomes a file in /extensions and a Bash function, so it must be a plain shell word that is
// not curl, a reserved word, or RocketClaw's own.
func TestCheckCommandName(t *testing.T) {
	for _, name := range []string{"deploy", "stripe-write", "a", "_x", "0day", "my.cmd", "g++", "echo", strings.Repeat("a", 64)} {
		require.NoError(t, CheckCommandName(name), name)
	}

	for name, msg := range map[string]string{
		"curl":                  "curl is the emulated shell's own",
		"if":                    "if is a Bash reserved word",
		"coproc":                "coproc is a Bash reserved word",
		"":                      `"": names are 1-64`,
		"-x":                    `"-x": names are`,
		"a b":                   `"a b": names are`,
		"a=b":                   `"a=b": names are`,
		"bin/x":                 `"bin/x": names are`,
		"rocketclawx":           `"rocketclawx": names are`,
		"ünï":                   `"ünï": names are`,
		strings.Repeat("a", 65): "names are 1-64",
	} {
		require.ErrorContains(t, CheckCommandName(name), msg, name)
	}
}

// The zero Runtime is unavailable, and says so, rather than running anything.
func TestZeroRuntimeRefuses(t *testing.T) {
	var rt Runtime

	require.EqualError(t, rt.Err(), "sandbox not prepared")

	_, err := rt.Run(t.Context(), &Command{Script: "true"}, probeHost{})
	require.EqualError(t, err, "sandbox not prepared")
}
