package ghratelimit

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

func do(ctx context.Context, transport http.RoundTripper, u *url.URL, dest any) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("http.NewRequestWithContext for %q failed: %w", u, err)
	}
	req.Header.Set("User-Agent", "github.com/bored-engineer/github-rate-limit-http-transport")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	resp, err := transport.RoundTrip(req)
	if err != nil {
		return nil, fmt.Errorf("(http.RoundTripper).RoundTrip for %q failed: %w", u, err)
	}
	if resp == nil {
		return nil, fmt.Errorf("(http.RoundTripper).RoundTrip for %q returned nil response", u)
	}
	defer func(Body io.ReadCloser) {
		_ = Body.Close()
	}(resp.Body)

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp, fmt.Errorf("(*http.Response).Body.Read for %q failed: %w", u, err)
	}
	if err := resp.Body.Close(); err != nil {
		return resp, fmt.Errorf("(*http.Response).Body.Close for %q failed: %w", u, err)
	}

	if resp.StatusCode != http.StatusOK {
		return resp, fmt.Errorf("(*http.Response).StatusCode(%d) != 200 for %q: %s", resp.StatusCode, u, string(body))
	}

	if err := json.Unmarshal(body, dest); err != nil {
		return resp, fmt.Errorf("json.Unmarshal for %q failed: %w", u, err)
	}

	return resp, nil
}
