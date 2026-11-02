//go:build !linux || !(amd64 || arm64)

package main

// sandboxRootfs is empty here: emulated shells need Linux.
var sandboxRootfs []byte
