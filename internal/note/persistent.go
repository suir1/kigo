package note

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/suir1/kigo/internal/secure"
	"golang.org/x/crypto/hkdf"
)

const (
	PersistentProtocolVersion = 1
	PersistentRecordVersion   = 1
	MaxPersistentMessageSize  = 3 << 20
	MaxPersistentRecordSize   = 2 << 20
	persistentKeyInfo         = "kigo-note-store-v1"
)

const (
	PersistentState = "state"
	PersistentPut   = "put"
	PersistentError = "error"
)

type PersistentRecord struct {
	Version    int    `json:"version"`
	Salt       []byte `json:"salt"`
	Nonce      []byte `json:"nonce"`
	Ciphertext []byte `json:"ciphertext"`
}

type PersistentMessage struct {
	Type           string            `json:"type"`
	Version        int               `json:"version"`
	Generation     uint64            `json:"generation"`
	BaseGeneration uint64            `json:"base_generation,omitempty"`
	Record         *PersistentRecord `json:"record,omitempty"`
	Error          string            `json:"error,omitempty"`
}

func PersistentPadToken(pad string) (string, error) {
	pad = NormalizePad(pad)
	if err := ValidatePad(pad); err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte("kigo-note-pad:" + pad))
	return hex.EncodeToString(sum[:]), nil
}

func SealPersistentDocument(code string, document Document) (PersistentRecord, error) {
	code, err := secure.ValidateCode(code)
	if err != nil {
		return PersistentRecord{}, err
	}
	document.Pad = NormalizePad(document.Pad)
	if err := ValidateDocument(document); err != nil {
		return PersistentRecord{}, err
	}
	plaintext, err := json.Marshal(document)
	if err != nil {
		return PersistentRecord{}, err
	}
	salt := make([]byte, 16)
	nonce := make([]byte, 12)
	if _, err := rand.Read(salt); err != nil {
		return PersistentRecord{}, err
	}
	if _, err := rand.Read(nonce); err != nil {
		return PersistentRecord{}, err
	}
	aead, err := persistentAEAD(code, salt)
	if err != nil {
		return PersistentRecord{}, err
	}
	record := PersistentRecord{
		Version:    PersistentRecordVersion,
		Salt:       salt,
		Nonce:      nonce,
		Ciphertext: aead.Seal(nil, nonce, plaintext, persistentAAD(document.Pad)),
	}
	if err := record.Validate(); err != nil {
		return PersistentRecord{}, err
	}
	return record, nil
}

func OpenPersistentDocument(code, pad string, record PersistentRecord) (Document, error) {
	code, err := secure.ValidateCode(code)
	if err != nil {
		return Document{}, err
	}
	pad = NormalizePad(pad)
	if err := ValidatePad(pad); err != nil {
		return Document{}, err
	}
	if err := record.Validate(); err != nil {
		return Document{}, err
	}
	aead, err := persistentAEAD(code, record.Salt)
	if err != nil {
		return Document{}, err
	}
	plaintext, err := aead.Open(nil, record.Nonce, record.Ciphertext, persistentAAD(pad))
	if err != nil {
		return Document{}, fmt.Errorf("decrypt persistent note: %w", err)
	}
	var document Document
	if err := json.Unmarshal(plaintext, &document); err != nil {
		return Document{}, fmt.Errorf("decode persistent note: %w", err)
	}
	document.Pad = NormalizePad(document.Pad)
	if document.Pad != pad {
		return Document{}, errors.New("persistent note pad mismatch")
	}
	if err := ValidateDocument(document); err != nil {
		return Document{}, fmt.Errorf("validate persistent note: %w", err)
	}
	return document, nil
}

func (r PersistentRecord) Validate() error {
	if r.Version != PersistentRecordVersion {
		return fmt.Errorf("unsupported persistent note record version %d", r.Version)
	}
	if len(r.Salt) != 16 {
		return errors.New("invalid persistent note salt")
	}
	if len(r.Nonce) != 12 {
		return errors.New("invalid persistent note nonce")
	}
	if len(r.Ciphertext) < 16 || len(r.Ciphertext) > MaxPersistentRecordSize {
		return errors.New("invalid persistent note ciphertext size")
	}
	return nil
}

func (m PersistentMessage) ValidateClient() error {
	if m.Version != PersistentProtocolVersion {
		return fmt.Errorf("unsupported persistent note protocol version %d", m.Version)
	}
	if m.Type != PersistentPut {
		return fmt.Errorf("unsupported persistent note message type %q", m.Type)
	}
	if m.Record == nil {
		return errors.New("persistent note update is missing its record")
	}
	return m.Record.Validate()
}

func persistentAEAD(code string, salt []byte) (cipher.AEAD, error) {
	key := make([]byte, 16)
	if _, err := io.ReadFull(hkdf.New(sha256.New, []byte(secure.NormalizeCode(code)), salt, []byte(persistentKeyInfo)), key); err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func persistentAAD(pad string) []byte {
	return []byte(persistentKeyInfo + "\x00" + NormalizePad(pad))
}
