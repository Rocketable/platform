package config

import (
	"errors"
	"fmt"
	"net/netip"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
)

func TestLockedConfig(t *testing.T) {
	rate := 0.5
	want := &Config{
		Workspace: "workspace", Overlays: []string{"overlay"}, Models: map[string]string{"main": "model"},
		WebUsers: map[netip.Addr]string{netip.MustParseAddr("127.0.0.1"): "alice"}, Environment: []string{"KEY=value"},
		Providers:  map[string]OpenAIConfig{"work": {APIKey: "invented"}},
		MCPServers: map[string]MCPServerConfig{"tools": {Args: []string{"arg"}, Env: map[string]string{"KEY": "value"}, Headers: map[string]string{"HEADER": "value"}}},
		Slack:      SlackConfig{Channels: []SlackChannelConfig{{Channel: "ops", Agents: []string{"main"}, AllowedUserIDs: []string{"alice"}}}},
		Web:        WebConfig{Sentry: SentryConfig{TracesSampleRate: &rate}},
	}

	locked := NewLockedConfig(want)
	for _, snapshot := range []*Config{want, locked.Clone()} {
		snapshot.Workspace = "changed"
		snapshot.Overlays[0] = "changed"
		snapshot.Models["main"] = "changed"
		snapshot.WebUsers[netip.MustParseAddr("127.0.0.1")] = "changed"
		snapshot.Environment[0] = "changed"
		snapshot.Providers["work"] = OpenAIConfig{APIKey: "changed"}
		server := snapshot.MCPServers["tools"]
		server.Args[0], server.Env["KEY"], server.Headers["HEADER"] = "changed", "changed", "changed"
		snapshot.Slack.Channels[0].Channel = "changed"
		snapshot.Slack.Channels[0].Agents[0] = "changed"
		snapshot.Slack.Channels[0].AllowedUserIDs[0] = "changed"
		*snapshot.Web.Sentry.TracesSampleRate = 1
	}

	snapshot := locked.Clone()
	require.Equal(t, "workspace", snapshot.Workspace)
	require.Equal(t, []string{"overlay"}, snapshot.Overlays)
	require.Equal(t, "model", snapshot.Models["main"])
	require.Equal(t, "alice", snapshot.WebUsers[netip.MustParseAddr("127.0.0.1")])
	require.Equal(t, []string{"KEY=value"}, snapshot.Environment)
	require.Equal(t, "invented", snapshot.Providers["work"].APIKey)
	require.Equal(t, MCPServerConfig{Args: []string{"arg"}, Env: map[string]string{"KEY": "value"}, Headers: map[string]string{"HEADER": "value"}}, snapshot.MCPServers["tools"])
	require.Equal(t, SlackChannelConfig{Channel: "ops", Agents: []string{"main"}, AllowedUserIDs: []string{"alice"}}, snapshot.Slack.Channels[0])
	require.InDelta(t, 0.5, *snapshot.Web.Sentry.TracesSampleRate, 0.001)

	errWork := errors.New("work failed")
	require.ErrorIs(t, locked.Do(func(cfg *Config) error {
		cfg.Workspace = "updated"
		return errWork
	}), errWork)
	require.Equal(t, "updated", locked.Clone().Workspace)
	locked.Store(new(Config))
	require.Equal(t, new(Config), locked.Clone())
}

func TestLockedConfigConcurrent(t *testing.T) {
	locked := new(LockedConfig)

	var group errgroup.Group

	for worker := range 3 {
		group.Go(func() error {
			for range 100 {
				switch worker {
				case 0:
					locked.Store(&Config{Workspace: "stored", WorkDir: "stored"})
				case 1:
					if err := locked.Do(func(cfg *Config) error {
						cfg.Workspace, cfg.WorkDir = "updated", "updated"
						return nil
					}); err != nil {
						return err
					}
				case 2:
					if cfg := locked.Clone(); cfg.Workspace != cfg.WorkDir {
						return fmt.Errorf("inconsistent snapshot: workspace=%q workdir=%q", cfg.Workspace, cfg.WorkDir)
					}
				}
			}

			return nil
		})
	}

	require.NoError(t, group.Wait())
}
