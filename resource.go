package ghratelimit

import (
	"net/http"
	"slices"
)

// Resource represents the X-Ratelimit-Resource header value.
type Resource string

const (
	// The core REST API's rate limit.
	ResourceCore Resource = "core"

	// Search API's rate limit.
	ResourceSearch Resource = "search"

	// GraphQL API's rate limit.
	ResourceGraphQL Resource = "graphql"

	// App manifest API's rate limit.
	ResourceIntegrationManifest Resource = "integration_manifest"

	// Import API's rate limit.
	ResourceSourceImport Resource = "source_import"

	// Code Scanning upload API's rate limit.
	ResourceCodeScanningUpload Resource = "code_scanning_upload"

	// Code Scanning autofix API's rate limit.
	ResourceCodeScanningAutofix Resource = "code_scanning_autofix"

	// Actions Runner Registration API's rate limit.
	ResourceActionsRunnerRegistration Resource = "actions_runner_registration"

	// SCIM API's rate limit.
	ResourceSCIM Resource = "scim"

	// Dependency Snapshots API's rate limit.
	ResourceDependencySnapshots Resource = "dependency_snapshots"

	// Audit Log API's rate limit.
	ResourceAuditLog Resource = "audit_log"

	// Audit Log Streaming API's rate limit.
	ResourceAuditLogStreaming Resource = "audit_log_streaming"

	// Code Search API's rate limit.
	ResourceCodeSearch Resource = "code_search"
)

// ValidResources represents the list of valid/known rate-limit resources.
// Modifying this slice at runtime may result in undefined behavior.
var ValidResources = []Resource{
	ResourceCore, ResourceSearch, ResourceGraphQL,
	ResourceIntegrationManifest, ResourceSourceImport,
	ResourceCodeScanningUpload, ResourceCodeScanningAutofix,
	ResourceActionsRunnerRegistration, ResourceSCIM,
	ResourceDependencySnapshots, ResourceAuditLog,
	ResourceAuditLogStreaming, ResourceCodeSearch,
}

// String implements fmt.Stringer.
func (r Resource) String() string {
	return string(r)
}

// Valid checks if the resource is valid/known.
func (r Resource) Valid() bool {
	return slices.Contains(ValidResources, r)
}

// ParseResource extracts the Resource from the X-RateLimit-Resource header of the HTTP response.
func ParseResource(headers http.Header) Resource {
	return Resource(headers.Get("X-RateLimit-Resource"))
}
