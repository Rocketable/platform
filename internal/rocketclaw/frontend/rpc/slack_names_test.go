package rpc

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

func TestSlackNames(t *testing.T) {
	channels := &mockChannels{SlackNamesFunc: func(_ context.Context, ids []string) map[string]string {
		resolved := map[string]string{"U1": "alan", "S1": "cs-operators", "C1": "general"}
		names := make(map[string]string)

		for _, id := range ids {
			if name, ok := resolved[id]; ok {
				names[id] = name
			}
		}

		return names
	}}
	cfg := &config.Config{WebUsers: map[netip.Addr]string{netip.MustParseAddr("192.0.2.1"): "alice"}}
	listener, err := Listen(testSocketPath(t))
	require.NoError(t, err)

	transport := grpc.NewServer()
	New(&mockBackend{}, nil, config.NewLockedConfig(cfg), channels, &mockCronJobs{}).Register(transport)

	var serving errgroup.Group
	serving.Go(func() error {
		if err := transport.Serve(listener); err != nil {
			return fmt.Errorf("serve test RPC: %w", err)
		}

		return nil
	})
	t.Cleanup(func() {
		transport.Stop()
		require.NoError(t, serving.Wait())
	})

	connection, err := grpc.NewClient("unix:"+listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, connection.Close()) })

	response := httptest.NewRecorder()
	NewHTTPHandler(connection, config.SentryConfig{}).ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/SlackNames", strings.NewReader(`{"ids":["U1","S1","C1","Umissing"]}`)))
	require.Equal(t, http.StatusOK, response.Code)

	var body struct {
		Names map[string]string `json:"names"`
	}
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
	require.Equal(t, map[string]string{"C1": "general", "S1": "cs-operators", "U1": "alan"}, body.Names)

	_, err = invoke[SlackNamesResponse](t.Context(), connection, "SlackNames", &SlackNamesRequest{Ids: []string{"U1"}})
	require.Equal(t, codes.Unauthenticated, status.Code(err))
	require.Len(t, channels.SlackNamesCalls(), 1)
}
