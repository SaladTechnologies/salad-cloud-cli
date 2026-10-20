package api

import (
	"context"
	"fmt"

	"github.com/saladtechnologies/salad-cloud-sdk-go/pkg/organizations"

	"github.com/saladtechnologies/salad-cloud-cli/internal/clierr"
)

// AvailabilityRequest is the requirement set the container-engine
// availability endpoints take: the GPU classes (GPU endpoint only), the
// resources one replica needs, and the countries it may run in. Unset
// fields are left out of the request.
type AvailabilityRequest struct {
	GpuClasses    []string `json:"gpu_classes,omitempty"`
	CPU           *int64   `json:"cpu,omitempty"`
	Memory        *int64   `json:"memory,omitempty"`         // MB
	StorageAmount *int64   `json:"storage_amount,omitempty"` // bytes
	CountryCodes  []string `json:"country_codes,omitempty"`
}

// availabilityPath builds /organizations/{org}/availability/<endpoint>.
func (c *Client) availabilityPath(endpoint string) string {
	return fmt.Sprintf("/organizations/%s/availability/%s", seg(c.org), endpoint)
}

// GetGpuAvailability asks sce-gpu-availability how many nodes could take
// one replica with these requirements, per priority tier. Each call is a
// platform query, so callers make one per class and never in a loop. An
// overloaded availability service answers 200 with every count 0, so a
// zero cannot be told apart from an empty class; on_call_gpu 0 is the tell.
func (c *Client) GetGpuAvailability(
	ctx context.Context,
	req AvailabilityRequest,
) (*organizations.GpuAvailability, *clierr.Error) {
	if err := c.RequireOrg(); err != nil {
		return nil, err
	}
	return restJSON[organizations.GpuAvailability](ctx, c, "POST", c.availabilityPath("sce-gpu-availability"), "", req)
}

// GetCPUAvailability is GetGpuAvailability for CPU-only requirements
// (sce-cpu-availability); any GPU classes in req are dropped.
func (c *Client) GetCPUAvailability(
	ctx context.Context,
	req AvailabilityRequest,
) (*organizations.CpuAvailability, *clierr.Error) {
	if err := c.RequireOrg(); err != nil {
		return nil, err
	}
	req.GpuClasses = nil
	return restJSON[organizations.CpuAvailability](ctx, c, "POST", c.availabilityPath("sce-cpu-availability"), "", req)
}
