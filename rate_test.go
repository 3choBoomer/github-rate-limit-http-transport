package ghratelimit

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRate_Parse(t *testing.T) {
	rate, err := ParseRate(http.Header{
		"X-Ratelimit-Limit":     []string{"5000"},
		"X-Ratelimit-Used":      []string{"1000"},
		"X-Ratelimit-Remaining": []string{"4000"},
		"X-Ratelimit-Reset":     []string{"1633036800"},
	})
	assert.NoError(t, err, "failed")
	assert.Equal(t, rate, Rate{
		Limit:     5000,
		Used:      1000,
		Remaining: 4000,
		Reset:     1633036800,
	}, "mismatch")

	_, err = ParseRate(http.Header{
		"X-Ratelimit-Limit":     []string{"invalid"},
		"X-Ratelimit-Used":      []string{"invalid"},
		"X-Ratelimit-Remaining": []string{"invalid"},
		"X-Ratelimit-Reset":     []string{"invalid"},
	})
	assert.Error(t, err, "expected error, got nil")
}

func TestRate_Parse_UsedInvalid(t *testing.T) {
	_, err := ParseRate(http.Header{
		"X-Ratelimit-Limit":     []string{"5000"},
		"X-Ratelimit-Used":      []string{"invalid"},
		"X-Ratelimit-Remaining": []string{"4000"},
		"X-Ratelimit-Reset":     []string{"1633036800"},
	})
	assert.ErrorContains(t, err, "X-Ratelimit-Used")
}

func TestRate_Parse_RemainingInvalid(t *testing.T) {
	_, err := ParseRate(http.Header{
		"X-Ratelimit-Limit":     []string{"5000"},
		"X-Ratelimit-Used":      []string{"1000"},
		"X-Ratelimit-Remaining": []string{"invalid"},
		"X-Ratelimit-Reset":     []string{"1633036800"},
	})
	assert.ErrorContains(t, err, "X-Ratelimit-Remaining")
}

func TestRate_Parse_ResetInvalid(t *testing.T) {
	_, err := ParseRate(http.Header{
		"X-Ratelimit-Limit":     []string{"5000"},
		"X-Ratelimit-Used":      []string{"1000"},
		"X-Ratelimit-Remaining": []string{"4000"},
		"X-Ratelimit-Reset":     []string{"invalid"},
	})
	assert.ErrorContains(t, err, "X-Ratelimit-Reset")
}

func TestRate_String(t *testing.T) {
	r := &Rate{Limit: 5000, Used: 1000, Remaining: 4000, Reset: 1633036800}
	assert.Equal(t, "Rate{Limit: 5000, Used: 1000, Remaining: 4000, Reset: 1633036800}", r.String())
}
