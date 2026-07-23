package relay

import (
	"strings"
	"testing"
	"time"
)

func TestRelayCredentialRoundTrip(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	credential, err := IssueCredential("shared-secret", "room-token", now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(credential, "v1.") {
		t.Fatalf("credential = %q", credential)
	}
	if !ValidateCredential("shared-secret", "room-token", credential, now) {
		t.Fatal("issued credential was rejected")
	}
}

func TestRelayCredentialRejectsWrongScopeExpiryAndTampering(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	credential, err := IssueCredential("shared-secret", "room-token", now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name       string
		secret     string
		room       string
		credential string
		now        time.Time
	}{
		{name: "wrong secret", secret: "wrong", room: "room-token", credential: credential, now: now},
		{name: "wrong room", secret: "shared-secret", room: "other-room", credential: credential, now: now},
		{name: "expired", secret: "shared-secret", room: "room-token", credential: credential, now: now.Add(time.Hour)},
		{name: "tampered", secret: "shared-secret", room: "room-token", credential: credential + "x", now: now},
		{name: "malformed", secret: "shared-secret", room: "room-token", credential: "v1.bad.value", now: now},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if ValidateCredential(tt.secret, tt.room, tt.credential, tt.now) {
				t.Fatal("invalid credential was accepted")
			}
		})
	}
}

func TestIssueRelayCredentialValidatesInputs(t *testing.T) {
	tests := []struct {
		secret  string
		room    string
		expires time.Time
	}{
		{room: "room", expires: time.Now().Add(time.Hour)},
		{secret: "secret", expires: time.Now().Add(time.Hour)},
		{secret: "secret", room: "room"},
	}
	for _, tt := range tests {
		if _, err := IssueCredential(tt.secret, tt.room, tt.expires); err == nil {
			t.Fatalf("IssueCredential(%q, %q, %v) succeeded", tt.secret, tt.room, tt.expires)
		}
	}
}
