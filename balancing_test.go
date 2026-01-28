package ghratelimit

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"testing"
	"time"
)

type mockBalancingRoundTripper struct {
	resp      *http.Response
	roundTrip func(*http.Request) (*http.Response, error)
	err       error
	req       *http.Request
}

func (m *mockBalancingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	m.req = req
	if m.roundTrip != nil {
		return m.roundTrip(req)
	}
	return m.resp, m.err
}

func TestBalancingTransport_RoundTrip(t *testing.T) {
	// Helper to create a dummy request
	req, _ := http.NewRequest("GET", "https://api.github.com/user", nil)

	t.Run("no transports", func(t *testing.T) {
		bt := NewBalancingTransport(nil)
		_, err := bt.RoundTrip(req)
		if err == nil {
			t.Error("expected error when no transports available, got nil")
		}
	})

	t.Run("selects transport with highest remaining limit", func(t *testing.T) {
		m1 := &mockBalancingRoundTripper{resp: &http.Response{StatusCode: 200}}
		t1 := NewTransport(m1)
		t1.Limits.Store(nil, ResourceCore, &Rate{Remaining: 10})

		m2 := &mockBalancingRoundTripper{resp: &http.Response{StatusCode: 200}}
		t2 := NewTransport(m2)
		t2.Limits.Store(nil, ResourceCore, &Rate{Remaining: 100}) // Highest

		m3 := &mockBalancingRoundTripper{resp: &http.Response{StatusCode: 200}}
		t3 := NewTransport(m3)
		t3.Limits.Store(nil, ResourceCore, &Rate{Remaining: 50})

		bt := NewBalancingTransport([]*Transport{t1, t2, t3})

		resp, err := bt.RoundTrip(req)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resp.StatusCode != 200 {
			t.Errorf("expected status 200, got %d", resp.StatusCode)
		}

		if m2.req == nil {
			t.Error("expected transport 2 to be used, but it wasn't")
		}
		if m1.req != nil {
			t.Error("transport 1 should not be used")
		}
		if m3.req != nil {
			t.Error("transport 3 should not be used")
		}
	})

	t.Run("fallbacks to random when no limits known", func(t *testing.T) {
		m1 := &mockBalancingRoundTripper{resp: &http.Response{StatusCode: 200}}
		t1 := NewTransport(m1)

		m2 := &mockBalancingRoundTripper{resp: &http.Response{StatusCode: 200}}
		t2 := NewTransport(m2)

		bt := NewBalancingTransport([]*Transport{t1, t2})

		// Since it's random, we can't be deterministic which one is picked,
		// but one of them MUST be picked.
		resp, err := bt.RoundTrip(req)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resp.StatusCode != 200 {
			t.Errorf("expected status 200, got %d", resp.StatusCode)
		}

		if m1.req == nil && m2.req == nil {
			t.Error("expected at least one transport to be used")
		}
		if m1.req != nil && m2.req != nil {
			t.Error("only one transport should be used")
		}
	})

	t.Run("handles mixed known and unknown limits", func(t *testing.T) {
		// Transport 1: no info
		m1 := &mockBalancingRoundTripper{resp: &http.Response{StatusCode: 200}}
		t1 := NewTransport(m1)

		// Transport 2: 10 remaining
		m2 := &mockBalancingRoundTripper{resp: &http.Response{StatusCode: 200}}
		t2 := NewTransport(m2)
		t2.Limits.Store(nil, ResourceCore, &Rate{Remaining: 10})

		bt := NewBalancingTransport([]*Transport{t1, t2})

		// It should pick T2 because it has a known positive limit which is implicitly better than unknown?
		// Let's check logic:
		// bestRemaining starts at 0.
		// T1: load -> nil.
		// T2: load -> 10. 10 > 0. bestTransport = T2.
		// Should pick T2.

		resp, err := bt.RoundTrip(req)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resp.StatusCode != 200 {
			t.Errorf("expected status 200, got %d", resp.StatusCode)
		}

		if m2.req == nil {
			t.Error("expected transport 2 (known limit) to be used over unknown")
		}
	})

	t.Run("resource inference", func(t *testing.T) {
		// Test that it uses the correct resource limit for decision
		// /search/users -> ResourceSearch
		searchReq, _ := http.NewRequest("GET", "https://api.github.com/search/users?q=foo", nil)

		m1 := &mockBalancingRoundTripper{resp: &http.Response{StatusCode: 200}}
		t1 := NewTransport(m1)
		t1.Limits.Store(nil, ResourceCore, &Rate{Remaining: 100})  // high core
		t1.Limits.Store(nil, ResourceSearch, &Rate{Remaining: 10}) // low search

		m2 := &mockBalancingRoundTripper{resp: &http.Response{StatusCode: 200}}
		t2 := NewTransport(m2)
		t2.Limits.Store(nil, ResourceCore, &Rate{Remaining: 10})    // low core
		t2.Limits.Store(nil, ResourceSearch, &Rate{Remaining: 100}) // high search

		bt := NewBalancingTransport([]*Transport{t1, t2})

		_, err := bt.RoundTrip(searchReq)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// API is search, so should use ResourceSearch limits.
		// T1 Search: 10. T2 Search: 100.
		// Should pick T2.
		if m2.req == nil {
			t.Error("expected transport 2 (better search limit) to be used")
		}
	})
}

func TestBalancingTransport_Poll(t *testing.T) {
	// This test just ensures no panic or hang.
	// We use a mock transport to avoid making real network requests and to ensure
	// Fetch returns successfully and quickly, so we don't trigger "context canceled" errors
	// during the Fetch call when we cancel the context.

	mockTransport := &mockBalancingRoundTripper{
		roundTrip: func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: 200,
				Body:       io.NopCloser(bytes.NewBufferString(`{"resources": {"core": {"limit": 5000, "remaining": 4999}}}`)),
				Header:     make(http.Header),
			}, nil
		},
	}

	// We need to provide a body literal for JSON unmarshalling if Limits.Fetch expects it.
	// Limits.Fetch expects `resources` key.
	// However, if we just want to avoid error in the network call layer:
	// If the body is empty, Parse might fail, but that's a different error.
	// Let's provide a minimal valid body.
	// But `mockBalancingRoundTripper` doesn't support Body content easily in the struct I defined?
	// Wait, I defined it with *http.Request/Response.
	// I can modify the test to return a body.

	bt := NewBalancingTransport([]*Transport{
		NewTransport(mockTransport),
		NewTransport(mockTransport),
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan bool)
	go func() {
		bt.Poll(ctx, time.Hour, nil)
		close(done)
	}()

	// Give Poll a tiny bit of time to start and hit the select,
	// although strictly not required if we just want to ensure it finishes.
	// But if we want to avoid the "context canceled" log from Fetch,
	// we rely on Fetch being fast (mocked) and finishing before cancel() is called.
	// The mock above returns immediately.
	// But we are in a race.
	// To be absolutely sure, we could wait a tiny bit, or just rely on the scheduler.
	// Using a minimal sleep increases reliability of "silence" but correctneess is guaranteed by `done`.
	time.Sleep(1 * time.Millisecond)

	cancel()
	select {
	case <-done:
		// success
	case <-time.After(time.Second):
		t.Error("Poll did not return after context cancellation")
	}
}

func TestNewBalancingTransport(t *testing.T) {
	t.Run("default strategy", func(t *testing.T) {
		bt := NewBalancingTransport(nil)
		if bt.strategy == nil {
			t.Error("expected default strategy to be set")
		}
	})

	t.Run("WithStrategy", func(t *testing.T) {
		called := false
		customStrategy := func(resource Resource, currentBest, candidate *Transport) *Transport {
			called = true
			return candidate
		}

		m := &mockBalancingRoundTripper{resp: &http.Response{StatusCode: 200}}
		bt := NewBalancingTransport([]*Transport{NewTransport(m)}, WithStrategy(customStrategy))

		req, _ := http.NewRequest("GET", "https://api.github.com/user", nil)
		_, _ = bt.RoundTrip(req)

		if !called {
			t.Error("custom strategy was not used")
		}
	})

	getEarliestResetError := func(req *http.Request, resource Resource, transports []*Transport) (*http.Response, error) {
		var earliestReset int64
		for _, t := range transports {
			if limits := t.Limits.Load(resource); limits != nil {
				if earliestReset == 0 {
					earliestReset = int64(limits.Reset)
				}
				if limits.Reset > 0 && int64(limits.Reset) < earliestReset {
					earliestReset = int64(limits.Reset)
				}
			}
		}
		return nil, &exhaustedWithReset{msg: "no transport available", earliestReset: time.Unix(earliestReset, 0)}
	}

	t.Run("returns configured error when strategy yields nil", func(t *testing.T) {
		// strategy always returns nil to simulate exhaustion
		strategy := func(resource Resource, currentBest, candidate *Transport) *Transport {
			return nil
		}

		m := &mockBalancingRoundTripper{resp: &http.Response{StatusCode: 200}}
		bt := NewBalancingTransport([]*Transport{NewTransport(m)},
			WithStrategy(strategy),
			WithErrorOrResponseOnTransportsExhausted(getEarliestResetError))

		req, _ := http.NewRequest("GET", "https://api.github.com/user", nil)
		_, err := bt.RoundTrip(req)
		if err == nil {
			t.Fatalf("expected error, got nil")
		}
		if err.Error() != "no transport available" {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("returns nil error when exhausted handler returns nil", func(t *testing.T) {
		strategy := func(resource Resource, currentBest, candidate *Transport) *Transport { return nil }
		returnNil := func(req *http.Request, resource Resource, transports []*Transport) (*http.Response, error) {
			return nil, nil
		}

		m := &mockBalancingRoundTripper{resp: &http.Response{StatusCode: 200}}
		bt := NewBalancingTransport([]*Transport{NewTransport(m)}, WithStrategy(strategy), WithErrorOrResponseOnTransportsExhausted(returnNil))

		req, _ := http.NewRequest("GET", "https://api.github.com/user", nil)
		resp, err := bt.RoundTrip(req)
		if err != nil {
			t.Fatalf("expected nil error, got %v", err)
		}
		if resp != nil {
			t.Fatalf("expected nil response when exhausted handler returns nil, got %v", resp)
		}
	})

	t.Run("sets earliest reset on exhausted error when supported", func(t *testing.T) {
		now := time.Now()
		earliest := now.Add(10 * time.Minute)
		latest := now.Add(20 * time.Minute)

		t1 := NewTransport(nil)
		t1.Limits.Store(nil, ResourceCore, &Rate{Remaining: 0, Reset: uint64(latest.Unix())})

		t2 := NewTransport(nil)
		t2.Limits.Store(nil, ResourceCore, &Rate{Remaining: 0, Reset: uint64(earliest.Unix())})

		errWithReset := &exhaustedWithReset{msg: "exhausted"}
		strategy := func(resource Resource, currentBest, candidate *Transport) *Transport { return nil }

		bt := NewBalancingTransport([]*Transport{t1, t2}, WithStrategy(strategy), WithErrorOrResponseOnTransportsExhausted(getEarliestResetError))

		req, _ := http.NewRequest("GET", "https://api.github.com/user", nil)
		_, err := bt.RoundTrip(req)
		if err == nil {
			t.Fatalf("expected error, got nil")
		}
		if err.Error() != "no transport available" {
			t.Fatalf("expected wrapped exhausted error, got %v", err)
		}
		var exErr *exhaustedWithReset
		if !errors.As(err, &exErr) {
			t.Fatalf("expected wrapped exhausted error, got %v", err)
		}
		if exErr.earliestReset.IsZero() {
			t.Fatalf("expected earliest reset to be set")
		}
		if exErr.earliestReset.Unix() != earliest.Unix() {
			t.Fatalf("expected earliest reset %v, got %v", earliest, errWithReset.earliestReset)
		}
	})

	t.Run("returns response when exhausted error provides response", func(t *testing.T) {
		strategy := func(resource Resource, currentBest, candidate *Transport) *Transport { return nil }
		respFromError := &http.Response{StatusCode: 503}
		exhaustedResponder := func(req *http.Request, resource Resource, transports []*Transport) (*http.Response, error) {
			return respFromError, nil
		}

		m := &mockBalancingRoundTripper{resp: &http.Response{StatusCode: 200}}
		bt := NewBalancingTransport([]*Transport{NewTransport(m)}, WithStrategy(strategy), WithErrorOrResponseOnTransportsExhausted(exhaustedResponder))

		req, _ := http.NewRequest("GET", "https://api.github.com/user", nil)
		gotResp, err := bt.RoundTrip(req)
		if err != nil {
			t.Fatalf("expected nil error, got %v", err)
		}
		if gotResp != respFromError {
			t.Fatalf("expected response from exhausted error, got %v", gotResp)
		}
		if m.req != nil {
			t.Fatalf("expected underlying transport not to be used when exhausted response returned")
		}
	})
}

// exhaustedWithReset is a helper error that records the earliest reset time via SetEarliestReset.
type exhaustedWithReset struct {
	earliestReset time.Time
	msg           string
}

func (e *exhaustedWithReset) Error() string                { return e.msg }
func (e *exhaustedWithReset) SetEarliestReset(t time.Time) { e.earliestReset = t }
func (e *exhaustedWithReset) GetEarliestReset() time.Time  { return e.earliestReset }

// Ensure mockRoundTripper implements http.RoundTripper
var _ http.RoundTripper = &mockRoundTripper{}

func TestStrategy_BestTransportByRemainingAndReset(t *testing.T) {
	now := time.Now()
	past := now.Add(-1 * time.Hour)
	future := now.Add(1 * time.Hour)
	olderPast := now.Add(-2 * time.Hour)

	tests := []struct {
		name      string
		rates     []*Rate
		wantIndex int // -1 for nil
	}{
		{
			name:      "single candidate, future reset, capacity",
			rates:     []*Rate{{Remaining: 10, Reset: uint64(future.Unix())}},
			wantIndex: 0,
		},
		{
			name: "both future, second has more remaining",
			rates: []*Rate{
				{Remaining: 10, Reset: uint64(future.Unix())},
				{Remaining: 20, Reset: uint64(future.Unix())},
			},
			wantIndex: 1,
		},
		{
			name: "both future, first has more remaining",
			rates: []*Rate{
				{Remaining: 20, Reset: uint64(future.Unix())},
				{Remaining: 10, Reset: uint64(future.Unix())},
			},
			wantIndex: 0,
		},
		{
			name: "second reset in past, first in future",
			rates: []*Rate{
				{Remaining: 100, Reset: uint64(future.Unix())},
				{Remaining: 0, Reset: uint64(past.Unix())},
			},
			wantIndex: 1,
		},
		{
			name: "first reset in past, second in future",
			rates: []*Rate{
				{Remaining: 0, Reset: uint64(past.Unix())},
				{Remaining: 100, Reset: uint64(future.Unix())},
			},
			wantIndex: 0,
		},
		{
			name: "both in past, second is older",
			rates: []*Rate{
				{Remaining: 0, Reset: uint64(past.Unix())},
				{Remaining: 0, Reset: uint64(olderPast.Unix())},
			},
			wantIndex: 1,
		},
		{
			name: "both in past, first is older",
			rates: []*Rate{
				{Remaining: 0, Reset: uint64(olderPast.Unix())},
				{Remaining: 0, Reset: uint64(past.Unix())},
			},
			wantIndex: 0,
		},
		{
			name:      "single candidate has 0 remaining and future reset",
			rates:     []*Rate{{Remaining: 0, Reset: uint64(future.Unix())}},
			wantIndex: -1,
		},
		{
			name: "both zero remaining and future resets",
			rates: []*Rate{
				{Remaining: 0, Reset: uint64(future.Add(time.Minute).Unix())},
				{Remaining: 0, Reset: uint64(future.Unix())},
			},
			// Both are skipped when comparing against nil because neither is viable (0 rem, reset > now).
			// So result is nil.
			wantIndex: -1,
		},
		{
			name: "second reset in past, first in future (non-zero remaining)",
			rates: []*Rate{
				{Remaining: 100, Reset: uint64(future.Unix())},
				{Remaining: 50, Reset: uint64(past.Unix())},
			},
			wantIndex: 1,
		},
		{
			name: "first reset in past, second in future (non-zero remaining)",
			rates: []*Rate{
				{Remaining: 50, Reset: uint64(past.Unix())},
				{Remaining: 75, Reset: uint64(future.Unix())},
			},
			wantIndex: 0,
		},
		{
			name: "both in past, second is older (non-zero remaining)",
			rates: []*Rate{
				{Remaining: 30, Reset: uint64(past.Unix())},
				{Remaining: 40, Reset: uint64(olderPast.Unix())},
			},
			wantIndex: 1,
		},
		{
			name: "both in past, first is older (non-zero remaining)",
			rates: []*Rate{
				{Remaining: 40, Reset: uint64(olderPast.Unix())},
				{Remaining: 30, Reset: uint64(past.Unix())},
			},
			wantIndex: 0,
		},
		{
			name:      "single candidate has remaining and future reset",
			rates:     []*Rate{{Remaining: 25, Reset: uint64(future.Unix())}},
			wantIndex: 0,
		},
		{
			name: "both non-zero remaining and future resets",
			rates: []*Rate{
				{Remaining: 25, Reset: uint64(future.Unix())},
				{Remaining: 50, Reset: uint64(future.Unix())},
			},
			wantIndex: 1,
		},
		{
			name: "both future, second resets sooner with capacity",
			rates: []*Rate{
				{Remaining: 10, Reset: uint64(future.Add(30 * time.Minute).Unix())},
				{Remaining: 5, Reset: uint64(future.Unix())},
			},
			wantIndex: 1,
		},
		{
			name: "both future, first resets sooner with capacity",
			rates: []*Rate{
				{Remaining: 5, Reset: uint64(future.Unix())},
				{Remaining: 10, Reset: uint64(future.Add(30 * time.Minute).Unix())},
			},
			wantIndex: 0,
		},
		{
			name: "equal remaining above threshold, second resets sooner",
			rates: []*Rate{
				{Remaining: 80, Reset: uint64(future.Add(30 * time.Minute).Unix())},
				{Remaining: 80, Reset: uint64(future.Unix())},
			},
			wantIndex: 1,
		},
		{
			name: "equal reset time, one exhausted, one has capacity",
			rates: []*Rate{
				{Remaining: 1400, Reset: uint64(future.Unix())},
				{Remaining: 0, Reset: uint64(future.Unix())},
			},
			wantIndex: 0,
		},
		{
			name: "3 candidates, ascending capacity, same reset",
			rates: []*Rate{
				{Remaining: 10, Reset: uint64(future.Unix())},
				{Remaining: 20, Reset: uint64(future.Unix())},
				{Remaining: 30, Reset: uint64(future.Unix())},
			},
			wantIndex: 2,
		},
		{
			name: "3 candidates, middle has highest capacity, same reset",
			rates: []*Rate{
				{Remaining: 10, Reset: uint64(future.Unix())},
				{Remaining: 50, Reset: uint64(future.Unix())},
				{Remaining: 6, Reset: uint64(future.Unix())},
			},
			wantIndex: 1,
		},
		{
			name: "3 candidates: exhausted, past(ready), capacity",
			rates: []*Rate{
				{Remaining: 0, Reset: uint64(future.Unix())},
				{Remaining: 0, Reset: uint64(past.Unix())},
				{Remaining: 10, Reset: uint64(future.Unix())},
			},
			wantIndex: 1,
		},
		{
			name: "3 candidates: capacity/future, capacity/sooner, capacity/soonest",
			rates: []*Rate{
				{Remaining: 100, Reset: uint64(future.Add(30 * time.Minute).Unix())},
				{Remaining: 100, Reset: uint64(future.Add(20 * time.Minute).Unix())},
				{Remaining: 100, Reset: uint64(future.Add(10 * time.Minute).Unix())},
			},
			wantIndex: 2,
		},
		{
			name: "4 candidates mixed",
			rates: []*Rate{
				{Remaining: 0, Reset: uint64(future.Unix())},                   // 0. Skip
				{Remaining: 10, Reset: uint64(future.Add(time.Hour).Unix())},   // 1. Pick (Better than nil)
				{Remaining: 10, Reset: uint64(future.Add(time.Minute).Unix())}, // 2. Pick (Sooner reset than 1)
				{Remaining: 0, Reset: uint64(past.Unix())},                     // 3. Pick (Past reset better than future)
			},
			wantIndex: 3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			transports := make([]*Transport, len(tt.rates))
			for i, r := range tt.rates {
				tr := NewTransport(nil)
				if r != nil {
					tr.Limits.Store(nil, ResourceCore, r)
				}
				transports[i] = tr
			}

			var best *Transport
			for _, tr := range transports {
				best = StrategyResetTimeInPastAndMostRemaining(ResourceCore, best, tr)
			}

			if tt.wantIndex == -1 {
				if best != nil {
					t.Errorf("wanted nil, got non-nil")
				}
			} else {
				if best == nil {
					t.Fatalf("wanted index %d, got nil", tt.wantIndex)
				}
				if best != transports[tt.wantIndex] {
					t.Errorf("wanted index %d, got different transport", tt.wantIndex)
				}
			}
		})
	}
}
