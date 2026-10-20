package api

import (
	"context"
	"net/http"
	"net/url"
	"strconv"

	"github.com/saladtechnologies/salad-cloud-cli/internal/clierr"
)

// Organization is one row of the organizations listing. The endpoint is
// not part of the documented API, so the row is modeled tolerantly: every
// field is a pointer, and a field the endpoint did not send renders null
// rather than a zero value.
type Organization struct {
	OrganizationName *string `json:"organization_name"`
	DisplayName      *string `json:"display_name"`
	OrganizationID   *string `json:"organization_id"`
	CreateTime       *string `json:"create_time"`
	UpdateTime       *string `json:"update_time"`
}

// OrganizationList is the organizations listing response.
type OrganizationList struct {
	Items      []Organization `json:"items"`
	TotalCount *int64         `json:"total_count"`

	// noItems is a first page that sent no items array at all: the
	// endpoint changed shape, which is "unknown", not "no organizations".
	noItems bool
}

// organizationPage is one page of the organizations listing.
type organizationPage struct {
	OrganizationList

	NextPageToken *string `json:"next_page_token"`
}

// orgPageSize is the most organizations the listing returns per page;
// maxOrgPages stops a listing whose tokens never run out.
const (
	orgPageSize = 100
	maxOrgPages = 100
)

// ListOrganizations lists the organizations the API key's account belongs
// to (GET /api/public/v1/organizations), every page of them, in the order
// they were created. total_count is the first page's. The endpoint is not
// part of the documented API, so callers treat any failure as "unknown",
// never as "none".
func (c *Client) ListOrganizations(ctx context.Context) (*OrganizationList, *clierr.Error) {
	all := OrganizationList{Items: make([]Organization, 0, orgPageSize), TotalCount: nil, noItems: false}
	token := ""
	seen := make(map[string]bool)
	for page := range maxOrgPages {
		q := url.Values{"page_size": {strconv.Itoa(orgPageSize)}}
		if token != "" {
			q.Set("page_token", token)
		}
		p, err := restJSON[organizationPage](ctx, c, "GET", "/v1/organizations?"+q.Encode(), "", nil)
		if err != nil {
			return nil, err
		}
		if page == 0 {
			all.TotalCount = p.TotalCount
			all.noItems = p.Items == nil
		}
		all.Items = append(all.Items, p.Items...)
		if p.NextPageToken == nil || *p.NextPageToken == "" || seen[*p.NextPageToken] {
			break
		}
		token = *p.NextPageToken
		seen[token] = true
	}
	return &all, nil
}

// OrganizationListing reads the listing the way setup uses it (login,
// whoami, doctor): the list when the endpoint answers with names; nil and no
// error when it cannot list (a 403 or 404, or rows without names), which is
// "unknown", never "none", and leaves the organization check to decide; a
// 401's error, which means the API key is invalid; any other failure (rate
// limiting, a server error, the network) as is.
func (c *Client) OrganizationListing(ctx context.Context) (*OrganizationList, *clierr.Error) {
	list, err := c.ListOrganizations(ctx)
	if err != nil {
		if s := err.HTTPStatus; s != nil && (*s == http.StatusForbidden || *s == http.StatusNotFound) {
			return nil, nil
		}
		return nil, err
	}
	if !list.Usable() {
		return nil, nil
	}
	return list, nil
}

// Usable reports whether the listing sent its items and every listed row
// carries an organization name. A 200 without items, or with nameless
// rows, means the endpoint changed shape, which is
// "unknown", not "no organizations".
func (l *OrganizationList) Usable() bool {
	if l.noItems {
		return false
	}
	for _, o := range l.Items {
		if o.OrganizationName == nil || *o.OrganizationName == "" {
			return false
		}
	}
	return true
}

// OrgNames returns the org names from a listing, for compact display.
func (l *OrganizationList) OrgNames() []string {
	names := make([]string, 0, len(l.Items))
	for _, o := range l.Items {
		if o.OrganizationName != nil && *o.OrganizationName != "" {
			names = append(names, *o.OrganizationName)
		}
	}
	return names
}
