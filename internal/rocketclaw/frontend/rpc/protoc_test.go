package rpc

import (
	"os/exec"
	"testing"

	"github.com/stretchr/testify/require"
)

const protocInstall = `Install the protobuf compiler and Go plugins, then put $(go env GOPATH)/bin on PATH:

  brew install protobuf
  go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.12
  go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.6.2
`

func TestProtocToolchain(t *testing.T) {
	for _, tool := range []struct {
		name, flag string
	}{
		{"protoc", "--version"},
		{"protoc-gen-go", "--version"},
		{"protoc-gen-go-grpc", "--version"},
	} {
		_, err := exec.LookPath(tool.name)
		require.NoError(t, err, "%s not found.\n\n%s", tool.name, protocInstall)
		out, err := exec.Command(tool.name, tool.flag).CombinedOutput()
		require.NoError(t, err, "%s %s failed: %s\n\n%s", tool.name, tool.flag, out, protocInstall)
		t.Logf("%s: %s", tool.name, out)
	}
}
