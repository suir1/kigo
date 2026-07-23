package directcandidate

import (
	"reflect"
	"testing"
)

func TestFromAddressClassifiesCandidates(t *testing.T) {
	tests := []struct {
		address  string
		manual   bool
		wantKind string
		want     int
	}{
		{"192.168.1.2:4000", false, KindLAN, PriorityLAN},
		{"[fd00::2]:4000", false, KindLAN, PriorityLAN},
		{"127.0.0.1:4000", false, KindLoopback, PriorityLoopback},
		{"[::1]:4000", false, KindLoopback, PriorityLoopback},
		{"[2001:db8::2]:4000", false, KindIPv6Global, PriorityIPv6Global},
		{"203.0.113.2:4000", false, KindUnknown, PriorityUnknown},
		{"host.example:4000", false, KindUnknown, PriorityUnknown},
		{"192.168.1.2:4000", true, KindManual, PriorityManual},
	}
	for _, test := range tests {
		got, err := FromAddress(test.address, test.manual)
		if err != nil {
			t.Fatalf("%s: %v", test.address, err)
		}
		if got.Kind != test.wantKind || got.Priority != test.want {
			t.Fatalf("%s: got %#v, want kind=%q priority=%d", test.address, got, test.wantKind, test.want)
		}
	}
}

func TestValidateAcceptsRelayObservedPublicCandidate(t *testing.T) {
	candidate, err := FromRelayObservation("203.0.113.2:4000")
	if err != nil {
		t.Fatal(err)
	}
	if candidate.Kind != KindPublic || candidate.Priority != PriorityPublic {
		t.Fatalf("candidate = %#v", candidate)
	}
	ipv6, err := FromRelayObservation("[2001:db8::2]:4000")
	if err != nil {
		t.Fatal(err)
	}
	if ipv6.Kind != KindIPv6Global || ipv6.Priority != PriorityIPv6Global {
		t.Fatalf("IPv6 candidate = %#v", ipv6)
	}
}

func TestValidateSetRejectsInvalidMetadata(t *testing.T) {
	addresses := []string{"127.0.0.1:4000"}
	tests := [][]Candidate{
		{{Address: "127.0.0.1:4000", Kind: "bad", Priority: 50}},
		{{Address: "127.0.0.1:4000", Kind: KindLoopback, Priority: 101}},
		{{Address: "127.0.0.1:4001", Kind: KindLoopback, Priority: 20}},
		{
			{Address: "127.0.0.1:4000", Kind: KindLoopback, Priority: 20},
			{Address: "127.0.0.1:4000", Kind: KindLoopback, Priority: 20},
		},
	}
	for _, metadata := range tests {
		if err := ValidateSet(addresses, metadata); err == nil {
			t.Fatalf("invalid metadata was accepted: %#v", metadata)
		}
	}
}

func TestMergePrefersMetadataAndInfersLegacyCandidates(t *testing.T) {
	addresses := []string{
		"192.168.1.2:4000",
		"[2001:db8::2]:4000",
		"host.example:4000",
	}
	metadata := []Candidate{{
		Address:  "host.example:4000",
		Kind:     KindManual,
		Priority: PriorityManual,
	}}
	got := Merge(addresses, metadata)
	want := []Candidate{
		{Address: "host.example:4000", Kind: KindManual, Priority: PriorityManual},
		{Address: "[2001:db8::2]:4000", Kind: KindIPv6Global, Priority: PriorityIPv6Global},
		{Address: "192.168.1.2:4000", Kind: KindLAN, Priority: PriorityLAN},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("candidates = %#v, want %#v", got, want)
	}
}
