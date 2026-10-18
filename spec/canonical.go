package spec

import (
	"bytes"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"encoding/json"
	"strings"
)

// Canonical returns the canonical JSON encoding of v: object keys sorted,
// no insignificant whitespace, no HTML escaping. Claims, verdicts and ledger
// entries are hashed over this form so any later edit is detectable.
func Canonical(v any) ([]byte, error) {
	raw, err := marshalNoEscape(v)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var generic any
	if err := dec.Decode(&generic); err != nil {
		return nil, err
	}
	return marshalNoEscape(generic)
}

func marshalNoEscape(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// Hash returns "sha256:<hex>" over the canonical encoding of v.
func Hash(v any) (string, error) {
	b, err := Canonical(v)
	if err != nil {
		return "", err
	}
	return HashBytes(b), nil
}

// MustHash is Hash for values that always encode.
func MustHash(v any) string {
	h, err := Hash(v)
	if err != nil {
		panic(err)
	}
	return h
}

// HashBytes returns "sha256:<hex>" of b.
func HashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

var crockford = base32.NewEncoding("0123456789ABCDEFGHJKMNPQRSTVWXYZ").WithPadding(base32.NoPadding)

// ShortID derives an n-character Crockford base32 id from a "sha256:<hex>" hash.
func ShortID(hash string, n int) string {
	h := strings.TrimPrefix(hash, "sha256:")
	b, err := hex.DecodeString(h)
	if err != nil || len(b) == 0 {
		sum := sha256.Sum256([]byte(hash))
		b = sum[:]
	}
	s := crockford.EncodeToString(b)
	if len(s) > n {
		s = s[:n]
	}
	return s
}

// ShortHex returns the first n hex characters of a "sha256:<hex>" hash.
func ShortHex(hash string, n int) string {
	h := strings.TrimPrefix(hash, "sha256:")
	if len(h) > n {
		return h[:n]
	}
	return h
}
