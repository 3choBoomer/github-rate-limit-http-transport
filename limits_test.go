package ghratelimit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// https://api.github.com/rate_limit
const limitsResponse = `{
  "resources": {
    "core": {
      "limit": 5000,
      "used": 0,
      "remaining": 5000,
      "reset": 1745121612
    },
    "search": {
      "limit": 30,
      "used": 0,
      "remaining": 30,
      "reset": 1745118072
    },
    "graphql": {
      "limit": 5000,
      "used": 0,
      "remaining": 5000,
      "reset": 1745121612
    },
    "integration_manifest": {
      "limit": 5000,
      "used": 0,
      "remaining": 5000,
      "reset": 1745121612
    },
    "source_import": {
      "limit": 100,
      "used": 0,
      "remaining": 100,
      "reset": 1745118072
    },
    "code_scanning_upload": {
      "limit": 1000,
      "used": 0,
      "remaining": 1000,
      "reset": 1745121612
    },
    "code_scanning_autofix": {
      "limit": 10,
      "used": 0,
      "remaining": 10,
      "reset": 1745118072
    },
    "actions_runner_registration": {
      "limit": 10000,
      "used": 0,
      "remaining": 10000,
      "reset": 1745121612
    },
    "scim": {
      "limit": 15000,
      "used": 0,
      "remaining": 15000,
      "reset": 1745121612
    },
    "dependency_snapshots": {
      "limit": 100,
      "used": 0,
      "remaining": 100,
      "reset": 1745118072
    },
    "audit_log": {
      "limit": 1750,
      "used": 0,
      "remaining": 1750,
      "reset": 1745121612
    },
    "audit_log_streaming": {
      "limit": 15,
      "used": 0,
      "remaining": 15,
      "reset": 1745121612
    },
    "code_search": {
      "limit": 10,
      "used": 0,
      "remaining": 10,
      "reset": 1745118072
    }
  },
  "rate": {
    "limit": 5000,
    "used": 0,
    "remaining": 5000,
    "reset": 1745121612
  }
}`

func TestLimits_Store(t *testing.T) {
	var resp struct {
		Resources map[Resource]Rate `json:"resources"`
	}
	err := json.Unmarshal([]byte(limitsResponse), &resp)
	assert.NoError(t, err, "json.Unmarshal failed")
	var limits Limits
	for resource, rate := range resp.Resources {
		limits.Store(nil, resource, &rate)
	}
	assert.Equal(t, map[Resource]*Rate{
		ResourceCore:                      {Limit: 5000, Used: 0, Remaining: 5000, Reset: 1745121612},
		ResourceSearch:                    {Limit: 30, Used: 0, Remaining: 30, Reset: 1745118072},
		ResourceGraphQL:                   {Limit: 5000, Used: 0, Remaining: 5000, Reset: 1745121612},
		ResourceIntegrationManifest:       {Limit: 5000, Used: 0, Remaining: 5000, Reset: 1745121612},
		ResourceSourceImport:              {Limit: 100, Used: 0, Remaining: 100, Reset: 1745118072},
		ResourceCodeScanningUpload:        {Limit: 1000, Used: 0, Remaining: 1000, Reset: 1745121612},
		ResourceCodeScanningAutofix:       {Limit: 10, Used: 0, Remaining: 10, Reset: 1745118072},
		ResourceActionsRunnerRegistration: {Limit: 10000, Used: 0, Remaining: 10000, Reset: 1745121612},
		ResourceSCIM:                      {Limit: 15000, Used: 0, Remaining: 15000, Reset: 1745121612},
		ResourceDependencySnapshots:       {Limit: 100, Used: 0, Remaining: 100, Reset: 1745118072},
		ResourceAuditLog:                  {Limit: 1750, Used: 0, Remaining: 1750, Reset: 1745121612},
		ResourceAuditLogStreaming:         {Limit: 15, Used: 0, Remaining: 15, Reset: 1745121612},
		ResourceCodeSearch:                {Limit: 10, Used: 0, Remaining: 10, Reset: 1745118072},
	}, maps.Collect(limits.Iter()))
}

func TestLimits_Store_Notify(t *testing.T) {
	resp := &http.Response{StatusCode: http.StatusOK}
	rate := &Rate{Limit: 5000, Used: 0, Remaining: 5000, Reset: 1745121612}

	var gotResp *http.Response
	var gotResource Resource
	var gotRate *Rate
	var limits Limits
	limits.Notify = func(r *http.Response, resource Resource, rate *Rate) {
		gotResp = r
		gotResource = resource
		gotRate = rate
	}
	limits.Store(resp, ResourceCore, rate)

	assert.Same(t, resp, gotResp, "Notify should receive the *http.Response passed to Store")
	assert.Equal(t, ResourceCore, gotResource, "Notify should receive the resource passed to Store")
	assert.Same(t, rate, gotRate, "Notify should receive the *Rate passed to Store")
}

func TestLimits_Store_AcceptsRegressionWithinSameWindow(t *testing.T) {
	// Store intentionally does not guard against same-window staleness: a response reporting less
	// consumption than one already seen (e.g. two concurrent requests whose responses completed
	// out of dispatch order) is accepted like any other update, in exchange for not needing to
	// compare Remaining at all.
	reset := uint64(time.Now().Add(time.Hour).Unix())

	var limits Limits
	limits.Store(nil, ResourceCore, &Rate{Limit: 5000, Used: 100, Remaining: 4900, Reset: reset})

	var notified bool
	limits.Notify = func(*http.Response, Resource, *Rate) { notified = true }

	limits.Store(nil, ResourceCore, &Rate{Limit: 5000, Used: 90, Remaining: 4910, Reset: reset})

	assert.Equal(t, &Rate{Limit: 5000, Used: 90, Remaining: 4910, Reset: reset}, limits.Load(ResourceCore))
	assert.True(t, notified, "Notify must fire for an accepted update")
}

func TestLimits_Store_AcceptsOlderWindow(t *testing.T) {
	// Store is unconditional: it doesn't compare Reset at all, so an update reporting an older
	// window than what's already known still overwrites it. Callers that need to filter out a
	// stale/out-of-order read (e.g. Fetch, for the /rate_limit endpoint) are responsible for doing
	// so themselves before calling Store.
	reset := uint64(time.Now().Add(time.Hour).Unix())

	var limits Limits
	limits.Store(nil, ResourceCore, &Rate{Limit: 5000, Used: 0, Remaining: 5000, Reset: reset})

	var notified bool
	limits.Notify = func(*http.Response, Resource, *Rate) { notified = true }

	limits.Store(nil, ResourceCore, &Rate{Limit: 5000, Used: 4000, Remaining: 1000, Reset: reset - 3600})

	assert.Equal(t, &Rate{Limit: 5000, Used: 4000, Remaining: 1000, Reset: reset - 3600}, limits.Load(ResourceCore))
	assert.True(t, notified, "Notify must fire for the accepted update")
}

func TestLimits_Store_NotBlockedByReserve(t *testing.T) {
	// Simulates a burst of concurrent requests: Reserve optimistically pre-decrements for every
	// one of them before any response comes back. Since Store is unconditional, Reserve's
	// speculation can never block a real response from taking effect, regardless of how far
	// Reserve has run ahead of it.
	reset := uint64(time.Now().Add(time.Hour).Unix())
	var limits Limits
	limits.Store(nil, ResourceCore, &Rate{Limit: 100, Used: 0, Remaining: 100, Reset: reset})
	for range 50 {
		limits.Reserve(ResourceCore)
	}
	assert.Equal(t, &Rate{Limit: 100, Used: 50, Remaining: 50, Reset: reset}, limits.Load(ResourceCore))

	var notified bool
	limits.Notify = func(*http.Response, Resource, *Rate) { notified = true }

	// The first of those 50 requests' real response now lands, reporting the true state as of
	// when GitHub processed it.
	limits.Store(nil, ResourceCore, &Rate{Limit: 100, Used: 1, Remaining: 99, Reset: reset})

	assert.Equal(t, &Rate{Limit: 100, Used: 1, Remaining: 99, Reset: reset}, limits.Load(ResourceCore), "a real confirmed response must take effect regardless of Reserve's speculative estimate")
	assert.True(t, notified, "Notify must fire for the accepted update")
}

func TestLimits_Store_AcceptsNewerWindow(t *testing.T) {
	var limits Limits
	limits.Store(nil, ResourceCore, &Rate{Limit: 5000, Used: 5000, Remaining: 0, Reset: 1745121612})

	// A later Reset (a new window) must be accepted even though Remaining is numerically greater
	// than the prior window's exhausted state.
	limits.Store(nil, ResourceCore, &Rate{Limit: 5000, Used: 0, Remaining: 5000, Reset: 1745125212})

	assert.Equal(t, &Rate{Limit: 5000, Used: 0, Remaining: 5000, Reset: 1745125212}, limits.Load(ResourceCore))
}

func TestLimits_Store_Concurrent(t *testing.T) {
	// Concurrent Store calls against the same resource must not race or panic; which one's data
	// ultimately wins isn't guaranteed (see TestLimits_Store_AcceptsRegressionWithinSameWindow),
	// but the final state must be one of the values actually written, not a torn/corrupted one.
	const n = 200
	reset := uint64(time.Now().Add(time.Hour).Unix())
	var limits Limits
	limits.Store(nil, ResourceCore, &Rate{Limit: n, Used: 0, Remaining: n, Reset: reset})

	var wg sync.WaitGroup
	for used := uint64(1); used <= n; used++ {
		wg.Add(1)
		go func(used uint64) {
			defer wg.Done()
			limits.Store(nil, ResourceCore, &Rate{Limit: n, Used: used, Remaining: n - used, Reset: reset})
		}(used)
	}
	wg.Wait()

	got := limits.Load(ResourceCore)
	if assert.NotNil(t, got) {
		assert.LessOrEqual(t, got.Used, uint64(n))
		assert.Equal(t, uint64(n), got.Used+got.Remaining)
		assert.Equal(t, reset, got.Reset)
	}
}

func TestLimits_Reserve(t *testing.T) {
	var limits Limits
	// No rate stored yet for the resource, should be a no-op.
	limits.Reserve(ResourceCore)
	assert.Nil(t, limits.Load(ResourceCore))

	limits.Store(nil, ResourceCore, &Rate{Limit: 5000, Used: 0, Remaining: 2, Reset: 1745121612})
	limits.Reserve(ResourceCore)
	assert.Equal(t, &Rate{Limit: 5000, Used: 1, Remaining: 1, Reset: 1745121612}, limits.Load(ResourceCore))

	limits.Reserve(ResourceCore)
	assert.Equal(t, &Rate{Limit: 5000, Used: 2, Remaining: 0, Reset: 1745121612}, limits.Load(ResourceCore))

	// Remaining is already 0, should be a no-op rather than underflowing.
	limits.Reserve(ResourceCore)
	assert.Equal(t, &Rate{Limit: 5000, Used: 2, Remaining: 0, Reset: 1745121612}, limits.Load(ResourceCore))
}

func TestLimits_Reserve_Concurrent(t *testing.T) {
	var limits Limits
	const n = 1000
	limits.Store(nil, ResourceCore, &Rate{Limit: n, Used: 0, Remaining: n, Reset: 1745121612})

	var wg sync.WaitGroup
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			limits.Reserve(ResourceCore)
		}()
	}
	wg.Wait()

	assert.Equal(t, &Rate{Limit: n, Used: n, Remaining: 0, Reset: 1745121612}, limits.Load(ResourceCore))
}

func TestLimits_Parse(t *testing.T) {
	var limits Limits
	err := limits.Parse(&http.Response{
		Header: http.Header{
			"X-Ratelimit-Limit":     []string{"5000"},
			"X-Ratelimit-Used":      []string{"0"},
			"X-Ratelimit-Remaining": []string{"5000"},
			"X-Ratelimit-Reset":     []string{"1745121612"},
			"X-Ratelimit-Resource":  []string{"core"},
		},
	})
	assert.NoError(t, err, "(*Limits).Parse failed")
	assert.Equal(t, &Rate{
		Limit:     5000,
		Used:      0,
		Remaining: 5000,
		Reset:     1745121612,
	}, limits.Load(ResourceCore))

	err = limits.Parse(&http.Response{
		Header: http.Header{
			"X-Ratelimit-Limit":     []string{"invalid"},
			"X-Ratelimit-Used":      []string{"invalid"},
			"X-Ratelimit-Remaining": []string{"invalid"},
			"X-Ratelimit-Reset":     []string{"invalid"},
			"X-Ratelimit-Resource":  []string{"invalid"},
		},
	})
	assert.Error(t, err, "expected error, got nil")
}

func TestLimits_Fetch_Success(t *testing.T) {
	var capturedReq *http.Request
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		capturedReq = req
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{},
			Body:       io.NopCloser(strings.NewReader(limitsResponse)),
		}, nil
	})

	var limits Limits
	err := limits.Fetch(context.Background(), transport, nil)
	assert.NoError(t, err, "(*Limits).Fetch failed")

	assert.Equal(t, DefaultURL.String(), capturedReq.URL.String(), "should default to DefaultURL when u is nil")
	assert.Equal(t, "github.com/bored-engineer/github-rate-limit-http-transport", capturedReq.Header.Get("User-Agent"))
	assert.Equal(t, "2022-11-28", capturedReq.Header.Get("X-GitHub-Api-Version"))

	assert.Equal(t, &Rate{Limit: 5000, Used: 0, Remaining: 5000, Reset: 1745121612}, limits.Load(ResourceCore))
	assert.Equal(t, &Rate{Limit: 30, Used: 0, Remaining: 30, Reset: 1745118072}, limits.Load(ResourceSearch))
}

func TestLimits_Fetch_AcceptsSameWindowUpdate(t *testing.T) {
	// A real, concurrent request already observed the resource as fully exhausted within the
	// current window.
	reset := uint64(time.Now().Add(time.Hour).Unix())
	var limits Limits
	limits.Store(nil, ResourceCore, &Rate{Limit: 5000, Used: 5000, Remaining: 0, Reset: reset})

	var notified bool
	limits.Notify = func(*http.Response, Resource, *Rate) { notified = true }

	// Fetch (e.g. from the periodic poller) reports more Remaining for the same reset window, as
	// if reading from a shard lagging behind the request above. Store doesn't guard against this
	// kind of same-window staleness, so it's accepted like any other update.
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{},
			Body:       io.NopCloser(strings.NewReader(fmt.Sprintf(`{"resources":{"core":{"limit":5000,"used":743,"remaining":4257,"reset":%d}}}`, reset))),
		}, nil
	})
	err := limits.Fetch(context.Background(), transport, nil)
	assert.NoError(t, err, "(*Limits).Fetch failed")

	assert.Equal(t, &Rate{Limit: 5000, Used: 743, Remaining: 4257, Reset: reset}, limits.Load(ResourceCore))
	assert.True(t, notified, "Notify must fire for an accepted update")
}

func TestLimits_Fetch_AcceptsOlderWindowWithPartialUsage(t *testing.T) {
	// Fetch's staleness filter only distrusts a full-quota (remaining == limit) reading; a reading
	// that shows actual consumption is always passed through to Store, which is itself
	// unconditional. So an older, stale Reset with partial usage still overwrites newer data.
	reset := uint64(time.Now().Add(time.Hour).Unix())
	var limits Limits
	limits.Store(nil, ResourceCore, &Rate{Limit: 5000, Used: 0, Remaining: 5000, Reset: reset})

	var notified bool
	limits.Notify = func(*http.Response, Resource, *Rate) { notified = true }

	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{},
			Body:       io.NopCloser(strings.NewReader(fmt.Sprintf(`{"resources":{"core":{"limit":5000,"used":4000,"remaining":1000,"reset":%d}}}`, reset-3600))),
		}, nil
	})
	err := limits.Fetch(context.Background(), transport, nil)
	assert.NoError(t, err, "(*Limits).Fetch failed")

	assert.Equal(t, &Rate{Limit: 5000, Used: 4000, Remaining: 1000, Reset: reset - 3600}, limits.Load(ResourceCore))
	assert.True(t, notified, "Notify must fire for the accepted update")
}

func TestLimits_Fetch_AcceptsNewerWindow(t *testing.T) {
	// The resource was exhausted in a prior window.
	var limits Limits
	limits.Store(nil, ResourceCore, &Rate{Limit: 5000, Used: 5000, Remaining: 0, Reset: 1745121612})

	// Fetch reports a later Reset (a new window), so the higher Remaining
	// must be accepted even though it's numerically greater than before.
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{},
			Body:       io.NopCloser(strings.NewReader(`{"resources":{"core":{"limit":5000,"used":0,"remaining":5000,"reset":1745125212}}}`)),
		}, nil
	})
	err := limits.Fetch(context.Background(), transport, nil)
	assert.NoError(t, err, "(*Limits).Fetch failed")

	assert.Equal(t, &Rate{Limit: 5000, Used: 0, Remaining: 5000, Reset: 1745125212}, limits.Load(ResourceCore))
}

func TestLimits_Fetch_AcceptsFurtherConsumption(t *testing.T) {
	// Normal, monotonic consumption within the same window must always be
	// accepted regardless of the staleness guard.
	var limits Limits
	limits.Store(nil, ResourceCore, &Rate{Limit: 5000, Used: 0, Remaining: 5000, Reset: 1745121612})

	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{},
			Body:       io.NopCloser(strings.NewReader(`{"resources":{"core":{"limit":5000,"used":100,"remaining":4900,"reset":1745121612}}}`)),
		}, nil
	})
	err := limits.Fetch(context.Background(), transport, nil)
	assert.NoError(t, err, "(*Limits).Fetch failed")

	assert.Equal(t, &Rate{Limit: 5000, Used: 100, Remaining: 4900, Reset: 1745121612}, limits.Load(ResourceCore))
}

func TestLimits_Fetch_DropsStaleFullQuota(t *testing.T) {
	// Live traffic (via X-Ratelimit headers) has already recorded real consumption within the
	// current, still-active window.
	reset := uint64(time.Now().Add(time.Hour).Unix())
	var limits Limits
	limits.Store(nil, ResourceCore, &Rate{Limit: 5000, Used: 100, Remaining: 4900, Reset: reset})

	var notified bool
	limits.Notify = func(*http.Response, Resource, *Rate) { notified = true }

	// A stale /rate_limit response reports remaining == limit (used == 0) for the same window -
	// this must be dropped rather than regressing the known usage.
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{},
			Body:       io.NopCloser(strings.NewReader(fmt.Sprintf(`{"resources":{"core":{"limit":5000,"used":0,"remaining":5000,"reset":%d}}}`, reset))),
		}, nil
	})
	err := limits.Fetch(context.Background(), transport, nil)
	assert.NoError(t, err, "(*Limits).Fetch failed")

	assert.Equal(t, &Rate{Limit: 5000, Used: 100, Remaining: 4900, Reset: reset}, limits.Load(ResourceCore), "a stale full-quota reading must not overwrite known usage")
	assert.False(t, notified, "Notify must not fire for a dropped stale reading")
}

func TestLimits_Fetch_AcceptsFullQuotaWhenExistingExpired(t *testing.T) {
	// The previously stored window has already expired (its Reset is in the past).
	var limits Limits
	limits.Store(nil, ResourceCore, &Rate{Limit: 5000, Used: 5000, Remaining: 0, Reset: 1745121612})

	var notified bool
	limits.Notify = func(*http.Response, Resource, *Rate) { notified = true }

	// A /rate_limit response reporting remaining == limit for a new window must be accepted, since
	// there's no fresher evidence to distrust it.
	newReset := uint64(time.Now().Add(time.Hour).Unix())
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{},
			Body:       io.NopCloser(strings.NewReader(fmt.Sprintf(`{"resources":{"core":{"limit":5000,"used":0,"remaining":5000,"reset":%d}}}`, newReset))),
		}, nil
	})
	err := limits.Fetch(context.Background(), transport, nil)
	assert.NoError(t, err, "(*Limits).Fetch failed")

	assert.Equal(t, &Rate{Limit: 5000, Used: 0, Remaining: 5000, Reset: newReset}, limits.Load(ResourceCore))
	assert.True(t, notified, "Notify must fire for an accepted update")
}

func TestLimits_Fetch_CustomURL(t *testing.T) {
	custom := &url.URL{Scheme: "https", Host: "example.com", Path: "/custom/rate_limit"}

	var capturedURL *url.URL
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		capturedURL = req.URL
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{},
			Body:       io.NopCloser(strings.NewReader(`{"resources":{}}`)),
		}, nil
	})

	var limits Limits
	err := limits.Fetch(context.Background(), transport, custom)
	assert.NoError(t, err, "(*Limits).Fetch failed")
	assert.Equal(t, custom.String(), capturedURL.String(), "should use the provided URL instead of DefaultURL")
}

func TestLimits_Fetch_RoundTripError(t *testing.T) {
	wantErr := errors.New("network down")
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return nil, wantErr
	})

	var limits Limits
	err := limits.Fetch(context.Background(), transport, nil)
	assert.ErrorIs(t, err, wantErr)
}

func TestLimits_Fetch_NonOKStatus(t *testing.T) {
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusForbidden,
			Header:     http.Header{},
			Body:       io.NopCloser(strings.NewReader("rate limited")),
		}, nil
	})

	var limits Limits
	err := limits.Fetch(context.Background(), transport, nil)
	assert.ErrorContains(t, err, "403")
	assert.ErrorContains(t, err, "rate limited")
}

func TestLimits_Fetch_MalformedJSON(t *testing.T) {
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{},
			Body:       io.NopCloser(strings.NewReader("not json")),
		}, nil
	})

	var limits Limits
	err := limits.Fetch(context.Background(), transport, nil)
	assert.ErrorContains(t, err, "json.Unmarshal")
}

// errReader always fails to Read, to simulate a body read failure.
type errReader struct{}

func (errReader) Read(p []byte) (int, error) {
	return 0, errors.New("read failed")
}

func TestLimits_Fetch_BodyReadError(t *testing.T) {
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{},
			Body:       io.NopCloser(errReader{}),
		}, nil
	})

	var limits Limits
	err := limits.Fetch(context.Background(), transport, nil)
	assert.ErrorContains(t, err, "Body.Read")
}
