//go:build linux

package sandbox

import _ "embed"

//go:embed packages.arm64.lock
var packagesLock []byte
