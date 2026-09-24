package ghratelimit

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestInferResource(t *testing.T) {
	tests := []struct {
		name   string
		method string
		path   string
		want   Resource
	}{
		{"code search", http.MethodGet, "/search/code", ResourceCodeSearch},
		{"other search", http.MethodGet, "/search/repositories", ResourceSearch},
		{"graphql", http.MethodPost, "/graphql", ResourceGraphQL},
		{"app manifest", http.MethodGet, "/app-manifests/abc123/conversions", ResourceIntegrationManifest},
		{"code scanning sarif upload", http.MethodPost, "/repos/o/r/code-scanning/sarifs", ResourceCodeScanningUpload},
		{"code scanning sarif upload wrong method", http.MethodGet, "/repos/o/r/code-scanning/sarifs", ResourceCore},
		{"code scanning autofix", http.MethodPost, "/repos/o/r/code-scanning/alerts/1/autofix", ResourceCodeScanningAutofix},
		{"code scanning autofix wrong method", http.MethodGet, "/repos/o/r/code-scanning/alerts/1/autofix", ResourceCore},
		{"actions runner registration", http.MethodPost, "/actions/runners/registration-token", ResourceActionsRunnerRegistration},
		{"actions runner registration wrong method", http.MethodGet, "/actions/runners/registration-token", ResourceCore},
		{"scim", http.MethodGet, "/scim/v2/Users", ResourceSCIM},
		{"dependency snapshots", http.MethodGet, "/repos/o/r/dependency-graph/snapshots", ResourceDependencySnapshots},
		{"enterprise audit log", http.MethodGet, "/enterprises/e/audit-log", ResourceAuditLog},
		{"organization audit log", http.MethodGet, "/organizations/o/audit-log", ResourceAuditLog},
		{"enterprise audit log streaming", http.MethodGet, "/enterprises/e/audit-log/streams", ResourceAuditLogStreaming},
		{"organization audit log streaming", http.MethodGet, "/organizations/o/audit-log/streams", ResourceAuditLogStreaming},
		{"core default", http.MethodGet, "/users/bored-engineer", ResourceCore},
		{"github enterprise /api/v3 prefix is trimmed", http.MethodGet, "/api/v3/search/code", ResourceCodeSearch},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := &http.Request{
				URL: &url.URL{
					Scheme: "https",
					Host:   "api.github.com",
					Path:   tt.path,
				},
				Method: tt.method,
			}
			assert.Equal(t, tt.want, InferResource(req), "mismatch for %s %s", tt.method, tt.path)
		})
	}
}
