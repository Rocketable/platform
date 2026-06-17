package rocketcode

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStandaloneConfigAutoApprovePermissionsFromEnv(t *testing.T) {
	t.Run("empty leaves disabled", func(t *testing.T) {
		t.Setenv("ROCKETCODE_AUTO_APPROVE_PERMISSIONS", "")

		config, err := StandaloneConfigFromEnv()

		require.NoError(t, err)
		require.False(t, config.AutoApprovePermissions)
	})

	t.Run("non empty enables", func(t *testing.T) {
		t.Setenv("ROCKETCODE_AUTO_APPROVE_PERMISSIONS", "1")

		config, err := StandaloneConfigFromEnv()

		require.NoError(t, err)
		require.True(t, config.AutoApprovePermissions)
	})
}
