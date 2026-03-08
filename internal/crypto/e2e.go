// Package crypto implements the E2E encryption primitives for agentcockpit.
//
// Scheme: ECDH P-256 key agreement → HKDF-SHA-256 → AES-256-GCM per message.
// The relay server never sees plaintext payload; it forwards opaque ciphertext.
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
)



const (
	ivLen  = 12 // AES-GCM nonce
	keyLen = 32 // AES-256
	hkdfInfo = "agentcockpit-session-v1"
)

// GenerateEphemeralKeypair creates a fresh P-256 ECDH keypair.
// The private key must be zeroed by the caller when done.
func GenerateEphemeralKeypair() (*ecdh.PrivateKey, error) {
	return ecdh.P256().GenerateKey(rand.Reader)
}

// MarshalPublicKeySPKI encodes a P-256 public key to SPKI DER bytes.
// This is the format WebCrypto's exportKey("spki") / importKey("spki") uses.
func MarshalPublicKeySPKI(pub *ecdh.PublicKey) ([]byte, error) {
	return x509.MarshalPKIXPublicKey(pub)
}

// ParsePublicKeySPKI decodes an SPKI DER blob into a P-256 public key.
func ParsePublicKeySPKI(der []byte) (*ecdh.PublicKey, error) {
	pub, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		return nil, fmt.Errorf("parse SPKI: %w", err)
	}
	ecdhPub, ok := pub.(*ecdh.PublicKey)
	if !ok {
		return nil, errors.New("SPKI key is not ECDH")
	}
	if ecdhPub.Curve() != ecdh.P256() {
		return nil, errors.New("SPKI key is not P-256")
	}
	return ecdhPub, nil
}

// DeriveSessionKey performs ECDH between localPriv and the peer's SPKI-encoded
// public key, then runs HKDF-SHA-256 to produce a 32-byte AES-256-GCM key.
// sessionID is used as the HKDF salt to bind the key to a specific session.
func DeriveSessionKey(localPriv *ecdh.PrivateKey, peerSPKI []byte, sessionID string) ([]byte, error) {
	peerPub, err := ParsePublicKeySPKI(peerSPKI)
	if err != nil {
		return nil, err
	}
	shared, err := localPriv.ECDH(peerPub)
	if err != nil {
		return nil, fmt.Errorf("ECDH: %w", err)
	}

	key, err := hkdf.Key(sha256.New, shared, []byte(sessionID), hkdfInfo, keyLen)
	if err != nil {
		return nil, fmt.Errorf("HKDF: %w", err)
	}
	return key, nil
}

// Seal encrypts plaintext with AES-256-GCM using a fresh random IV.
// Returns [12-byte IV][ciphertext+16-byte GCM tag].
// additionalData is authenticated but not encrypted (use sessionID or requestID).
func Seal(key, plaintext, additionalData []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	iv := make([]byte, ivLen)
	if _, err := io.ReadFull(rand.Reader, iv); err != nil {
		return nil, err
	}
	ciphertext := gcm.Seal(nil, iv, plaintext, additionalData)
	out := make([]byte, ivLen+len(ciphertext))
	copy(out, iv)
	copy(out[ivLen:], ciphertext)
	return out, nil
}

// Open decrypts a blob produced by Seal: [12-byte IV][ciphertext+tag].
func Open(key, sealed, additionalData []byte) ([]byte, error) {
	if len(sealed) < ivLen {
		return nil, errors.New("sealed data too short")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	iv := sealed[:ivLen]
	ciphertext := sealed[ivLen:]
	return gcm.Open(nil, iv, ciphertext, additionalData)
}

// ZeroKey overwrites a key slice with zeros to reduce time sensitive key
// material lives in memory.
func ZeroKey(key []byte) {
	for i := range key {
		key[i] = 0
	}
}
