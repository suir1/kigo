package secure

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"strings"
	"unicode"

	"golang.org/x/crypto/hkdf"
)

const (
	CodeAlphabet  = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	CodeLength    = 6
	MaxCodeLength = 64
)

func GenerateCode() (string, error) {
	var out strings.Builder
	out.Grow(CodeLength)
	buf := make([]byte, CodeLength)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	for _, b := range buf {
		out.WriteByte(CodeAlphabet[int(b)%len(CodeAlphabet)])
	}
	return out.String(), nil
}

func NormalizeCode(code string) string {
	code = strings.TrimSpace(code)
	code = strings.ToUpper(code)
	compact := strings.Map(func(value rune) rune {
		if value == '-' || unicode.IsSpace(value) {
			return -1
		}
		return value
	}, code)
	if len(compact) == CodeLength && pairingAlphanumeric(compact) {
		return compact
	}
	return code
}

func ValidateCode(code string) (string, error) {
	normalized := NormalizeCode(code)
	if len(normalized) < CodeLength || len(normalized) > MaxCodeLength {
		return "", fmt.Errorf("pairing code must be between %d and %d characters", CodeLength, MaxCodeLength)
	}
	if strings.Contains(normalized, "-") {
		if strings.HasPrefix(normalized, "-") || strings.HasSuffix(normalized, "-") || strings.Contains(normalized, "--") {
			return "", errors.New("pairing code contains an empty mnemonic segment")
		}
		for _, segment := range strings.Split(normalized, "-") {
			if !pairingAlphanumeric(segment) {
				return "", errors.New("pairing code may contain only letters, digits, and mnemonic separators")
			}
		}
	} else if !pairingAlphanumeric(normalized) {
		return "", errors.New("pairing code may contain only letters and digits")
	}
	return normalized, nil
}

func ResolveSenderCode(code string) (string, error) {
	if strings.TrimSpace(code) == "" {
		return GenerateCode()
	}
	return ValidateCode(code)
}

func pairingAlphanumeric(value string) bool {
	if value == "" {
		return false
	}
	for _, character := range []byte(value) {
		if !((character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9')) {
			return false
		}
	}
	return true
}

func RoomToken(code string) string {
	sum := sha256.Sum256([]byte("kigo-room:" + NormalizeCode(code)))
	return hex.EncodeToString(sum[:])
}

func RandomNonce() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

type Session struct {
	aead        cipher.AEAD
	noncePrefix [4]byte
}

func NewSession(code, senderNonce, receiverNonce string) (*Session, error) {
	return NewSessionWithInfo(code, senderNonce, receiverNonce, "kigo-v1 aes-128-gcm")
}

func NewSessionWithInfo(code, senderNonce, receiverNonce, info string) (*Session, error) {
	salt := []byte(senderNonce + ":" + receiverNonce)
	material, err := derive(sha256.New, []byte(NormalizeCode(code)), salt, []byte(info), 20)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(material[:16])
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	s := &Session{aead: aead}
	copy(s.noncePrefix[:], material[16:20])
	return s, nil
}

func derive(h func() hash.Hash, secret, salt, info []byte, size int) ([]byte, error) {
	out := make([]byte, size)
	reader := hkdf.New(h, secret, salt, info)
	if _, err := io.ReadFull(reader, out); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Session) Encrypt(seq uint64, plaintext []byte) ([]byte, error) {
	if s == nil {
		return nil, errors.New("secure session is nil")
	}
	return s.aead.Seal(nil, s.nonce(seq), plaintext, aad(seq)), nil
}

func (s *Session) Decrypt(seq uint64, ciphertext []byte) ([]byte, error) {
	if s == nil {
		return nil, errors.New("secure session is nil")
	}
	return s.aead.Open(nil, s.nonce(seq), ciphertext, aad(seq))
}

func (s *Session) nonce(seq uint64) []byte {
	nonce := make([]byte, 12)
	copy(nonce[:4], s.noncePrefix[:])
	for i := 0; i < 8; i++ {
		nonce[11-i] = byte(seq >> (8 * i))
	}
	return nonce
}

func aad(seq uint64) []byte {
	return []byte(fmt.Sprintf("kigo-v1:%d", seq))
}
