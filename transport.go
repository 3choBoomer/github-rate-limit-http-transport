package ghratelimit

import (
	"context"
	"log"
	"net/http"
	"net/url"
	"time"
)

// Transport updates the Limits field with the most recent rate-limit information as responses from GitHub are executed.
// It implements the http.RoundTripper interface, so it can be used as base transport for http.Client.
type Transport struct {
	// Base is the base RoundTripper used to make HTTP requests.
	// If nil, http.DefaultTransport is used.
	Base http.RoundTripper
	// Limits is the most recent rate-limit information
	Limits Limits
	// userLoginHeader is the header key used to add the user login to the response.
	// If empty, the header is not added.
	userLoginHeader string
}

// Option configures the Transport
type Option func(*Transport)

// NewTransport creates new Transport with the provided options.
func NewTransport(base http.RoundTripper, opts ...Option) *Transport {
	t := &Transport{
		Base: base,
	}
	for _, opt := range opts {
		opt(t)
	}
	return t
}

// WithNotifyCallback configures the callback to be called when a new rate limit is stored.
func WithNotifyCallback(callback func(*http.Response, Resource, *Rate, *User)) Option {
	return func(t *Transport) {
		t.Limits.Notify = callback
	}
}

// WithFetchUser configures whether the user information should be fetched.
func WithFetchUser(fetch bool) Option {
	return func(t *Transport) {
		t.Limits.fetchUser = fetch
	}
}

// WithUserLoginHeader configures the Transport to add the user login to the response headers.
// The provided string overrides the header key. If no string or an empty string is provided,
// the default key "X-GitHub-User-Login" is used.
func WithUserLoginHeader(headerKey string) Option {
	return func(t *Transport) {
		if headerKey != "" {
			t.userLoginHeader = headerKey
		} else {
			t.userLoginHeader = "X-GitHub-User-Login"
		}
	}
}

// RoundTrip implements http.RoundTripper
func (t *Transport) RoundTrip(req *http.Request) (resp *http.Response, err error) {
	if t.Base == nil {
		resp, err = http.DefaultTransport.RoundTrip(req)
	} else {
		resp, err = t.Base.RoundTrip(req)
	}
	if resp != nil {
		if err := t.Limits.Parse(resp); err != nil {
			return nil, err
		}
		if t.userLoginHeader != "" {
			user := t.Limits.LoadUser()
			if user != nil && user.Login != nil {
				// We are ADDing user to the response headers since this request may be retried
				resp.Header.Add(t.userLoginHeader, *user.Login)
			}
		}
	}
	return resp, err
}

// Poll calls (*Transport).Limits.Update every interval, starting immediately.
func (t *Transport) Poll(ctx context.Context, interval time.Duration, u *url.URL) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := t.Limits.Fetch(ctx, t, u); err != nil {
			log.Printf("(*ghratelimit.Transport).Limits.Fetch failed: %v\n", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
