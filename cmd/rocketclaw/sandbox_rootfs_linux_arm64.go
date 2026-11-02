package main

import _ "embed"

//go:embed sandbox-rootfs-arm64.tar.zst
var sandboxRootfs []byte
