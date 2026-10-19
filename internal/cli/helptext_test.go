package cli

import (
	"strings"
	"testing"
)

// TestProjectFlagHiddenWhereNothingReadsIt: the organization-scoped
// commands take --project but never read it, so their help does not offer
// it; logs and availability say what it means there.
func TestProjectFlagHiddenWhereNothingReadsIt(t *testing.T) {
	t.Parallel()
	app := newTestRun(t)
	for _, path := range [][]string{
		{"inference", "list"},
		{"inference", "jobs", "list"},
		{"inference", "transcribe"},
		{"gpus", "list"},
		{"orgs", "list"},
		{"quotas", "get"},
		{"webhook-secret", "get"},
		{"ai-gateway", "test"},
		{"ai-gateway", "models", "list"},
	} {
		c, _, err := app.root.Find(path)
		if err != nil {
			t.Fatalf("%v: %v", path, err)
		}
		if f := c.Flag("project"); f == nil || !f.Hidden {
			t.Errorf("%s offers --project, which it never reads", c.CommandPath())
		}
	}
	if c := app.findCommand(t, "container-groups", "list"); c.Flag("project").Hidden {
		t.Error("container-groups list hides --project, which it reads")
	}
	logs := app.findCommand(t, "logs")
	if u := logs.Flag("project").Usage; !strings.Contains(u, "the saved project and SALAD_PROJECT do not narrow logs") {
		t.Errorf("logs --project help = %q", u)
	}
	avail := app.findCommand(t, "availability")
	if u := avail.Flag("project").Usage; !strings.HasPrefix(u, "the project --from <group> looks in") {
		t.Errorf("availability --project help = %q", u)
	}
}

// TestFlagHelpShowsOneDefault: a default that depends on another flag is
// stated once, by the help, never beside cobra's own "(default ...)".
func TestFlagHelpShowsOneDefault(t *testing.T) {
	t.Parallel()
	app := newTestRun(t)
	c := app.findCommand(t, "ai-gateway", "test")
	f := c.Flag("max-tokens")
	if f.DefValue != "0" || f.Usage != "completion token budget (default 64, or 1024 with --thinking)" {
		t.Errorf("--max-tokens default %q, usage %q", f.DefValue, f.Usage)
	}
	if defaultMaxTokens(false) != 64 || defaultMaxTokens(true) != 1024 {
		t.Errorf("defaultMaxTokens = %d, %d", defaultMaxTokens(false), defaultMaxTokens(true))
	}
}

// TestConfigGetSaysKeysAreMasked: webhook-secret get prints a whole secret,
// so masking is not something to assume; config get says it masks.
func TestConfigGetSaysKeysAreMasked(t *testing.T) {
	t.Parallel()
	app := newTestRun(t)
	c := app.findCommand(t, "config", "get")
	if !strings.Contains(c.Short, "masked") || !strings.Contains(c.Long, "masked to their last four characters") {
		t.Errorf("config get help does not say keys print masked: %q / %q", c.Short, c.Long)
	}
}
