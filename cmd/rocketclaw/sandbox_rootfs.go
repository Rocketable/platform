package main

// The emulated shell's Ubuntu rootfs, built on Linux with mmdebstrap and embedded in Linux builds; it is not
// committed. Elsewhere the generator builds nothing and emulated shells refuse.
//go:generate go run ../../internal/rocketclaw/sandbox/rootfs -out sandbox-rootfs-$GOARCH.tar.zst
