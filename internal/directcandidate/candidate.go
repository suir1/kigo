package directcandidate

import (
	"fmt"
	"net"
	"net/netip"
	"sort"
	"strconv"
	"strings"
)

const MaxCandidates = 8

const (
	KindManual     = "manual"
	KindIPv6Global = "ipv6-global"
	KindLAN        = "lan"
	KindPublic     = "public"
	KindLoopback   = "loopback"
	KindUnknown    = "unknown"
)

const (
	PriorityManual     = 100
	PriorityIPv6Global = 90
	PriorityLAN        = 70
	PriorityUnknown    = 50
	PriorityPublic     = 30
	PriorityLoopback   = 20
)

type Candidate struct {
	Address  string `json:"address"`
	Kind     string `json:"kind"`
	Priority int    `json:"priority"`
}

func FromAddress(address string, manual bool) (Candidate, error) {
	if err := ValidateAddress(address); err != nil {
		return Candidate{}, err
	}
	if manual {
		return Candidate{Address: address, Kind: KindManual, Priority: PriorityManual}, nil
	}
	host, _, _ := net.SplitHostPort(address)
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return Candidate{Address: address, Kind: KindUnknown, Priority: PriorityUnknown}, nil
	}
	ip = ip.Unmap()
	switch {
	case ip.IsLoopback():
		return Candidate{Address: address, Kind: KindLoopback, Priority: PriorityLoopback}, nil
	case ip.IsPrivate():
		return Candidate{Address: address, Kind: KindLAN, Priority: PriorityLAN}, nil
	case ip.Is6() && ip.IsGlobalUnicast():
		return Candidate{Address: address, Kind: KindIPv6Global, Priority: PriorityIPv6Global}, nil
	default:
		return Candidate{Address: address, Kind: KindUnknown, Priority: PriorityUnknown}, nil
	}
}

func FromRelayObservation(address string) (Candidate, error) {
	candidate, err := FromAddress(address, false)
	if err != nil {
		return Candidate{}, err
	}
	if candidate.Kind == KindIPv6Global {
		return candidate, nil
	}
	return Candidate{Address: address, Kind: KindPublic, Priority: PriorityPublic}, nil
}

func Validate(candidate Candidate) error {
	if err := ValidateAddress(candidate.Address); err != nil {
		return err
	}
	if !ValidKind(candidate.Kind) {
		return fmt.Errorf("invalid direct candidate kind %q", candidate.Kind)
	}
	if candidate.Priority < 0 || candidate.Priority > 100 {
		return fmt.Errorf("invalid direct candidate priority %d", candidate.Priority)
	}
	return nil
}

func ValidateSet(addresses []string, metadata []Candidate) error {
	if len(addresses) > MaxCandidates {
		return fmt.Errorf("too many direct candidates: %d", len(addresses))
	}
	if len(metadata) > MaxCandidates {
		return fmt.Errorf("too many direct candidate metadata entries: %d", len(metadata))
	}
	known := make(map[string]struct{}, len(addresses))
	for _, address := range addresses {
		if err := ValidateAddress(address); err != nil {
			return err
		}
		known[address] = struct{}{}
	}
	seenMetadata := make(map[string]struct{}, len(metadata))
	for _, candidate := range metadata {
		if err := Validate(candidate); err != nil {
			return err
		}
		if _, ok := known[candidate.Address]; !ok {
			return fmt.Errorf("direct candidate metadata address %q is not advertised", candidate.Address)
		}
		if _, ok := seenMetadata[candidate.Address]; ok {
			return fmt.Errorf("duplicate direct candidate metadata for %q", candidate.Address)
		}
		seenMetadata[candidate.Address] = struct{}{}
	}
	return nil
}

func Merge(addresses []string, metadata []Candidate) []Candidate {
	byAddress := make(map[string]Candidate, len(metadata))
	for _, candidate := range metadata {
		if _, exists := byAddress[candidate.Address]; exists {
			continue
		}
		if Validate(candidate) == nil {
			byAddress[candidate.Address] = candidate
		}
	}
	seen := make(map[string]struct{}, len(addresses))
	out := make([]Candidate, 0, min(len(addresses), MaxCandidates))
	for _, address := range addresses {
		if len(out) >= MaxCandidates {
			break
		}
		if _, exists := seen[address]; exists {
			continue
		}
		candidate, ok := byAddress[address]
		if !ok {
			var err error
			candidate, err = FromAddress(address, false)
			if err != nil {
				continue
			}
		}
		seen[address] = struct{}{}
		out = append(out, candidate)
	}
	sort.SliceStable(out, func(left, right int) bool {
		return out[left].Priority > out[right].Priority
	})
	return out
}

func Addresses(candidates []Candidate) []string {
	out := make([]string, 0, min(len(candidates), MaxCandidates))
	seen := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		if len(out) >= MaxCandidates {
			break
		}
		if Validate(candidate) != nil {
			continue
		}
		if _, exists := seen[candidate.Address]; exists {
			continue
		}
		seen[candidate.Address] = struct{}{}
		out = append(out, candidate.Address)
	}
	return out
}

func ValidKind(kind string) bool {
	switch kind {
	case KindManual, KindIPv6Global, KindLAN, KindPublic, KindLoopback, KindUnknown:
		return true
	default:
		return false
	}
}

func ValidateAddress(address string) error {
	if address == "" || strings.TrimSpace(address) != address {
		return fmt.Errorf("invalid direct candidate %q", address)
	}
	host, portText, err := net.SplitHostPort(address)
	if err != nil || strings.TrimSpace(host) == "" || strings.TrimSpace(host) != host {
		return fmt.Errorf("invalid direct candidate %q", address)
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return fmt.Errorf("invalid direct candidate %q", address)
	}
	return nil
}
