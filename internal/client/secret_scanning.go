package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/google/go-github/v93/github"
)

// CustomPattern is one enterprise secret scanning custom pattern. The provider
// keeps its own type so the SDK's shape does not leak into the resource layer.
type CustomPattern struct {
	ID                    int64
	Name, Pattern, Slug   string
	State                 string
	PushProtectionEnabled bool
	StartDelimiter        *string
	EndDelimiter          *string
	MustMatch             []string
	MustNotMatch          []string
	Version               string
}

// CustomPatternInput carries the writable fields. GitHub has no endpoint to
// publish a pattern or to enable push protection, so neither is part of it.
type CustomPatternInput struct {
	Name, Pattern  string
	StartDelimiter *string
	EndDelimiter   *string
	MustMatch      []string
	MustNotMatch   []string
}

func customPatternsPath(enterprise string, id int64) string {
	p := "enterprises/" + seg(enterprise) + "/secret-scanning/custom-patterns"
	if id > 0 {
		p += "/" + strconv.FormatInt(id, 10)
	}
	return p
}

func customPatternOf(p *github.SecretScanningCustomPattern) *CustomPattern {
	if p == nil {
		return nil
	}
	out := &CustomPattern{
		ID:                    p.ID,
		Name:                  p.Name,
		Pattern:               p.Pattern,
		Slug:                  p.Slug,
		State:                 p.State,
		PushProtectionEnabled: p.PushProtectionEnabled,
		StartDelimiter:        p.StartDelimiter,
		EndDelimiter:          p.EndDelimiter,
		MustMatch:             p.MustMatch,
		MustNotMatch:          p.MustNotMatch,
	}
	if p.CustomPatternVersion != nil {
		out.Version = *p.CustomPatternVersion
	}
	return out
}

// CreateCustomPattern posts a batch of one pattern. A 201 means every pattern
// in the batch was created, so anything other than exactly one created pattern
// is an error the caller must see instead of guessing at partial success.
func (c *Client) CreateCustomPattern(ctx context.Context, enterprise string, in CustomPatternInput) (*CustomPattern, error) {
	body := github.SecretScanningCreateCustomPatternsRequest{Patterns: []*github.SecretScanningCustomPatternRequest{{
		Name:           in.Name,
		Pattern:        in.Pattern,
		StartDelimiter: in.StartDelimiter,
		EndDelimiter:   in.EndDelimiter,
		MustMatch:      in.MustMatch,
		MustNotMatch:   in.MustNotMatch,
	}}}
	out, _, err := c.api.SecretScanning.CreateCustomPatternsForEnterprise(ctx, seg(enterprise), body)
	if err != nil {
		return nil, callError(http.MethodPost, customPatternsPath(enterprise, 0), err)
	}
	if out == nil || len(out.CreatedPatterns) != 1 || out.CreatedPatterns[0] == nil {
		return nil, fmt.Errorf("GitHub did not confirm the creation of exactly one custom pattern")
	}
	p := customPatternOf(out.CreatedPatterns[0])
	if p.ID <= 0 {
		return nil, fmt.Errorf("GitHub created a custom pattern without its ID")
	}
	return p, nil
}

// ListCustomPatterns follows the page links with per_page=100. There is no
// state filter: unpublished patterns must be readable for drift detection.
func (c *Client) ListCustomPatterns(ctx context.Context, enterprise string) ([]CustomPattern, error) {
	return paginate(func(page int) ([]CustomPattern, int, error) {
		patterns, resp, err := c.api.SecretScanning.ListCustomPatternsForEnterprise(ctx, seg(enterprise), &github.SecretScanningCustomPatternListOptions{
			ListOptions: github.ListOptions{Page: page, PerPage: 100},
		})
		if err != nil {
			return nil, 0, callError(http.MethodGet, customPatternsPath(enterprise, 0), err)
		}
		// The SDK accepts an empty body as success; a list must be a JSON array.
		if patterns == nil {
			return nil, 0, fmt.Errorf("GitHub returned an empty custom pattern list")
		}
		out := make([]CustomPattern, 0, len(patterns))
		for _, p := range patterns {
			if p == nil || p.ID <= 0 {
				return nil, 0, fmt.Errorf("GitHub returned a custom pattern without its ID")
			}
			out = append(out, *customPatternOf(p))
		}
		next, err := nextPage(resp, page)
		return out, next, err
	})
}

// GetCustomPattern reads one pattern from the list; the API has no GET by ID.
// A missing pattern is (nil, nil), not an error.
func (c *Client) GetCustomPattern(ctx context.Context, enterprise string, id int64) (*CustomPattern, error) {
	patterns, err := c.ListCustomPatterns(ctx, enterprise)
	if err != nil {
		return nil, err
	}
	for i := range patterns {
		if patterns[i].ID == id {
			p := patterns[i]
			return &p, nil
		}
	}
	return nil, nil
}

// UpdateCustomPattern sends a raw body because the SDK request struct uses
// omitempty: it cannot express an explicit null for a removed delimiter or an
// empty must_match list, both of which this provider must be able to send.
func (c *Client) UpdateCustomPattern(ctx context.Context, enterprise string, id int64, version string, patch map[string]json.RawMessage) (*CustomPattern, error) {
	body := make(map[string]json.RawMessage, len(patch)+1)
	for k, v := range patch {
		body[k] = v
	}
	// The version travels with every update; a stale one is a 412.
	v, _ := json.Marshal(version)
	body["custom_pattern_version"] = v
	var p *github.SecretScanningCustomPattern
	if _, err := c.request(ctx, http.MethodPatch, customPatternsPath(enterprise, id), body, &p); err != nil {
		var apiErr *APIError
		if errors.As(err, &apiErr) && apiErr.Status == http.StatusPreconditionFailed {
			return nil, fmt.Errorf("custom pattern %d changed outside Terraform; refresh the state and retry: %w", id, err)
		}
		return nil, err
	}
	if p == nil || p.ID != id {
		return nil, fmt.Errorf("GitHub did not confirm the update of custom pattern %d", id)
	}
	return customPatternOf(p), nil
}

// DeleteCustomPattern deletes a batch of one pattern. A 404 means the pattern
// is already gone, which is the desired end state, not a failure.
func (c *Client) DeleteCustomPattern(ctx context.Context, enterprise string, id int64, version, postDeleteAction string) error {
	body := github.SecretScanningDeleteCustomPatternsRequest{
		Patterns: []*github.SecretScanningCustomPatternToDelete{{
			PatternID:            id,
			CustomPatternVersion: &version,
		}},
		PostDeleteAction: &postDeleteAction,
	}
	_, err := c.api.SecretScanning.DeleteCustomPatternsForEnterprise(ctx, seg(enterprise), body)
	if err == nil {
		return nil
	}
	err = callError(http.MethodDelete, customPatternsPath(enterprise, id), err)
	if IsNotFound(err) {
		return nil
	}
	return err
}
