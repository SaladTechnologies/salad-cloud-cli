package cli

import (
	"errors"
	"strings"
	"testing"

	"github.com/saladtechnologies/salad-cloud-cli/internal/clierr"
)

// TestUsageErrorsPrintWhatToType: every wrong-shape command line ends at a
// usage error (exit 2) that names the input and prints the command to type,
// in one phrasing, never cobra's or pflag's.
func TestUsageErrorsPrintWhatToType(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		args []string
		want string
	}{
		// A parent given as a positional.
		{
			[]string{"cg", "instances", "list", "sr-sleep-two"},
			"instances list takes the group as -c, not as an argument | run saladcloud container-groups instances " +
				"list -c sr-sleep-two",
		},
		{
			[]string{"queues", "jobs", "create", "sr-q1", "--input", `{"x":1}`},
			`queues jobs create takes the queue as --queue, not as an argument | run saladcloud queues jobs create ` +
				`--queue sr-q1 --input '{"x":1}'`,
		},
		{
			[]string{"inference", "jobs", "list", "transcribe", "--limit", "3"},
			"inference jobs list takes the endpoint as --endpoint, not as an argument | run saladcloud inference " +
				"jobs list --endpoint transcribe --limit 3",
		},
		{
			[]string{"cg", "instances", "restart", "my-app", "8a2f13ba"},
			"instances restart takes the group as -c, not as an argument | run saladcloud container-groups instances " +
				"restart 8a2f13ba -c my-app",
		},
		{
			[]string{"queues", "jobs", "get", "sr-q1", "c39e9f69"},
			"queues jobs get takes the queue as --queue, not as an argument | run saladcloud queues jobs get " +
				"c39e9f69 --queue sr-q1",
		},
		{
			[]string{"logs", "my-app", "--since", "1h"},
			"logs takes the group as -c, not as an argument | run saladcloud logs -c my-app --since 1h",
		},
		// The instance given as a second positional.
		{
			[]string{"cg", "ssh", "my-app", "8a2f13ba", "--", "hostname"},
			"ssh takes the instance as --instance, not as an argument | run saladcloud container-groups ssh my-app " +
				"--instance 8a2f13ba -- hostname",
		},
		{
			[]string{"cg", "sftp", "my-app", "8a2f13ba"},
			"sftp takes the instance as --instance, not as an argument | run saladcloud container-groups sftp my-app " +
				"--instance 8a2f13ba",
		},
		// -c where the group is the argument.
		{
			[]string{"cg", "ssh", "5da47e1c", "-c", "sr-sleep-two", "--", "hostname"},
			"ssh takes the group as its argument and the instance as --instance | run saladcloud container-groups " +
				"ssh sr-sleep-two --instance 5da47e1c -- hostname",
		},
		{
			[]string{"cg", "logs", "0521ee78", "-c", "sr-ex2", "-f"},
			"logs takes the group as its argument and the instance as --instance | run saladcloud container-groups " +
				"logs sr-ex2 --instance 0521ee78 --follow",
		},
		{
			[]string{"cg", "sftp", "-c", "my-app"},
			"sftp takes the group as its argument and the instance as --instance | run saladcloud container-groups " +
				"sftp my-app",
		},
		{
			[]string{"cg", "cp", "-c", "my-app", "/etc/hostname", "./h"},
			"cp takes the group in its remote path, not as -c | run saladcloud container-groups cp " +
				"my-app[/<instance>]:<path> <local-path>",
		},
		// Missing arguments and flags.
		{
			[]string{"cg", "instances", "restart", "0521ee78"},
			"missing required flag: -c/--container-group <group> | run saladcloud container-groups instances restart " +
				"<id> -c <group>",
		},
		{
			[]string{"queues", "jobs", "create", "--input", "{}"},
			"missing required flag: --queue <queue> | run saladcloud queues jobs create --queue <queue> (-f <file> | " +
				"--input '<json>') [--webhook <url>]",
		},
		{
			[]string{"cg", "instances", "restart", "-c", "sr-ex2"},
			"missing argument: <id> | run saladcloud container-groups instances restart <id> -c <group>",
		},
		{
			[]string{"cg", "logs"},
			"missing argument: <group> | run saladcloud container-groups logs <group> [--instance <id>]",
		},
		{
			[]string{"cg", "get", "a", "b"},
			`unexpected argument "b" | run saladcloud container-groups get <group>`,
		},
		// Flag values.
		{
			[]string{"cg", "scale", "sr-ex2", "--replicas", "two"},
			`invalid --replicas "two": not a number`,
		},
		{
			[]string{"cg", "update", "sr-ex2", "--timeout", "soon"},
			`invalid --timeout "soon": not a duration such as 30s, 5m or 1h`,
		},
		{
			[]string{"queues", "jobs", "list", "--queue"},
			"missing value for --queue <queue> | run saladcloud queues jobs list --queue <queue>",
		},
		// An unknown flag: the nearest spelling goes in the hint.
		{
			[]string{"cg", "start", "sr-ex2", "--waitt"},
			"unknown flag: --waitt | did you mean --wait?; run saladcloud container-groups start <group>, or to list " +
				"its flags, run saladcloud container-groups start --help",
		},
		// Waiting: which default applies.
		{
			[]string{"cg", "start", "sr-ex2", "--no-wait"},
			"unknown flag: --no-wait | leave it out; start returns at once, and --wait blocks until the group is " +
				"running",
		},
		{
			[]string{"cg", "stop", "sr-ex2", "--no-wait"},
			"unknown flag: --no-wait | leave it out; stop returns at once, and --wait blocks until the group is " +
				"stopped",
		},
		{
			[]string{"cg", "update", "sr-ex2", "--wait"},
			"unknown flag: --wait | leave it out; update waits for the change to settle by default, and --no-wait " +
				"returns at once",
		},
		{
			[]string{"cg", "scale", "sr-ex2", "--replicas", "1", "--wait"},
			"unknown flag: --wait | leave it out; scale waits for the change to settle by default, and --no-wait " +
				"returns at once",
		},
	} {
		// A subtest each: runRoot resets the tree's flags when its test ends.
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			t.Parallel()
			app := newTestRun(t)
			_, err := app.runRoot(t, tc.args...)
			var cerr *clierr.Error
			ok := errors.As(err, &cerr)
			if !ok {
				t.Fatalf("%v (%T), want a *clierr.Error", err, err)
			}
			if cerr.Code != clierr.CodeUsage || cerr.Kind != clierr.KindUsage {
				t.Errorf("(%d, %s), want (2, usage)", cerr.Code, cerr.Kind)
			}
			got := cerr.Message
			if cerr.Hint != nil {
				got += " | " + *cerr.Hint
			}
			if got != tc.want {
				t.Errorf("\n got  %s\n want %s", got, tc.want)
			}
		})
	}
}

func TestEditDistance(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		a, b string
		want int
	}{{"lst", "list", 1}, {"loggs", "logs", 1}, {"jobs", "logs", 2}, {"", "abc", 3}, {"same", "same", 0}} {
		if got := editDistance(tc.a, tc.b); got != tc.want {
			t.Errorf("editDistance(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}
