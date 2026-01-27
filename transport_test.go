package ghratelimit

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

type mockRoundTripper struct {
	roundTripFunc func(*http.Request) (*http.Response, error)
}

func (m *mockRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if m.roundTripFunc != nil {
		return m.roundTripFunc(req)
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewBufferString("{}")),
		Header:     make(http.Header),
	}, nil
}

func TestNewTransport(t *testing.T) {
	base := &mockRoundTripper{}

	t.Run("defaults", func(t *testing.T) {
		tr := NewTransport(base)
		assert.Equal(t, base, tr.Base)
	})

	t.Run("options", func(t *testing.T) {
		callback := func(*http.Response, Resource, *Rate) {}
		tr := NewTransport(base,
			WithNotifyCallback(callback),
		)
		assert.NotNil(t, tr.Limits.Notify)
	})

}

func TestTransport_RoundTrip(t *testing.T) {
	t.Run("delegation", func(t *testing.T) {
		called := false
		base := &mockRoundTripper{
			roundTripFunc: func(req *http.Request) (*http.Response, error) {
				called = true
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(bytes.NewBufferString("{}")),
					Header:     http.Header{},
				}, nil
			},
		}
		tr := NewTransport(base)
		req, _ := http.NewRequest("GET", "https://api.github.com/users/octocat", nil)
		_, err := tr.RoundTrip(req)
		assert.NoError(t, err)
		assert.True(t, called)
	})

	t.Run("rate limit parsing", func(t *testing.T) {
		base := &mockRoundTripper{
			roundTripFunc: func(req *http.Request) (*http.Response, error) {
				h := http.Header{}
				h.Set("X-RateLimit-Limit", "5000")
				h.Set("X-RateLimit-Remaining", "4999")
				h.Set("X-RateLimit-Reset", "1600000000")
				h.Set("X-RateLimit-Used", "1")
				h.Set("X-RateLimit-Resource", "core")
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(bytes.NewBufferString("{}")),
					Header:     h,
				}, nil
			},
		}
		tr := NewTransport(base)
		req, _ := http.NewRequest("GET", "https://api.github.com/users/octocat", nil)
		_, err := tr.RoundTrip(req)
		assert.NoError(t, err)

		rate := tr.Limits.Load(ResourceCore)
		// Use manual check to avoid panic if testify is not available or behaves differently
		if rate == nil {
			t.Fatal("expected rate for ResourceCore to be present")
		}
		assert.Equal(t, uint64(5000), rate.Limit)
		assert.Equal(t, uint64(4999), rate.Remaining)
	})

	t.Run("poll", func(t *testing.T) {
		called := make(chan struct{})
		base := &mockRoundTripper{
			roundTripFunc: func(req *http.Request) (*http.Response, error) {
				// Only close once
				select {
				case <-called:
				default:
					close(called)
				}
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(bytes.NewBufferString("{}")),
					Header:     http.Header{},
				}, nil
			},
		}
		tr := NewTransport(base)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		go tr.Poll(ctx, time.Millisecond, nil)

		select {
		case <-called:
			// success
		case <-time.After(100 * time.Millisecond):
			t.Fatal("Poll did not call RoundTrip")
		}
	})

	t.Run("poll multiple ticks", func(t *testing.T) {
		calls := make(chan struct{}, 10)

		base := &mockRoundTripper{
			roundTripFunc: func(req *http.Request) (*http.Response, error) {
				select {
				case calls <- struct{}{}:
				default:
				}
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(bytes.NewBufferString(`{"resources":{}}`)),
					Header:     http.Header{},
				}, nil
			},
		}
		tr := NewTransport(base)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		interval := 10 * time.Millisecond
		go tr.Poll(ctx, interval, nil)

		// Expect at least 3 calls:
		// 1. Initial immediate call
		// 2. First tick
		// 3. Second tick
		expectedCalls := 3
		received := 0
		timeout := time.After(100 * time.Millisecond)

		for received < expectedCalls {
			select {
			case <-calls:
				received++
			case <-timeout:
				t.Fatalf("timed out waiting for %d calls, got %d", expectedCalls, received)
			}
		}
	})
}
