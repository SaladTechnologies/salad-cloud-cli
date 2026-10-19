package cli

import (
	"slices"
	"strings"
	"testing"
)

// TestCtrlCDuringARequest: Ctrl-C while a request is in flight ends the
// command as "<command> canceled", exit 1, kind interrupted, never a
// network error; the command is named by its full path even when an alias
// ran it.
func TestCtrlCDuringARequest(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		args     []string
		cancelOn string
		want     string
	}{
		{[]string{"quotas", "get"}, "/quotas", "quotas get canceled"},
		{[]string{"cg", "list"}, "/containers", "container-groups list canceled"},
		{[]string{"ai-gateway", "test"}, "/models", "ai-gateway test canceled"},
	} {
		for _, mode := range []string{"table", "json"} {
			t.Run(strings.Join(tc.args, " ")+"/"+mode, func(t *testing.T) {
				t.Parallel()
				api := newFakeAPI()
				api.cancelOn = tc.cancelOn
				o := session{
					args: append(slices.Clone(tc.args), "-o", mode), api: api,
					env: with(keyEnv(), "SALAD_AI_GATEWAY_API_KEY", testGatewayKey),
				}.run(t)
				checkCanceledRun(t, &o, mode, tc.want)
			})
		}
	}
}

// checkCanceledRun checks that a run Ctrl-C ended mid-request exits 1 with the error want, of kind interrupted, in
// the output mode it ran in.
func checkCanceledRun(t *testing.T, o *outcome, mode, want string) {
	t.Helper()
	if o.code != 1 {
		t.Fatalf("exit %d, want 1:\n%s", o.code, o.stderr)
	}
	if mode == "table" {
		if got := strings.TrimSpace(o.stderr); got != "saladcloud: error: "+want {
			t.Errorf("stderr = %q, want saladcloud: error: %s", got, want)
		}
		return
	}
	doc, err := o.errorDocument()
	if err != nil {
		t.Fatalf("%v:\n%s", err, o.stderr)
	}
	if doc["kind"] != "interrupted" || doc["message"] != want || doc["hint"] != nil ||
		doc["http_status"] != nil {
		t.Errorf("error document = %v", doc)
	}
}

// TestCtrlCEndsOpenEndedCommandsCleanly: logs --follow and --watch stop with
// exit 0 when Ctrl-C lands mid-request, as before.
func TestCtrlCEndsOpenEndedCommandsCleanly(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		args     []string
		cancelOn string
	}{
		{[]string{"logs", "--follow"}, "/log-entries"},
		{[]string{"logs", "--follow", "-o", "json"}, "/log-entries"},
		{[]string{"cg", "list", "--watch"}, "/containers"},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			t.Parallel()
			api := newFakeAPI()
			api.cancelOn = tc.cancelOn
			o := session{args: tc.args, api: api, env: keyEnv()}.run(t)
			if o.code != 0 {
				t.Errorf("exit %d, want 0:\n%s", o.code, o.stderr)
			}
			if strings.Contains(o.stderr, "canceled") || strings.Contains(o.stderr, "error") {
				t.Errorf("stderr reports an error:\n%s", o.stderr)
			}
		})
	}
}
