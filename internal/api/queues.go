// Job queues are project-scoped: /organizations/{org}/projects/{project}/queues.
// A queue job is the same {input, metadata?, webhook?} document as an
// inference job; its output is whatever the user's own container returns.
package api

import (
	"context"
	"fmt"

	"github.com/saladtechnologies/salad-cloud-sdk-go/pkg/queues"

	"github.com/saladtechnologies/salad-cloud-cli/internal/clierr"
)

// queueJobsPageSize is the largest page the queue jobs listing accepts: the
// API rejects page_size above 25 with a 400, as on inference endpoints.
const queueJobsPageSize = 25

// queueJobsMaxPages bounds the listing loop (400 pages = 10k jobs).
const queueJobsMaxPages = 400

// queuePath builds /organizations/{org}/projects/{project}/queues[/...].
func (c *Client) queuePath(parts ...string) string {
	p := fmt.Sprintf("/organizations/%s/projects/%s/queues", seg(c.org), seg(c.project))
	for _, part := range parts {
		p += "/" + seg(part)
	}
	return p
}

// ListQueues lists the project's queues.
func (c *Client) ListQueues(ctx context.Context) (*queues.QueueCollection, *clierr.Error) {
	if err := c.requireOrgProject(); err != nil {
		return nil, err
	}
	return restJSON[queues.QueueCollection](ctx, c, "GET", c.queuePath(), "", nil)
}

// GetQueue fetches one queue by name.
func (c *Client) GetQueue(ctx context.Context, name string) (*queues.Queue, *clierr.Error) {
	if err := c.requireOrgProject(); err != nil {
		return nil, err
	}
	return restJSON[queues.Queue](ctx, c, "GET", c.queuePath(name), "", nil)
}

// CreateQueue creates a queue.
func (c *Client) CreateQueue(ctx context.Context, proto queues.QueuePrototype) (*queues.Queue, *clierr.Error) {
	if err := c.requireOrgProject(); err != nil {
		return nil, err
	}
	return restJSON[queues.Queue](ctx, c, "POST", c.queuePath(), "", proto)
}

// UpdateQueue merge-patches a queue's display name or description.
func (c *Client) UpdateQueue(ctx context.Context, name string, patch queues.QueuePatch) (*queues.Queue, *clierr.Error) {
	if err := c.requireOrgProject(); err != nil {
		return nil, err
	}
	return restJSON[queues.Queue](ctx, c, "PATCH", c.queuePath(name), "application/merge-patch+json", patch)
}

// DeleteQueue deletes a queue.
func (c *Client) DeleteQueue(ctx context.Context, name string) *clierr.Error {
	if err := c.requireOrgProject(); err != nil {
		return err
	}
	return restNoBody(ctx, c, "DELETE", c.queuePath(name), nil)
}

// QueueJobsCap is the most jobs ListQueueJobs reads: queueJobsMaxPages full
// pages.
const QueueJobsCap = queueJobsMaxPages * queueJobsPageSize

// QueueJobsBudget is how many jobs ListQueueJobs reads when a limit is set:
// 40 pages, or the limit's worth of pages when that is more.
const QueueJobsBudget = 40 * queueJobsPageSize

// ListQueueJobs lists a queue's newest jobs, newest first: limit of them (0
// means all, up to QueueJobsCap). The API lists queue jobs in job ID order,
// so no page holds the newest: the pages are read and the jobs sorted by
// creation time. With a limit, reading stops after QueueJobsBudget jobs (or
// the limit, if larger), which keeps one listing from spending the key's
// request allowance; partial then reports that the queue holds more than
// was read, so the newest are the newest of those read only. more reports
// jobs read but left out by the limit or the cap.
func (c *Client) ListQueueJobs(
	ctx context.Context,
	queue string,
	limit int,
) (jobs []queues.QueueJob, more, partial bool, cerr *clierr.Error) {
	if err := c.requireOrgProject(); err != nil {
		return nil, false, false, err
	}
	maxPages := queueJobsMaxPages
	if limit > 0 {
		maxPages = (max(limit, QueueJobsBudget) + queueJobsPageSize - 1) / queueJobsPageSize
		maxPages = min(maxPages, queueJobsMaxPages)
	}
	jobs = []queues.QueueJob{}
	// One page at a time: reading several at once gains nothing, because the
	// API answers one key's concurrent page reads one after another.
	for page := 1; page <= maxPages; page++ {
		path := c.queuePath(queue, "jobs") + fmt.Sprintf("?page=%d&page_size=%d", page, queueJobsPageSize)
		coll, cerr := restJSON[queues.QueueJobCollection](ctx, c, "GET", path, "", nil)
		if cerr != nil {
			return nil, false, false, cerr
		}
		jobs = append(jobs, coll.Items...)
		if len(coll.Items) < queueJobsPageSize {
			break
		}
		if page == maxPages { // a full last page: there may be more
			if maxPages < queueJobsMaxPages {
				partial = true
			} else {
				more = true
			}
		}
	}
	sortNewestFirst(jobs, func(j *queues.QueueJob) *string { return j.CreateTime })
	if want := jobsWanted(limit, QueueJobsCap); len(jobs) > want {
		jobs, more = jobs[:want], true
	}
	return jobs, more, partial, nil
}

// CreateQueueJob submits a job to a queue.
func (c *Client) CreateQueueJob(
	ctx context.Context,
	queue string,
	proto queues.QueueJobPrototype,
) (*queues.QueueJob, *clierr.Error) {
	if err := c.requireOrgProject(); err != nil {
		return nil, err
	}
	return restJSON[queues.QueueJob](ctx, c, "POST", c.queuePath(queue, "jobs"), "", proto)
}

// GetQueueJob fetches one queue job.
func (c *Client) GetQueueJob(ctx context.Context, queue, id string) (*queues.QueueJob, *clierr.Error) {
	if err := c.requireOrgProject(); err != nil {
		return nil, err
	}
	return restJSON[queues.QueueJob](ctx, c, "GET", c.queuePath(queue, "jobs", id), "", nil)
}

// CancelQueueJob cancels a queue job; only a job not yet handed to an
// instance can be cancelled.
func (c *Client) CancelQueueJob(ctx context.Context, queue, id string) *clierr.Error {
	if err := c.requireOrgProject(); err != nil {
		return err
	}
	return restNoBody(ctx, c, "DELETE", c.queuePath(queue, "jobs", id), nil)
}
