package gitaccount

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// Keys derive from one secret: the AES-256-GCM key of the token store and the HMAC key of the connect state.
type Keys struct {
	aead  cipher.AEAD
	state []byte
}

// NewKeys needs at least 32 bytes of secret material (the chart generates them).
func NewKeys(material []byte) (*Keys, error) {
	if len(material) < 32 {
		return nil, errors.New("git accounts: the encryption key needs at least 32 bytes")
	}
	derive := func(label string) []byte {
		h := sha256.Sum256(append([]byte("eduide git accounts "+label+"\n"), material...))
		return h[:]
	}
	b, err := aes.NewCipher(derive("encryption"))
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(b)
	if err != nil {
		return nil, err
	}
	return &Keys{aead: aead, state: derive("state")}, nil
}

// seal encrypts plain; aad binds the blob to one Secret name, so it cannot be moved to another user.
func (k *Keys) seal(plain, aad []byte) []byte {
	nonce := make([]byte, k.aead.NonceSize())
	_, _ = rand.Read(nonce)
	return k.aead.Seal(nonce, nonce, plain, aad)
}

func (k *Keys) open(blob, aad []byte) ([]byte, error) {
	n := k.aead.NonceSize()
	if len(blob) < n {
		return nil, errors.New("git accounts: blob too short")
	}
	return k.aead.Open(nil, blob[:n], blob[n:], aad)
}

// State is the signed content of the OAuth state parameter. It holds no secret: the browser and the provider see it.
type State struct {
	Owner    string `json:"o"`
	Provider string `json:"p"`
	// Nonce also sits in the flow cookie, which binds the flow to the browser that started it.
	Nonce    string `json:"n"`
	Expires  int64  `json:"e"` // unix seconds
	ReturnTo string `json:"r,omitempty"`
}

// StateTTL is how long a connect flow may take.
const StateTTL = 10 * time.Minute

var (
	ErrStateInvalid = errors.New("state is not valid")
	ErrStateExpired = errors.New("state is expired")
)

// NewNonce returns a random value for State.Nonce.
func NewNonce() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func (k *Keys) mac(parts ...string) []byte {
	m := hmac.New(sha256.New, k.state)
	m.Write([]byte(strings.Join(parts, "\n")))
	return m.Sum(nil)
}

// Sign returns payload.signature, both base64url.
func (k *Keys) Sign(s State) string {
	b, _ := json.Marshal(s)
	p := base64.RawURLEncoding.EncodeToString(b)
	return p + "." + base64.RawURLEncoding.EncodeToString(k.mac("state", p))
}

// ParseState checks signature and expiry.
func (k *Keys) ParseState(raw string, now time.Time) (State, error) {
	p, sig, ok := strings.Cut(raw, ".")
	if !ok || len(raw) > 2048 {
		return State{}, ErrStateInvalid
	}
	got, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil || !hmac.Equal(got, k.mac("state", p)) {
		return State{}, ErrStateInvalid
	}
	b, err := base64.RawURLEncoding.DecodeString(p)
	var s State
	if err != nil || json.Unmarshal(b, &s) != nil || s.Owner == "" || s.Provider == "" || s.Nonce == "" {
		return State{}, ErrStateInvalid
	}
	if now.Unix() >= s.Expires {
		return State{}, ErrStateExpired
	}
	return s, nil
}

// Verifier is the PKCE code verifier of the flow with this nonce. It is derived with the state key, so it needs
// no storage and never appears in a URL; whoever sees the redirect cannot compute it.
func (k *Keys) Verifier(nonce string) string {
	return base64.RawURLEncoding.EncodeToString(k.mac("pkce", nonce))
}
