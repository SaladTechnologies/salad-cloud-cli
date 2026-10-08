package cli

import (
	"testing"

	"github.com/saladtechnologies/salad-cloud-sdk-go/pkg/shared"

	"github.com/saladtechnologies/salad-cloud-cli/internal/clierr"
)

// counts builds a status-count struct from plain values.
func counts(allocating, creating, running, stopping int64) *shared.ContainerGroupInstanceStatusCount {
	c := &shared.ContainerGroupInstanceStatusCount{}
	c.SetAllocatingCount(allocating)
	c.SetCreatingCount(creating)
	c.SetRunningCount(running)
	c.SetStoppingCount(stopping)
	return c
}

func TestEmptyInstancesNote(t *testing.T) {
	tests := []struct {
		name   string
		counts *shared.ContainerGroupInstanceStatusCount
		want   string
	}{
		{
			name:   "allocating only",
			counts: counts(2, 0, 0, 0),
			want:   "(2 allocating; not yet assigned to machines, try again shortly)",
		},
		{
			name:   "allocating and creating",
			counts: counts(2, 1, 0, 0),
			want:   "(2 allocating, 1 creating; not yet assigned to machines, try again shortly)",
		},
		{
			name:   "stopping only",
			counts: counts(0, 0, 0, 3),
			want:   "(3 stopping; not yet assigned to machines, try again shortly)",
		},
		{
			name:   "all three transitional states",
			counts: counts(1, 2, 0, 3),
			want:   "(1 allocating, 2 creating, 3 stopping; not yet assigned to machines, try again shortly)",
		},
		{
			name:   "all zero",
			counts: counts(0, 0, 0, 0),
			want:   "no instances; the group has no active deployment",
		},
		{
			name:   "nil counts treated as all zero",
			counts: nil,
			want:   "no instances; the group has no active deployment",
		},
		{
			name:   "running only is an inconsistent snapshot: stay silent",
			counts: counts(0, 0, 2, 0),
			want:   "",
		},
		{
			name:   "running plus allocating lists only the non-running counts",
			counts: counts(1, 0, 2, 0),
			want:   "(1 allocating; not yet assigned to machines, try again shortly)",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := emptyInstancesNote(tt.counts); got != tt.want {
				t.Errorf("emptyInstancesNote() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestInstanceNotFound(t *testing.T) {
	const (
		group = "my-app"
		id    = "f51baccc-dc95-40fb-a5d1-6d0ee0db31d2"
	)
	tests := []struct {
		name     string
		in       *clierr.Error
		wantCode int
		wantMsg  string
	}{
		{
			name: "nil passes through",
			in:   nil,
		},
		{
			name:     "bare 404 is rewritten with instance and group",
			in:       clierr.HTTP(clierr.CodeNotFound, clierr.KindNotFound, 404, "Not Found"),
			wantCode: clierr.CodeNotFound,
			wantMsg:  `instance "f51baccc-dc95-40fb-a5d1-6d0ee0db31d2" not found in container group my-app`,
		},
		{
			name:     "other errors pass through unchanged",
			in:       clierr.New(clierr.CodeRateLimited, clierr.KindRateLimited, "slow down"),
			wantCode: clierr.CodeRateLimited,
			wantMsg:  "slow down",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := instanceNotFound(tt.in, group, id)
			if tt.in == nil {
				if got != nil {
					t.Fatalf("instanceNotFound(nil) = %v, want nil", got)
				}
				return
			}
			if got == nil {
				t.Fatal("instanceNotFound() = nil, want error")
			}
			if got.Code != tt.wantCode {
				t.Errorf("Code = %d, want %d", got.Code, tt.wantCode)
			}
			if got.Message != tt.wantMsg {
				t.Errorf("Message = %q, want %q", got.Message, tt.wantMsg)
			}
			// The rewrite keeps the status of the response it replaces.
			if (got.HTTPStatus == nil) != (tt.in.HTTPStatus == nil) || got.HTTPStatus != nil && *got.HTTPStatus != *tt.in.HTTPStatus {
				t.Errorf("HTTPStatus = %v, want %v", got.HTTPStatus, tt.in.HTTPStatus)
			}
		})
	}
}
