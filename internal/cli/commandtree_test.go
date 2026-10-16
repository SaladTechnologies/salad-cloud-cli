package cli

import (
	"bytes"
	"regexp"
	"strings"
	"testing"

	"github.com/saladtechnologies/salad-cloud-sdk-go/pkg/containergroups"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/saladtechnologies/salad-cloud-cli/internal/clierr"
	"github.com/saladtechnologies/salad-cloud-cli/internal/output"
	"github.com/saladtechnologies/salad-cloud-cli/internal/ptr"
)

// groupVerbs are the commands whose subject is a container group. They live
// under 'saladcloud container-groups' and nowhere at the top level.
var groupVerbs = []string{
	"create",
	"list",
	"get",
	"update",
	"start",
	"stop",
	"delete",
	"scale",
	"instances",
	"ssh",
	"sftp",
	"cp",
	"logs",
}

// TestContainerGroupVerbsLiveUnderTheNoun pins the command tree.
func TestContainerGroupVerbsLiveUnderTheNoun(t *testing.T) {
	top := map[string]bool{}
	for _, c := range rootCmd.Commands() {
		top[c.Name()] = true
	}
	for _, v := range groupVerbs {
		if v != "logs" && top[v] { // top-level logs is the organization-wide query
			t.Errorf("%q is still a top-level command; it belongs under container-groups", v)
		}
		if c, _, err := rootCmd.Find([]string{"container-groups", v}); err != nil || c.Name() != v {
			t.Errorf("container-groups %s: not found (got %v, err %v)", v, c.Name(), err)
		}
	}
	for _, alias := range []string{"cg", "container-group"} {
		if c, _, err := rootCmd.Find([]string{alias, "list"}); err != nil || c.Parent() != containerGroupsCmd {
			t.Errorf("alias %q does not reach container-groups list (err %v)", alias, err)
		}
	}
}

// removedSpellings match command forms the grammar batch replaced; a bare
// old verb after "saladcloud" (ssh-key is a different command and must not
// match) and the moved or renamed commands and flags.
var removedSpellings = []*regexp.Regexp{
	regexp.MustCompile(`saladcloud (create|list|get|update|start|stop|delete|scale|instances|ssh|sftp|cp)\b[^-]`),
	regexp.MustCompile(`saladcloud transcribe\b`),
	regexp.MustCompile(`inference endpoints (list|get)\b`),
	regexp.MustCompile(`ssh-key show`),
	regexp.MustCompile(`saladcloud (gpus|orgs|quotas)(\s*$|['"\x60)]| -| --)`),
	regexp.MustCompile(`instances (list|get|restart|recreate|reallocate) [a-z][a-z0-9-]+ [0-9a-f]`),
	regexp.MustCompile(`saladcloud logs [a-z]`),
	regexp.MustCompile(`--lite\b|--no-availability|--diarize\b`),
	regexp.MustCompile(`jobs delete\b`),
}

// TestHelpNeverSpellsARemovedCommand: every Use, Short, Long, and Example in
// the tree must spell commands the current way.
func TestHelpNeverSpellsARemovedCommand(t *testing.T) {
	walkTree(rootCmd, func(c *cobra.Command) {
		for field, text := range map[string]string{"Use": c.Use, "Short": c.Short, "Long": c.Long, "Example": c.Example} {
			for _, re := range removedSpellings {
				for _, line := range strings.Split(text, "\n") {
					if m := re.FindString(line); m != "" {
						t.Errorf("%s %s spells a removed form: %q", c.CommandPath(), field, strings.TrimSpace(m))
					}
				}
			}
		}
	})
}

// TestPickInstanceNotesTheChoice: when the CLI picks the instance it says
// which; a named instance, even by prefix, prints nothing.
func TestPickInstanceNotesTheChoice(t *testing.T) {
	running := containergroups.THE_CONTAINER_GROUP_INSTANCE_STATE_RUNNING
	instances := []containergroups.ContainerGroupInstance{
		{Id: ptr.To("8a2f13ba-01c2-4a29-9c4d-52c7e5a44e37"), State: &running},
		{Id: ptr.To("1111aaaa-01c2-4a29-9c4d-52c7e5a44e37"), State: &running},
	}
	var picked bytes.Buffer
	inst, cerr := pickInstance(&picked, instances, "my-app", "")
	if cerr != nil {
		t.Fatalf("pickInstance: %v", cerr)
	}
	if want := "note: using instance " + ptr.Deref(inst.Id) + "\n"; picked.String() != want {
		t.Errorf("note = %q, want %q", picked.String(), want)
	}
	var named bytes.Buffer
	if _, cerr := pickInstance(&named, instances, "my-app", "1111"); cerr != nil {
		t.Fatalf("pickInstance with a prefix: %v", cerr)
	}
	if named.Len() != 0 {
		t.Errorf("a named instance printed %q, want nothing", named.String())
	}
}

// allowedAliases is the alias rule as data: every plural resource noun
// answers to its singular; a noun may also answer to its full name or API
// resource name; cg is the one abbreviation; singletons get none. Every
// alias in the tree must be listed here, and every command listed must
// carry exactly these.
var allowedAliases = map[string][]string{
	"saladcloud container-groups":           {"container-group", "cg"},
	"saladcloud container-groups instances": {"instance"},
	"saladcloud orgs":                       {"org", "organizations"},
	"saladcloud gpus":                       {"gpu", "gpu-classes"},
	"saladcloud queues":                     {"queue"},
	"saladcloud queues jobs":                {"job"},
	"saladcloud inference jobs":             {"job"},
	"saladcloud ai-gateway models":          {"model"},
}

// singletonNouns keep the API's spelling, plural or not, and get no
// singular alias.
var singletonNouns = map[string]bool{"quotas": true}

func walkTree(c *cobra.Command, fn func(*cobra.Command)) {
	fn(c)
	for _, sub := range c.Commands() {
		walkTree(sub, fn)
	}
}

func TestAliasRule(t *testing.T) {
	prepareTree()
	// The rule itself, independent of the table: a plural noun (a command
	// with subcommands whose name ends in s) answers to its singular.
	walkTree(rootCmd, func(c *cobra.Command) {
		if c == rootCmd || !c.HasSubCommands() || singletonNouns[c.Name()] || !strings.HasSuffix(c.Name(), "s") {
			return
		}
		if singular := strings.TrimSuffix(c.Name(), "s"); !c.HasAlias(singular) {
			t.Errorf("%s is a plural noun without its singular alias %q", c.CommandPath(), singular)
		}
	})
	walkTree(rootCmd, func(c *cobra.Command) {
		want := allowedAliases[c.CommandPath()]
		if strings.Join(c.Aliases, ",") != strings.Join(want, ",") {
			t.Errorf("%s aliases = %v, want %v (see the alias rule in AGENTS.md)", c.CommandPath(), c.Aliases, want)
		}
	})
	for path := range allowedAliases {
		if c, _, err := rootCmd.Find(strings.Fields(path)[1:]); err != nil || c.CommandPath() != path {
			t.Errorf("%s: not in the tree", path)
		}
	}
}

// TestAIGatewayHasNoAliases: gateway would bring back the container-gateway
// confusion and ai is the word any future AI product wants.
func TestAIGatewayHasNoAliases(t *testing.T) {
	if c, _, err := rootCmd.Find(
		[]string{"ai-gateway", "models", "list"},
	); err != nil ||
		c.Parent().Parent().Name() != "ai-gateway" {
		t.Fatalf("ai-gateway models list not found (err %v)", err)
	}
	for _, name := range []string{"gateway", "ai", "connect"} {
		if _, _, err := rootCmd.Find([]string{name}); err == nil {
			t.Errorf("%q still resolves to a command", name)
		}
	}
}

// runRoot executes argv through the real tree with stdout captured, the way
// Execute does, and returns stdout and the error.
func runRoot(t *testing.T, args ...string) (string, error) {
	t.Helper()
	prepareTree()
	var buf bytes.Buffer
	old := output.Stdout
	output.Stdout = &buf
	t.Cleanup(func() {
		output.Stdout = old
		resetRun()
	})
	rootCmd.SetArgs(args)
	err := rootCmd.Execute()
	return buf.String(), err
}

// resetFlags puts every flag in the tree back to its default, --output
// included: cobra keeps flag values between in-process runs, so a later test
// would inherit them, and a repeatable flag would keep appending. It resets
// flags that are not marked changed too: a value that fails to parse
// (--timeout soon) is stored before the error, without marking the flag.
func resetFlags() {
	reset := func(f *pflag.Flag) {
		if sv, ok := f.Value.(pflag.SliceValue); ok {
			_ = sv.Replace(nil)
		} else {
			_ = f.Value.Set(f.DefValue)
		}
		f.Changed = false
	}
	walkTree(rootCmd, func(c *cobra.Command) {
		c.Flags().VisitAll(reset)
		c.PersistentFlags().VisitAll(reset)
		// pflag never resets where "--" was found between parses, so a later
		// create or update would see an earlier run's "--" (Init resets only
		// that, the name and the error handling).
		c.Flags().Init(c.Name(), pflag.ContinueOnError)
	})
}

// TestBareNounIsUsageError: every command that only groups others exits 2
// with the error document when run bare, never help on stdout with exit 0
// ('gpus -o json | jq' must fail loudly).
func TestBareNounIsUsageError(t *testing.T) {
	prepareTree()
	var parents []*cobra.Command
	walkTree(rootCmd, func(c *cobra.Command) {
		if c != rootCmd && c.HasSubCommands() {
			parents = append(parents, c)
		}
	})
	if len(parents) < 10 {
		t.Fatalf("found %d parent commands; the walk is not reaching the tree", len(parents))
	}
	if c, _, err := rootCmd.Find([]string{"completion"}); err != nil || c.Name() != "completion" {
		t.Fatal("cobra's completion command is not in the tree the test walks")
	}
	for _, p := range parents {
		path := strings.Fields(p.CommandPath())[1:]
		t.Run(strings.Join(path, " "), func(t *testing.T) {
			out, err := runRoot(t, append(path, "-o", "json")...)
			cerr, ok := err.(*clierr.Error)
			if !ok || cerr.Code != clierr.CodeUsage {
				t.Fatalf("bare %v = %v, want a usage error", path, err)
			}
			if !strings.Contains(cerr.Message, "needs a verb: ") {
				t.Errorf("message = %q, want it to list the verbs", cerr.Message)
			}
			if out != "" {
				t.Errorf("stdout = %q, want empty", out)
			}
		})
	}
	if _, err := runRoot(t, "gpus", "bogus"); err == nil || !strings.Contains(err.Error(), `unknown command "bogus"`) {
		t.Errorf("gpus bogus = %v, want an unknown-command usage error", err)
	}
}

// TestSingletonsTakeGetNeverList: one per organization or account, so get
// plus action verbs.
func TestSingletonsTakeGetNeverList(t *testing.T) {
	for _, noun := range []string{"quotas", "webhook-secret", "ssh-key"} {
		if c, _, err := rootCmd.Find([]string{noun, "get"}); err != nil || c.Name() != "get" {
			t.Errorf("%s get: not found", noun)
		}
		if c, _, _ := rootCmd.Find([]string{noun, "list"}); c != nil && c.Name() == "list" {
			t.Errorf("%s list exists; a singleton never lists", noun)
		}
	}
}

// TestResourceNounsTakeListAndGet: the resource nouns read through list
// and get.
func TestResourceNounsTakeListAndGet(t *testing.T) {
	for _, noun := range [][]string{
		{"container-groups"},
		{"container-groups", "instances"},
		{"orgs"},
		{"gpus"},
		{"inference"},
		{"inference", "jobs"},
		{"queues"},
		{"queues", "jobs"},
	} {
		for _, verb := range []string{"list", "get"} {
			if c, _, err := rootCmd.Find(append(append([]string{}, noun...), verb)); err != nil || c.Name() != verb {
				t.Errorf("%v %s: not found", noun, verb)
			}
		}
	}
}

// TestJobsCancelNotDelete: the API cancels a job, it does not delete one, so
// both job nouns take cancel; delete is gone with no alias.
func TestJobsCancelNotDelete(t *testing.T) {
	for _, noun := range [][]string{{"inference", "jobs"}, {"queues", "jobs"}} {
		if c, _, err := rootCmd.Find(
			append(append([]string{}, noun...), "cancel"),
		); err != nil ||
			c.Name() != "cancel" {
			t.Errorf("%v cancel: not found", noun)
		}
		p, _, _ := rootCmd.Find(noun)
		for _, sub := range p.Commands() {
			if sub.Name() == "delete" || sub.HasAlias("delete") {
				t.Errorf("%s still answers to delete", sub.CommandPath())
			}
		}
	}
}

// TestLeafPositionalAncestorsAsFlags: the thing a verb acts on is its one
// positional; every ancestor is a required named flag.
func TestLeafPositionalAncestorsAsFlags(t *testing.T) {
	for _, tc := range []struct {
		parent []string
		flag   string
	}{
		{[]string{"container-groups", "instances"}, "container-group"},
		{[]string{"inference", "jobs"}, "endpoint"},
		{[]string{"queues", "jobs"}, "queue"},
	} {
		p, _, err := rootCmd.Find(tc.parent)
		if err != nil {
			t.Fatalf("%v: %v", tc.parent, err)
		}
		for _, verb := range p.Commands() {
			f := verb.Flags().Lookup(tc.flag)
			if f == nil {
				t.Errorf("%s has no --%s", verb.CommandPath(), tc.flag)
				continue
			}
			if req := f.Annotations[cobra.BashCompOneRequiredFlag]; len(req) == 0 || req[0] != "true" {
				t.Errorf("%s --%s is not required", verb.CommandPath(), tc.flag)
			}
		}
	}
	for _, path := range [][]string{{"logs"}, {"container-groups", "instances", "list"}, {"inference", "jobs", "list"}} {
		c, _, _ := rootCmd.Find(path)
		if err := c.Args(c, []string{"extra"}); err == nil {
			t.Errorf("%s accepts a positional; its scope is flags", c.CommandPath())
		}
	}
	for _, path := range [][]string{{"container-groups", "ssh"}, {"container-groups", "sftp"}} {
		c, _, _ := rootCmd.Find(path)
		if c.Flags().Lookup("instance") == nil {
			t.Errorf("%s has no --instance", c.CommandPath())
		}
		if err := c.Args(c, []string{"my-app", "8a2f"}); err == nil {
			t.Errorf("%s accepts the instance as a second positional", c.CommandPath())
		}
	}
}

// shortFlags is the closed table of short flags and the one long flag each
// stands for.
var shortFlags = map[string][]string{
	"o": {"output"},
	"f": {"file", "follow"},
	"e": {"env"},
	"c": {"container-group"},
	"w": {"watch"},
	"i": {"identity"},
	"r": {"recursive"},
}

func TestShortFlagsAreAClosedTable(t *testing.T) {
	walkTree(rootCmd, func(c *cobra.Command) {
		c.Flags().VisitAll(func(f *pflag.Flag) {
			if f.Shorthand == "" || f.Name == "help" {
				return
			}
			allowed := shortFlags[f.Shorthand]
			ok := false
			for _, name := range allowed {
				ok = ok || name == f.Name
			}
			if !ok {
				t.Errorf("%s: -%s for --%s is not in the short-flag table", c.CommandPath(), f.Shorthand, f.Name)
			}
		})
	})
}

// TestRepeatableFlagsAreRepeated: a repeatable flag is a StringArray, never
// a comma-split StringSlice (GPU class names contain commas).
func TestRepeatableFlagsAreRepeated(t *testing.T) {
	walkTree(rootCmd, func(c *cobra.Command) {
		c.Flags().VisitAll(func(f *pflag.Flag) {
			if strings.HasSuffix(f.Value.Type(), "Slice") {
				t.Errorf(
					"%s --%s is a %s; repeatable flags are repeated, never comma-split",
					c.CommandPath(),
					f.Name,
					f.Value.Type(),
				)
			}
		})
	})
}

// TestInferencePorcelainNamesAreEndpoints: each porcelain verb is named
// exactly after an endpoint, never a reserved word.
func TestInferencePorcelainNamesAreEndpoints(t *testing.T) {
	reserved := map[string]bool{"list": true, "get": true, "jobs": true, "help": true, "completion": true}
	for _, ep := range transcribeEndpoints {
		if reserved[ep.name] {
			t.Errorf("porcelain %q takes a reserved name", ep.name)
		}
		if c, _, err := rootCmd.Find([]string{"inference", ep.name}); err != nil || c.Name() != ep.name {
			t.Errorf("inference %s: not found", ep.name)
		}
	}
	for _, name := range []string{"transcribe", "endpoints"} {
		if c, _, _ := rootCmd.Find([]string{name}); c != nil && c != rootCmd {
			t.Errorf("top-level %q exists; inference owns it", name)
		}
	}
}

func TestDidYouMean(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string // "" = no suggestion list
	}{
		{[]string{"transcribe", "x.mp3"}, "did you mean saladcloud inference transcribe?"},
		{[]string{"restart", "x"}, "did you mean saladcloud container-groups instances restart?"},
		{[]string{"create"}, "did you mean saladcloud container-groups create, saladcloud inference jobs create, saladcloud queues create, saladcloud queues jobs create?"},
		{[]string{"cg", "transcribe"}, "did you mean saladcloud inference transcribe?"},
		{[]string{"bogusverb"}, ""},
		// Nouns resolve too, with the verb that followed them.
		{[]string{"instances", "list"}, "did you mean saladcloud container-groups instances list?"},
		{[]string{"jobs", "list"}, "did you mean saladcloud inference jobs list, saladcloud queues jobs list?"},
		{[]string{"instances"}, "did you mean saladcloud container-groups instances?"},
		// A misplaced verb's own flags do not hide the suggestion.
		{[]string{"cg", "instances", "logs", "0521ee78", "-c", "sr-ex2", "-f"}, "did you mean saladcloud container-groups logs, saladcloud logs?"},
		// Typos get the near spellings under the same parent, one line.
		{[]string{"cg", "lst"}, "did you mean saladcloud container-groups list?"},
		{[]string{"loggs"}, "did you mean saladcloud logs?"},
		// A guess a command answers without being an alias (gh auth status).
		{[]string{"ai-gateway", "status"}, "did you mean saladcloud whoami?"},
		{[]string{"status"}, "did you mean saladcloud whoami?"},
	} {
		_, err := runRoot(t, tc.args...)
		if err == nil {
			t.Fatalf("%v: no error", tc.args)
		}
		cerr, ok := err.(*clierr.Error)
		if !ok {
			cerr = suggestUnknownCommand(rootCmd, err.Error(), tc.args)
		}
		hint := cerr.HintText()
		if strings.Contains(cerr.Message+hint, "\n") {
			t.Errorf("%v: %q / %q spans lines; did-you-mean is one line", tc.args, cerr.Message, hint)
		}
		if tc.want == "" {
			if hint != "" {
				t.Errorf("%v: hint %q, want no list", tc.args, hint)
			}
			continue
		}
		if hint != tc.want {
			t.Errorf("%v: hint %q, want %q", tc.args, hint, tc.want)
		}
	}
	// More than maxSuggestions matches: no list.
	if got := withSuggestions(rootCmd, "get", nil); got.Hint != nil {
		t.Errorf("get matches more than %d leaves but listed them: %q", maxSuggestions, *got.Hint)
	}
}

// TestHelpHidesTestKnobs: the SALAD_CLI_*_BASE_URL overrides exist for tests
// and are documented as test knobs in the guide, never in --help.
func TestHelpHidesTestKnobs(t *testing.T) {
	walkTree(rootCmd, func(c *cobra.Command) {
		text := c.Use + c.Short + c.Long + c.Example
		c.Flags().VisitAll(func(f *pflag.Flag) { text += f.Usage })
		if strings.Contains(text, "BASE_URL") {
			t.Errorf("%s help mentions a *_BASE_URL test knob", c.CommandPath())
		}
	})
}
