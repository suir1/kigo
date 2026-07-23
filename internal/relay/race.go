package relay

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/suir1/kigo/internal/directcandidate"
)

const relaySettleWindow = 120 * time.Millisecond
const publicProbeTimeout = 600 * time.Millisecond

type Candidate struct {
	Addr       string
	Kind       string
	Priority   int
	StartDelay time.Duration
	UseProxy   bool
}

type RaceOptions struct {
	Candidates   []Candidate
	Join         JoinOptions
	SettleWindow time.Duration
}

type RaceResult struct {
	JoinResult
	Candidate Candidate
}

type candidateResult struct {
	result RaceResult
	err    error
}

func RaceJoin(ctx context.Context, opts RaceOptions) (RaceResult, error) {
	candidates := normalizeRaceCandidates(opts.Candidates)
	if len(candidates) == 0 {
		return RaceResult{}, errors.New("no relay candidates")
	}
	settleWindow := opts.SettleWindow
	if settleWindow <= 0 {
		settleWindow = relaySettleWindow
	}
	raceCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	results := make(chan candidateResult, len(candidates))
	for _, candidate := range candidates {
		candidate := candidate
		go joinCandidate(raceCtx, opts.Join, candidate, results)
	}

	remaining := len(candidates)
	var best *RaceResult
	var failures []string
	var settle <-chan time.Time
	for remaining > 0 {
		select {
		case <-ctx.Done():
			cancel()
			closeRaceResult(best)
			go drainCandidateResults(results, remaining)
			return RaceResult{}, ctx.Err()
		case <-settle:
			cancel()
			if best != nil {
				go drainCandidateResults(results, remaining)
				return *best, nil
			}
		case outcome := <-results:
			remaining--
			if outcome.err != nil {
				failures = append(failures, outcome.err.Error())
				continue
			}
			if best == nil || betterCandidate(outcome.result.Candidate, best.Candidate) {
				closeRaceResult(best)
				selected := outcome.result
				best = &selected
			} else {
				_ = outcome.result.Transport.Close()
			}
			if settle == nil {
				timer := time.NewTimer(settleWindow)
				defer timer.Stop()
				settle = timer.C
			}
		}
	}
	if best != nil {
		cancel()
		return *best, nil
	}
	if len(failures) == 0 {
		return RaceResult{}, errors.New("relay race ended without a result")
	}
	return RaceResult{}, fmt.Errorf("all relay candidates failed: %s", strings.Join(failures, "; "))
}

func joinCandidate(ctx context.Context, base JoinOptions, candidate Candidate, results chan<- candidateResult) {
	if candidate.StartDelay > 0 {
		timer := time.NewTimer(candidate.StartDelay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			results <- candidateResult{err: ctx.Err()}
			return
		case <-timer.C:
		}
	}
	join := base
	join.Addr = candidate.Addr
	proxied := candidate.UseProxy && join.DialContext != nil
	if !candidate.UseProxy {
		join.DialContext = nil
	}
	if join.DirectProbeLocalPort > 0 && !proxied {
		probeCtx, cancel := context.WithTimeout(ctx, publicProbeTimeout)
		publicAddress, err := ProbePublicAddress(probeCtx, PublicProbeOptions{
			Addr:      candidate.Addr,
			RoomToken: join.RoomToken,
			Role:      join.Role,
			Pass:      join.Pass,
			LocalPort: join.DirectProbeLocalPort,
		})
		cancel()
		if err == nil {
			join.DirectCandidates, join.DirectCandidateMetadata = appendPublicCandidate(
				join.DirectCandidates,
				join.DirectCandidateMetadata,
				publicAddress,
			)
			join.Direct = firstCandidate(join.DirectCandidates)
		}
	}
	result, err := JoinWithOptions(ctx, join)
	outcome := candidateResult{
		result: RaceResult{JoinResult: result, Candidate: candidate},
		err:    err,
	}
	results <- outcome
}

func appendPublicCandidate(
	addresses []string,
	metadata []directcandidate.Candidate,
	address string,
) ([]string, []directcandidate.Candidate) {
	if directcandidate.ValidateAddress(address) != nil {
		return addresses, metadata
	}
	for _, existing := range addresses {
		if existing == address {
			return addresses, metadata
		}
	}
	if len(addresses) >= directcandidate.MaxCandidates {
		return addresses, metadata
	}
	candidate, err := directcandidate.FromRelayObservation(address)
	if err != nil {
		return addresses, metadata
	}
	addresses = append(append([]string(nil), addresses...), address)
	metadata = append(append([]directcandidate.Candidate(nil), metadata...), candidate)
	return addresses, metadata
}

func normalizeRaceCandidates(candidates []Candidate) []Candidate {
	seen := make(map[string]struct{}, len(candidates))
	out := make([]Candidate, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.Addr == "" {
			continue
		}
		if _, ok := seen[candidate.Addr]; ok {
			continue
		}
		seen[candidate.Addr] = struct{}{}
		out = append(out, candidate)
	}
	return out
}

func betterCandidate(left, right Candidate) bool {
	if left.Priority != right.Priority {
		return left.Priority < right.Priority
	}
	if left.StartDelay != right.StartDelay {
		return left.StartDelay < right.StartDelay
	}
	return left.Addr < right.Addr
}

func closeRaceResult(result *RaceResult) {
	if result != nil && result.Transport != nil {
		_ = result.Transport.Close()
	}
}

func drainCandidateResults(results <-chan candidateResult, remaining int) {
	for range remaining {
		outcome := <-results
		if outcome.err == nil && outcome.result.Transport != nil {
			_ = outcome.result.Transport.Close()
		}
	}
}
