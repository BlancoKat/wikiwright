// Package auth hashes passwords with Argon2id and checks TOTP codes.
package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base32"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/argon2"
)

// Params controls Argon2id. Production uses ProductionParams.
type Params struct {
	Time    uint32
	Memory  uint32
	Threads uint8
	KeyLen  uint32
}

// ProductionParams is an OWASP-aligned Argon2id choice for a single-owner wiki.
var ProductionParams = Params{Time: 2, Memory: 64 * 1024, Threads: 1, KeyLen: 32}

// LightParams is only for tests.
var LightParams = Params{Time: 1, Memory: 8, Threads: 1, KeyLen: 32}

// Hash returns a PHC-style argon2id string.
func Hash(password string, p Params) (string, error) {
	if p.KeyLen == 0 {
		p.KeyLen = 32
	}
	if p.Threads == 0 {
		p.Threads = 1
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	sum := argon2.IDKey([]byte(password), salt, p.Time, p.Memory, p.Threads, p.KeyLen)
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s",
		p.Memory, p.Time, p.Threads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(sum),
	), nil
}

// Verify checks password against a Hash result. A malformed hash returns false.
func Verify(password, encoded string) bool {
	p, salt, want, err := parsePHC(encoded)
	if err != nil {
		return false
	}
	got := argon2.IDKey([]byte(password), salt, p.Time, p.Memory, p.Threads, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

func parsePHC(s string) (Params, []byte, []byte, error) {
	parts := strings.Split(s, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return Params{}, nil, nil, fmt.Errorf("bad hash")
	}
	var mem, timeCost uint32
	var threads uint8
	for _, kv := range strings.Split(parts[3], ",") {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			return Params{}, nil, nil, fmt.Errorf("bad params")
		}
		n, err := strconv.Atoi(v)
		if err != nil {
			return Params{}, nil, nil, err
		}
		switch k {
		case "m":
			mem = uint32(n)
		case "t":
			timeCost = uint32(n)
		case "p":
			threads = uint8(n)
		}
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return Params{}, nil, nil, err
	}
	sum, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return Params{}, nil, nil, err
	}
	return Params{Time: timeCost, Memory: mem, Threads: threads, KeyLen: uint32(len(sum))}, salt, sum, nil
}

// RandomToken returns n bytes as hex.
func RandomToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	const hexdigits = "0123456789abcdef"
	out := make([]byte, len(b)*2)
	for i, v := range b {
		out[i*2] = hexdigits[v>>4]
		out[i*2+1] = hexdigits[v&0x0f]
	}
	return string(out), nil
}

// NewTOTPSecret returns a base32 secret (no padding).
func NewTOTPSecret() (string, error) {
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return strings.TrimRight(base32.StdEncoding.EncodeToString(b), "="), nil
}

// OTPAuthURL is an otpauth URI a user can type into an authenticator.
func OTPAuthURL(issuer, account, secret string) string {
	return fmt.Sprintf("otpauth://totp/%s:%s?secret=%s&issuer=%s&algorithm=SHA1&digits=6&period=30",
		urlQuery(issuer), urlQuery(account), secret, urlQuery(issuer))
}

func urlQuery(s string) string {
	// Path and query components in otpauth URIs are percent-encoded.
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.' {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// CurrentCode is the 6-digit TOTP code for now.
func CurrentCode(secret string, now time.Time) string {
	return hotp(secret, now.Unix()/30)
}

// TOTP verifies a 6-digit code in a ±1 step window.
func TOTP(secret, code string, now time.Time) bool {
	code = strings.TrimSpace(code)
	if len(code) != 6 {
		return false
	}
	for _, c := range code {
		if c < '0' || c > '9' {
			return false
		}
	}
	counter := now.Unix() / 30
	for delta := int64(-1); delta <= 1; delta++ {
		if subtle.ConstantTimeCompare([]byte(hotp(secret, counter+delta)), []byte(code)) == 1 {
			return true
		}
	}
	return false
}

func hotp(secret string, counter int64) string {
	raw, err := base32.StdEncoding.DecodeString(strings.ToUpper(secret) + strings.Repeat("=", (8-len(secret)%8)%8))
	if err != nil {
		return ""
	}
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], uint64(counter))
	mac := hmac.New(sha1.New, raw)
	_, _ = mac.Write(buf[:])
	sum := mac.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	bin := (int(sum[off])&0x7f)<<24 | int(sum[off+1])<<16 | int(sum[off+2])<<8 | int(sum[off+3])
	return fmt.Sprintf("%06d", bin%1000000)
}
