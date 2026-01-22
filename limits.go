package ghratelimit

import (
	"context"
	"fmt"
	"iter"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
)

// DefaultURL is the default URL used to poll rate limits.
// It is set to https://api.github.com/rate_limit.
var DefaultURL = &url.URL{
	Scheme: "https",
	Host:   "api.github.com",
	Path:   "/rate_limit",
}

// Limits represents the rate limits for all known resource types.
type Limits struct {
	m         sync.Map
	user      atomic.Pointer[User]
	fetchUser bool

	// Notify is called when a new rate limit is stored.
	// It can be a useful hook to update metric gauges.
	// User can be nil
	Notify func(*http.Response, Resource, *Rate, *User)
}

// Store the rate limit for the given resource type.
func (l *Limits) Store(resp *http.Response, resource Resource, rate *Rate) {
	l.m.Store(resource, rate)
	if l.Notify != nil {
		l.Notify(resp, resource, rate, l.LoadUser())
	}
}

// Load the rate-limit for the given resource type.
func (l *Limits) Load(resource Resource) *Rate {
	val, ok := l.m.Load(resource)
	if !ok {
		return nil
	}
	r, ok := val.(*Rate)
	if !ok {
		return nil
	}
	return r
}

// Iter loops over the resource types and yields each resource type and its rate limit.
func (l *Limits) Iter() iter.Seq2[Resource, *Rate] {
	return func(yield func(Resource, *Rate) bool) {
		l.m.Range(func(key, value any) bool {
			resource, ok := key.(Resource)
			if !ok {
				return false
			}
			rate, ok := value.(*Rate)
			if !ok {
				return false
			}
			return yield(resource, rate)
		})
	}
}

// String implements fmt.Stringer
func (l *Limits) String() string {
	var sb strings.Builder
	sb.WriteString("Limits{")
	first := true
	for resource, rate := range l.Iter() {
		if !first {
			sb.WriteString(", ")
		}
		first = false
		sb.WriteString(resource.String())
		sb.WriteString(": ")
		sb.WriteString(rate.String())
	}
	sb.WriteString("}")
	return sb.String()
}

// Parse updates the rate limits based on the provided HTTP response.
func (l *Limits) Parse(resp *http.Response) error {
	resource := ParseResource(resp.Header)
	if resource == "" {
		return nil // possibly an error or an endpoint without a rate-limit
	}
	rate, err := ParseRate(resp.Header)
	if err != nil {
		return err
	}
	l.Store(resp, resource, &rate)
	return nil
}

// Fetch the latest rate limits from the GitHub API and update the Limits instance.
// If the provided URL is nil, it defaults to DefaultURL (https://api.github.com/rate_limit).
func (l *Limits) Fetch(ctx context.Context, transport http.RoundTripper, u *url.URL) error {
	if u == nil {
		u = DefaultURL
	}

	var limits struct {
		Resources map[Resource]Rate `json:"resources"`
	}

	resp, err := do(ctx, transport, u, &limits)
	if err != nil {
		return err
	}

	for resource, rate := range limits.Resources {
		l.Store(resp, resource, &rate)
	}
	// Only fetch user info if we haven't already fetched it and we have remaining core requests
	if l.fetchUser && l.LoadUser() == nil && limits.Resources[ResourceCore].Remaining > 0 {
		if err := l.FetchUser(ctx, transport, u); err != nil {
			return fmt.Errorf("Limits.FetchUser failed: %w", err)
		}
	}

	return nil
}

// StoreUser stores the user information.
func (l *Limits) StoreUser(u *User) {
	l.user.Store(u)
}

// LoadUser loads the user information.
func (l *Limits) LoadUser() *User {
	return l.user.Load()
}

// FetchUser fetches the latest user information from the GitHub API and updates the Limits instance.
func (l *Limits) FetchUser(ctx context.Context, transport http.RoundTripper, link *url.URL) error {
	var u User
	if err := u.Fetch(ctx, transport, link); err != nil {
		return err
	}
	l.StoreUser(&u)
	return nil
}
