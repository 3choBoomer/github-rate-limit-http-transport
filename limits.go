package ghratelimit

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"iter"
	"net/http"
	"net/url"
	"strings"
	"sync"
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
	m sync.Map // Resource -> *Rate
	// Notify is called when a new rate limit is stored.
	// It can be a useful hook to update metric gauges.
	Notify func(*http.Response, Resource, *Rate)
}

// Store the rate limit for the given resource type unconditionally, overwriting any existing
// value. Callers that need to filter out stale or out-of-order updates before they reach Store
// are responsible for doing so themselves (see (*Limits).Fetch).
func (l *Limits) Store(resp *http.Response, resource Resource, rate *Rate) {
	l.m.Store(resource, rate)
	if l.Notify != nil {
		l.Notify(resp, resource, rate)
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

// Reserve proactively decrements the Remaining count (and increments Used) by one for the given resource, if known.
// This is useful to optimistically account for an in-flight request before its response (and updated rate-limit
// headers) has been received, to avoid a burst of concurrent requests overrunning the actual rate limit.
// A CompareAndSwap loop is used so concurrent calls don't lose updates to one another.
// Unlike Store, this does not invoke Notify, since the estimate is superseded by the real rate limit once available.
func (l *Limits) Reserve(resource Resource) {
	for {
		rate := l.Load(resource)
		if rate == nil || rate.Remaining == 0 {
			return
		}
		updated := &Rate{
			Limit:     rate.Limit,
			Used:      rate.Used + 1,
			Remaining: rate.Remaining - 1,
			Reset:     rate.Reset,
		}
		if l.m.CompareAndSwap(resource, rate, updated) {
			return
		}
	}
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
		return nil // possibly a error or an endpoint without a rate-limit
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
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return fmt.Errorf("http.NewRequestWithContext for %q failed: %w", u, err)
	}
	req.Header.Set("User-Agent", "github.com/bored-engineer/github-rate-limit-http-transport")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	resp, err := transport.RoundTrip(req)
	if err != nil {
		return fmt.Errorf("(http.RoundTripper).RoundTrip for %q failed: %w", u, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("(*http.Response).Body.Read for %q failed: %w", u, err)
	}
	if err := resp.Body.Close(); err != nil {
		return fmt.Errorf("(*http.Response).Body.Close for %q failed: %w", u, err)
	}

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("(*http.Response).StatusCode(%d) != 200 for %q: %s", resp.StatusCode, u, string(body))
	}

	var limits struct {
		Resources map[Resource]Rate `json:"resources"`
	}

	if err := json.Unmarshal(body, &limits); err != nil {
		return fmt.Errorf("json.Unmarshal for %q failed: %w", u, err)
	}

	for resource, rate := range limits.Resources {
		// The /rate_limit endpoint's response can itself be stale (e.g. served from a cache or a
		// lagging replica), reporting remaining == limit (used == 0) even though live traffic has
		// already been consuming the resource. Trust that "full quota" reading only when there's no
		// fresher evidence to the contrary: either nothing is stored yet, or the stored window has
		// already expired. A reading that shows actual consumption (remaining < limit) proves it
		// reflects real usage, so it's always safe to store regardless of what's already known.
		if rate.Remaining >= rate.Limit {
			if existing := l.Load(resource); existing != nil && !existing.expired() {
				continue
			}
		}
		l.Store(resp, resource, &rate)
	}

	return nil
}
