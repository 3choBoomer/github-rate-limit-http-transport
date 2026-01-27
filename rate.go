package ghratelimit

import (
	"fmt"
	"net/http"
	"strconv"
	"time"
)

// Rate represents the rate limit information for a given resource type.
type Rate struct {
	// The maximum number of requests that you can make per hour.
	Limit uint64 `json:"limit"`
	// The number of requests you have made in the current rate limit window.
	Used uint64 `json:"used"`
	// The number of requests remaining in the current rate limit window.
	Remaining uint64 `json:"remaining"`
	// The time at which the current rate limit window resets, in UTC epoch seconds.
	Reset uint64 `json:"reset"`
}

// String implements fmt.Stringer
func (r *Rate) String() string {
	return fmt.Sprintf("Rate{Limit: %d, Used: %d, Remaining: %d, Reset: %s}", r.Limit, r.Used, r.Remaining, time.Unix(int64(r.Reset), 0).UTC().Format(time.RFC3339))
}

// Parse extracts the rate limit information from the HTTP response headers.
func ParseRate(headers http.Header) (r Rate, _ error) {
	if val, err := strconv.ParseUint(headers.Get("X-Ratelimit-Limit"), 10, 64); err != nil {
		return r, fmt.Errorf("failed to parse X-Ratelimit-Limit header: %w", err)
	} else {
		r.Limit = val
	}
	if val, err := strconv.ParseUint(headers.Get("X-Ratelimit-Used"), 10, 64); err != nil {
		return r, fmt.Errorf("failed to parse X-Ratelimit-Used header: %w", err)
	} else {
		r.Used = val
	}
	if val, err := strconv.ParseUint(headers.Get("X-Ratelimit-Remaining"), 10, 64); err != nil {
		return r, fmt.Errorf("failed to parse X-Ratelimit-Remaining header: %w", err)
	} else {
		r.Remaining = val
	}
	if val, err := strconv.ParseUint(headers.Get("X-Ratelimit-Reset"), 10, 64); err != nil {
		return r, fmt.Errorf("failed to parse X-Ratelimit-Reset header: %w", err)
	} else {
		r.Reset = val
	}
	return r, nil
}
