//go:build linux

package sandbox

import _ "embed"

//go:embed packages.amd64.lock
var packagesLock []byte
