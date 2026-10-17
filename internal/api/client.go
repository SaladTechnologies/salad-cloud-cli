// Package api exposes the SaladCloud Public API behind intent-named methods
// that return the CLI's structured errors and apply org/project defaults.
// SDK packages supply the codegen'd request/response types; the HTTP
// transport is the CLI's own (see rest.go for why).
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/saladtechnologies/salad-cloud-sdk-go/pkg/containergroups"
	"github.com/saladtechnologies/salad-cloud-sdk-go/pkg/logs"
	"github.com/saladtechnologies/salad-cloud-sdk-go/pkg/organizationdata"
	"github.com/saladtechnologies/salad-cloud-sdk-go/pkg/quotas"
	"github.com/saladtechnologies/salad-cloud-sdk-go/pkg/shared"
	"github.com/saladtechnologies/salad-cloud-sdk-go/pkg/webhooksecretkey"

	"github.com/saladtechnologies/salad-cloud-cli/internal/clierr"
	"github.com/saladtechnologies/salad-cloud-cli/internal/config"
	"github.com/saladtechnologies/salad-cloud-cli/internal/version"
)

// UserAgent identifies the CLI on every request it makes: the API, the AI
// Gateway, and S4.
var UserAgent = fmt.Sprintf("saladcloud/%s (%s/%s)", version.Version, runtime.GOOS, runtime.GOARCH)

// DefaultBaseURL is the production Public API base URL.
const DefaultBaseURL = "https://api.salad.com/api/public"

// Client is the CLI's API client: resolved credentials and context.
type Client struct {
	org     string
	project string
	apiKey  string
	baseURL string

	// projectAssumed: the project is the "default" fallback, not set by a
	// flag, the environment, or the config file.
	projectAssumed bool
	// OnAssumedProject, when set, runs once, before the first request that
	// uses an assumed project; the CLI prints its warning there.
	OnAssumedProject func()
	assumedNoted     sync.Once
	// checkingProject guards the check assumedProjectMissing makes, which
	// would otherwise recurse through restJSON.
	checkingProject atomic.Bool

	// OnRejected, when set, builds the error for a 401: the CLI names the
	// key and where it came from, which the client does not know.
	OnRejected func() *clierr.Error
	// OnOrgGone, when set, builds the error for an organization the key
	// cannot see: after a 404 under the organization the client checks it
	// once, and a 404 there is this error.
	OnOrgGone   func() *clierr.Error
	checkingOrg atomic.Bool
}

// New builds a Client from resolved configuration. It requires an API key
// (CodeAuth error otherwise) but does NOT require org/project — some
// commands don't need them; use RequireOrg/RequireProject in those that do.
// The base URL can be overridden via the SALAD_CLI_API_BASE_URL environment
// variable (for testing).
func New(res config.Resolved) (*Client, *clierr.Error) {
	if res.APIKey == "" {
		return nil, clierr.New(clierr.CodeAuth, clierr.KindAuth, "no API key set").
			WithHint("run saladcloud login in a terminal, or set SALAD_API_KEY")
	}
	baseURL := DefaultBaseURL
	if override := os.Getenv("SALAD_CLI_API_BASE_URL"); override != "" {
		baseURL = override
	}
	return &Client{
		org:            res.Organization,
		project:        res.Project,
		apiKey:         res.APIKey,
		baseURL:        baseURL,
		projectAssumed: res.ProjectSource == config.SourceDefault && res.Project == config.DefaultProject,
	}, nil
}

// AssumedProjectMissing is the error for a project that defaulted to
// "default" because none was set, in an organization that has no project
// by that name.
func AssumedProjectMissing(org string) *clierr.Error {
	return clierr.New(clierr.CodeUsage, clierr.KindUsage, `no valid project set: %s has no "default" project`, org).
		WithHint("create one at https://portal.salad.com/organizations/%s/projects/new, then pass --project or run saladcloud use %s/<project>", org, org)
}

// RequireOrg returns a usage error if no organization is set.
func (c *Client) RequireOrg() *clierr.Error {
	if c.org == "" {
		return clierr.New(clierr.CodeUsage, clierr.KindUsage, "no organization set").
			WithHint("pass --org, set SALAD_ORGANIZATION, or run saladcloud use <org>")
	}
	return nil
}

// RequireProject returns a usage error if no project is set.
func (c *Client) RequireProject() *clierr.Error {
	if c.project == "" {
		return clierr.New(clierr.CodeUsage, clierr.KindUsage, "no project set").
			WithHint("pass --project, set SALAD_PROJECT, or run saladcloud use <org>/<project>")
	}
	return nil
}

// Org returns the resolved organization name ("" if unset).
func (c *Client) Org() string { return c.org }

// Project returns the resolved project name ("" if unset).
func (c *Client) Project() string { return c.project }

// APIKey returns the resolved API key.
func (c *Client) APIKey() string { return c.apiKey }

// BaseURL returns the API base URL in effect.
func (c *Client) BaseURL() string { return c.baseURL }

// ListGroups lists the container groups in the resolved org/project.
func (c *Client) ListGroups(ctx context.Context) (*containergroups.ContainerGroupCollection, *clierr.Error) {
	if err := c.requireOrgProject(); err != nil {
		return nil, err
	}
	return restJSON[containergroups.ContainerGroupCollection](ctx, c, "GET", c.groupPath(), "", nil)
}

// GetGroup fetches one container group by name.
func (c *Client) GetGroup(ctx context.Context, name string) (*shared.ContainerGroup, *clierr.Error) {
	if err := c.requireOrgProject(); err != nil {
		return nil, err
	}
	return restJSON[shared.ContainerGroup](ctx, c, "GET", c.groupPath(name), "", nil)
}

// CreateGroup creates a container group.
func (c *Client) CreateGroup(
	ctx context.Context,
	req containergroups.ContainerGroupCreationRequest,
) (*shared.ContainerGroup, *clierr.Error) {
	if err := c.requireOrgProject(); err != nil {
		return nil, err
	}
	return restJSON[shared.ContainerGroup](ctx, c, "POST", c.groupPath(), "", req)
}

// CreateGroupRaw creates a container group from the caller's JSON document
// sent byte-for-byte. It exists for create --allow-unknown-fields, where the
// outgoing request must carry fields the SDK's typed struct would drop.
func (c *Client) CreateGroupRaw(ctx context.Context, req json.RawMessage) (*shared.ContainerGroup, *clierr.Error) {
	if err := c.requireOrgProject(); err != nil {
		return nil, err
	}
	return restJSON[shared.ContainerGroup](ctx, c, "POST", c.groupPath(), "", req)
}

// UpdateGroup patches a container group.
func (c *Client) UpdateGroup(
	ctx context.Context,
	name string,
	patch containergroups.ContainerGroupPatch,
) (*shared.ContainerGroup, *clierr.Error) {
	if err := c.requireOrgProject(); err != nil {
		return nil, err
	}
	return restJSON[shared.ContainerGroup](ctx, c, "PATCH", c.groupPath(name), "application/merge-patch+json", patch)
}

// UpdateGroupRaw patches a container group with the caller's JSON document
// sent byte-for-byte (json.RawMessage marshals to itself in restJSON).
// UpdateGroup re-marshals through the SDK's omitempty-tagged patch struct,
// which drops explicit empty values ([], "", 0) — in merge-patch semantics
// those mean "clear this field", so file-driven updates must use this
// instead and keep the typed struct for validation only.
func (c *Client) UpdateGroupRaw(
	ctx context.Context,
	name string,
	patch json.RawMessage,
) (*shared.ContainerGroup, *clierr.Error) {
	if err := c.requireOrgProject(); err != nil {
		return nil, err
	}
	// A typed nil/empty RawMessage would marshal to "null" (or send nothing);
	// so would an empty YAML document converted to JSON. And a keyless object
	// ({}) is a merge patch that changes nothing — the API rejects all of
	// these with a bare 400, so fail fast with a friendly message instead.
	trimmed := bytes.TrimSpace(patch)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, clierr.New(clierr.CodeInvalidInput, clierr.KindInvalidInput, "empty patch document")
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &keys); err == nil && len(keys) == 0 {
		return nil, clierr.New(clierr.CodeInvalidInput, clierr.KindInvalidInput, "empty patch document")
	}
	return restJSON[shared.ContainerGroup](ctx, c, "PATCH", c.groupPath(name), "application/merge-patch+json", patch)
}

// DeleteGroup deletes a container group.
func (c *Client) DeleteGroup(ctx context.Context, name string) *clierr.Error {
	if err := c.requireOrgProject(); err != nil {
		return err
	}
	return restNoBody(ctx, c, "DELETE", c.groupPath(name))
}

// StartGroup starts a container group.
func (c *Client) StartGroup(ctx context.Context, name string) *clierr.Error {
	if err := c.requireOrgProject(); err != nil {
		return err
	}
	return restNoBody(ctx, c, "POST", c.groupPath(name, "start"))
}

// StopGroup stops a container group.
func (c *Client) StopGroup(ctx context.Context, name string) *clierr.Error {
	if err := c.requireOrgProject(); err != nil {
		return err
	}
	return restNoBody(ctx, c, "POST", c.groupPath(name, "stop"))
}

// ListInstances lists the instances of a container group.
func (c *Client) ListInstances(
	ctx context.Context,
	group string,
) (*containergroups.ContainerGroupInstanceCollection, *clierr.Error) {
	if err := c.requireOrgProject(); err != nil {
		return nil, err
	}
	return restJSON[containergroups.ContainerGroupInstanceCollection](
		ctx,
		c,
		"GET",
		c.groupPath(group, "instances"),
		"",
		nil,
	)
}

// GetInstance fetches one container group instance by id.
func (c *Client) GetInstance(
	ctx context.Context,
	group, id string,
) (*containergroups.ContainerGroupInstance, *clierr.Error) {
	if err := c.requireOrgProject(); err != nil {
		return nil, err
	}
	return restJSON[containergroups.ContainerGroupInstance](ctx, c, "GET", c.groupPath(group, "instances", id), "", nil)
}

// UpdateInstance merge-patches one container group instance; deletion_cost
// is the only writable field.
func (c *Client) UpdateInstance(
	ctx context.Context,
	group, id string,
	patch containergroups.ContainerGroupInstancePatch,
) (*containergroups.ContainerGroupInstance, *clierr.Error) {
	if err := c.requireOrgProject(); err != nil {
		return nil, err
	}
	return restJSON[containergroups.ContainerGroupInstance](
		ctx,
		c,
		"PATCH",
		c.groupPath(group, "instances", id),
		"application/merge-patch+json",
		patch,
	)
}

// RestartInstance restarts a container group instance.
func (c *Client) RestartInstance(ctx context.Context, group, id string) *clierr.Error {
	if err := c.requireOrgProject(); err != nil {
		return err
	}
	return restNoBody(ctx, c, "POST", c.groupPath(group, "instances", id, "restart"))
}

// RecreateInstance recreates a container group instance.
func (c *Client) RecreateInstance(ctx context.Context, group, id string) *clierr.Error {
	if err := c.requireOrgProject(); err != nil {
		return err
	}
	return restNoBody(ctx, c, "POST", c.groupPath(group, "instances", id, "recreate"))
}

// ReallocateInstance reallocates a container group instance to another node.
func (c *Client) ReallocateInstance(ctx context.Context, group, id string) *clierr.Error {
	if err := c.requireOrgProject(); err != nil {
		return err
	}
	return restNoBody(ctx, c, "POST", c.groupPath(group, "instances", id, "reallocate"))
}

// QueryLogs queries log entries for the resolved organization.
func (c *Client) QueryLogs(ctx context.Context, q logs.LogEntryQuery) (*logs.LogEntryCollection, *clierr.Error) {
	if err := c.RequireOrg(); err != nil {
		return nil, err
	}
	return restJSON[logs.LogEntryCollection](
		ctx,
		c,
		"POST",
		fmt.Sprintf("/organizations/%s/log-entries", seg(c.org)),
		"",
		q,
	)
}

// GetWebhookSecret fetches the key the platform signs webhook deliveries
// with.
func (c *Client) GetWebhookSecret(ctx context.Context) (*webhooksecretkey.WebhookSecretKey, *clierr.Error) {
	if err := c.RequireOrg(); err != nil {
		return nil, err
	}
	return restJSON[webhooksecretkey.WebhookSecretKey](ctx, c, "GET", c.webhookSecretPath(), "", nil)
}

// RotateWebhookSecret replaces the webhook secret key and returns the new
// one; deliveries signed with the old key stop verifying.
func (c *Client) RotateWebhookSecret(ctx context.Context) (*webhooksecretkey.WebhookSecretKey, *clierr.Error) {
	if err := c.RequireOrg(); err != nil {
		return nil, err
	}
	return restJSON[webhooksecretkey.WebhookSecretKey](ctx, c, "POST", c.webhookSecretPath(), "", nil)
}

// CheckOrg validates the key + organization with a status-only request —
// no response body decoding, so a malformed body can never fail validation.
func (c *Client) CheckOrg(ctx context.Context) *clierr.Error {
	if err := c.RequireOrg(); err != nil {
		return err
	}
	return restNoBody(ctx, c, "GET", c.quotasPath())
}

// CheckProject validates the resolved org + project with a status-only request.
func (c *Client) CheckProject(ctx context.Context) *clierr.Error {
	if err := c.requireOrgProject(); err != nil {
		return err
	}
	return restNoBody(ctx, c, "GET", c.groupPath())
}

// GetQuotas fetches the resolved organization's quotas.
func (c *Client) GetQuotas(ctx context.Context) (*quotas.Quotas, *clierr.Error) {
	if err := c.RequireOrg(); err != nil {
		return nil, err
	}
	return restJSON[quotas.Quotas](ctx, c, "GET", fmt.Sprintf("/organizations/%s/quotas", seg(c.org)), "", nil)
}

// ListGpuClasses lists the GPU classes available to the resolved organization.
func (c *Client) ListGpuClasses(ctx context.Context) (*organizationdata.GpuClassesList, *clierr.Error) {
	if err := c.RequireOrg(); err != nil {
		return nil, err
	}
	return restJSON[organizationdata.GpuClassesList](
		ctx,
		c,
		"GET",
		fmt.Sprintf("/organizations/%s/gpu-classes", seg(c.org)),
		"",
		nil,
	)
}

// quotasPath is the organization check's path.
func (c *Client) quotasPath() string { return fmt.Sprintf("/organizations/%s/quotas", seg(c.org)) }

// orgGone is OnOrgGone's error when a 404 under the organization is the
// organization's own: the request that failed was the organization check,
// or the check, made once, also answers 404. It returns nil otherwise.
func (c *Client) orgGone(ctx context.Context, path string) *clierr.Error {
	if c.OnOrgGone == nil || c.org == "" || !strings.HasPrefix(path, "/organizations/"+seg(c.org)+"/") ||
		!c.checkingOrg.CompareAndSwap(false, true) {
		return nil
	}
	defer c.checkingOrg.Store(false)
	if path != c.quotasPath() {
		if err := restNoBody(ctx, c, "GET", c.quotasPath()); err == nil || err.Code != clierr.CodeNotFound {
			return nil
		}
	}
	return c.OnOrgGone().Final()
}

// projectPrefix is the path prefix of every project-scoped endpoint.
func (c *Client) projectPrefix() string {
	return fmt.Sprintf("/organizations/%s/projects/%s/", seg(c.org), seg(c.project))
}

// assumedProjectMissing turns a 404 under an assumed project into the
// no-valid-project error when the project is what is missing: it lists the
// project's container groups (unless that is the request that failed) and
// checks the organization, so a missing group or a wrong organization keeps
// its own error. It returns nil when the 404 is not the assumed project's.
func (c *Client) assumedProjectMissing(ctx context.Context, method, path string) *clierr.Error {
	if !c.projectAssumed || !strings.HasPrefix(path, c.projectPrefix()) ||
		!c.checkingProject.CompareAndSwap(false, true) {
		return nil
	}
	defer c.checkingProject.Store(false)
	if method != "GET" || path != c.groupPath() {
		if err := restNoBody(ctx, c, "GET", c.groupPath()); err == nil || err.Code != clierr.CodeNotFound {
			return nil
		}
	}
	if err := c.CheckOrg(ctx); err != nil {
		return nil
	}
	return AssumedProjectMissing(c.org)
}

// requireOrgProject bundles the two checks every project-scoped call makes,
// and runs OnAssumedProject the first time the project is an assumed one.
func (c *Client) requireOrgProject() *clierr.Error {
	if err := c.RequireOrg(); err != nil {
		return err
	}
	if err := c.RequireProject(); err != nil {
		return err
	}
	if c.projectAssumed && c.OnAssumedProject != nil {
		c.assumedNoted.Do(c.OnAssumedProject)
	}
	return nil
}

// webhookSecretPath is the organization's webhook secret key resource.
func (c *Client) webhookSecretPath() string {
	return fmt.Sprintf("/organizations/%s/webhook-secret-key", seg(c.org))
}
