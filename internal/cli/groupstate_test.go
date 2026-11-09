package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/saladtechnologies/salad-cloud-sdk-go/pkg/shared"
	"github.com/saladtechnologies/salad-cloud-sdk-go/pkg/util"

	"github.com/saladtechnologies/salad-cloud-cli/internal/output"
)

// failedGroup is a group the platform could not start, with its reason.
func failedGroup(status shared.ContainerGroupStatus, reason *util.Nullable[string]) *shared.ContainerGroup {
	return &shared.ContainerGroup{
		Name:     new("my-app"),
		Replicas: new(int64(2)),
		Container: &shared.Container{
			Image: new("ghcr.io/me/app:latest"),
		},
		CurrentState: &shared.ContainerGroupState{
			Status:      &status,
			Description: reason,
			InstanceStatusCounts: &shared.ContainerGroupInstanceStatusCount{
				RunningCount: new(int64(1)),
			},
		},
	}
}

// TestGetShowsTheReason: a failed group's reason is in the get table, not
// only in -o json's current_state.description.
func TestGetShowsTheReason(t *testing.T) {
	t.Parallel()
	g := failedGroup(
		shared.CONTAINER_GROUP_STATUS_FAILED,
		&util.Nullable[string]{Value: "Access Denied, Check Permissions"},
	)
	var b bytes.Buffer
	writeGroupDetail(output.NewWriter(&b), g)
	if !strings.Contains(b.String(), "Status\tfailed\nReason\tAccess Denied, Check Permissions\n") {
		t.Errorf("get table lacks the Reason row:\n%s", b.String())
	}
	for _, none := range []*util.Nullable[string]{nil, {IsNull: true}, {Value: ""}} {
		b.Reset()
		writeGroupDetail(output.NewWriter(&b), failedGroup(shared.CONTAINER_GROUP_STATUS_STOPPED, none))
		if strings.Contains(b.String(), "Reason") {
			t.Errorf("a group with no description shows a Reason row:\n%s", b.String())
		}
	}
}

// TestListColumns: list shows running instances against replicas, and the
// image.
func TestListColumns(t *testing.T) {
	t.Parallel()
	app := newTestRun(t)
	g := failedGroup(shared.CONTAINER_GROUP_STATUS_RUNNING, nil)
	if got := runningCell(g); got != "1/2" {
		t.Errorf("runningCell = %q, want 1/2", got)
	}
	if got := imageCell(g); got != "ghcr.io/me/app:latest" {
		t.Errorf("imageCell = %q", got)
	}
	g.CurrentState.InstanceStatusCounts = nil
	g.Container = nil
	if runningCell(g) != "-/2" || imageCell(g) != "-" {
		t.Errorf("unknown counts and image render %q %q, want -/2 and -", runningCell(g), imageCell(g))
	}

	app.stubAPI(t, map[string]string{"/containers": `{"items":[{"name":"my-app","display_name":"My App","replicas":2,` +
		`"container":{"image":"docker.io/library/busybox:1.36"},` +
		`"current_state":{"status":"running","instance_status_counts":{"running_count":1}}}]}`})
	out, err := app.runRoot(t, "cg", "list")
	if err != nil {
		t.Fatal(err)
	}
	header, row, _ := strings.Cut(out, "\n")
	for _, col := range []string{"RUNNING", "IMAGE"} {
		if !strings.Contains(header, col) {
			t.Errorf("header %q lacks %s", header, col)
		}
	}
	for _, cell := range []string{"1/2", "docker.io/library/busybox:1.36"} {
		if !strings.Contains(row, cell) {
			t.Errorf("row %q lacks %q", row, cell)
		}
	}
}

// TestFollowSilentGroupNote: following a failed or stopped group says it
// will stay silent, and why when the platform says.
func TestFollowSilentGroupNote(t *testing.T) {
	t.Parallel()
	failed := failedGroup(
		shared.CONTAINER_GROUP_STATUS_FAILED,
		&util.Nullable[string]{Value: "Access Denied, Check Permissions"},
	)
	want := `note: group my-app is failed: Access Denied, Check Permissions; nothing will be logged until it runs`
	if got := groupSilentNote(failed); got != want {
		t.Errorf("note = %q, want %q", got, want)
	}
	stopped := failedGroup(shared.CONTAINER_GROUP_STATUS_STOPPED, nil)
	want = `note: group my-app is stopped; nothing will be logged until it runs`
	if got := groupSilentNote(stopped); got != want {
		t.Errorf("note = %q, want %q", got, want)
	}
	if got := groupSilentNote(failedGroup(shared.CONTAINER_GROUP_STATUS_RUNNING, nil)); got != "" {
		t.Errorf("a running group got a note: %q", got)
	}
}

// TestDeleteHelpSaysNamesAreKept: a kubectl habit is delete then create
// with the same name; the help says that cannot work.
func TestDeleteHelpSaysNamesAreKept(t *testing.T) {
	t.Parallel()
	app := newTestRun(t)
	c, _, err := app.root.Find([]string{"container-groups", "delete"})
	if err != nil || !strings.Contains(c.Long, "name is never released") {
		t.Errorf("delete --help does not say names are never released: %q", c.Long)
	}
}
