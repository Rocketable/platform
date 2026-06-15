package emoji

import (
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEmojiAliasesAreBidirectional(t *testing.T) {
	flag, ok := FromAlias(":checkered_flag:")
	require.True(t, ok)
	assert.Equal(t, "🏁", flag)

	aliases, ok := ToAliases("🏁")
	require.True(t, ok)
	assert.True(t, slices.Contains(aliases, ":checkered_flag:"))

	primary, ok := ToPrimaryAlias("🏁")
	require.True(t, ok)
	assert.Equal(t, ":checkered_flag:", primary)
}

func TestCanonicalizeLeadingAlias(t *testing.T) {
	assert.Equal(t, "🏁 finish", CanonicalizeLeadingAlias(" :checkered_flag: finish "))
	assert.Equal(t, ":custom: finish", CanonicalizeLeadingAlias(" :custom: finish "))
}
