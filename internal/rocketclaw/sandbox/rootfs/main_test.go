package main

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/klauspost/compress/zstd"
	"github.com/stretchr/testify/require"
)

// TestMain lets this binary stand in for mmdebstrap: it writes a small Ubuntu-like tar where the real one
// would, with the things the build must strip or refuse.
func TestMain(m *testing.M) {
	if filepath.Base(os.Args[0]) != "mmdebstrap" {
		os.Exit(m.Run())
	}

	raw := os.Args[slices.Index(os.Args, suite)+1]

	f, err := os.Create(raw)
	if err != nil {
		panic(err)
	}

	tw := tar.NewWriter(f)
	entries := []struct {
		h    tar.Header
		data string
	}{
		{tar.Header{Typeflag: tar.TypeDir, Name: "./usr/", Mode: 0o755}, ""},
		{tar.Header{Typeflag: tar.TypeSymlink, Name: "./bin", Linkname: "usr/bin", Mode: 0o777}, ""},
		{tar.Header{Typeflag: tar.TypeChar, Name: "./dev/null", Mode: 0o666}, ""},
		{tar.Header{Typeflag: tar.TypeReg, Name: "./usr/bin/su", Mode: 0o4755 | int64(fs.ModeSetuid), Uid: 7, Uname: "root"}, "#!/bin/true"},
		{tar.Header{Typeflag: tar.TypeReg, Name: "./etc/passwd", Mode: 0o644}, "root:x:0:0:root:/root:/bin/bash\n"},
		{tar.Header{Typeflag: tar.TypeReg, Name: "./etc/group", Mode: 0o644}, "root:x:0:\n"},
		{tar.Header{Typeflag: tar.TypeReg, Name: "./usr/lib/os-release", Mode: 0o644}, "PRETTY_NAME=\"Ubuntu 26.04 LTS\"\nVERSION_ID=\"26.04\"\n"},
		{tar.Header{Typeflag: tar.TypeReg, Name: "./var/lib/dpkg/status", Mode: 0o644}, "Package: zlib1g\nStatus: install ok installed\nVersion: 1:1.3\n\nPackage: gone\nStatus: deinstall ok config-files\nVersion: 2\n\nPackage: bash\nStatus: install ok installed\nVersion: 5.3-2ubuntu1\n"},
	}

	if os.Getenv("ROOTFS_TEST_CAPABILITY") == "1" {
		entries = append(entries, struct {
			h    tar.Header
			data string
		}{tar.Header{Typeflag: tar.TypeReg, Name: "./usr/bin/ping", Mode: 0o755, Format: tar.FormatPAX, PAXRecords: map[string]string{"SCHILY.xattr.security.capability": "x"}}, "ping"})
	}

	for i := range entries {
		e := &entries[i]
		e.h.Size = int64(len(e.data))

		if err := tw.WriteHeader(&e.h); err != nil {
			panic(err)
		}

		if _, err := io.WriteString(tw, e.data); err != nil {
			panic(err)
		}
	}

	if err := tw.Close(); err != nil {
		panic(err)
	}

	if err := f.Close(); err != nil {
		panic(err)
	}

	os.Exit(0)
}

// The build turns mmdebstrap's tar into the image RocketClaw embeds, with devices gone, setuid bits dropped,
// the guest user added and js-exec linked; locks the installed packages; and writes an SBOM for the release
// the image says it is. A lock that disagrees fails the build; a file capability refuses the image.
func TestBuild(t *testing.T) {
	self, err := os.Executable()
	require.NoError(t, err)

	bin := t.TempDir()
	require.NoError(t, os.Symlink(self, filepath.Join(bin, "mmdebstrap")))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	dir := t.TempDir()
	out, lockDir := filepath.Join(dir, "sandbox-rootfs-amd64.tar.zst"), filepath.Join(dir, "lock")
	require.NoError(t, os.Mkdir(lockDir, 0o755))

	require.NoError(t, build(out, "amd64", lockDir, true))

	lock, err := os.ReadFile(filepath.Join(lockDir, "packages.amd64.lock"))
	require.NoError(t, err)
	require.Equal(t, "bash=5.3-2ubuntu1\nzlib1g=1:1.3\n", string(lock), "installed packages, sorted; removed ones left out")

	files := map[string]tar.Header{}
	data := map[string]string{}

	f, err := os.Open(out)
	require.NoError(t, err)
	t.Cleanup(func() { _ = f.Close() })

	zr, err := zstd.NewReader(f)
	require.NoError(t, err)
	t.Cleanup(zr.Close)

	tr := tar.NewReader(zr)

	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}

		require.NoError(t, err)

		body, err := io.ReadAll(tr)
		require.NoError(t, err)

		files[filepath.Clean(h.Name)] = *h
		data[filepath.Clean(h.Name)] = string(body)
	}

	require.NotContains(t, files, "dev/null", "device nodes are left out")
	require.Equal(t, int64(0o755), files["usr/bin/su"].Mode, "setuid is dropped")
	require.Equal(t, 0, files["usr/bin/su"].Uid)
	require.Empty(t, files["usr/bin/su"].Uname)
	require.Equal(t, "usr/bin", files["bin"].Linkname)
	require.Equal(t, "/usr/bin/node", files["usr/local/bin/js-exec"].Linkname)
	require.Equal(t, "root:x:0:0:root:/root:/bin/bash\nuser:x:1000:1000::/home/user:/bin/bash\n", data["etc/passwd"])
	require.Equal(t, "root:x:0:\nuser:x:1000:\n", data["etc/group"])

	var sbom struct {
		Name     string `json:"name"`
		Packages []struct {
			Name         string `json:"name"`
			Version      string `json:"versionInfo"`
			ExternalRefs []struct {
				Locator string `json:"referenceLocator"`
			} `json:"externalRefs"`
		} `json:"packages"`
	}

	raw, err := os.ReadFile(filepath.Join(dir, "sandbox-rootfs-amd64.spdx.json"))
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &sbom))
	require.Equal(t, "rocketclaw-sandbox-rootfs-amd64", sbom.Name)
	require.Len(t, sbom.Packages, 2)
	require.Equal(t, "pkg:deb/ubuntu/bash@5.3-2ubuntu1?arch=amd64&distro=ubuntu-26.04", sbom.Packages[0].ExternalRefs[0].Locator, "the release comes from the image's os-release")

	require.NoError(t, build(out, "amd64", lockDir, false), "a matching lock passes")

	require.NoError(t, os.WriteFile(filepath.Join(lockDir, "packages.amd64.lock"), []byte("bash=5.2\n"), 0o644))
	err = build(out, "amd64", lockDir, false)
	require.ErrorContains(t, err, "installed packages differ from")
	require.ErrorContains(t, err, "rerun with -update")

	require.ErrorContains(t, build(out, "arm64", lockDir, false), "no such file", "a missing lock fails too")

	t.Setenv("ROOTFS_TEST_CAPABILITY", "1")
	require.ErrorContains(t, build(out, "amd64", lockDir, true), "usr/bin/ping has file capabilities")
	require.NoDirExists(t, out+".work", "the work directory is removed")
}

// dpkgInstalled keeps only installed packages, as name=version, sorted; a status file with no trailing blank
// line still yields its last package.
func TestDpkgInstalled(t *testing.T) {
	status := "Package: b\nStatus: install ok installed\nVersion: 2\n\nPackage: half\nStatus: install ok half-configured\nVersion: 1\n\nPackage: a\nVersion: 1\nStatus: install ok installed"
	require.Equal(t, []string{"a=1", "b=2"}, dpkgInstalled([]byte(status)))
	require.Empty(t, dpkgInstalled(nil))
}

// A tar without os-release is refused: the SBOM would name no release.
func TestRewriteNeedsOSRelease(t *testing.T) {
	var buf bytes.Buffer

	tw := tar.NewWriter(&buf)
	require.NoError(t, tw.WriteHeader(&tar.Header{Typeflag: tar.TypeDir, Name: "./usr/", Mode: 0o755}))
	require.NoError(t, tw.Close())

	raw := filepath.Join(t.TempDir(), "rootfs.tar")
	require.NoError(t, os.WriteFile(raw, buf.Bytes(), 0o644))

	_, _, err := rewrite(raw, filepath.Join(t.TempDir(), "out.tar.zst"))
	require.ErrorContains(t, err, "os-release has no VERSION_ID")
}
