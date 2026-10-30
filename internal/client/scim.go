package client

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
)

const (
	scimAccept     = "application/scim+json"
	scimListSchema = "urn:ietf:params:scim:api:messages:2.0:ListResponse"
	scimPageSize   = 100
)

// SCIMGroup keeps the opaque SCIM identity and only the attributes the provider
// observes. Omitted attributes remain nil so callers can tell absence from zero.
type SCIMGroup struct {
	ID          string  `json:"id"`
	ExternalID  *string `json:"externalId,omitempty"`
	DisplayName *string `json:"displayName,omitempty"`
}

type SCIMUser struct {
	ID          string  `json:"id"`
	ExternalID  *string `json:"externalId,omitempty"`
	DisplayName *string `json:"displayName,omitempty"`
	UserName    *string `json:"userName,omitempty"`
	Active      *bool   `json:"active,omitempty"`
}

type scimListResponse[T any] struct {
	Schemas      []string `json:"schemas"`
	TotalResults *int     `json:"totalResults"`
	StartIndex   *int     `json:"startIndex"`
	ItemsPerPage *int     `json:"itemsPerPage"`
	Resources    []T      `json:"Resources"`
}

func scimPath(enterprise, resource, id string) string {
	p := "scim/v2/enterprises/" + url.PathEscape(enterprise) + "/" + resource
	if id != "" {
		p += "/" + url.PathEscape(id)
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

func scimGet[T any](ctx context.Context, c *Client, enterprise, resource, id string, idOf func(T) string) (*T, error) {
	if err := validSCIMID(id); err != nil {
		return nil, err
	}
	var out T
	if _, err := c.requestAccept(ctx, http.MethodGet, scimPath(enterprise, resource, id), scimAccept, nil, &out); err != nil {
		return nil, err
	}
	got := idOf(out)
	if got != id {
		if got == "" {
			return nil, fmt.Errorf("GitHub returned a SCIM resource without its identity")
		}
		return nil, fmt.Errorf("SCIM resource identity changed during lookup; retry the operation")
	}
	return &out, nil
}

// scimList pages from startIndex 1 until totalResults is reached, advancing by
// the number of Resources actually returned. Link and meta references are ignored.
func scimList[T any](ctx context.Context, c *Client, enterprise, resource, filter string, idOf func(T) string) ([]T, error) {
	all := make([]T, 0)
	seen := map[string]bool{}
	startIndex := 1
	for {
		q := url.Values{}
		if filter != "" {
			q.Set("filter", filter)
		}
		q.Set("startIndex", strconv.Itoa(startIndex))
		q.Set("count", strconv.Itoa(scimPageSize))
		var out scimListResponse[T]
		if _, err := c.requestAccept(ctx, http.MethodGet, scimPath(enterprise, resource, "")+"?"+q.Encode(), scimAccept, nil, &out); err != nil {
			return nil, err
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

func (c *Client) SCIMGroup(ctx context.Context, enterprise, id string) (*SCIMGroup, error) {
	return scimGet(ctx, c, enterprise, "Groups", id, func(g SCIMGroup) string { return g.ID })
}

func (c *Client) SCIMGroups(ctx context.Context, enterprise, filter string) ([]SCIMGroup, error) {
	return scimList(ctx, c, enterprise, "Groups", filter, func(g SCIMGroup) string { return g.ID })
}

func (c *Client) SCIMUser(ctx context.Context, enterprise, id string) (*SCIMUser, error) {
	return scimGet(ctx, c, enterprise, "Users", id, func(u SCIMUser) string { return u.ID })
}

func (c *Client) SCIMUsers(ctx context.Context, enterprise, filter string) ([]SCIMUser, error) {
	return scimList(ctx, c, enterprise, "Users", filter, func(u SCIMUser) string { return u.ID })
}
