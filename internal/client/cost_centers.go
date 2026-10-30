package client

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"github.com/google/go-github/v93/github"
)

const costCenterArchivedState = "CostCenterArchived"

const costCenterPageSize = 100

// CostCenterAssignments is the body for adding or removing resources. It also
// carries enterprise teams, which CostCenterResourceRequest does not model;
// shortcut: drop it and use the SDK type once it does.
type CostCenterAssignments struct {
	Users           []string `json:"users,omitempty"`
	Organizations   []string `json:"organizations,omitempty"`
	Repositories    []string `json:"repositories,omitempty"`
	EnterpriseTeams []string `json:"enterprise_teams,omitempty"`
}

// ReassignedResource reports a resource moved from another cost center.
type ReassignedResource struct {
	ResourceType       string `json:"resource_type"`
	Name               string `json:"name"`
	PreviousCostCenter string `json:"previous_cost_center"`
}

type costCenterResourcesResult struct {
	Message             string               `json:"message"`
	ReassignedResources []ReassignedResource `json:"reassigned_resources"`
}

func costCenterPath(enterprise, id string) string {
	p := "enterprises/" + seg(enterprise) + "/settings/billing/cost-centers"
	if id != "" {
		p += "/" + seg(id)
	}
	return p
}

func sameStr(a, b *string) bool { return a == nil && b == nil || a != nil && b != nil && *a == *b }

// validCostCenterID keeps IDs opaque but refuses values that would turn the
// escaped single path segment into a dot segment ("." or "..").
func validCostCenterID(id string) error {
	if id == "" || id == "." || id == ".." {
		return fmt.Errorf("invalid cost center ID %q", id)
	}
	return nil
}

func validCostCenter(cc *github.CostCenter) error {
	if validCostCenterID(cc.ID) != nil || cc.Name == "" {
		return fmt.Errorf("GitHub returned a cost center without its identity")
	}
	return nil
}

// CostCenters returns all cost centers, optionally filtered by state ("" for none).
// It stays a raw request because ListCostCenterOptions has no page parameter and
// the provider must follow every page.
func (c *Client) CostCenters(ctx context.Context, enterprise, state string) ([]*github.CostCenter, error) {
	all := make([]*github.CostCenter, 0)
	seen := map[string]bool{}
	path := costCenterPath(enterprise, "")
	for page, n := 1, 0; page != 0; n++ {
		if n >= maxPages {
			return nil, fmt.Errorf("too many cost center pages")
		}
		q := url.Values{}
		if state != "" {
			q.Set("state", state)
		}
		if page > 1 {
			q.Set("page", strconv.Itoa(page))
		}
		p := path
		if len(q) > 0 {
			p += "?" + q.Encode()
		}
		var out struct {
			CostCenters []*github.CostCenter `json:"costCenters"`
		}
		resp, err := c.request(ctx, http.MethodGet, p, nil, &out)
		if err != nil {
			return nil, err
		}
		if out.CostCenters == nil {
			return nil, fmt.Errorf("GitHub returned a cost center list without costCenters")
		}
		for _, cc := range out.CostCenters {
			if err := validCostCenter(cc); err != nil {
				return nil, err
			}
			if cc.Resources == nil {
				return nil, fmt.Errorf("GitHub returned a cost center without its resources")
			}
			if seen[cc.ID] {
				return nil, fmt.Errorf("GitHub returned a duplicate cost center ID")
			}
			seen[cc.ID] = true
		}
		all = append(all, out.CostCenters...)
		if page, err = nextPage(resp, page); err != nil {
			return nil, err
		}
	}
	return all, nil
}

func (c *Client) CreateCostCenter(ctx context.Context, enterprise, name string) (*github.CostCenter, error) {
	cc, _, err := c.api.Enterprise.CreateCostCenter(ctx, seg(enterprise), github.CostCenterRequest{Name: name})
	if err != nil {
		return nil, callError(http.MethodPost, costCenterPath(enterprise, ""), err)
	}
	if cc == nil || validCostCenterID(cc.ID) != nil {
		return nil, fmt.Errorf("GitHub returned a cost center without a valid ID; inspect the enterprise before retrying creation")
	}
	if cc.Name == "" {
		// Return the created identity so callers can keep it; they must refresh
		// manually before retrying instead of adopting a cost center by name.
		return cc, fmt.Errorf("GitHub created cost center %q but returned an incomplete response; refresh before retrying", cc.ID)
	}
	return cc, nil
}

// CostCenter reads every resource page and fails if the identity or the
// pagination flag is inconsistent rather than returning a truncated object. The
// typed method takes no page options, so this stays a raw request.
func (c *Client) CostCenter(ctx context.Context, enterprise, id string) (*github.CostCenter, error) {
	if err := validCostCenterID(id); err != nil {
		return nil, err
	}
	var result *github.CostCenter
	for page := 1; ; page++ {
		if page > maxPages {
			return nil, fmt.Errorf("too many cost center pages")
		}
		var out struct {
			github.CostCenter
			HasNextPage *bool `json:"has_next_page"`
		}
		_, err := c.request(ctx, http.MethodGet, costCenterPath(enterprise, id)+"?page="+strconv.Itoa(page)+"&per_page="+strconv.Itoa(costCenterPageSize), nil, &out)
		if err != nil {
			return nil, err
		}
		if err := validCostCenter(&out.CostCenter); err != nil {
			return nil, err
		}
		if out.Resources == nil {
			return nil, fmt.Errorf("GitHub returned a cost center without its resources")
		}
		if out.ID != id {
			return nil, fmt.Errorf("cost center identity changed during lookup; retry the operation")
		}
		if result == nil {
			cc := out.CostCenter
			result = &cc
		} else {
			if out.Name != result.Name || !sameStr(out.State, result.State) || !sameStr(out.AzureSubscription, result.AzureSubscription) {
				return nil, fmt.Errorf("cost center changed between pages; retry the operation")
			}
			result.Resources = append(result.Resources, out.Resources...)
		}
		if out.HasNextPage == nil {
			if page == 1 && len(out.Resources) < costCenterPageSize {
				return result, nil // a short first page without the flag cannot hide more pages
			}
			return nil, fmt.Errorf("GitHub omitted has_next_page on a full or later cost center page")
		}
		if !*out.HasNextPage {
			return result, nil
		}
		if len(out.Resources) == 0 {
			return nil, fmt.Errorf("GitHub reported another cost center page after an empty one")
		}
	}
}

func (c *Client) UpdateCostCenter(ctx context.Context, enterprise, id, name string) (*github.CostCenter, error) {
	if err := validCostCenterID(id); err != nil {
		return nil, err
	}
	cc, _, err := c.api.Enterprise.UpdateCostCenter(ctx, seg(enterprise), seg(id), github.CostCenterRequest{Name: name})
	if err != nil {
		return nil, callError(http.MethodPatch, costCenterPath(enterprise, id), err)
	}
	if cc == nil {
		return nil, fmt.Errorf("GitHub returned an empty response when updating the cost center")
	}
	if err := validCostCenter(cc); err != nil {
		return nil, err
	}
	if cc.ID != id {
		return nil, fmt.Errorf("GitHub returned a different cost center than requested")
	}
	return cc, nil
}

// DeleteCostCenter archives the cost center and verifies GitHub's archive confirmation.
func (c *Client) DeleteCostCenter(ctx context.Context, enterprise, id string) error {
	if err := validCostCenterID(id); err != nil {
		return err
	}
	out, _, err := c.api.Enterprise.DeleteCostCenter(ctx, seg(enterprise), seg(id))
	if err != nil {
		return callError(http.MethodDelete, costCenterPath(enterprise, id), err)
	}
	if out == nil || out.ID != id || out.CostCenterState != costCenterArchivedState {
		return fmt.Errorf("GitHub did not confirm the cost center was archived")
	}
	return nil
}

// AddCostCenterResources assigns resources and returns resources reassigned from other cost centers.
func (c *Client) AddCostCenterResources(ctx context.Context, enterprise, id string, r CostCenterAssignments) ([]ReassignedResource, error) {
	out, err := c.changeCostCenterResources(ctx, http.MethodPost, enterprise, id, r)
	if out == nil {
		return nil, err
	}
	return out.ReassignedResources, err
}

func (c *Client) RemoveCostCenterResources(ctx context.Context, enterprise, id string, r CostCenterAssignments) error {
	_, err := c.changeCostCenterResources(ctx, http.MethodDelete, enterprise, id, r)
	return err
}

// changeCostCenterResources stays a raw request to keep enterprise team assignments.
func (c *Client) changeCostCenterResources(ctx context.Context, method, enterprise, id string, r CostCenterAssignments) (*costCenterResourcesResult, error) {
	if err := validCostCenterID(id); err != nil {
		return nil, err
	}
	if len(r.Users)+len(r.Organizations)+len(r.Repositories)+len(r.EnterpriseTeams) == 0 {
		return nil, nil
	}
	var out costCenterResourcesResult
	if _, err := c.request(ctx, method, costCenterPath(enterprise, id)+"/resource", r, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
