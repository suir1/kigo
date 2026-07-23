package secure

import (
	"strings"
	"testing"
)

func TestRoomTokenNormalizesCode(t *testing.T) {
	a := RoomToken("k7m9-q2")
	b := RoomToken("K7M9Q2")
	if a != b {
		t.Fatalf("tokens differ: %s != %s", a, b)
	}
	if RoomToken(" project-Alpha-2026 ") != RoomToken("PROJECT-ALPHA-2026") {
		t.Fatal("custom mnemonic room tokens differ")
	}
}

func TestValidateCodeNormalizesAndRejectsInvalidValues(t *testing.T) {
	code, err := ValidateCode(" k7m9-q2 ")
	if err != nil {
		t.Fatal(err)
	}
	if code != "K7M9Q2" {
		t.Fatalf("code = %q", code)
	}
	for _, value := range []string{"", "ABC12", "ABC1?O", "-ABC1234", "ABC1234-", "ABC--1234", strings.Repeat("A", MaxCodeLength+1)} {
		if _, err := ValidateCode(value); err == nil {
			t.Fatalf("ValidateCode(%q) succeeded", value)
		}
	}
	if code, err := ValidateCode("abc123"); err != nil || code != "ABC123" {
		t.Fatalf("legacy alphanumeric code = %q, %v", code, err)
	}
	if code, err := ValidateCode(" project-Alpha-2026 "); err != nil || code != "PROJECT-ALPHA-2026" {
		t.Fatalf("custom mnemonic code = %q, %v", code, err)
	}
	if code, err := ValidateCode("project2026"); err != nil || code != "PROJECT2026" {
		t.Fatalf("custom alphanumeric code = %q, %v", code, err)
	}
}

func TestResolveSenderCodeUsesCustomOrRandomCode(t *testing.T) {
	custom, err := ResolveSenderCode("release-2026")
	if err != nil || custom != "RELEASE-2026" {
		t.Fatalf("custom code = %q, %v", custom, err)
	}
	random, err := ResolveSenderCode("")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateCode(random); err != nil || len(random) != CodeLength {
		t.Fatalf("random code = %q, %v", random, err)
	}
}

func TestSessionRoundTripAndWrongCodeFails(t *testing.T) {
	sender, err := NewSession("K7M9Q2", "sender", "receiver")
	if err != nil {
		t.Fatal(err)
	}
	receiver, err := NewSession("K7M9Q2", "sender", "receiver")
	if err != nil {
		t.Fatal(err)
	}
	ct, err := sender.Encrypt(7, []byte("hello"))
	if err != nil {
		t.Fatal(err)
	}
	pt, err := receiver.Decrypt(7, ct)
	if err != nil {
		t.Fatal(err)
	}
	if string(pt) != "hello" {
		t.Fatalf("got %q", pt)
	}
	wrong, err := NewSession("AAAAAA", "sender", "receiver")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wrong.Decrypt(7, ct); err == nil {
		t.Fatal("wrong code decrypted ciphertext")
	}
}
