// Package keys manages the local ed25519 signing key used for claims and
// ledger entries, and the secret that salts redaction placeholders.
package keys

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/BTONGK/credence/internal/home"
	"github.com/BTONGK/credence/spec"
)

// Signer signs content hashes.
type Signer struct {
	priv  ed25519.PrivateKey
	Pub   ed25519.PublicKey
	KeyID string
}

var (
	once    sync.Once
	signer  *Signer
	loadErr error
)

// Load returns the local signer, generating a key on first use.
func Load() (*Signer, error) {
	once.Do(func() { signer, loadErr = loadOrCreate(home.Keys()) })
	return signer, loadErr
}

func loadOrCreate(dir string) (*Signer, error) {
	path := filepath.Join(dir, "ledger_ed25519.pem")
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		_, priv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return nil, err
		}
		der, err := x509.MarshalPKCS8PrivateKey(priv)
		if err != nil {
			return nil, err
		}
		b = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
		if err := os.WriteFile(path, b, 0o600); err != nil {
			return nil, err
		}
		pub := priv.Public().(ed25519.PublicKey)
		_ = os.WriteFile(filepath.Join(dir, "ledger_ed25519.pub"), []byte(PublicKeyString(pub)+"\n"), 0o644)
	} else if err != nil {
		return nil, err
	}
	blk, _ := pem.Decode(b)
	if blk == nil {
		return nil, errors.New("bad ledger key file")
	}
	k, err := x509.ParsePKCS8PrivateKey(blk.Bytes)
	if err != nil {
		return nil, err
	}
	priv, ok := k.(ed25519.PrivateKey)
	if !ok {
		return nil, errors.New("ledger key is not ed25519")
	}
	pub := priv.Public().(ed25519.PublicKey)
	return &Signer{priv: priv, Pub: pub, KeyID: KeyID(pub)}, nil
}

// KeyID is a short fingerprint of a public key.
func KeyID(pub ed25519.PublicKey) string { return spec.ShortHex(spec.HashBytes(pub), 12) }

// PublicKeyString encodes a public key as "ed25519:<b64>".
func PublicKeyString(pub ed25519.PublicKey) string {
	return "ed25519:" + base64.StdEncoding.EncodeToString(pub)
}

// ParsePublicKey decodes "ed25519:<b64>".
func ParsePublicKey(s string) (ed25519.PublicKey, error) {
	b, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(strings.TrimSpace(s), "ed25519:"))
	if err != nil || len(b) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("bad public key")
	}
	return ed25519.PublicKey(b), nil
}

// NewSigner wraps a private key (tests and fixtures; real keys come from Load).
func NewSigner(priv ed25519.PrivateKey) *Signer {
	pub := priv.Public().(ed25519.PublicKey)
	return &Signer{priv: priv, Pub: pub, KeyID: KeyID(pub)}
}

// Sign returns "ed25519:<keyid>:<b64sig>" over the given hash string.
func (s *Signer) Sign(hash string) (string, error) {
	sig := ed25519.Sign(s.priv, []byte(hash))
	return "ed25519:" + s.KeyID + ":" + base64.StdEncoding.EncodeToString(sig), nil
}

// Verify checks a signature produced by Sign.
func Verify(pub ed25519.PublicKey, hash, sig string) bool {
	parts := strings.SplitN(sig, ":", 3)
	if len(parts) != 3 || parts[0] != "ed25519" {
		return false
	}
	raw, err := base64.StdEncoding.DecodeString(parts[2])
	if err != nil {
		return false
	}
	return ed25519.Verify(pub, []byte(hash), raw)
}

// RedactionKey returns the local secret used to salt redaction placeholders.
func RedactionKey() []byte {
	path := filepath.Join(home.Keys(), "redact.key")
	if b, err := os.ReadFile(path); err == nil && len(b) >= 32 {
		return b
	}
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	_ = os.WriteFile(path, b, 0o600)
	return b
}
