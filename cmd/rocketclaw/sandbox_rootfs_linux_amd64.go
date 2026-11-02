package main

import _ "embed"

//go:embed sandbox-rootfs-amd64.tar.zst
var sandboxRootfs []byte
