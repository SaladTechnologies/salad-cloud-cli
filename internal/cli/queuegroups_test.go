package cli

import (
	"testing"

	"github.com/saladtechnologies/salad-cloud-sdk-go/pkg/shared"
)

// TestQueueGroupsCell: the API returns an empty container_groups list even
// while a connected group runs, so `queues list` shows "-" (unknown) for an
// empty list rather than claiming 0 groups.
func TestQueueGroupsCell(t *testing.T) {
	t.Parallel()
	if got := queueGroupsCell(nil); got != "-" {
		t.Errorf("nil list = %q, want -", got)
	}
	if got := queueGroupsCell([]shared.ContainerGroup{}); got != "-" {
		t.Errorf("empty list = %q, want -", got)
	}
	if got := queueGroupsCell(make([]shared.ContainerGroup, 2)); got != "2" {
		t.Errorf("two groups = %q, want 2", got)
	}
}
