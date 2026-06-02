package main

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGeneratorMainWritesGeneratedSource(t *testing.T) {
	workspace := t.TempDir()
	cwd, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(workspace))
	t.Cleanup(func() { require.NoError(t, os.Chdir(cwd)) })

	main()

	data, err := os.ReadFile("gcm_gen.go")
	require.NoError(t, err)

	generated := string(data)

	assert.Contains(t, generated, "package aesgcm8")
	assert.Contains(t, generated, "TagSize = 8")
	assert.Contains(t, generated, "gcmMinimumTagSize    = 8")
	assert.NotContains(t, generated, "minTagSize()")
}

func TestGeneratorExtractsAndRewritesSource(t *testing.T) {
	gcmSource := `package cipher

const gcmMinimumTagSize    = 12

// gcmAble is an interface
type gcmFallback struct { cipher    Block }

func (g *gcmFallback) Open() { _ = g.cipher; _ = alias.InexactOverlap; _ = alias.AnyOverlap; _ = byteorder.BEPutUint64; _ = byteorder.BEUint64; _ = byteorder.BEPutUint32; _ = byteorder.BEUint32; _ = gcm.GHASH; _ = "crypto/cipher: "; _ = "cipher: " }
func gcmCounterCryptGeneric(b Block) {}
func gcmAuth() {}
func deriveCounter() {}
// sliceForAppend
func sliceForAppend() {
}
`
	ghashSource := `package gcm

type gcmFieldElement struct{}
// GHASH is exposed to allow crypto/cipher to implement non-AES GCM modes.
// It is not allowed as a stand-alone operation in FIPS mode because it
// is not ACVP tested.
func GHASH(key *[16]byte, inputs ...[]byte) []byte {
	fips140.RecordNonApproved()
	return nil
}
func ghash() { _ = byteorder.BEPutUint64; _ = byteorder.BEUint64; _ = "crypto/cipher"; _ = "FIPS mode" }
func ghashMul() {}
`
	aliasSource := `package alias

func AnyOverlap() bool { return false }

// InexactOverlap reports overlap.
func InexactOverlap() bool { return AnyOverlap() }
`

	checkSourceShape("crypto/cipher/gcm.go", []byte(gcmSource))
	checkSourceShape("crypto/internal/fips140/aes/gcm/ghash.go", []byte(ghashSource))
	checkSourceShape("crypto/internal/fips140/alias/alias.go", []byte(aliasSource))

	generated := generatedSource("go version test", []string{"// Source: test"}, map[string]string{
		"crypto/cipher/gcm.go":                     gcmSource,
		"crypto/internal/fips140/aes/gcm/ghash.go": ghashSource,
		"crypto/internal/fips140/alias/alias.go":   aliasSource,
	})

	assert.Contains(t, generated, "Derived from the Go standard library GCM implementation in go version test")
	assert.Contains(t, generated, "type gcm struct")
	assert.Contains(t, generated, "block     cipher.Block")
	assert.Contains(t, generated, "func gcmCounterCryptGeneric(b cipher.Block)")
	assert.Contains(t, generated, "func ghashAll")
	assert.Contains(t, generated, "func anyOverlap")
	assert.Contains(t, generated, "func inexactOverlap")
	assert.NotContains(t, generated, "gcmFallback")
	assert.NotContains(t, generated, "alias.InexactOverlap")
	assert.NotContains(t, generated, "byteorder.")
	assert.NotContains(t, generated, "func GHASH")
}

func TestGeneratorStringHelpers(t *testing.T) {
	text := "prefix START body END suffix"

	assert.Equal(t, "START body ", between(text, "START", "END"))
	assert.Equal(t, "body END suffix", after(text, "body"))
	assert.Equal(t, "one two", replaceAll("1 2", []struct{ old, new string }{{"1", "one"}, {"2", "two"}}))
	assert.NotContains(t, replaceAll("alias.AnyOverlap", []struct{ old, new string }{{"alias.AnyOverlap", "anyOverlap"}}), "alias.")
}
