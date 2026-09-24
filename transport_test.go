package ghratelimit

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// roundTripFunc lets a http.RoundTripper be implemented inline for tests.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestTransport_Reserve(t *testing.T) {
	req := &http.Request{
		URL: &url.URL{
			Scheme: "https",
			Host:   "api.github.com",
			Path:   "/users/bored-engineer",
		},
		Method: http.MethodGet,
	}

	var remainingDuringRoundTrip uint64
	transport := &Transport{Reserve: true}
	transport.Base = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		remainingDuringRoundTrip = transport.Limits.Load(ResourceCore).Remaining
		return &http.Response{
			StatusCode: http.StatusOK,
			Header: http.Header{
				"X-Ratelimit-Limit":     []string{"5000"},
				"X-Ratelimit-Used":      []string{"1"},
				"X-Ratelimit-Remaining": []string{"4999"},
				"X-Ratelimit-Reset":     []string{"1745121612"},
				"X-Ratelimit-Resource":  []string{"core"},
			},
		}, nil
	})
	transport.Limits.Store(nil, ResourceCore, &Rate{Limit: 5000, Used: 0, Remaining: 5000, Reset: 1745121612})

	_, err := transport.RoundTrip(req)
	assert.NoError(t, err, "(*Transport).RoundTrip failed")

	// The remaining count should have already been decremented before the base RoundTrip executed.
	assert.Equal(t, uint64(4999), remainingDuringRoundTrip)
	// Once the (real) response headers are parsed, they take precedence.
	assert.Equal(t, &Rate{Limit: 5000, Used: 1, Remaining: 4999, Reset: 1745121612}, transport.Limits.Load(ResourceCore))
}

// closeTrackingBody wraps an io.Reader and records whether Close was called on it.
type closeTrackingBody struct {
	io.Reader
	closed bool
}

func (b *closeTrackingBody) Close() error {
	b.closed = true
	return nil
}

func TestTransport_RoundTrip_MalformedHeadersDoNotDropResponse(t *testing.T) {
	req := &http.Request{
		URL: &url.URL{
			Scheme: "https",
			Host:   "api.github.com",
			Path:   "/users/bored-engineer",
		},
		Method: http.MethodGet,
	}

	body := &closeTrackingBody{Reader: strings.NewReader("real payload")}
	transport := &Transport{
		Base: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header: http.Header{
					// X-Ratelimit-Resource is present but the other rate-limit headers are malformed,
					// so (*Limits).Parse will fail: that must not cause the response itself to be lost.
					"X-Ratelimit-Resource":  []string{"core"},
					"X-Ratelimit-Remaining": []string{"not-a-number"},
				},
				Body: body,
			}, nil
		}),
	}

	resp, err := transport.RoundTrip(req)
	assert.NoError(t, err, "(*Transport).RoundTrip should not fail because of malformed rate-limit headers")
	if assert.NotNil(t, resp, "the real response must still be returned") {
		got, readErr := io.ReadAll(resp.Body)
		assert.NoError(t, readErr)
		assert.Equal(t, "real payload", string(got), "the caller must still be able to read the real body")
		assert.NoError(t, resp.Body.Close())
		assert.True(t, body.closed, "the caller must be able to close the real body")
	}
}

func TestTransport_RoundTrip_DefaultBase(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Ratelimit-Resource", "core")
		w.Header().Set("X-Ratelimit-Limit", "5000")
		w.Header().Set("X-Ratelimit-Used", "1")
		w.Header().Set("X-Ratelimit-Remaining", "4999")
		w.Header().Set("X-Ratelimit-Reset", "1745121612")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	req, err := http.NewRequest(http.MethodGet, server.URL, nil)
	assert.NoError(t, err, "http.NewRequest failed")

	var transport Transport // Base is left nil, so http.DefaultTransport should be used.
	resp, err := transport.RoundTrip(req)
	assert.NoError(t, err, "(*Transport).RoundTrip failed")
	if assert.NotNil(t, resp) {
		assert.NoError(t, resp.Body.Close())
	}
	assert.Equal(t, &Rate{Limit: 5000, Used: 1, Remaining: 4999, Reset: 1745121612}, transport.Limits.Load(ResourceCore))
}

func TestTransport_Spoof_ExhaustedReturnsSyntheticResponse(t *testing.T) {
	req := &http.Request{
		URL: &url.URL{
			Scheme: "https",
			Host:   "api.github.com",
			Path:   "/users/bored-engineer",
		},
		Method: http.MethodGet,
	}

	reset := uint64(time.Now().Add(time.Hour).Unix())
	var baseCalled bool
	transport := &Transport{
		Spoof: true,
		Base: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			baseCalled = true
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: http.NoBody}, nil
		}),
	}
	transport.Limits.Store(nil, ResourceCore, &Rate{Limit: 5000, Used: 5000, Remaining: 0, Reset: reset})

	resp, err := transport.RoundTrip(req)
	assert.NoError(t, err, "(*Transport).RoundTrip failed")
	assert.False(t, baseCalled, "the base transport must not be invoked when the request would have been rate-limited")
	if assert.NotNil(t, resp) {
		assert.Equal(t, http.StatusForbidden, resp.StatusCode)
		assert.Equal(t, "0", resp.Header.Get("X-Ratelimit-Remaining"))
		body, readErr := io.ReadAll(resp.Body)
		assert.NoError(t, readErr)
		var parsed struct {
			Message string `json:"message"`
		}
		assert.NoError(t, json.Unmarshal(body, &parsed), "the synthetic body must be valid JSON")
		assert.Contains(t, parsed.Message, "API rate limit exceeded")
	}
	// The synthetic response must not overwrite the (already exhausted) Limits state.
	assert.Equal(t, &Rate{Limit: 5000, Used: 5000, Remaining: 0, Reset: reset}, transport.Limits.Load(ResourceCore))
}

func TestTransport_Spoof_PassesThroughWhenExpired(t *testing.T) {
	req := &http.Request{
		URL: &url.URL{
			Scheme: "https",
			Host:   "api.github.com",
			Path:   "/users/bored-engineer",
		},
		Method: http.MethodGet,
	}

	// The tracked window's Reset has already passed (e.g. Poll simply hasn't ticked since the
	// real reset happened, whether on schedule or early), so a cached Remaining == 0 can no
	// longer be trusted: the real request must be allowed through instead of being spoofed.
	var baseCalled bool
	transport := &Transport{
		Spoof: true,
		Base: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			baseCalled = true
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: http.NoBody}, nil
		}),
	}
	transport.Limits.Store(nil, ResourceCore, &Rate{Limit: 5000, Used: 5000, Remaining: 0, Reset: uint64(time.Now().Add(-time.Hour).Unix())})

	resp, err := transport.RoundTrip(req)
	assert.NoError(t, err, "(*Transport).RoundTrip failed")
	assert.True(t, baseCalled, "the base transport must be invoked once the cached window has expired")
	if assert.NotNil(t, resp) {
		assert.Equal(t, http.StatusOK, resp.StatusCode)
	}
}

func TestTransport_Spoof_PassesThroughWhenNotExhausted(t *testing.T) {
	req := &http.Request{
		URL: &url.URL{
			Scheme: "https",
			Host:   "api.github.com",
			Path:   "/users/bored-engineer",
		},
		Method: http.MethodGet,
	}

	var baseCalled bool
	transport := &Transport{
		Spoof: true,
		Base: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			baseCalled = true
			return &http.Response{
				StatusCode: http.StatusOK,
				Header: http.Header{
					"X-Ratelimit-Limit":     []string{"5000"},
					"X-Ratelimit-Used":      []string{"1"},
					"X-Ratelimit-Remaining": []string{"4999"},
					"X-Ratelimit-Reset":     []string{"1745121612"},
					"X-Ratelimit-Resource":  []string{"core"},
				},
				Body: http.NoBody,
			}, nil
		}),
	}
	transport.Limits.Store(nil, ResourceCore, &Rate{Limit: 5000, Used: 1, Remaining: 4999, Reset: 1745121612})

	resp, err := transport.RoundTrip(req)
	assert.NoError(t, err, "(*Transport).RoundTrip failed")
	assert.True(t, baseCalled, "the base transport must be invoked when quota remains")
	if assert.NotNil(t, resp) {
		assert.Equal(t, http.StatusOK, resp.StatusCode)
	}
}

func TestTransport_Spoof_PassesThroughWhenUnknown(t *testing.T) {
	req := &http.Request{
		URL: &url.URL{
			Scheme: "https",
			Host:   "api.github.com",
			Path:   "/users/bored-engineer",
		},
		Method: http.MethodGet,
	}

	var baseCalled bool
	transport := &Transport{
		Spoof: true,
		Base: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			baseCalled = true
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: http.NoBody}, nil
		}),
	}

	resp, err := transport.RoundTrip(req)
	assert.NoError(t, err, "(*Transport).RoundTrip failed")
	assert.True(t, baseCalled, "the base transport must be invoked when the resource's limit is unknown")
	if assert.NotNil(t, resp) {
		assert.Equal(t, http.StatusOK, resp.StatusCode)
	}
}

func TestTransport_Spoof_PassesThroughForRateLimitEndpoint(t *testing.T) {
	for name, path := range map[string]string{
		"cloud":      "/rate_limit",
		"enterprise": "/api/v3/rate_limit",
	} {
		t.Run(name, func(t *testing.T) {
			req := &http.Request{
				URL: &url.URL{
					Scheme: "https",
					Host:   "api.github.com",
					Path:   path,
				},
				Method: http.MethodGet,
			}

			var baseCalled bool
			transport := &Transport{
				Spoof: true,
				Base: roundTripFunc(func(req *http.Request) (*http.Response, error) {
					baseCalled = true
					return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: http.NoBody}, nil
				}),
			}
			// The core resource is exhausted within a window that's still current, but /rate_limit
			// is exempt from rate-limiting and must never be spoofed, since it's the only way to
			// learn the limit has reset.
			transport.Limits.Store(nil, ResourceCore, &Rate{Limit: 5000, Used: 5000, Remaining: 0, Reset: uint64(time.Now().Add(time.Hour).Unix())})

			resp, err := transport.RoundTrip(req)
			assert.NoError(t, err, "(*Transport).RoundTrip failed")
			assert.True(t, baseCalled, "the base transport must be invoked for the /rate_limit endpoint even when exhausted")
			if assert.NotNil(t, resp) {
				assert.Equal(t, http.StatusOK, resp.StatusCode)
			}
		})
	}
}

func TestTransport_Reserve_ExemptForRateLimitEndpoint(t *testing.T) {
	for name, path := range map[string]string{
		"cloud":      "/rate_limit",
		"enterprise": "/api/v3/rate_limit",
	} {
		t.Run(name, func(t *testing.T) {
			req := &http.Request{
				URL: &url.URL{
					Scheme: "https",
					Host:   "api.github.com",
					Path:   path,
				},
				Method: http.MethodGet,
			}

			var remainingDuringRoundTrip uint64
			transport := &Transport{Reserve: true}
			transport.Base = roundTripFunc(func(req *http.Request) (*http.Response, error) {
				remainingDuringRoundTrip = transport.Limits.Load(ResourceCore).Remaining
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: http.NoBody}, nil
			})
			transport.Limits.Store(nil, ResourceCore, &Rate{Limit: 5000, Used: 0, Remaining: 5000, Reset: 1745121612})

			_, err := transport.RoundTrip(req)
			assert.NoError(t, err, "(*Transport).RoundTrip failed")

			// /rate_limit never counts against any resource, so Reserve must not touch it.
			assert.Equal(t, uint64(5000), remainingDuringRoundTrip)
			assert.Equal(t, &Rate{Limit: 5000, Used: 0, Remaining: 5000, Reset: 1745121612}, transport.Limits.Load(ResourceCore))
		})
	}
}

func TestTransport_RoundTrip_DoesNotParseRateLimitEndpointResponse(t *testing.T) {
	for name, path := range map[string]string{
		"cloud":      "/rate_limit",
		"enterprise": "/api/v3/rate_limit",
	} {
		t.Run(name, func(t *testing.T) {
			req := &http.Request{
				URL: &url.URL{
					Scheme: "https",
					Host:   "api.github.com",
					Path:   path,
				},
				Method: http.MethodGet,
			}

			var notifyCalls atomic.Int32
			transport := &Transport{
				Base: roundTripFunc(func(req *http.Request) (*http.Response, error) {
					// GitHub's real /rate_limit response carries the same X-Ratelimit-* headers
					// describing the core resource that any other endpoint's response would.
					return &http.Response{
						StatusCode: http.StatusOK,
						Header: http.Header{
							"X-Ratelimit-Limit":     []string{"5000"},
							"X-Ratelimit-Used":      []string{"9999"},
							"X-Ratelimit-Remaining": []string{"1"},
							"X-Ratelimit-Reset":     []string{"1745121612"},
							"X-Ratelimit-Resource":  []string{"core"},
						},
						Body: http.NoBody,
					}, nil
				}),
			}
			transport.Limits.Store(nil, ResourceCore, &Rate{Limit: 5000, Used: 1, Remaining: 4999, Reset: 1745121612})
			transport.Limits.Notify = func(*http.Response, Resource, *Rate) { notifyCalls.Add(1) }

			_, err := transport.RoundTrip(req)
			assert.NoError(t, err, "(*Transport).RoundTrip failed")

			// The /rate_limit response's own headers must not be parsed into Limits: that's
			// (*Limits).Fetch's job (via Store, using the full JSON body), not RoundTrip's.
			// Parsing it here too would redundantly re-store the same entry and fire a
			// spurious extra Notify.
			assert.Zero(t, notifyCalls.Load(), "RoundTrip must not Notify for a /rate_limit response")
			assert.Equal(t, &Rate{Limit: 5000, Used: 1, Remaining: 4999, Reset: 1745121612}, transport.Limits.Load(ResourceCore))
		})
	}
}

func TestTransport_Poll_DoesNotDoubleNotify(t *testing.T) {
	transport := &Transport{
		Base: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			// Mirrors a real GitHub /rate_limit response: X-Ratelimit-* headers for core
			// alongside the full per-resource JSON body.
			return &http.Response{
				StatusCode: http.StatusOK,
				Header: http.Header{
					"X-Ratelimit-Limit":     []string{"5000"},
					"X-Ratelimit-Used":      []string{"0"},
					"X-Ratelimit-Remaining": []string{"5000"},
					"X-Ratelimit-Reset":     []string{"1745121612"},
					"X-Ratelimit-Resource":  []string{"core"},
				},
				Body: io.NopCloser(strings.NewReader(`{"resources":{"core":{"limit":5000,"used":0,"remaining":5000,"reset":1745121612}}}`)),
			}, nil
		}),
	}
	var notifyCalls atomic.Int32
	transport.Limits.Notify = func(*http.Response, Resource, *Rate) { notifyCalls.Add(1) }

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Millisecond)
	defer cancel()
	transport.Poll(ctx, time.Hour, nil)

	// Exactly one Notify (from Fetch's own Store call), not two (the second being the bug:
	// RoundTrip separately re-parsing the same response's headers).
	assert.Equal(t, int32(1), notifyCalls.Load())
	assert.Equal(t, &Rate{Limit: 5000, Used: 0, Remaining: 5000, Reset: 1745121612}, transport.Limits.Load(ResourceCore))
}

func TestTransport_Poll(t *testing.T) {
	var calls atomic.Int32
	transport := &Transport{
		Base: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			calls.Add(1)
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{},
				Body:       io.NopCloser(strings.NewReader(`{"resources":{"core":{"limit":5000,"used":0,"remaining":5000,"reset":1745121612}}}`)),
			}, nil
		}),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 55*time.Millisecond)
	defer cancel()
	transport.Poll(ctx, 20*time.Millisecond, nil)

	assert.GreaterOrEqual(t, calls.Load(), int32(2), "expected Poll to fetch immediately and again on subsequent ticks")
	assert.Equal(t, &Rate{Limit: 5000, Used: 0, Remaining: 5000, Reset: 1745121612}, transport.Limits.Load(ResourceCore))
}
