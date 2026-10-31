package client

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
)

// Budget is one entry of the billing budgets API. The SDK type omits user,
// expires_at and consumed_amount, and its request structs omit user and
// expires_at, so the provider keeps its own wire types.
type Budget struct {
	ID                  string
	Scope               string
	EntityName          string
	User                string
	Amount              int64
	PreventFurtherUsage bool
	Type                string
	SKU                 string
	WillAlert           bool
	AlertRecipients     []string
	ExpiresAt           string
	ConsumedAmount      float64
}

// BudgetInput carries the writable fields. Amount is a whole number of dollars
// (or of licenses for license-based products), which is what the API accepts.
type BudgetInput struct {
	Scope               string
	EntityName          string
	User                string
	Amount              int64
	PreventFurtherUsage bool
	Type                string
	SKU                 string
	WillAlert           bool
	AlertRecipients     []string
	ExpiresAt           string
}

type budgetAlerting struct {
	WillAlert       bool     `json:"will_alert"`
	AlertRecipients []string `json:"alert_recipients"`
}

type budgetJSON struct {
	ID                  string          `json:"id,omitempty"`
	Scope               string          `json:"budget_scope,omitempty"`
	EntityName          string          `json:"budget_entity_name,omitempty"`
	User                string          `json:"user,omitempty"`
	Amount              *int64          `json:"budget_amount,omitempty"`
	PreventFurtherUsage *bool           `json:"prevent_further_usage,omitempty"`
	Type                string          `json:"budget_type,omitempty"`
	SKU                 string          `json:"budget_product_sku,omitempty"`
	Alerting            *budgetAlerting `json:"budget_alerting,omitempty"`
	ExpiresAt           string          `json:"expires_at,omitempty"`
	ConsumedAmount      float64         `json:"consumed_amount,omitempty"`
}

func (b *budgetJSON) budget() *Budget {
	if b == nil {
		return nil
	}
	out := &Budget{
		ID:             b.ID,
		Scope:          b.Scope,
		EntityName:     b.EntityName,
		User:           b.User,
		Type:           b.Type,
		SKU:            b.SKU,
		ExpiresAt:      b.ExpiresAt,
		ConsumedAmount: b.ConsumedAmount,
	}
	if b.Amount != nil {
		out.Amount = *b.Amount
	}
	if b.PreventFurtherUsage != nil {
		out.PreventFurtherUsage = *b.PreventFurtherUsage
	}
	if b.Alerting != nil {
		out.WillAlert = b.Alerting.WillAlert
		out.AlertRecipients = b.Alerting.AlertRecipients
	}
	return out
}

func budgetPath(enterprise, id string) string {
	p := "enterprises/" + seg(enterprise) + "/settings/billing/budgets"
	if id != "" {
		p += "/" + seg(id)
	}
	return p
}

// Budgets lists every budget, following has_next_page until the server stops.
func (c *Client) Budgets(ctx context.Context, enterprise string) ([]*Budget, error) {
	path := budgetPath(enterprise, "")
	query := url.Values{"per_page": {strconv.Itoa(100)}}
	var all []*Budget
	for page := 1; page <= maxPages; page++ {
		query.Set("page", strconv.Itoa(page))
		var out struct {
			Budgets     []*budgetJSON `json:"budgets"`
			HasNextPage bool          `json:"has_next_page"`
		}
		if _, err := c.request(ctx, http.MethodGet, path+"?"+query.Encode(), nil, &out); err != nil {
			return nil, err
		}
		if out.Budgets == nil {
			return nil, fmt.Errorf("GitHub returned an empty budget list")
		}
		for _, b := range out.Budgets {
			if b == nil || b.ID == "" {
				return nil, fmt.Errorf("GitHub returned a budget without its ID")
			}
			all = append(all, b.budget())
		}
		if !out.HasNextPage {
			return all, nil
		}
	}
	return nil, fmt.Errorf("GitHub returned more than %d budget pages; refusing to continue", maxPages)
}

func (c *Client) Budget(ctx context.Context, enterprise, id string) (*Budget, error) {
	var out budgetJSON
	if _, err := c.request(ctx, http.MethodGet, budgetPath(enterprise, id), nil, &out); err != nil {
		return nil, err
	}
	if out.ID != id {
		return nil, fmt.Errorf("GitHub returned budget %q when %q was requested", out.ID, id)
	}
	return out.budget(), nil
}

// CreateBudget sends a raw body because the SDK request struct cannot carry the
// user field that user-scoped budgets require.
// shortcut: use EnterpriseCreateBudget when it gains user and expires_at.
func (c *Client) CreateBudget(ctx context.Context, enterprise string, in BudgetInput) (*Budget, error) {
	var out struct {
		Budget *budgetJSON `json:"budget"`
	}
	if _, err := c.request(ctx, http.MethodPost, budgetPath(enterprise, ""), budgetBody(in), &out); err != nil {
		return nil, err
	}
	if out.Budget == nil || out.Budget.ID == "" {
		return nil, fmt.Errorf("GitHub returned no budget when creating one")
	}
	return out.Budget.budget(), nil
}

// UpdateBudget cannot change scope, entity, type or SKU: the resource replaces
// the budget for those, so the body only carries the mutable fields.
func (c *Client) UpdateBudget(ctx context.Context, enterprise, id string, in BudgetInput) (*Budget, error) {
	var out struct {
		Budget *budgetJSON `json:"budget"`
	}
	body := map[string]any{
		"budget_amount":         in.Amount,
		"prevent_further_usage": in.PreventFurtherUsage,
		"budget_alerting":       budgetAlerting{WillAlert: in.WillAlert, AlertRecipients: recipients(in.AlertRecipients)},
	}
	if _, err := c.request(ctx, http.MethodPatch, budgetPath(enterprise, id), body, &out); err != nil {
		return nil, err
	}
	if out.Budget == nil || out.Budget.ID != id {
		return nil, fmt.Errorf("GitHub did not confirm the update of budget %q", id)
	}
	return out.Budget.budget(), nil
}

func (c *Client) DeleteBudget(ctx context.Context, enterprise, id string) error {
	var out struct {
		ID string `json:"id"`
	}
	if _, err := c.request(ctx, http.MethodDelete, budgetPath(enterprise, id), nil, &out); err != nil {
		return err
	}
	if out.ID != id {
		return fmt.Errorf("GitHub did not confirm the deletion of budget %q", id)
	}
	return nil
}

func budgetBody(in BudgetInput) map[string]any {
	body := map[string]any{
		"budget_amount":         in.Amount,
		"prevent_further_usage": in.PreventFurtherUsage,
		"budget_scope":          in.Scope,
		"budget_type":           in.Type,
		"budget_alerting":       budgetAlerting{WillAlert: in.WillAlert, AlertRecipients: recipients(in.AlertRecipients)},
	}
	if in.EntityName != "" {
		body["budget_entity_name"] = in.EntityName
	}
	if in.User != "" {
		body["user"] = in.User
	}
	if in.SKU != "" {
		body["budget_product_sku"] = in.SKU
	}
	if in.ExpiresAt != "" {
		body["expires_at"] = in.ExpiresAt
	}
	return body
}

// recipients keeps the API's required array non-null when nothing is configured.
func recipients(list []string) []string {
	if list == nil {
		return []string{}
	}
	return list
}
