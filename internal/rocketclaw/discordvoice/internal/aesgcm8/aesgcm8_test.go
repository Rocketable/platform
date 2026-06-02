package aesgcm8

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"testing"
)

func TestNewUsesEightByteTags(t *testing.T) {
	block, err := aes.NewCipher([]byte("0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}

	aead, err := New(block)
	if err != nil {
		t.Fatal(err)
	}

	if got := aead.Overhead(); got != TagSize {
		t.Fatalf("Overhead() = %d; want %d", got, TagSize)
	}

	if got := aead.NonceSize(); got != 12 {
		t.Fatalf("NonceSize() = %d; want 12", got)
	}
}

func TestRoundTripAndStdlibTagPrefix(t *testing.T) {
	block, err := aes.NewCipher([]byte("0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}

	aead8, err := New(block)
	if err != nil {
		t.Fatal(err)
	}

	aead16, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}

	nonce := []byte("nonce-12byte")
	plaintext := []byte("discord dave opus frame")
	aad := []byte("authenticated bytes")

	sealed8 := aead8.Seal(nil, nonce, plaintext, aad)

	sealed16 := aead16.Seal(nil, nonce, plaintext, aad)
	if !bytes.Equal(sealed8[:len(plaintext)], sealed16[:len(plaintext)]) {
		t.Fatalf("ciphertext differs from stdlib AES-GCM")
	}

	if !bytes.Equal(sealed8[len(plaintext):], sealed16[len(plaintext):len(plaintext)+TagSize]) {
		t.Fatalf("8-byte tag is not stdlib AES-GCM tag prefix")
	}

	opened, err := aead8.Open(nil, nonce, sealed8, aad)
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(opened, plaintext) {
		t.Fatalf("Open() = %q; want %q", opened, plaintext)
	}
}

func TestOpenRejectsTampering(t *testing.T) {
	block, err := aes.NewCipher([]byte("0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}

	aead, err := New(block)
	if err != nil {
		t.Fatal(err)
	}

	nonce := []byte("nonce-12byte")
	sealed := aead.Seal(nil, nonce, []byte("frame"), nil)

	sealed[len(sealed)-1] ^= 1
	if _, err := aead.Open(nil, nonce, sealed, nil); err == nil {
		t.Fatal("Open() succeeded with tampered tag")
	}
}

func TestNewGCMRejectsInvalidParameters(t *testing.T) {
	block, err := aes.NewCipher([]byte("0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}

	if _, err := newGCM(block, gcmStandardNonceSize, gcmMinimumTagSize-1); err == nil {
		t.Fatal("newGCM(short tag) succeeded")
	}

	if _, err := newGCM(block, gcmStandardNonceSize, gcmBlockSize+1); err == nil {
		t.Fatal("newGCM(long tag) succeeded")
	}

	if _, err := newGCM(block, 0, TagSize); err == nil {
		t.Fatal("newGCM(zero nonce) succeeded")
	}

	if _, err := newGCM(testBlock{size: 8}, gcmStandardNonceSize, TagSize); err == nil {
		t.Fatal("newGCM(short block) succeeded")
	}
}

func TestNonStandardNonceRoundTrip(t *testing.T) {
	block, err := aes.NewCipher([]byte("0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}

	aead, err := newGCM(block, 8, TagSize)
	if err != nil {
		t.Fatal(err)
	}

	nonce := []byte("8-byte-n")
	plaintext := []byte("discord media")
	aad := []byte("aad")
	sealed := aead.Seal(nil, nonce, plaintext, aad)

	opened, err := aead.Open(nil, nonce, sealed, aad)
	if err != nil {
		t.Fatalf("Open() returned error: %v", err)
	}

	if !bytes.Equal(opened, plaintext) {
		t.Fatalf("Open() = %q; want %q", opened, plaintext)
	}
}

func TestOpenRejectsShortCiphertextAndClearsDestination(t *testing.T) {
	block, err := aes.NewCipher([]byte("0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}

	aead, err := New(block)
	if err != nil {
		t.Fatal(err)
	}

	nonce := []byte("nonce-12byte")
	if _, err := aead.Open(nil, nonce, []byte{1, 2, 3}, nil); err == nil {
		t.Fatal("Open(short ciphertext) succeeded")
	}

	plaintext := []byte("frame")
	sealed := aead.Seal(nil, nonce, plaintext, nil)
	sealed[0] ^= 1

	buf := append([]byte("prefix"), bytes.Repeat([]byte{0xff}, len(plaintext))...)
	if _, err := aead.Open(buf[:len("prefix")], nonce, sealed, nil); err == nil {
		t.Fatal("Open(tampered ciphertext) succeeded")
	}

	if tail := buf[len("prefix"):]; !bytes.Equal(tail, make([]byte, len(plaintext))) {
		t.Fatalf("Open(tampered ciphertext) left dst tail %x; want zeroed", tail)
	}
}

func TestInexactOverlap(t *testing.T) {
	buf := []byte{0, 1, 2, 3, 4}

	tests := []struct {
		name string
		x    []byte
		y    []byte
		want bool
	}{
		{name: "empty left", x: nil, y: buf, want: false},
		{name: "empty right", x: buf, y: nil, want: false},
		{name: "same start", x: buf[1:3], y: buf[1:4], want: false},
		{name: "disjoint same backing", x: buf[:2], y: buf[2:], want: false},
		{name: "shifted", x: buf[:3], y: buf[1:4], want: true},
		{name: "separate arrays", x: []byte{1}, y: []byte{1}, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := inexactOverlap(tt.x, tt.y); got != tt.want {
				t.Fatalf("inexactOverlap(%v, %v) = %t; want %t", tt.x, tt.y, got, tt.want)
			}
		})
	}
}

type testBlock struct {
	size int
}

func (b testBlock) BlockSize() int {
	return b.size
}

func (b testBlock) Encrypt(dst, src []byte) {
	copy(dst, src)
}

func (b testBlock) Decrypt(dst, src []byte) {
	copy(dst, src)
}
