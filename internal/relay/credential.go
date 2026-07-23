package relay

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
	"time"
)

const relayCredentialVersion = "v1"

func IssueCredential(secret, roomToken string, expires time.Time) (string, error) {
	if secret == "" {
		return "", errors.New("relay credential secret is empty")
	}
	if roomToken == "" {
		return "", errors.New("relay credential room token is empty")
	}
	if expires.IsZero() {
		return "", errors.New("relay credential expiry is empty")
	}
	expiry := strconv.FormatInt(expires.Unix(), 10)
	signature := signCredential(secret, roomToken, expiry)
	return relayCredentialVersion + "." + expiry + "." +
		base64.RawURLEncoding.EncodeToString(signature), nil
}

func ValidateCredential(secret, roomToken, credential string, now time.Time) bool {
	if secret == "" || roomToken == "" || credential == "" {
		return false
	}
	parts := strings.Split(credential, ".")
	if len(parts) != 3 || parts[0] != relayCredentialVersion {
		return false
	}
	expires, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || !now.Before(time.Unix(expires, 0)) {
		return false
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(signature) != sha256.Size {
		return false
	}
	return hmac.Equal(signature, signCredential(secret, roomToken, parts[1]))
}

func signCredential(secret, roomToken, expiry string) []byte {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte("kigo-relay-v1\n"))
	_, _ = mac.Write([]byte(roomToken))
	_, _ = mac.Write([]byte("\n"))
	_, _ = mac.Write([]byte(expiry))
	return mac.Sum(nil)
}
