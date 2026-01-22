package ghratelimit

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// DefaultUserURL is the default URL used to poll rate limits.
// It is set to https://api.github.com/user.
var DefaultUserURL = &url.URL{
	Scheme: "https",
	Host:   "api.github.com",
	Path:   "/user",
}

// User represents a GitHub user.
type User struct {
	Login                   *string    `json:"login,omitempty"`
	ID                      *int64     `json:"id,omitempty"`
	NodeID                  *string    `json:"node_id,omitempty"`
	AvatarURL               *string    `json:"avatar_url,omitempty"`
	HTMLURL                 *string    `json:"html_url,omitempty"`
	GravatarID              *string    `json:"gravatar_id,omitempty"`
	Name                    *string    `json:"name,omitempty"`
	Company                 *string    `json:"company,omitempty"`
	Blog                    *string    `json:"blog,omitempty"`
	Location                *string    `json:"location,omitempty"`
	Email                   *string    `json:"email,omitempty"`
	Hireable                *bool      `json:"hireable,omitempty"`
	Bio                     *string    `json:"bio,omitempty"`
	TwitterUsername         *string    `json:"twitter_username,omitempty"`
	PublicRepos             *int       `json:"public_repos,omitempty"`
	PublicGists             *int       `json:"public_gists,omitempty"`
	Followers               *int       `json:"followers,omitempty"`
	Following               *int       `json:"following,omitempty"`
	CreatedAt               *time.Time `json:"created_at,omitempty"`
	UpdatedAt               *time.Time `json:"updated_at,omitempty"`
	SuspendedAt             *time.Time `json:"suspended_at,omitempty"`
	Type                    *string    `json:"type,omitempty"`
	SiteAdmin               *bool      `json:"site_admin,omitempty"`
	TotalPrivateRepos       *int64     `json:"total_private_repos,omitempty"`
	OwnedPrivateRepos       *int64     `json:"owned_private_repos,omitempty"`
	PrivateGists            *int       `json:"private_gists,omitempty"`
	DiskUsage               *int       `json:"disk_usage,omitempty"`
	Collaborators           *int       `json:"collaborators,omitempty"`
	TwoFactorAuthentication *bool      `json:"two_factor_authentication,omitempty"`
	LdapDn                  *string    `json:"ldap_dn,omitempty"`
}

// Fetch the latest user details from the GitHub API and update the User instance.
// If the provided URL is nil, it defaults to DefaultUserURL (https://api.github.com/user).
func (u *User) Fetch(ctx context.Context, transport http.RoundTripper, url *url.URL) error {
	if url == nil {
		url = DefaultUserURL
	}
	url.Path = DefaultUserURL.Path // Ensure we are using the /user path since this can be called from a parent
	_, err := do(ctx, transport, url, u)
	return err
}

// String implements fmt.Stringer
func (u *User) String() string {
	var login, name, userType, email string
	var id int64

	if u.Login != nil {
		login = *u.Login
	}
	if u.ID != nil {
		id = *u.ID
	}
	if u.Name != nil {
		name = *u.Name
	}
	if u.Type != nil {
		userType = *u.Type
	}
	if u.Email != nil {
		email = *u.Email
	}

	return fmt.Sprintf("User{Login: %s, ID: %d, Name: %s, Type: %s, Email: %s}", login, id, name, userType, email)
}
