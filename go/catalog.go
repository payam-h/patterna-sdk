package patterna

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// DeleteItem removes an item from the catalog for good (engine 0.2:
// DELETE /v1/catalog/items/{external_id}?kind=). Archive with SetItemStatus when you may want it
// back. Deleting an item that does not exist answers ErrUnknownItem.
func (c *Client) DeleteItem(ctx context.Context, kind ItemKind, externalID string, opts ...CallOption) error {
	const op = "deleteItem"
	if externalID == "" {
		return newError(CodeInvalidArgument, op, "external id is required")
	}
	if !kind.Valid() {
		return newError(CodeInvalidArgument, op, "unknown kind %q", string(kind))
	}
	return c.do(ctx, call{
		op: op, method: http.MethodDelete, path: "/catalog/items/" + url.PathEscape(externalID),
		query: url.Values{"kind": {string(kind)}},
		scope: scopeIngest, timeout: c.cfg.IngestTimeout, idempotent: true, opts: collect(opts),
	}, nil)
}

// ListItems returns one page of the catalog. Follow ItemPage.NextCursor, or use
// ItemsChangedSince.
func (c *Client) ListItems(ctx context.Context, p ListItemsParams, opts ...CallOption) (*ItemPage, error) {
	const op = "listItems"
	if p.Limit < 0 || p.Limit > maxItems {
		return nil, newError(CodeInvalidArgument, op, "limit must be between 1 and %d", maxItems)
	}
	q := url.Values{}
	if p.Kind != "" {
		q.Set("kind", string(p.Kind))
	}
	if p.Status != "" {
		q.Set("status", string(p.Status))
	}
	if !p.ChangedSince.IsZero() {
		q.Set("changed_since", p.ChangedSince.UTC().Format(time.RFC3339Nano))
	}
	if p.Cursor != "" {
		q.Set("cursor", p.Cursor)
	}
	if p.Limit > 0 {
		q.Set("limit", strconv.Itoa(p.Limit))
	}
	var out ItemPage
	err := c.do(ctx, call{
		op: op, method: http.MethodGet, path: "/catalog/items", query: q,
		scope: scopeIngest, timeout: c.cfg.IngestTimeout, idempotent: true, opts: collect(opts),
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// ItemsChangedSince calls fn for every item changed after since (engine 0.2), page by page, so a
// nightly job can reconcile Patterna's catalog with yours. kind may be empty for all kinds. It
// stops at the first error from fn or the API. Each page has its own IngestTimeout.
func (c *Client) ItemsChangedSince(ctx context.Context, since time.Time, kind ItemKind, fn func(Item) error, opts ...CallOption) error {
	if since.IsZero() {
		return newError(CodeInvalidArgument, "listItems", "since is required")
	}
	p := ListItemsParams{Kind: kind, ChangedSince: since, Limit: maxItems}
	for {
		page, err := c.ListItems(ctx, p, opts...)
		if err != nil {
			return err
		}
		for _, it := range page.Items {
			if err := fn(it); err != nil {
				return err
			}
		}
		if page.NextCursor == "" || len(page.Items) == 0 {
			return nil
		}
		p.Cursor = page.NextCursor
	}
}

// SigningSecret is the answer to CreateSigningSecret. The secret is shown once.
type SigningSecret struct {
	// SigningSecret is the string to sign with. Pass it to SignEvent as it is: the SDKs sign with
	// its bytes, never with a hex-decoded form.
	SigningSecret string `json:"signing_secret"`
	Note          string `json:"note,omitempty"`
}

// CreateSigningSecret issues the tenant's first event signing secret (engine 0.2:
// POST /v1/settings/signing-secret, admin key). Store it in your secret manager; it is not shown
// again. When one exists already the engine answers 409; rotate it in the panel or with
// admin.Client.RotateSigningSecret.
func (c *Client) CreateSigningSecret(ctx context.Context, opts ...CallOption) (*SigningSecret, error) {
	var out SigningSecret
	err := c.do(ctx, call{
		op: "createSigningSecret", method: http.MethodPost, path: "/settings/signing-secret",
		scope: scopeAdmin, timeout: c.cfg.AdminTimeout, idempotent: false, opts: collect(opts),
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// AdminRequest is one admin-scope call for AdminCall.
type AdminRequest struct {
	Op     string // name for errors and logs, e.g. "activateFlow"
	Method string
	Path   string // below the base URL, e.g. "/surfaces/home/flows"
	Query  url.Values
	Body   any
	// Idempotent calls are retried once on 5xx and network errors.
	Idempotent bool
}

// AdminCall runs an admin-scope request with the admin key, through the admin breaker and the
// AdminTimeout, decoding a 2xx JSON answer into out (which may be nil). It is the transport of
// the admin package and an escape hatch for admin endpoints the SDK does not wrap.
func (c *Client) AdminCall(ctx context.Context, r AdminRequest, out any, opts ...CallOption) error {
	if r.Op == "" {
		r.Op = "admin"
	}
	if r.Method == "" || r.Path == "" || r.Path[0] != '/' {
		return newError(CodeInvalidArgument, r.Op, "method and a path starting with / are required")
	}
	return c.do(ctx, call{
		op: r.Op, method: r.Method, path: r.Path, query: r.Query, body: r.Body,
		scope: scopeAdmin, timeout: c.cfg.AdminTimeout, idempotent: r.Idempotent, opts: collect(opts),
	}, out)
}
