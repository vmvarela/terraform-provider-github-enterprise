// Package client implements the GitHub Enterprise REST API on top of go-github.
package client

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/go-github/v93/github"
)

const DefaultAPIVersion = "2026-03-10"

// maxPages bounds pagination loops against a misbehaving server.
const maxPages = 1000

// Client wraps go-github. Typed SDK methods are used wherever their request and
// response types carry the provider's semantics; the few raw requests cover the
// gaps documented next to them.
type Client struct {
	api *github.Client
}

// APIError deliberately excludes response bodies and request headers. GitHub
// error bodies can echo user input; diagnostics must never expose credentials.
type APIError struct {
	Status                              int
	Method, Path, RequestID, RetryAfter string
}

func (e *APIError) Error() string {
	s := fmt.Sprintf("GitHub API %s %s: HTTP %d", e.Method, e.Path, e.Status)
	if e.RequestID != "" {
		s += " (request ID " + e.RequestID + ")"
	}
	if e.RetryAfter != "" {
		s += "; retry after " + e.RetryAfter
	}
	if hint := statusHint(e.Status); hint != "" {
		s += ". " + hint
	}
	return s
}

// statusHint tells the reader what to do next. GitHub reports permission
// failures as 404, so the message must name both possibilities.
func statusHint(status int) string {
	switch {
	case status == http.StatusUnauthorized:
		return "check that the token is valid and has not expired"
	case status == http.StatusForbidden:
		return "the token may lack the required enterprise scope, or the account may not be an enterprise owner"
	case status == http.StatusUnprocessableEntity:
		return "GitHub rejected the request; check that the scope, pricing type, SKU and alerting combination is valid for this enterprise"
	case status == http.StatusNotFound:
		return "the object does not exist, or the token cannot see it; check the slug or ID and the token permissions"
	case status == http.StatusTooManyRequests:
		return "GitHub rate-limited the request; wait before retrying"
	case status >= 500:
		return "GitHub returned a server error; retry later"
	}
	return ""
}

func IsNotFound(err error) bool {
	var e *APIError
	return errors.As(err, &e) && e.Status == http.StatusNotFound
}

// apiVersionTransport applies the configured API version to every request,
// including the ones go-github builds internally. Clone the request: transports
// must not mutate what they are given.
type apiVersionTransport struct {
	base    http.RoundTripper
	version string
}

func (t apiVersionTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	clone := r.Clone(r.Context())
	clone.Header.Set("X-GitHub-Api-Version", t.version)
	// Normalize only the SDK default; SCIM methods already set their own media type.
	if a := clone.Header.Get("Accept"); a == "" || a == "application/vnd.github.v3+json" {
		clone.Header.Set("Accept", "application/vnd.github+json")
	}
	return t.base.RoundTrip(clone)
}

func New(baseURL, token, version string) (*Client, error) {
	u, err := url.Parse(baseURL)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("base_url must be an absolute HTTP(S) URL without credentials, query parameters or a fragment")
	}
	if u.Scheme == "http" && u.Hostname() != "localhost" && u.Hostname() != "127.0.0.1" && u.Hostname() != "::1" {
		return nil, fmt.Errorf("base_url requires HTTPS, except on loopback hosts for local testing")
	}
	if token == "" {
		return nil, fmt.Errorf("token must be non-empty")
	}
	for _, ch := range token {
		if ch < 33 || ch > 126 {
			return nil, fmt.Errorf("token must contain only printable ASCII characters without whitespace")
		}
	}
	if _, err := time.Parse("2006-01-02", version); err != nil {
		return nil, fmt.Errorf("api_version must be a date in YYYY-MM-DD format")
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/"
	base := u.String()
	api, err := github.NewClient(
		github.WithHTTPClient(&http.Client{
			Timeout: 60 * time.Second,
			// Enterprise endpoints do not require redirects. Never replay privileged
			// credentials against a redirect target supplied by a remote server.
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
			Transport:     apiVersionTransport{base: http.DefaultTransport, version: version},
		}),
		github.WithAuthToken(token),
		// WithURLs, not WithEnterpriseURLs: the latter rewrites the path to
		// /api/v3/ and this provider targets GitHub Enterprise Cloud base URLs.
		github.WithURLs(&base, &base),
		github.WithUserAgent("terraform-provider-github-enterprise"),
		// Keep failing immediately instead of consulting the SDK's local rate
		// limit cache before the request.
		github.WithDisableRateLimitCheck(),
	)
	if err != nil {
		return nil, err
	}
	return &Client{api: api}, nil
}

// seg escapes one opaque path segment. go-github interpolates path arguments
// without escaping, so callers escape them here; TestPathsEscapeOpaqueIDs fails
// if upstream starts escaping too, turning this into a double escape.
func seg(s string) string { return url.PathEscape(s) }

func teamPath(enterprise string, selector string) string {
	p := "enterprises/" + seg(enterprise) + "/teams"
	if selector != "" {
		p += "/" + seg(selector)
	}
	return p
}

func (c *Client) request(ctx context.Context, method, path string, body, out any) (*github.Response, error) {
	req, err := c.api.NewRequest(ctx, method, path, body)
	if err != nil {
		return nil, err
	}
	resp, err := c.api.Do(req, out)
	if err != nil {
		return resp, newAPIError(req.Method, req.URL.EscapedPath(), err)
	}
	return resp, nil
}

// callError maps a typed SDK error to the same sanitized APIError. The SDK builds
// the request internally, so method and path are only used for diagnostics.
func callError(method, path string, err error) error {
	if err == nil {
		return nil
	}
	return newAPIError(method, path, err)
}

// newAPIError reduces go-github errors carrying a response to the sanitized
// APIError, so status handling stays uniform across typed and raw requests.
func newAPIError(method, path string, err error) error {
	if resp := responseOf(err); resp != nil {
		return &APIError{
			Status:     resp.StatusCode,
			Method:     method,
			Path:       path,
			RequestID:  resp.Header.Get("X-GitHub-Request-Id"),
			RetryAfter: resp.Header.Get("Retry-After"),
		}
	}
	var accepted *github.AcceptedError
	if errors.As(err, &accepted) {
		return &APIError{Status: http.StatusAccepted, Method: method, Path: path}
	}
	// A decode error can quote a server-supplied value (e.g. a timestamp), so it
	// must not reach Terraform diagnostics verbatim.
	var parseErr *time.ParseError
	if errors.As(err, &parseErr) {
		return fmt.Errorf("GitHub API %s %s: response contained an unparsable value", method, path)
	}
	return err
}

func responseOf(err error) *http.Response {
	var er *github.ErrorResponse
	if errors.As(err, &er) {
		return er.Response
	}
	// TwoFactorAuthError is a distinct type that also embeds the response.
	var otp *github.TwoFactorAuthError
	if errors.As(err, &otp) {
		return otp.Response
	}
	var rate *github.RateLimitError
	if errors.As(err, &rate) {
		return rate.Response
	}
	var abuse *github.AbuseRateLimitError
	if errors.As(err, &abuse) {
		return abuse.Response
	}
	var redirect *github.RedirectionError
	if errors.As(err, &redirect) {
		return redirect.Response
	}
	return nil
}

// paginate calls fetch with the page the server asked for next, so typed SDK
// methods keep the provider's full-pagination contract.
func paginate[T any](fetch func(page int) ([]T, int, error)) ([]T, error) {
	all := make([]T, 0)
	for page, n := 1, 0; page != 0; n++ {
		if n >= maxPages {
			return nil, fmt.Errorf("too many pages")
		}
		items, next, err := fetch(page)
		if err != nil {
			return nil, err
		}
		all = append(all, items...)
		page = next
	}
	return all, nil
}

// Only the page number among the server's Link values is used, and only to
// advance. Refuse repeated or backwards pages so a misbehaving server cannot
// loop the provider.
func nextPage(resp *github.Response, current int) (int, error) {
	if resp == nil || resp.NextPage == 0 {
		return 0, nil
	}
	if resp.NextPage <= current {
		return 0, fmt.Errorf("invalid next page in GitHub API response")
	}
	return resp.NextPage, nil
}

func (c *Client) Teams(ctx context.Context, enterprise string) ([]*github.EnterpriseTeam, error) {
	return paginate(func(page int) ([]*github.EnterpriseTeam, int, error) {
		teams, resp, err := c.api.Enterprise.ListTeams(ctx, seg(enterprise), &github.ListOptions{Page: page, PerPage: 100})
		if err != nil {
			return nil, 0, callError(http.MethodGet, teamPath(enterprise, ""), err)
		}
		// The SDK accepts an empty body as success; a list must be a JSON array.
		if teams == nil {
			return nil, 0, fmt.Errorf("GitHub returned an empty team list")
		}
		for _, t := range teams {
			if t == nil {
				return nil, 0, fmt.Errorf("GitHub returned an empty team in the team list")
			}
		}
		next, err := nextPage(resp, page)
		return teams, next, err
	})
}

func (c *Client) TeamBySlug(ctx context.Context, enterprise, slug string) (*github.EnterpriseTeam, error) {
	team, _, err := c.api.Enterprise.GetTeam(ctx, seg(enterprise), seg(slug))
	if err != nil {
		return nil, callError(http.MethodGet, teamPath(enterprise, slug), err)
	}
	if team == nil || team.ID <= 0 || team.Slug == "" {
		return nil, fmt.Errorf("GitHub returned a team without its identity")
	}
	return team, nil
}

// FindTeam verifies the numeric identity even when the old slug has been
// reused. Listing is a fallback for imports and out-of-band renames only.
func (c *Client) FindTeam(ctx context.Context, enterprise string, id int64, slug string) (*github.EnterpriseTeam, error) {
	if slug != "" {
		t, err := c.TeamBySlug(ctx, enterprise, slug)
		if err == nil && t.ID == id {
			return t, nil
		}
		if err != nil && !IsNotFound(err) {
			return nil, err
		}
	}
	teams, err := c.Teams(ctx, enterprise)
	if err != nil {
		return nil, err
	}
	for _, t := range teams {
		if t.ID != id {
			continue
		}
		team, err := c.TeamBySlug(ctx, enterprise, t.Slug)
		if IsNotFound(err) {
			// A positive ID match followed by a disappearing slug is inconclusive:
			// the team may have been renamed again. Do not expose this 404 as a
			// missing team (including through error unwrapping), which would cause
			// callers to drop state or report a successful deletion.
			return nil, fmt.Errorf("enterprise team %d was listed but its slug could not be resolved; retry the operation", id)
		}
		if err != nil {
			return nil, err
		}
		if team.ID != id {
			return nil, fmt.Errorf("enterprise team identity changed during lookup; retry the operation")
		}
		return team, nil
	}
	return nil, &APIError{Status: http.StatusNotFound, Method: http.MethodGet, Path: teamPath(enterprise, strconv.FormatInt(id, 10))}
}

func (c *Client) CreateTeam(ctx context.Context, enterprise string, body github.EnterpriseTeamCreateOrUpdateRequest) (*github.EnterpriseTeam, error) {
	team, _, err := c.api.Enterprise.CreateTeam(ctx, seg(enterprise), body)
	if err != nil {
		return nil, callError(http.MethodPost, teamPath(enterprise, ""), err)
	}
	if team == nil || team.ID <= 0 || team.Slug == "" {
		return nil, fmt.Errorf("GitHub returned a team without its identity; inspect the enterprise before retrying creation")
	}
	return team, nil
}

// UpdateTeam sends a raw body because unlinking an IdP group needs an explicit
// JSON null, which EnterpriseTeamCreateOrUpdateRequest cannot express.
// shortcut: switch to the typed request when the SDK models explicit nulls.
func (c *Client) UpdateTeam(ctx context.Context, enterprise, slug string, body map[string]any) (*github.EnterpriseTeam, error) {
	var team *github.EnterpriseTeam
	if _, err := c.request(ctx, http.MethodPatch, teamPath(enterprise, slug), body, &team); err != nil {
		return nil, err
	}
	if team == nil {
		return nil, fmt.Errorf("GitHub returned an empty response when updating the team")
	}
	return team, nil
}

func (c *Client) DeleteTeam(ctx context.Context, enterprise, slug string) error {
	_, err := c.api.Enterprise.DeleteTeam(ctx, seg(enterprise), seg(slug))
	return callError(http.MethodDelete, teamPath(enterprise, slug), err)
}

func (c *Client) Membership(ctx context.Context, enterprise string, teamID int64, username string) (*github.User, error) {
	user, _, err := c.api.Enterprise.GetTeamMembership(ctx, seg(enterprise), strconv.FormatInt(teamID, 10), seg(username))
	if err != nil {
		return nil, callError(http.MethodGet, memberPath(enterprise, teamID, username), err)
	}
	if user == nil || user.Login == nil || *user.Login == "" {
		return nil, fmt.Errorf("GitHub returned a team membership without its user")
	}
	return user, nil
}

func (c *Client) AddMember(ctx context.Context, enterprise string, teamID int64, username string) (*github.User, error) {
	user, _, err := c.api.Enterprise.AddTeamMember(ctx, seg(enterprise), strconv.FormatInt(teamID, 10), seg(username))
	if err != nil {
		return nil, callError(http.MethodPut, memberPath(enterprise, teamID, username), err)
	}
	if user == nil {
		user = &github.User{}
	}
	return user, nil
}

func (c *Client) RemoveMember(ctx context.Context, enterprise string, teamID int64, username string) error {
	_, err := c.api.Enterprise.RemoveTeamMember(ctx, seg(enterprise), strconv.FormatInt(teamID, 10), seg(username))
	return callError(http.MethodDelete, memberPath(enterprise, teamID, username), err)
}

func (c *Client) Organizations(ctx context.Context, enterprise string, teamID int64) ([]*github.Organization, error) {
	path := teamPath(enterprise, "") + "/" + strconv.FormatInt(teamID, 10) + "/organizations"
	return paginate(func(page int) ([]*github.Organization, int, error) {
		orgs, resp, err := c.api.Enterprise.ListAssignments(ctx, seg(enterprise), strconv.FormatInt(teamID, 10), &github.ListOptions{Page: page, PerPage: 100})
		if err != nil {
			return nil, 0, callError(http.MethodGet, path, err)
		}
		if orgs == nil {
			return nil, 0, fmt.Errorf("GitHub returned an empty organization list")
		}
		for _, o := range orgs {
			if o == nil {
				return nil, 0, fmt.Errorf("GitHub returned an empty organization in the assignment list")
			}
		}
		next, err := nextPage(resp, page)
		return orgs, next, err
	})
}

func (c *Client) ChangeOrganizations(ctx context.Context, enterprise string, teamID int64, operation string, slugs []string) error {
	if len(slugs) == 0 {
		return nil
	}
	var err error
	switch operation {
	case "add":
		_, _, err = c.api.Enterprise.AddMultipleAssignments(ctx, seg(enterprise), strconv.FormatInt(teamID, 10), slugs)
	case "remove":
		_, _, err = c.api.Enterprise.RemoveMultipleAssignments(ctx, seg(enterprise), strconv.FormatInt(teamID, 10), slugs)
	default:
		return fmt.Errorf("invalid assignment operation")
	}
	return callError(http.MethodPost, teamPath(enterprise, "")+"/"+strconv.FormatInt(teamID, 10)+"/organizations/"+operation, err)
}

func memberPath(enterprise string, teamID int64, username string) string {
	return teamPath(enterprise, "") + "/" + strconv.FormatInt(teamID, 10) + "/memberships/" + seg(username)
}
