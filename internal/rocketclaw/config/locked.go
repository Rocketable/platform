package config

import (
	"maps"
	"slices"
	"sync"
)

// LockedConfig owns configuration and serializes work that depends on its runtime assets.
// A LockedConfig must not be copied after first use.
type LockedConfig struct {
	mu sync.Mutex

	cfg Config
}

// NewLockedConfig stores an independent copy of cfg.
func NewLockedConfig(cfg *Config) *LockedConfig {
	locked := new(LockedConfig)
	locked.Store(cfg)

	return locked
}

// Store replaces the configuration with an independent copy.
func (c *LockedConfig) Store(cfg *Config) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.cfg = clone(cfg)
}

// Clone returns an independent configuration snapshot.
func (c *LockedConfig) Clone() *Config {
	c.mu.Lock()
	defer c.mu.Unlock()

	cfg := clone(&c.cfg)

	return &cfg
}

// Do holds the lock throughout work. work must not retain cfg or its mutable fields,
// or call Store, Clone, or Do on c.
func (c *LockedConfig) Do(work func(cfg *Config) error) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	return work(&c.cfg)
}

func clone(source *Config) Config {
	cfg := *source

	cfg.Overlays = slices.Clone(cfg.Overlays)
	cfg.Models = maps.Clone(cfg.Models)
	cfg.WebUsers = maps.Clone(cfg.WebUsers)
	cfg.Environment = slices.Clone(cfg.Environment)
	cfg.Providers = maps.Clone(cfg.Providers)

	cfg.MCPServers = maps.Clone(cfg.MCPServers)
	for name, server := range cfg.MCPServers {
		server.Args = slices.Clone(server.Args)
		server.Env = maps.Clone(server.Env)
		server.Headers = maps.Clone(server.Headers)
		cfg.MCPServers[name] = server
	}

	cfg.Slack.Channels = slices.Clone(cfg.Slack.Channels)
	for i := range cfg.Slack.Channels {
		cfg.Slack.Channels[i].Agents = slices.Clone(cfg.Slack.Channels[i].Agents)
		cfg.Slack.Channels[i].AllowedUserIDs = slices.Clone(cfg.Slack.Channels[i].AllowedUserIDs)
	}

	if cfg.Web.Sentry.TracesSampleRate != nil {
		rate := *cfg.Web.Sentry.TracesSampleRate
		cfg.Web.Sentry.TracesSampleRate = &rate
	}

	return cfg
}
