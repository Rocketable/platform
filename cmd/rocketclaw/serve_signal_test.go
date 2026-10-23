package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The helper process waits for shutdownSignals' first signal, then takes a moment to "shut down" before it exits.
func TestShutdownSignalsHelper(t *testing.T) {
	if os.Getenv("ROCKETCLAW_SIGNAL_HELPER") == "" {
		t.Skip("run by TestShutdownSignals")
	}

	ctx, stop := shutdownSignals()
	defer stop()

	fmt.Println("ready")
	<-ctx.Done()
	fmt.Println("shutting down")
	time.Sleep(time.Second)
	fmt.Println("stopped")
}

// A second SIGTERM, as systemd sends once a unit's main process exits, leaves the graceful shutdown running; a
// second Ctrl-C still forces the exit.
func TestShutdownSignals(t *testing.T) {
	for _, tc := range []struct {
		signal  syscall.Signal
		stopped bool
	}{
		{syscall.SIGTERM, true},
		{syscall.SIGINT, false},
	} {
		t.Run(tc.signal.String(), func(t *testing.T) {
			helper := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestShutdownSignalsHelper$")
			helper.Env = append(os.Environ(), "ROCKETCLAW_SIGNAL_HELPER=1")
			output, err := helper.StdoutPipe()
			require.NoError(t, err)
			require.NoError(t, helper.Start())

			lines := bufio.NewScanner(output)
			require.True(t, lines.Scan())
			require.Equal(t, "ready", lines.Text())
			require.NoError(t, helper.Process.Signal(tc.signal))
			require.True(t, lines.Scan())
			require.Equal(t, "shutting down", lines.Text())
			require.NoError(t, helper.Process.Signal(tc.signal))

			rest := []string{}
			for lines.Scan() {
				rest = append(rest, lines.Text())
			}

			err = helper.Wait()
			if tc.stopped {
				require.NoError(t, err, "a repeated SIGTERM is ignored")
				require.Contains(t, rest, "stopped")

				return
			}

			require.Error(t, err, "a second Ctrl-C forces the exit")
			require.NotContains(t, rest, "stopped")
		})
	}
}
