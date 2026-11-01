// Command rootfs builds the emulated shell's pinned Ubuntu rootfs for RocketClaw to embed. cmd/rocketclaw runs
// it through go generate:
//
//	go run ./internal/rocketclaw/sandbox/rootfs -out sandbox-rootfs-$GOARCH.tar.zst [-arch amd64|arm64] [-update]
//
// It needs Linux, mmdebstrap and unprivileged user namespaces, plus qemu-user-static for the other
// architecture. It writes the zstd tar and, beside it, an SPDX SBOM. The build fails when the installed
// packages differ from packages.<arch>.lock beside this file, so the guest cannot change without a commit.
// Elsewhere it builds nothing: RocketClaw there has no sandbox.
//
// Upgrading the guest: set snapshot to a newer archive moment for security updates, or suite to the next
// Ubuntu release, then run `make -C internal/rocketclaw sandbox-rootfs-update` on Linux. It rewrites both
// locks; review their diffs and commit them with the constant.
package main

import (
	"archive/tar"
	"bufio"
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"
)

// The archive snapshot pins every package version; the suite is the Ubuntu release, 26.04 LTS.
const (
	snapshot = "20261031T000000Z"
	suite    = "resolute"
	mirror   = "https://snapshot.ubuntu.com/ubuntu/" + snapshot
)

// The archive signing keys, from ubuntu-keyring 2023.11.28.1 in the snapshot (sha256
// 36de43b15853ccae0028e9a767613770c704833f82586f28eb262f0311adb8a8); trusted as committed.
//
//go:embed ubuntu-archive-keyring.gpg
var keyring []byte

func main() {
	out := flag.String("out", "", "output .tar.zst")
	// GOARCH names match Ubuntu's for amd64 and arm64, the architectures the sandbox supports.
	arch := flag.String("arch", runtime.GOARCH, "the guest's architecture")
	update := flag.Bool("update", false, "rewrite packages.<arch>.lock instead of checking it")

	flag.Parse()

	if *out == "" {
		log.Fatal("rootfs: -out is required")
	}

	if runtime.GOOS != "linux" {
		log.Printf("rootfs: skipped on %s; RocketClaw built here has no emulated shell", runtime.GOOS)
		return
	}

	pc := make([]uintptr, 1)
	runtime.Callers(1, pc)
	self, _ := runtime.CallersFrames(pc).Next()

	if err := build(*out, *arch, filepath.Dir(self.File), *update); err != nil {
		log.Fatal("rootfs: ", err)
	}
}

// build writes the image to out for arch, checking or, with update, rewriting the lock in lockDir.
func build(out, arch, lockDir string, update bool) error {
	work := out + ".work"
	if err := os.RemoveAll(work); err != nil {
		return fmt.Errorf("clear %s: %w", work, err)
	}

	if err := os.MkdirAll(work, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", work, err)
	}
	defer func() { _ = os.RemoveAll(work) }()

	keyringPath := filepath.Join(work, "ubuntu-archive-keyring.gpg")
	if err := os.WriteFile(keyringPath, keyring, 0o644); err != nil {
		return fmt.Errorf("write keyring: %w", err)
	}

	raw := filepath.Join(work, "rootfs.tar")
	epoch, _ := time.Parse("20060102T150405Z", snapshot)

	// Installed on top of Ubuntu's essential set (spec §4.2). Adding one needs a security review and a lock
	// update.
	packages := "bash,bzip2,ca-certificates,coreutils,diffutils,file,findutils,gawk,grep,gzip,jq,nodejs,procps,python3,sed,sqlite3,tar,wget,xz-utils"

	cmd := exec.Command("mmdebstrap", "--mode=unshare", "--variant=essential", "--arch="+arch, "--format=tar",
		"--keyring="+keyringPath, "--aptopt=Acquire::Check-Valid-Until false", "--aptopt=Acquire::Retries 5",
		"--include="+packages, suite, raw,
		fmt.Sprintf("deb %s %s main universe", mirror, suite),
		fmt.Sprintf("deb %s %s-updates main universe", mirror, suite),
		fmt.Sprintf("deb %s %s-security main universe", mirror, suite))
	cmd.Env = append(os.Environ(), fmt.Sprintf("SOURCE_DATE_EPOCH=%d", epoch.Unix()))
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("mmdebstrap: %w", err)
	}

	installed, release, err := rewrite(raw, out)
	if err != nil {
		return err
	}

	lock := strings.Join(installed, "\n") + "\n"
	lockPath := filepath.Join(lockDir, "packages."+arch+".lock")

	if update {
		if err := os.WriteFile(lockPath, []byte(lock), 0o644); err != nil {
			return fmt.Errorf("write lock: %w", err)
		}
	} else if want, err := os.ReadFile(lockPath); err != nil || string(want) != lock {
		return fmt.Errorf("installed packages differ from %s (review, then rerun with -update): %v", lockPath, err)
	}

	return writeSBOM(strings.TrimSuffix(out, ".tar.zst")+".spdx.json", arch, release, installed)
}

// rewrite copies the mmdebstrap tar into the zstd archive RocketClaw embeds: no device nodes (the sandbox
// mounts its own /dev), no setuid or setgid bits, no file capabilities, plus the guest user and js-exec. It
// returns the installed packages as name=version lines and the Ubuntu release (VERSION_ID) they came from.
func rewrite(raw, out string) (installed []string, release string, err error) {
	in, err := os.Open(raw)
	if err != nil {
		return nil, "", fmt.Errorf("open %s: %w", raw, err)
	}
	defer func() { _ = in.Close() }()

	f, err := os.Create(out)
	if err != nil {
		return nil, "", fmt.Errorf("create %s: %w", out, err)
	}
	defer func() { _ = f.Close() }()

	zw, err := zstd.NewWriter(f, zstd.WithEncoderLevel(zstd.SpeedBestCompression))
	if err != nil {
		return nil, "", fmt.Errorf("zstd: %w", err)
	}

	tw := tar.NewWriter(zw)

	tr := tar.NewReader(in)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}

		if err != nil {
			return nil, "", fmt.Errorf("read %s: %w", raw, err)
		}

		name := filepath.Clean(h.Name)
		if h.Typeflag == tar.TypeChar || h.Typeflag == tar.TypeBlock || h.Typeflag == tar.TypeFifo || strings.HasPrefix(name, "dev/") {
			continue
		}

		if _, ok := h.PAXRecords["SCHILY.xattr.security.capability"]; ok {
			return nil, "", fmt.Errorf("%s has file capabilities (spec §4.2)", name)
		}

		data, err := io.ReadAll(tr)
		if err != nil {
			return nil, "", fmt.Errorf("read %s: %w", name, err)
		}

		switch name {
		case "etc/passwd":
			data = append(data, "user:x:1000:1000::/home/user:/bin/bash\n"...)
		case "etc/group":
			data = append(data, "user:x:1000:\n"...)
		case "var/lib/dpkg/status":
			installed = dpkgInstalled(data)
		case "usr/lib/os-release":
			_, rest, _ := strings.Cut(string(data), "VERSION_ID=\"")
			release, _, _ = strings.Cut(rest, "\"")
		}

		h.Mode &^= int64(fs.ModeSetuid | fs.ModeSetgid | 0o6000)
		h.Size, h.Uid, h.Gid, h.Uname, h.Gname, h.PAXRecords = int64(len(data)), 0, 0, "", "", nil
		h.Format = tar.FormatPAX

		if err := tw.WriteHeader(h); err != nil {
			return nil, "", fmt.Errorf("write %s: %w", name, err)
		}

		if _, err := tw.Write(data); err != nil {
			return nil, "", fmt.Errorf("write %s: %w", name, err)
		}
	}

	if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeSymlink, Name: "./usr/local/bin/js-exec", Linkname: "/usr/bin/node", Mode: 0o777}); err != nil {
		return nil, "", fmt.Errorf("write js-exec: %w", err)
	}

	if err := errors.Join(tw.Close(), zw.Close(), f.Close()); err != nil {
		return nil, "", fmt.Errorf("close %s: %w", out, err)
	}

	if release == "" {
		return nil, "", errors.New("usr/lib/os-release has no VERSION_ID")
	}

	return installed, release, nil
}

// dpkgInstalled lists the installed packages in dpkg's status file as sorted name=version lines.
func dpkgInstalled(status []byte) []string {
	var out []string

	var name, version, state string

	flush := func() {
		if state == "install ok installed" {
			out = append(out, name+"="+version)
		}

		name, version, state = "", "", ""
	}

	s := bufio.NewScanner(bytes.NewReader(status))
	s.Buffer(nil, 1<<20)

	for s.Scan() {
		k, v, _ := strings.Cut(s.Text(), ": ")

		switch k {
		case "":
			flush()
		case "Package":
			name = v
		case "Version":
			version = v
		case "Status":
			state = v
		}
	}

	flush()
	slices.Sort(out)

	return out
}

// writeSBOM writes an SPDX 2.3 document listing every installed package.
func writeSBOM(path, arch, release string, pkgs []string) error {
	type ref struct {
		Category string `json:"referenceCategory"`
		Type     string `json:"referenceType"`
		Locator  string `json:"referenceLocator"`
	}

	type pkg struct {
		SPDXID           string `json:"SPDXID"`
		Name             string `json:"name"`
		Version          string `json:"versionInfo"`
		DownloadLocation string `json:"downloadLocation"`
		Supplier         string `json:"supplier"`
		ExternalRefs     []ref  `json:"externalRefs"`
	}

	type creationInfo struct {
		Created  string   `json:"created"`
		Creators []string `json:"creators"`
	}

	created, _ := time.Parse("20060102T150405Z", snapshot)

	doc := struct {
		SPDXVersion  string       `json:"spdxVersion"`
		DataLicense  string       `json:"dataLicense"`
		SPDXID       string       `json:"SPDXID"`
		Name         string       `json:"name"`
		Namespace    string       `json:"documentNamespace"`
		CreationInfo creationInfo `json:"creationInfo"`
		Packages     []pkg        `json:"packages"`
	}{
		SPDXVersion: "SPDX-2.3", DataLicense: "CC0-1.0", SPDXID: "SPDXRef-DOCUMENT",
		Name:         "rocketclaw-sandbox-rootfs-" + arch,
		Namespace:    fmt.Sprintf("https://github.com/Rocketable/platform/sandbox-rootfs/%s/%s", snapshot, arch),
		CreationInfo: creationInfo{created.Format(time.RFC3339), []string{"Tool: internal/rocketclaw/sandbox/rootfs"}},
	}

	for _, p := range pkgs {
		name, version, _ := strings.Cut(p, "=")
		doc.Packages = append(doc.Packages, pkg{
			SPDXID:           "SPDXRef-Package-" + strings.ReplaceAll(name, "+", "-"), // dpkg names are [a-z0-9.+-]
			Name:             name,
			Version:          version,
			DownloadLocation: "NOASSERTION",
			Supplier:         "Organization: Canonical",
			ExternalRefs:     []ref{{"PACKAGE-MANAGER", "purl", fmt.Sprintf("pkg:deb/ubuntu/%s@%s?arch=%s&distro=ubuntu-%s", name, version, arch, release)}},
		})
	}

	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("sbom: %w", err)
	}

	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("sbom: %w", err)
	}

	return nil
}
