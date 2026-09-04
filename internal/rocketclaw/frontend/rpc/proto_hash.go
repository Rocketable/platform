package rpc

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
)

//go:embed proto/web.proto
var protoSource []byte

func protoSHA256() string {
	sum := sha256.Sum256(protoSource)
	return hex.EncodeToString(sum[:])
}
