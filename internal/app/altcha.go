package app

// Altcha proof-of-work challenges, server side.
//
// The client is handed a random salt plus the SHA-256 of (salt || secret
// number) and must brute-force the number back out — a second or so of CPU per
// signup, negligible for a person, expensive for a bulk registration script.
// The challenge is HMAC-signed so only challenges this server issued count,
// each solution is accepted once, and challenges expire.

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	altchaMaxNumber = 300000
	altchaTTL       = 15 * time.Minute
)

type altchaChallenge struct {
	Algorithm string `json:"algorithm"`
	Challenge string `json:"challenge"`
	MaxNumber int    `json:"maxnumber"`
	Salt      string `json:"salt"`
	Signature string `json:"signature"`
}

type altchaPayload struct {
	Algorithm string `json:"algorithm"`
	Challenge string `json:"challenge"`
	Number    int64  `json:"number"`
	Salt      string `json:"salt"`
	Signature string `json:"signature"`
}

// altchaVerifier issues and checks challenges. The HMAC key lives in the
// settings table so restarts do not invalidate outstanding challenges.
type altchaVerifier struct {
	key  []byte
	mu   sync.Mutex
	used map[string]time.Time // signature -> when it was spent
}

func newAltchaVerifier(key []byte) *altchaVerifier {
	return &altchaVerifier{key: key, used: map[string]time.Time{}}
}

func altchaHash(salt string, number int64) string {
	sum := sha256.Sum256([]byte(salt + strconv.FormatInt(number, 10)))
	return hex.EncodeToString(sum[:])
}

func (a *altchaVerifier) sign(challenge string) string {
	mac := hmac.New(sha256.New, a.key)
	mac.Write([]byte(challenge))
	return hex.EncodeToString(mac.Sum(nil))
}

func (a *altchaVerifier) newChallenge() (altchaChallenge, error) {
	saltBytes := make([]byte, 12)
	if _, err := rand.Read(saltBytes); err != nil {
		return altchaChallenge{}, err
	}
	n, err := rand.Int(rand.Reader, big.NewInt(altchaMaxNumber))
	if err != nil {
		return altchaChallenge{}, err
	}
	salt := fmt.Sprintf("%s?expires=%d", hex.EncodeToString(saltBytes), time.Now().Add(altchaTTL).Unix())
	challenge := altchaHash(salt, n.Int64())
	return altchaChallenge{
		Algorithm: "SHA-256",
		Challenge: challenge,
		MaxNumber: altchaMaxNumber,
		Salt:      salt,
		Signature: a.sign(challenge),
	}, nil
}

var errAltcha = errors.New("captcha check failed, please try again")

// verify checks a base64 payload from the widget's hidden input.
func (a *altchaVerifier) verify(encoded string) error {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encoded))
	if err != nil {
		return errAltcha
	}
	var p altchaPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return errAltcha
	}
	if p.Algorithm != "SHA-256" || p.Number < 0 || p.Number > altchaMaxNumber {
		return errAltcha
	}
	// the signature must be ours: without this the client could mint its own
	// trivially-solvable challenge
	if !hmac.Equal([]byte(a.sign(p.Challenge)), []byte(p.Signature)) {
		return errAltcha
	}
	if altchaHash(p.Salt, p.Number) != p.Challenge {
		return errAltcha
	}
	if exp, ok := altchaExpiry(p.Salt); ok && time.Now().After(exp) {
		return errAltcha
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.sweepLocked()
	if _, spent := a.used[p.Signature]; spent {
		return errAltcha
	}
	a.used[p.Signature] = time.Now()
	return nil
}

// altchaExpiry reads the ?expires= parameter carried in the salt.
func altchaExpiry(salt string) (time.Time, bool) {
	_, query, found := strings.Cut(salt, "?")
	if !found {
		return time.Time{}, false
	}
	v, err := url.ParseQuery(query)
	if err != nil {
		return time.Time{}, false
	}
	unix, err := strconv.ParseInt(v.Get("expires"), 10, 64)
	if err != nil {
		return time.Time{}, false
	}
	return time.Unix(unix, 0), true
}

func (a *altchaVerifier) sweepLocked() {
	cutoff := time.Now().Add(-altchaTTL - time.Minute)
	for sig, at := range a.used {
		if at.Before(cutoff) {
			delete(a.used, sig)
		}
	}
}
