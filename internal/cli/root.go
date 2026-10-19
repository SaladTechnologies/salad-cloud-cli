// Package cli wires up the saladcloud command tree, global flags, and the
// error-to-exit-code mapping.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/saladtechnologies/salad-cloud-cli/internal/api"
	"github.com/saladtechnologies/salad-cloud-cli/internal/clierr"
	"github.com/saladtechnologies/salad-cloud-cli/internal/config"
	"github.com/saladtechnologies/salad-cloud-cli/internal/output"
	"github.com/saladtechnologies/salad-cloud-cli/internal/version"
)

var rootCmd = &cobra.Command{
	Use:   "saladcloud",
	Short: "A thin command-line wrapper over the SaladCloud Public API",
	Long: `saladcloud is a thin command-line wrapper over the SaladCloud Public API, built for people, scripts, CI and AI agents.

Get started:
  saladcloud login     save your API key, organization and project
  saladcloud doctor    check that everything works

Scripts and CI set SALAD_API_KEY, SALAD_ORGANIZATION and SALAD_PROJECT instead of logging in.
Coding agents: ask the user to run saladcloud login in their own terminal; never ask for a key in the chat.

This is a ` + version.Stage + ` release: expect bugs and changes. Report them at ` + version.IssuesURL + `, with the output of saladcloud version.

Configuration resolves per field with precedence flag > environment > config
file. SALAD_AI_GATEWAY_API_KEY sets the AI Gateway key, and
SALAD_CLI_CONFIG_DIR the config file's directory. A saved project applies
only to the saved organization. With no project set, commands use the
organization's "default" project and warn on stderr; if it has none, they
exit 2. Project names are the <project> in
https://portal.salad.com/organizations/<org>/projects/<project>.

Exit codes: 0 ok, 1 error, 2 usage, 3 auth, 4 not found, 5 rate limited,
6 invalid input, 7 network, 10 not available; other: the ssh/sftp child's
exit code from container-groups ssh, sftp or cp (kind child_exit).

With --output json every command emits a single JSON document, except logs,
which streams NDJSON (one JSON object per line), container-groups ssh and
sftp, whose stdout is the remote session's, and ssh --config, completion and
help, which print text; --output yaml emits the same
document as YAML (logs: one YAML document per entry). Errors are written to
stderr as a single-line JSON document: {"error": {...}}, in yaml mode too.

--debug prints each API request (method, URL, status, duration) on stderr;
--debug=body adds headers and bodies, with credentials redacted.`,
	SilenceUsage:  true,
	SilenceErrors: true,
	PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
		fr := readFlags(cmd.Flags())
		m, err := cmd.Flags().GetString("output")
		if err != nil {
			return clierr.New(clierr.CodeUsage, clierr.KindUsage, "%v", err)
		}
		if cerr := output.SetMode(m); cerr != nil {
			return cerr
		}
		d := fr.String("debug")
		if cerr := fr.Err(); cerr != nil {
			return cerr
		}
		switch d {
		case "":
			api.SetDebug(api.DebugOff)
		case "requests":
			api.SetDebug(api.DebugRequests)
		case "body":
			api.SetDebug(api.DebugBodies)
		default:
			return clierr.New(clierr.CodeUsage, clierr.KindUsage, `invalid --debug "%s"`, d).
				WithHint("use --debug or --debug=body")
		}
		return rejectShadowed(cmd)
	},
}

func init() {
	rootCmd.AddCommand(containerGroupsCmd)
	rootCmd.PersistentFlags().StringP("output", "o", "table", "output format: table|json|yaml")
	rootCmd.PersistentFlags().
		String("org", "", "organization name as in the portal URL, not the display name (overrides SALAD_ORGANIZATION and the config file)")
	rootCmd.PersistentFlags().
		String("debug", "", "print each API request (method, URL, status, duration) on stderr; --debug=body adds headers and bodies, credentials redacted")
	rootCmd.PersistentFlags().Lookup("debug").NoOptDefVal = "requests"
	rootCmd.PersistentFlags().
		String("project", "", "project name as in the portal URL, not the display name (overrides SALAD_PROJECT and the config file; with none set, \"default\" is used with a warning)")
	rootCmd.SetFlagErrorFunc(flagError)
	// cobra's multi-line "Did you mean this?" block; withSuggestions gives
	// the one-line form instead.
	rootCmd.DisableSuggestions = true
}

// Root returns the root command so other files' init() can attach
// subcommands (rootCmd.AddCommand) or inspect flags.
func Root() *cobra.Command { return rootCmd }

// Register attaches a subcommand to the root command.
func Register(cmd *cobra.Command) { rootCmd.AddCommand(cmd) }

// containerGroupsCmd is the parent of every command whose subject is a
// container group, so a future compute primitive gets its own noun instead
// of colliding with bare verbs. Plural like the CLI's other nouns (orgs,
// gpus, quotas); the singular and cg are aliases so a guess never fails.
var containerGroupsCmd = &cobra.Command{
	Use:     "container-groups",
	Aliases: []string{"container-group", "cg"},
	Short:   "Create, inspect, and manage container groups and their instances",
	Long: `Create, inspect, and manage container groups and their instances.

cg is short for container-groups: saladcloud cg list lists your container
groups. For logs across the organization or a project, run saladcloud logs.`,
}

// orgScoped marks c and the commands under it organization-scoped: nothing
// there reads the project, so --project is neither offered nor accepted.
func orgScoped(c *cobra.Command) { shadow(c, "project") }

// contextFree marks c and the commands under it as reading neither the
// organization nor the project: --org and --project are neither offered
// nor accepted there.
func contextFree(c *cobra.Command) { shadow(c, "org", "project") }

// shadow gives c hidden flags of these names in place of the global ones,
// so its help does not offer them; rejectShadowed refuses one that is set.
func shadow(c *cobra.Command, names ...string) {
	for _, n := range names {
		if c.PersistentFlags().Lookup(n) == nil {
			c.PersistentFlags().String(n, "", "")
			noteTreeErr(c.PersistentFlags().MarkHidden(n))
		}
	}
}

// rejectShadowed is the unknown-flag error for a global flag cmd shadows
// (orgScoped, contextFree) but was given anyway: the command would ignore it.
func rejectShadowed(cmd *cobra.Command) error {
	for _, n := range []string{"org", "project"} {
		if f := cmd.Flags().Lookup(n); f != nil && f.Hidden && f.Changed {
			why := "is organization-scoped and takes no --project"
			if !readsOrg(cmd) {
				why = "takes neither --org nor --project"
			}
			return clierr.New(clierr.CodeUsage, clierr.KindUsage, "unknown flag: --%s", n).
				WithHint("%s %s; run %s, or to list its flags, run %s --help", strings.TrimPrefix(cmd.CommandPath(), "saladcloud "), why, usageLine(cmd), cmd.CommandPath())
		}
	}
	return nil
}

// RegisterContainerGroups adds cmd under 'saladcloud container-groups'.
func RegisterContainerGroups(cmd *cobra.Command) { containerGroupsCmd.AddCommand(cmd) }

// Execute runs the CLI and returns the process exit code. Errors are
// printed to stderr: in json mode as a one-line {"error": {...}} document,
// in table mode as "saladcloud: error: <message>" and, when the error has a
// fix, "hint: <fix>" under it.
func Execute() int {
	prepareTree()
	registerCompletions(rootCmd)
	if err := errors.Join(treeErrs...); err != nil {
		ce := clierr.New(clierr.CodeGeneric, clierr.KindInternal, "the command tree is incomplete: %v", err)
		printError(ce)
		return ce.Code
	}
	// One signal handler for the whole run: Ctrl-C cancels the root
	// context. A wait sees that and fails with kind interrupted; a request
	// in flight ends as "<command> canceled", also interrupted; an
	// open-ended command (logs --follow, --watch) stops cleanly with exit 0.
	// After the first signal the default handler is back, so a second
	// Ctrl-C kills the process.
	ctx, stop := signal.NotifyContext(baseContext(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		stop()
	}()
	rootCmd.SetArgs(commandLine)
	cmd, err := rootCmd.ExecuteContextC(ctx)
	// cobra keeps the context on every command it ran; clear it so a later
	// in-process run (tests) does not inherit this run's canceled one.
	for c := cmd; c != nil; c = c.Parent() {
		c.SetContext(nil) //nolint:staticcheck // nil is what cobra checks for
	}
	if err == nil {
		return clierr.CodeOK
	}
	adoptOutputFlag(cmd)
	adoptOutputArg(commandLine)
	var ce *clierr.Error
	if !errors.As(err, &ce) {
		// Anything that escapes without a *clierr.Error came from cobra's
		// own parsing (unknown command, bad args, flag errors that bypass
		// the flag error func) — those are usage errors by definition,
		// because command RunE funcs always return *clierr.Error.
		ce = suggestUnknownCommand(rootCmd, err.Error(), commandLine)
	}
	if canceledByCtrlC(ctx, ce) {
		ce = clierr.New(clierr.CodeGeneric, clierr.KindInterrupted, "%s canceled", commandPath(cmd))
	}
	printError(ce)
	return ce.Code
}

// treeErrs are the errors from building the command tree. Marking a flag
// required or hidden, or giving it completion, fails only for a flag the
// command does not define or completion given twice: a bug, which Execute
// reports before it runs anything.
var treeErrs []error

// noteTreeErr records an error from building the command tree.
func noteTreeErr(err error) {
	if err != nil {
		treeErrs = append(treeErrs, err)
	}
}

// baseContext is the context a run starts from. Tests replace it to cancel a
// run the way Ctrl-C does.
var baseContext = context.Background

// canceledByCtrlC reports whether ce is a request Ctrl-C stopped, or any
// failure that followed Ctrl-C: either way the command was canceled. A
// command's own interrupted error (a wait, a prompt, a config writer saying
// nothing changed) keeps its message, and an ssh, sftp or cp child keeps its
// exit code.
func canceledByCtrlC(ctx context.Context, ce *clierr.Error) bool {
	if ce.IsCancel() {
		return true
	}
	return ctx.Err() != nil && ce.Kind != clierr.KindInterrupted && ce.Kind != clierr.KindChildExit
}

// commandPath is cmd's full path without the binary's name, for example
// "container-groups list" (aliases resolve to the command's own name).
func commandPath(cmd *cobra.Command) string {
	if cmd == nil || cmd == rootCmd {
		return rootCmd.Name()
	}
	return strings.TrimPrefix(cmd.CommandPath(), rootCmd.Name()+" ")
}

// prepareTree finishes the command tree before a run. cobra adds its
// completion command inside ExecuteC; adding it first lets requireVerbs make
// a bare 'completion' a usage error like every other noun, and wrapArgs give
// its verbs the CLI's usage messages.
func prepareTree() {
	rootCmd.InitDefaultCompletionCmd()
	for _, c := range rootCmd.Commands() {
		if contextFreeCommands[c.Name()] {
			contextFree(c)
		}
	}
	requireVerbs(rootCmd)
	wrapArgs(rootCmd)
}

// contextFreeCommands are the top-level commands that read neither the
// organization nor the project, with everything under them.
var contextFreeCommands = map[string]bool{
	"agent": true, "ai-gateway": true, "changelog": true, "completion": true, "config": true,
	"logout": true, "orgs": true, "ssh-key": true, "use": true, "version": true,
}

// adoptOutputFlag sets the output mode from the --output value cobra parsed
// for cmd when PersistentPreRunE never got to: cobra validates positional
// arguments before the pre-run, so without this an argument error would
// report in table form under -o json. An invalid value is left alone, so
// that error still reports in table form. Errors raised before the flag was
// parsed are adoptOutputArg's.
func adoptOutputFlag(cmd *cobra.Command) {
	if cmd == nil || output.Machine() {
		return
	}
	if f := cmd.Flag("output"); f != nil && f.Changed {
		if cerr := output.SetMode(f.Value.String()); cerr != nil {
			return // an invalid -o keeps table mode, the form its own error reports in
		}
	}
}

// adoptOutputArg sets the output mode from -o/--output on the raw command
// line, for errors cobra raises before it parses that flag: an unknown
// command (whose did-you-mean an agent must be able to read), or a bad flag
// ahead of -o. The last valid value wins, as with the flag itself; words
// after "--" are arguments, not flags; an invalid value is ignored, so that
// error reports in table form.
func adoptOutputArg(args []string) {
	if output.Machine() {
		return
	}
	mode := ""
	for i := 0; i < len(args); i++ {
		a := args[i]
		var v string
		switch {
		case a == "--":
			i = len(args)
			continue
		case a == "-o" || a == "--output":
			if i+1 >= len(args) {
				continue
			}
			i++
			v = args[i]
		case strings.HasPrefix(a, "--output="):
			v = strings.TrimPrefix(a, "--output=")
		case strings.HasPrefix(a, "-o") && !strings.HasPrefix(a, "--"):
			v = strings.TrimPrefix(strings.TrimPrefix(a, "-o"), "=")
		default:
			continue
		}
		if v == "json" || v == "yaml" || v == "table" {
			mode = v
		}
	}
	if mode != "" {
		if cerr := output.SetMode(mode); cerr != nil {
			return // not reached: mode is one of the three modes
		}
	}
}

// commandLine is the command line Execute runs. Tests replace it.
var commandLine = os.Args[1:]

// errOut is where Execute writes errors. Tests may swap it.
var errOut io.Writer = os.Stderr

func printError(e *clierr.Error) {
	// Errors are the one-line JSON document in every machine mode, yaml
	// included, so a script parses one error shape.
	if output.Machine() {
		if payload, err := output.Marshal(map[string]*clierr.Error{"error": e}); err == nil {
			fprintln(errOut, string(output.ASCII(payload)))
			return
		}
	}
	fprintln(errOut, "saladcloud: error: "+e.Message)
	if h := e.HintText(); h != "" {
		fprintln(errOut, "hint: "+h)
	}
}

// wrapErr coerces any error into a *clierr.Error so nothing escapes a RunE
// as a plain error (Execute would then misreport it as a usage error).
func wrapErr(err error) error {
	if err == nil {
		return nil
	}
	var ce *clierr.Error
	if errors.As(err, &ce) {
		return ce
	}
	return clierr.New(clierr.CodeGeneric, clierr.KindAPIError, "%v", err)
}

// NewClient is the helper command files use to build an API client: it
// reads the --org/--project flags, resolves configuration, and constructs
// the client (which requires an API key but not org/project).
func NewClient(cmd *cobra.Command) (*api.Client, config.Resolved, *clierr.Error) {
	fr := readFlags(cmd.Flags())
	orgFlag := fr.String("org")
	projectFlag := fr.String("project")
	if cerr := fr.Err(); cerr != nil {
		return nil, config.Resolved{}, cerr
	}
	res, err := config.Resolve(orgFlag, projectFlag)
	if err != nil {
		path := configPath()
		return nil, config.Resolved{}, clierr.New(clierr.CodeGeneric, clierr.KindAPIError, "cannot read the config file %s: %s", path, readReason(err)).
			WithHint("fix or delete it")
	}
	// A name from a flag or variable that cannot be a URL name fails before
	// any request, and so does a saved one that looks like a key; each only
	// where the command reads it.
	for _, n := range []struct {
		kind, name, source, flag, env string
		reads                         bool
	}{
		{"organization", res.Organization, res.OrganizationSource, "--org", envOrganization, readsOrg(cmd)},
		{"project", res.Project, res.ProjectSource, "--project", envProject, readsProject(cmd)},
	} {
		switch {
		case !n.reads || n.name == "":
		case n.source == "flag" || n.source == "env":
			if cerr := checkName(n.kind, n.name, originOf(n.source, n.flag, n.env)); cerr != nil {
				return nil, res, cerr
			}
		case n.source == "config" && looksLikeKey(n.name):
			return nil, res, keyLikeName(n.kind, "config")
		}
	}
	keyFrom := originOf(res.APIKeySource, "", envAPIKey)
	if res.APIKey != "" {
		key, ok := normalizeKey(res.APIKey)
		if !ok {
			return nil, res, malformedKey(labelAPIKey, key, keyFrom)
		}
		res.APIKey = key
	}
	assumedWarned.Store(false)
	client, cerr := api.New(res)
	if cerr != nil {
		return nil, res, cerr
	}
	warnOnAssumedProject(client, res)
	saved := savedConfig()
	client.OnRejected = func() *clierr.Error { return keyRejected(res.APIKey, keyFrom, saved.APIKey) }
	ctx := cmd.Context()
	client.OnOrgGone = func() *clierr.Error { return organizationGone(ctx, res) }
	return client, res, nil
}

// readsProject reports whether cmd uses the project: organization-scoped
// commands hide the --project they cannot use.
func readsProject(cmd *cobra.Command) bool {
	f := cmd.Flags().Lookup("project")
	return f != nil && !f.Hidden
}

// readsOrg reports whether cmd reads the organization: --org is its own,
// not shadowed by contextFree.
func readsOrg(cmd *cobra.Command) bool {
	f := cmd.Flags().Lookup("org")
	return f != nil && !f.Hidden
}

// keyRejected is the run-time error for an API key the API rejected (401),
// with the fix for where it came from.
func keyRejected(key, from, saved string) *clierr.Error {
	e := clierr.New(clierr.CodeAuth, clierr.KindAuth, "API key %s%s was rejected", maskAPIKey(key), fromTag(from)).
		WithStatus(http.StatusUnauthorized)
	switch {
	case quotedKey(key):
		return e.WithHint("remove the quotes around it")
	case from != envAPIKey:
		return e.WithHint("run saladcloud login to save a new key")
	case saved != "" && saved != key:
		return e.WithHint(
			"remove SALAD_API_KEY to use the saved API key (%s), or set it to a working key",
			maskAPIKey(saved),
		)
	}
	return e.WithHint("set SALAD_API_KEY to a working key, or remove it and run saladcloud login")
}

// organizationGone is the run-time error for an organization the key cannot
// see (a 404 under it, confirmed by the organization check): it names the
// organization and its source, and the fix there; at a terminal the hint
// lists your organizations.
func organizationGone(ctx context.Context, res config.Resolved) *clierr.Error {
	from := originOf(res.OrganizationSource, "--org", envOrganization)
	e := clierr.New(clierr.CodeNotFound, clierr.KindNotFound, `organization "%s"%s not found`, res.Organization, fromTag(from)).
		WithStatus(http.StatusNotFound)
	var names []string
	if atTerminal() {
		if c, cerr := api.New(
			config.Resolved{
				APIKey:              res.APIKey,
				APIKeySource:        "",
				Organization:        "",
				OrganizationSource:  "",
				Project:             "",
				ProjectSource:       "",
				GatewayAPIKey:       "",
				GatewayAPIKeySource: "",
				SkippedProject:      "",
				SkippedProjectOrg:   "",
			},
		); cerr == nil {
			if list, lerr := c.OrganizationListing(ctx); lerr == nil && list != nil {
				names = list.OrgNames()
			}
		}
	}
	fix := ""
	switch from {
	case envOrganization:
		fix = "fix or remove SALAD_ORGANIZATION"
	case "config":
		fix = "to switch, run saladcloud use <org>"
	}
	if h := hintClauses(orgsClause(names), fix); h != "" {
		e = e.WithHint("%s", h)
	}
	return e
}

// assumedWarned keeps the assumed-project warning to one line per command,
// however many clients the command builds (a --from source has its own).
var assumedWarned atomic.Bool

// warnOnAssumedProject makes c print the assumed-project warning, once per
// command, the first time it uses an assumed project.
func warnOnAssumedProject(c *api.Client, res config.Resolved) {
	c.OnAssumedProject = func() {
		if assumedWarned.CompareAndSwap(false, true) {
			fprintln(os.Stderr, assumedProjectWarning(res))
		}
	}
}

// assumedProjectWarning is the stderr line a project-scoped command prints
// when its project is the "default" fallback rather than one that was set.
func assumedProjectWarning(res config.Resolved) string {
	if res.SkippedProject != "" {
		return fmt.Sprintf(
			`warning: using project "%s": none was set for organization "%s" (the saved project "%s" is for "%s"); pass --project or set SALAD_PROJECT`,
			res.Project,
			res.Organization,
			shownName(res.SkippedProject),
			shownName(res.SkippedProjectOrg),
		)
	}
	return fmt.Sprintf(
		`warning: using project "%s" because none was set; pass --project or set SALAD_PROJECT to choose one`,
		res.Project,
	)
}
