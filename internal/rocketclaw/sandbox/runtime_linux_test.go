//go:build amd64 || arm64

package sandbox

import (
	"archive/tar"
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/klauspost/compress/zstd"
	"github.com/stretchr/testify/require"
)

// rootfsArchive is a small zstd tar in the generator's shape.
func rootfsArchive(t *testing.T, entries ...tar.Header) []byte {
	t.Helper()

	var buf bytes.Buffer

	zw, err := zstd.NewWriter(&buf)
	require.NoError(t, err)

	tw := tar.NewWriter(zw)

	for i := range entries {
		require.NoError(t, tw.WriteHeader(&entries[i]))
	}

	require.NoError(t, tw.Close())
	require.NoError(t, zw.Close())

	return buf.Bytes()
}

// Unpacking writes the image once per content, strips setuid, locks read-only directories last, removes other
// versions, and refuses entries the generator never writes.
func TestUnpack(t *testing.T) {
	image := rootfsArchive(t,
		tar.Header{Typeflag: tar.TypeDir, Name: "./usr/", Mode: 0o555},
		tar.Header{Typeflag: tar.TypeDir, Name: "./usr/bin/", Mode: 0o755},
		tar.Header{Typeflag: tar.TypeReg, Name: "./usr/bin/sh", Mode: 0o755 | int64(fs.ModeSetuid)},
		tar.Header{Typeflag: tar.TypeLink, Name: "./usr/bin/bash", Linkname: "./usr/bin/sh"},
		tar.Header{Typeflag: tar.TypeSymlink, Name: "./bin", Linkname: "usr/bin", Mode: 0o777},
	)

	dir := t.TempDir()
	old := filepath.Join(dir, "rootfs-old")
	require.NoError(t, os.Mkdir(old, 0o755))

	dst := filepath.Join(dir, "rootfs-new")
	require.NoError(t, unpack(image, dir, dst))
	require.NoDirExists(t, old, "older images are removed")

	info, err := os.Lstat(filepath.Join(dst, "usr", "bin", "sh"))
	require.NoError(t, err)
	require.Equal(t, fs.FileMode(0o755), info.Mode().Perm(), "setuid never survives")

	info, err = os.Stat(filepath.Join(dst, "usr"))
	require.NoError(t, err)
	require.Equal(t, fs.FileMode(0o555), info.Mode().Perm(), "read-only directories are filled before being locked")

	require.NoError(t, os.Chmod(filepath.Join(dst, "usr"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dst, "usr", "marker"), nil, 0o644))
	require.NoError(t, unpack(image, dir, dst))
	require.FileExists(t, filepath.Join(dst, "usr", "marker"), "a complete image is kept as is")

	fifo := rootfsArchive(t, tar.Header{Typeflag: tar.TypeFifo, Name: "./dev/initctl", Mode: 0o600})
	require.ErrorContains(t, unpack(fifo, dir, filepath.Join(dir, "rootfs-fifo")), "unexpected tar entry type")
}

// Prepare fails closed: without the image or without bubblewrap there is no Runtime that runs anything.
func TestPrepareFailsClosed(t *testing.T) {
	dir := t.TempDir()
	require.ErrorContains(t, Prepare(t.Context(), nil, dir).Err(), "no sandbox rootfs")

	t.Setenv("PATH", t.TempDir())

	rt := Prepare(t.Context(), rootfsArchive(t, tar.Header{Typeflag: tar.TypeDir, Name: "./usr/", Mode: 0o755}), dir)
	require.ErrorContains(t, rt.Err(), "bubblewrap is required")

	_, err := rt.Run(t.Context(), &Command{Script: "true"}, probeHost{})
	require.ErrorContains(t, err, "bubblewrap is required")
}

// Outside a sandbox the guest entry points refuse: this is not the relay, there is no broker, and init checks
// its arguments before touching limits.
func TestGuestOutsideSandbox(t *testing.T) {
	_, ok := Guest([]string{"rocketclaw"})
	require.False(t, ok)

	require.Equal(t, 126, relay("deploy", nil))
	require.ErrorContains(t, guestInit(nil), "usage: rocketclaw init")
	require.ErrorContains(t, guestInit([]string{"x", "1024", "5", "--", "/bin/true"}), "init:")
}
