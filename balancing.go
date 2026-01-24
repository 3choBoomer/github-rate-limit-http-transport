package ghratelimit

import (
	"context"
	"fmt"
	"math/rand"
	"net/http"
	"net/url"
	"time"
)

// TransportsExhaustedError allows callers to receive the earliest reset time when all transports are exhausted.
type TransportsExhaustedError interface {
	error
	SetEarliestReset(time.Time)
	GetEarliestReset() time.Time
	Is(error) bool
	As(any) bool
}

// BalancingTransport distributes requests to the transport with the highest "remaining" rate limit to execute the request.
// This can be used to distributes requests across multiple GitHub authentication tokens or applications.
type BalancingTransport struct {
	transports   []*Transport
	strategy     func(resource Resource, currentBest *Transport, candidate *Transport) *Transport
	exhaustedErr TransportsExhaustedError // returned when strategy yields nil and WithErrorOnTransportsExhausted is set
}

// BalancingOption configures the BalancingTransport
type BalancingOption func(*BalancingTransport)

// NewBalancingTransport creates a new BalancingTransport with the provided transports and options.
func NewBalancingTransport(transports []*Transport, opts ...BalancingOption) *BalancingTransport {
	bt := &BalancingTransport{
		transports: transports,
		strategy:   StrategyMostRemaining,
	}
	for _, opt := range opts {
		opt(bt)
	}
	return bt
}

// WithStrategy configures the strategy used to select the best transport.
func WithStrategy(strategy func(resource Resource, currentBest *Transport, candidate *Transport) *Transport) BalancingOption {
	return func(bt *BalancingTransport) {
		bt.strategy = strategy
	}
}

// WithErrorOnTransportsExhausted configures an error to return when the strategy cannot select a viable transport.
func WithErrorOnTransportsExhausted(err TransportsExhaustedError) BalancingOption {
	return func(bt *BalancingTransport) {
		bt.exhaustedErr = err
	}
}

// Poll calls (*Transport).Poll for every transport
func (bt *BalancingTransport) Poll(ctx context.Context, interval time.Duration, u *url.URL) {
	for _, transport := range bt.transports {
		go transport.Poll(ctx, interval, u)
	}
	<-ctx.Done()
}

// RoundTrip implements http.RoundTripper
func (bt *BalancingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if len(bt.transports) == 0 {
		return nil, fmt.Errorf("no transports available")
	}

	resource := InferResource(req)
	if resource == "" {
		return nil, fmt.Errorf("unknown resource for request: %q", req.URL)
	}

	var bestTransport *Transport
	for _, t := range bt.transports {
		bestTransport = bt.strategy(resource, bestTransport, t)
	}

	if bestTransport == nil {
		if bt.exhaustedErr != nil {
			bt.exhaustedErr.SetEarliestReset(earliestResetTime(resource, bt.transports))
			return nil, bt.exhaustedErr
		}
		bestTransport = bt.transports[rand.Intn(len(bt.transports))]
	}
	// if the strategy returned a transport, use it
	// note that it could be exhausted,
	// but the strategy may want the error to use elsewhere in the transport
	return bestTransport.RoundTrip(req)
}

// StrategyMostRemaining selects the transport with the highest remaining rate limit.
// Uses only one lookup per transport and avoids time conversions to minimize overhead.
func StrategyMostRemaining(resource Resource, best, candidate *Transport) *Transport {
	bestRem, _ := extractValues(resource, best)
	candidateRem, _ := extractValues(resource, candidate)

	if candidateRem > bestRem {
		return candidate
	}

	return best
}

// StrategyResetTimeInPastAndMostRemaining prefers transports whose reset is already in the past,
// then earlier resets, and finally the one with the most remaining tokens.
// In real world usage, this strategy will favor transports that will reset sooner.
// always returns one of the transports provided.
func StrategyResetTimeInPastAndMostRemaining(resource Resource, best, candidate *Transport) *Transport {
	bestRem, bestReset := extractValues(resource, best)
	candidateRem, candidateReset := extractValues(resource, candidate)

	// if both transports have zero remaining tokens
	// return the one that will reset first
	if bestRem == 0 && candidateRem == 0 {
		if candidateReset < bestReset {
			return candidate
		}
		return best
	}

	// Prefer transports whose reset wil
	// happen sooner if they both have capacity
	if candidateRem > 0 && candidateReset < bestReset {
		return candidate
	}
	if bestRem > 0 && bestReset < candidateReset {
		return best
	}

	// Prefer transports whose reset is already in the past
	// relative to now because it will already have replenished tokens.
	if resetIsInPastAndEarlierThanOther(candidateReset, bestReset) {
		return candidate
	}
	if resetIsInPastAndEarlierThanOther(bestReset, candidateReset) {
		return best
	}

	// Fallback to the transport with more remaining tokens.
	if candidateRem > bestRem {
		return candidate
	}

	return best
}

// extractValues reads remaining tokens and reset epoch seconds for a transport.
// Returns zero values when the transport or limit is missing (no allocation occurs).
func extractValues(resource Resource, t *Transport) (uint64, int64) {
	if t != nil {
		if r := t.Limits.Load(resource); r != nil {
			return r.Remaining, int64(r.Reset)
		}
	}
	return 0, 0
}

// earliestResetTime returns the earliest non-zero reset time across transports for the resource.
// If no reset times are available, it returns the zero time value.
func earliestResetTime(resource Resource, transports []*Transport) time.Time {
	var earliest time.Time
	for _, t := range transports {
		if t == nil {
			continue
		}
		if r := t.Limits.Load(resource); r != nil {
			if r.Reset == 0 {
				continue
			}
			resetTime := time.Unix(int64(r.Reset), 0)
			if earliest.IsZero() || resetTime.Before(earliest) {
				earliest = resetTime
			}
		}
	}
	return earliest
}

// resetIsInPastAndEarlierThanOther returns true when `reset` is non-zero, already in the past
// relative to `now`, and either the other reset is zero or occurs later.
func resetIsInPastAndEarlierThanOther(reset, otherReset int64) bool {
	return reset != 0 && reset < time.Now().Unix() && (otherReset == 0 || reset < otherReset)
}
