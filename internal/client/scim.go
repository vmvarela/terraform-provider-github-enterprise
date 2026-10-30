package client

import (
	"context"
	"fmt"
	"net/http"

	"github.com/google/go-github/v93/github"
)

const (
	scimAccept     = "application/scim+json"
	scimListSchema = "urn:ietf:params:scim:api:messages:2.0:ListResponse"
	scimPageSize   = 100
)

func scimPath(enterprise, resource, id string) string {
	p := "scim/v2/enterprises/" + seg(enterprise) + "/" + resource
	if id != "" {
		p += "/" + seg(id)
	}
	return p
}

// validSCIMID keeps IDs opaque but refuses values that would make the escaped
// single segment resolve as a dot segment.
func validSCIMID(id string) error {
	if id == "" || id == "." || id == ".." {
		return fmt.Errorf("invalid SCIM resource ID %q", id)
	}
	return nil
}

func scimHasSchema(schemas []string, want string) bool {
	for _, s := range schemas {
		if s == want {
			return true
		}
	}
	return false
}

func scimID(id *string) string {
	if id == nil {
		return ""
	}
	return *id
}

// scimGet checks that a single-resource read returns the requested identity.
func scimGet[T any](id string, got *T, idOf func(*T) string, err error, path string) (*T, error) {
	if err != nil {
		return nil, callError(http.MethodGet, path, err)
	}
	if got == nil {
		return nil, fmt.Errorf("GitHub returned a SCIM resource without its identity")
	}
	switch v := idOf(got); v {
	case "":
		return nil, fmt.Errorf("GitHub returned a SCIM resource without its identity")
	case id:
		return got, nil
	default:
		return nil, fmt.Errorf("SCIM resource identity changed during lookup; retry the operation")
	}
}

// scimPage normalizes the SDK list responses so pagination validation stays in
// one place.
type scimPage[T any] struct {
	Schemas      []string
	TotalResults *int
	StartIndex   *int
	ItemsPerPage *int
	Resources    []T
}

// scimList pages from startIndex 1 until totalResults is reached, advancing by
// the number of Resources actually returned. Link and meta references are ignored.
func scimList[T any](next func(startIndex int) (scimPage[T], error), idOf func(T) string) ([]T, error) {
	all := make([]T, 0)
	seen := map[string]bool{}
	startIndex := 1
	for {
		out, err := next(startIndex)
		if err != nil {
			return nil, err
		}
		if out.Schemas == nil && out.Resources == nil {
			return nil, fmt.Errorf("GitHub returned an empty SCIM response")
		}
		if !scimHasSchema(out.Schemas, scimListSchema) {
			return nil, fmt.Errorf("GitHub returned a SCIM list without the ListResponse schema")
		}
		if out.Resources == nil {
			return nil, fmt.Errorf("GitHub returned a SCIM list without Resources")
		}
		if out.TotalResults == nil || out.StartIndex == nil || out.ItemsPerPage == nil {
			return nil, fmt.Errorf("GitHub returned a SCIM list without pagination metadata")
		}
		if *out.TotalResults < 0 || *out.StartIndex < 0 || *out.ItemsPerPage < 0 {
			return nil, fmt.Errorf("GitHub returned negative SCIM pagination metadata")
		}
		if *out.StartIndex != startIndex {
			return nil, fmt.Errorf("SCIM list returned startIndex %d, expected %d", *out.StartIndex, startIndex)
		}
		received := startIndex - 1
		if received > *out.TotalResults || len(out.Resources) > *out.TotalResults-received {
			return nil, fmt.Errorf("SCIM list returned more resources than totalResults")
		}
		for i := range out.Resources {
			id := idOf(out.Resources[i])
			if err := validSCIMID(id); err != nil {
				return nil, err
			}
			if seen[id] {
				return nil, fmt.Errorf("GitHub returned a duplicate SCIM resource ID")
			}
			seen[id] = true
		}
		all = append(all, out.Resources...)
		received += len(out.Resources)
		if received == *out.TotalResults {
			return all, nil
		}
		if len(out.Resources) == 0 {
			return nil, fmt.Errorf("GitHub returned an empty SCIM page before totalResults")
		}
		startIndex = received + 1
	}
}

func (c *Client) SCIMGroup(ctx context.Context, enterprise, id string) (*github.SCIMEnterpriseGroupAttributes, error) {
	if err := validSCIMID(id); err != nil {
		return nil, err
	}
	g, _, err := c.api.Enterprise.GetProvisionedSCIMGroup(ctx, seg(enterprise), seg(id), nil)
	return scimGet(id, g, func(g *github.SCIMEnterpriseGroupAttributes) string { return scimID(g.ID) }, err, scimPath(enterprise, "Groups", id))
}

func (c *Client) SCIMGroups(ctx context.Context, enterprise, filter string) ([]*github.SCIMEnterpriseGroupAttributes, error) {
	path := scimPath(enterprise, "Groups", "")
	return scimList(func(start int) (scimPage[*github.SCIMEnterpriseGroupAttributes], error) {
		count := scimPageSize
		opts := &github.ListProvisionedSCIMGroupsEnterpriseOptions{StartIndex: &start, Count: &count}
		if filter != "" {
			opts.Filter = &filter
		}
		out, _, err := c.api.Enterprise.ListProvisionedSCIMGroups(ctx, seg(enterprise), opts)
		if err != nil {
			return scimPage[*github.SCIMEnterpriseGroupAttributes]{}, callError(http.MethodGet, path, err)
		}
		if out == nil {
			return scimPage[*github.SCIMEnterpriseGroupAttributes]{}, fmt.Errorf("GitHub returned an empty SCIM response")
		}
		return scimPage[*github.SCIMEnterpriseGroupAttributes]{out.Schemas, out.TotalResults, out.StartIndex, out.ItemsPerPage, out.Resources}, nil
	}, func(g *github.SCIMEnterpriseGroupAttributes) string {
		if g == nil {
			return ""
		}
		return scimID(g.ID)
	})
}

func (c *Client) SCIMUser(ctx context.Context, enterprise, id string) (*github.SCIMEnterpriseUserAttributes, error) {
	if err := validSCIMID(id); err != nil {
		return nil, err
	}
	u, _, err := c.api.Enterprise.GetProvisionedSCIMUser(ctx, seg(enterprise), seg(id))
	return scimGet(id, u, func(u *github.SCIMEnterpriseUserAttributes) string { return scimID(u.ID) }, err, scimPath(enterprise, "Users", id))
}

func (c *Client) SCIMUsers(ctx context.Context, enterprise, filter string) ([]*github.SCIMEnterpriseUserAttributes, error) {
	path := scimPath(enterprise, "Users", "")
	return scimList(func(start int) (scimPage[*github.SCIMEnterpriseUserAttributes], error) {
		count := scimPageSize
		opts := &github.ListProvisionedSCIMUsersEnterpriseOptions{StartIndex: &start, Count: &count}
		if filter != "" {
			opts.Filter = &filter
		}
		out, _, err := c.api.Enterprise.ListProvisionedSCIMUsers(ctx, seg(enterprise), opts)
		if err != nil {
			return scimPage[*github.SCIMEnterpriseUserAttributes]{}, callError(http.MethodGet, path, err)
		}
		if out == nil {
			return scimPage[*github.SCIMEnterpriseUserAttributes]{}, fmt.Errorf("GitHub returned an empty SCIM response")
		}
		return scimPage[*github.SCIMEnterpriseUserAttributes]{out.Schemas, out.TotalResults, out.StartIndex, out.ItemsPerPage, out.Resources}, nil
	}, func(u *github.SCIMEnterpriseUserAttributes) string {
		if u == nil {
			return ""
		}
		return scimID(u.ID)
	})
}
