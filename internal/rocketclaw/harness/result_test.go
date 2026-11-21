package harness

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Rocketable/platform/internal/rocketclaw/codemode"
)

func TestNewFileValue(t *testing.T) {
	file := newFileValue(codemode.FileSpec{Name: "notes.pdf", Mime: "application/pdf", Content: "%PDF-1.4", Source: "read notes.pdf"})

	require.Equal(t, "notes.pdf (application/pdf, 8 B)", file.String())
	require.Equal(t, []string{"images[]"}, codeModeFileArgs("decision"))
}
