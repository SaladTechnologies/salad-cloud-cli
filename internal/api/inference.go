// Managed inference endpoints (transcription etc.) are ORG-scoped — their
// paths have no project segment: /organizations/{org}/inference-endpoints.
// Same SDK-types-over-our-transport split as the rest of the client.
package api

import (
	"context"
	"fmt"

	"github.com/saladtechnologies/salad-cloud-sdk-go/pkg/inferenceendpoints"

	"github.com/saladtechnologies/salad-cloud-cli/internal/clierr"
)

// inferencePath builds /organizations/{org}/inference-endpoints[/...].
func (c *Client) inferencePath(parts ...string) string {
	p := fmt.Sprintf("/organizations/%s/inference-endpoints", seg(c.org))
	for _, part := range parts {
		p += "/" + seg(part)
	}
	return p
}

// inferencePageSize is the largest page the inference endpoints accept:
// page_size caps at 25 here.
const inferencePageSize = 25

// inferenceMaxPages bounds any pagination loop as a safety net against a
// server that keeps returning full pages (400 pages = 10k items).
const inferenceMaxPages = 400

// inferencePage renders the query string for one page.
func inferencePage(page int) string {
	return fmt.Sprintf("?page=%d&page_size=%d", page, inferencePageSize)
}

// ListInferenceEndpoints lists the managed inference endpoints available to
// the resolved organization, paging through the whole catalog.
func (c *Client) ListInferenceEndpoints(
	ctx context.Context,
) (*inferenceendpoints.InferenceEndpointCollection, *clierr.Error) {
	if err := c.RequireOrg(); err != nil {
		return nil, err
	}
	var all *inferenceendpoints.InferenceEndpointCollection
	for page := 1; page <= inferenceMaxPages; page++ {
		coll, cerr := restJSON[inferenceendpoints.InferenceEndpointCollection](
			ctx,
			c,
			"GET",
			c.inferencePath()+inferencePage(page),
			"",
			nil,
		)
		if cerr != nil {
			return nil, cerr
		}
		if all == nil {
			all = coll
		} else {
			all.Items = append(all.Items, coll.Items...)
		}
		// A short page is the last one; total_size is not trusted to end
		// the loop (it can be null or stale).
		if len(coll.Items) < inferencePageSize {
			break
		}
	}
	return all, nil
}

// InferenceJobsCap is the most jobs ListInferenceJobs returns with no limit:
// inferenceMaxPages full pages.
const InferenceJobsCap = inferenceMaxPages * inferencePageSize

// jobsWanted is how many jobs a listing returns for a --limit: the limit, or
// the cap for 0 (all) and for a limit above it.
func jobsWanted(limit, cap int) int {
	if limit <= 0 || limit > cap {
		return cap
	}
	return limit
}

// GetInferenceEndpoint fetches one managed inference endpoint by name.
func (c *Client) GetInferenceEndpoint(
	ctx context.Context,
	name string,
) (*inferenceendpoints.InferenceEndpoint, *clierr.Error) {
	if err := c.RequireOrg(); err != nil {
		return nil, err
	}
	return restJSON[inferenceendpoints.InferenceEndpoint](ctx, c, "GET", c.inferencePath(name), "", nil)
}

// ListInferenceJobs lists the organization's newest jobs on one inference
// endpoint, newest first: limit of them (0 means all, up to
// InferenceJobsCap). The API lists oldest first, so this reads from the
// last page backwards (newestPages); more reports older jobs left out.
func (c *Client) ListInferenceJobs(
	ctx context.Context,
	endpoint string,
	limit int,
) (jobs []inferenceendpoints.InferenceEndpointJob, more bool, cerr *clierr.Error) {
	if err := c.RequireOrg(); err != nil {
		return nil, false, err
	}
	fetch := func(page int) ([]inferenceendpoints.InferenceEndpointJob, *clierr.Error) {
		coll, cerr := restJSON[inferenceendpoints.InferenceEndpointJobCollection](
			ctx,
			c,
			"GET",
			c.inferencePath(endpoint, "jobs")+inferencePage(page),
			"",
			nil,
		)
		if cerr != nil {
			return nil, cerr
		}
		return coll.Items, nil
	}
	jobs, more, cerr = newestPages(fetch, inferencePageSize, jobsWanted(limit, InferenceJobsCap))
	if cerr != nil {
		return nil, false, cerr
	}
	sortNewestFirst(jobs, func(j *inferenceendpoints.InferenceEndpointJob) *string { return j.CreateTime })
	if jobs == nil {
		jobs = []inferenceendpoints.InferenceEndpointJob{}
	}
	return jobs, more, nil
}

// CreateInferenceJob queues a job on an inference endpoint.
func (c *Client) CreateInferenceJob(
	ctx context.Context,
	endpoint string,
	proto inferenceendpoints.InferenceEndpointJobPrototype,
) (*inferenceendpoints.InferenceEndpointJob, *clierr.Error) {
	if err := c.RequireOrg(); err != nil {
		return nil, err
	}
	return restJSON[inferenceendpoints.InferenceEndpointJob](
		ctx,
		c,
		"POST",
		c.inferencePath(endpoint, "jobs"),
		"",
		proto,
	)
}

// GetInferenceJob fetches one inference endpoint job by id.
func (c *Client) GetInferenceJob(
	ctx context.Context,
	endpoint, id string,
) (*inferenceendpoints.InferenceEndpointJob, *clierr.Error) {
	if err := c.RequireOrg(); err != nil {
		return nil, err
	}
	return restJSON[inferenceendpoints.InferenceEndpointJob](
		ctx,
		c,
		"GET",
		c.inferencePath(endpoint, "jobs", id),
		"",
		nil,
	)
}

// CancelInferenceJob cancels one inference endpoint job.
func (c *Client) CancelInferenceJob(ctx context.Context, endpoint, id string) *clierr.Error {
	if err := c.RequireOrg(); err != nil {
		return err
	}
	return restNoBody(ctx, c, "DELETE", c.inferencePath(endpoint, "jobs", id), nil)
}
