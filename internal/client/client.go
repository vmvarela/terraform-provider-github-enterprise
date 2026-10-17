// Package client implements the GitHub Enterprise Teams REST API.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const DefaultAPIVersion = "2026-03-10"

type Client struct {
	base           *url.URL
	token, version string
	http           *http.Client
}

type Team struct {
	ID                        int64  `json:"id"`
	Name                      string `json:"name"`
	Slug                      string `json:"slug"`
	Description               string `json:"description"`
	GroupID                   string `json:"group_id"`
	OrganizationSelectionType string `json:"organization_selection_type"`
	NotificationSetting       string `json:"notification_setting"`
}

type User struct {
	Login string `json:"login"`
	ID    int64  `json:"id"`
}

type Organization struct {
	Login string `json:"login"`
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
	return s
}

func IsNotFound(err error) bool {
	var e *APIError
	return errors.As(err, &e) && e.Status == http.StatusNotFound
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
	return &Client{base: u, token: token, version: version, http: &http.Client{
		Timeout: 60 * time.Second,
		// Enterprise endpoints do not require redirects. Never replay privileged
		// credentials against a redirect target supplied by a remote server.
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
	}}, nil
}

func teamPath(enterprise string, selector string) string {
	p := "enterprises/" + url.PathEscape(enterprise) + "/teams"
	if selector != "" {
		p += "/" + url.PathEscape(selector)
	}
	return p
}

func (c *Client) request(ctx context.Context, method, path string, body, out any) (http.Header, error) {
	u := *c.base
	// PathEscape is applied by callers; retain it when resolving the relative path.
	rel, err := url.Parse(path)
	if err != nil {
		return nil, err
	}
	target := u.ResolveReference(rel)
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, target.String(), reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", c.version)
	req.Header.Set("User-Agent", "terraform-provider-github-enterprise")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp.Header, &APIError{Status: resp.StatusCode, Method: method, Path: target.EscapedPath(), RequestID: resp.Header.Get("X-GitHub-Request-Id"), RetryAfter: resp.Header.Get("Retry-After")}
	}
	if out != nil && resp.StatusCode != http.StatusNoContent {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return resp.Header, fmt.Errorf("decode GitHub API response: %w", err)
		}
	}
	return resp.Header, nil
}

// Only consume the page number from Link. Never follow an arbitrary absolute
// next URL with the enterprise token. Refuse repeated/backwards pages.
func nextPage(header http.Header, current int) (int, error) {
	for _, value := range header.Values("Link") {
		for _, link := range strings.Split(value, ",") {
			parts := strings.Split(link, ";")
			next := false
			for _, part := range parts[1:] {
				if strings.TrimSpace(part) == `rel="next"` {
					next = true
				}
			}
			if !next {
				continue
			}
			u, err := url.Parse(strings.Trim(strings.TrimSpace(parts[0]), "<>"))
			if err != nil {
				return 0, fmt.Errorf("invalid pagination link")
			}
			n, err := strconv.Atoi(u.Query().Get("page"))
			if err != nil || n <= current {
				return 0, fmt.Errorf("invalid next page in GitHub API response")
			}
			return n, nil
		}
	}
	return 0, nil
}

func list[T any](ctx context.Context, c *Client, path string) ([]T, error) {
	all := make([]T, 0)
	for page := 1; page != 0; {
		var items []T
		h, err := c.request(ctx, http.MethodGet, path+"?per_page=100&page="+strconv.Itoa(page), nil, &items)
		if err != nil {
			return nil, err
		}
		all = append(all, items...)
		page, err = nextPage(h, page)
		if err != nil {
			return nil, err
		}
	}
	return all, nil
}

func (c *Client) Teams(ctx context.Context, enterprise string) ([]Team, error) {
	return list[Team](ctx, c, teamPath(enterprise, ""))
}

func (c *Client) TeamBySlug(ctx context.Context, enterprise, slug string) (*Team, error) {
	var team Team
	_, err := c.request(ctx, http.MethodGet, teamPath(enterprise, slug), nil, &team)
	if err != nil {
		return nil, err
	}
	if team.ID <= 0 || team.Slug == "" {
		return nil, fmt.Errorf("GitHub returned a team without its identity")
	}
	return &team, nil
}

// FindTeam verifies the numeric identity even when the old slug has been
// reused. Listing is a fallback for imports and out-of-band renames only.
func (c *Client) FindTeam(ctx context.Context, enterprise string, id int64, slug string) (*Team, error) {
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

func (c *Client) CreateTeam(ctx context.Context, enterprise string, body map[string]any) (*Team, error) {
	var team Team
	_, err := c.request(ctx, http.MethodPost, teamPath(enterprise, ""), body, &team)
	if err != nil {
		return nil, err
	}
	if team.ID <= 0 || team.Slug == "" {
		return nil, fmt.Errorf("GitHub returned a team without its identity; inspect the enterprise before retrying creation")
	}
	return &team, nil
}

func (c *Client) UpdateTeam(ctx context.Context, enterprise, slug string, body map[string]any) (*Team, error) {
	var team Team
	_, err := c.request(ctx, http.MethodPatch, teamPath(enterprise, slug), body, &team)
	if err != nil {
		return nil, err
	}
	return &team, nil
}

func (c *Client) DeleteTeam(ctx context.Context, enterprise, slug string) error {
	_, err := c.request(ctx, http.MethodDelete, teamPath(enterprise, slug), nil, nil)
	return err
}

func (c *Client) Membership(ctx context.Context, enterprise string, teamID int64, username string) (*User, error) {
	var user User
	_, err := c.request(ctx, http.MethodGet, teamPath(enterprise, strconv.FormatInt(teamID, 10))+"/memberships/"+url.PathEscape(username), nil, &user)
	return &user, err
}

func (c *Client) AddMember(ctx context.Context, enterprise string, teamID int64, username string) (*User, error) {
	var user User
	_, err := c.request(ctx, http.MethodPut, teamPath(enterprise, strconv.FormatInt(teamID, 10))+"/memberships/"+url.PathEscape(username), nil, &user)
	return &user, err
}

func (c *Client) RemoveMember(ctx context.Context, enterprise string, teamID int64, username string) error {
	_, err := c.request(ctx, http.MethodDelete, teamPath(enterprise, strconv.FormatInt(teamID, 10))+"/memberships/"+url.PathEscape(username), nil, nil)
	return err
}

func (c *Client) Organizations(ctx context.Context, enterprise string, teamID int64) ([]Organization, error) {
	return list[Organization](ctx, c, teamPath(enterprise, strconv.FormatInt(teamID, 10))+"/organizations")
}

func (c *Client) ChangeOrganizations(ctx context.Context, enterprise string, teamID int64, operation string, slugs []string) error {
	if len(slugs) == 0 {
		return nil
	}
	if operation != "add" && operation != "remove" {
		return fmt.Errorf("invalid assignment operation")
	}
	_, err := c.request(ctx, http.MethodPost, teamPath(enterprise, strconv.FormatInt(teamID, 10))+"/organizations/"+operation, map[string]any{"organization_slugs": slugs}, nil)
	return err
}
