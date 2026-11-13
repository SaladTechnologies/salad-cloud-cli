// Package cli wires up the saladcloud command tree, global flags, and the
// error-to-exit-code mapping.
package cli

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/saladtechnologies/salad-cloud-cli/internal/api"
	"github.com/saladtechnologies/salad-cloud-cli/internal/clierr"
	"github.com/saladtechnologies/salad-cloud-cli/internal/config"
	"github.com/saladtechnologies/salad-cloud-cli/internal/output"
	"github.com/saladtechnologies/salad-cloud-cli/internal/version"
)

// The global flags whose definitions root.go owns and other commands read or shadow, and the --output modes.
const (
	globalFlagOrg     = "org"
	globalFlagProject = "project"
	globalOutputTable = "table"
	globalOutputJSON  = "json"
	globalOutputYAML  = "yaml"
)

// The sources config.Resolve records for a resolved value.
const (
	resolveSourceFlag   = "flag"
	resolveSourceEnv    = "env"
	resolveSourceConfig = "config"
)

// The top-level commands whose names more than one file spells.
const (
	cmdNameConfig = "config"
	cmdNameLogout = "logout"
)

// The kinds of context name checkName checks.
const (
	contextKindOrganization = "organization"
	contextKindProject      = "project"
)

// newRootCmd is the root command with the global flags.
func (app *App) newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "saladcloud",
		Short: "A thin command-line wrapper over the SaladCloud Public API",
		Long: `saladcloud is a thin command-line wrapper over the SaladCloud Public API, ` +
			`built for people, scripts, CI and AI agents.

Get started:
  saladcloud login     save your API key, organization and project
  saladcloud doctor    check that everything works

Scripts and CI set SALAD_API_KEY, SALAD_ORGANIZATION and SALAD_PROJECT instead of logging in.
Coding agents: read saladcloud agent init --print for this CLI's rules; ask the user to run
saladcloud login in their own terminal; never ask for a key in the chat.

A command takes what it acts on as its one argument and every resource above
it as a flag (--org, --project, -c <group>): container-groups instances list
-c <group>. container-groups ssh, sftp and logs take the group as the argument
and the instance as --instance.

This is a ` + version.Stage + ` release: expect bugs and changes. Report them at ` + version.IssuesURL +
			`, with the output of saladcloud version.

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
			if cerr := app.printer.SetMode(app.flags.output); cerr != nil {
				return cerr
			}
			d := app.flags.debug
			switch d {
			case "":
				app.debugLevel = api.DebugOff
			case "requests":
				app.debugLevel = api.DebugRequests
			case "body":
				app.debugLevel = api.DebugBodies
			default:
				return clierr.New(clierr.CodeUsage, clierr.KindUsage, `invalid --debug "%s"`, d).
					WithHint("use --debug or --debug=body")
			}
			return rejectShadowed(cmd)
		},
	}
	root.PersistentFlags().
		StringVarP(&app.flags.output, "output", "o", globalOutputTable, "output format: table|json|yaml")
	root.PersistentFlags().StringVar(&app.flags.org, globalFlagOrg, "",
		"organization name as in the portal URL, not the display name "+
			"(overrides SALAD_ORGANIZATION and the config file)")
	root.PersistentFlags().StringVar(&app.flags.debug, "debug", "",
		"print each API request (method, URL, status, duration) on stderr; "+
			"--debug=body adds headers and bodies, credentials redacted")
	root.PersistentFlags().Lookup("debug").NoOptDefVal = "requests"
	root.PersistentFlags().StringVar(&app.flags.project, globalFlagProject, "",
		"project name as in the portal URL, not the display name "+
			"(overrides SALAD_PROJECT and the config file; with none set, \"default\" is used with a warning)")
	root.SetFlagErrorFunc(flagError)
	// cobra's multi-line "Did you mean this?" block; withSuggestions gives
	// the one-line form instead.
	root.DisableSuggestions = true
	return root
}

// containerGroupsCmd is the parent of every command whose subject is a
// container group, so a future compute primitive gets its own noun instead
// of colliding with bare verbs. Plural like the CLI's other nouns (orgs,
// gpus, quotas); the singular and cg are aliases so a guess never fails.
func newContainerGroupsCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "container-groups",
		Aliases: []string{"container-group", "cg"},
		Short:   "Create, inspect, and manage container groups and their instances",
		Long: `Create, inspect, and manage container groups and their instances.

cg is short for container-groups: saladcloud cg list lists your container
groups. For logs across the organization or a project, run saladcloud logs.`,
	}
}

// orgScoped marks c and the commands under it organization-scoped: nothing
// there reads the project, so --project is neither offered nor accepted.
func (app *App) orgScoped(c *cobra.Command) { app.shadow(c, globalFlagProject) }

// contextFree marks c and the commands under it as reading neither the
// organization nor the project: --org and --project are neither offered
// nor accepted there.
func (app *App) contextFree(c *cobra.Command) { app.shadow(c, globalFlagOrg, globalFlagProject) }

// shadow gives c hidden flags of these names in place of the global ones,
// so its help does not offer them; rejectShadowed refuses one that is set.
func (app *App) shadow(c *cobra.Command, names ...string) {
	for _, n := range names {
		if c.PersistentFlags().Lookup(n) == nil {
			c.PersistentFlags().StringVar(app.contextFlag(n), n, "", "")
			app.noteTreeErr(c.PersistentFlags().MarkHidden(n))
		}
	}
}

// contextFlag is the field --org or --project binds to.
func (app *App) contextFlag(name string) *string {
	if name == globalFlagOrg {
		return &app.flags.org
	}
	return &app.flags.project
}

// rejectShadowed is the unknown-flag error for a global flag cmd shadows
// (orgScoped, contextFree) but was given anyway: the command would ignore it.
func rejectShadowed(cmd *cobra.Command) error {
	for _, n := range []string{globalFlagOrg, globalFlagProject} {
		if f := cmd.Flags().Lookup(n); f != nil && f.Hidden && f.Changed {
			why := "is organization-scoped and takes no --project"
			if !readsOrg(cmd) {
				why = "takes neither --org nor --project"
			}
			return clierr.New(clierr.CodeUsage, clierr.KindUsage, "unknown flag: --%s", n).
				WithHint(
					"%s %s; run %s, or to list its flags, run %s --help",
					strings.TrimPrefix(cmd.CommandPath(), "saladcloud "),
					why,
					usageLine(cmd),
					cmd.CommandPath(),
				)
		}
	}
	return nil
}

// New builds the command tree for one run.
func New(app *App) *cobra.Command {
	root := app.newRootCmd()
	cg := newContainerGroupsCmd()
	root.AddCommand(cg)
	app.addAgentCommands(root)
	app.addAvailabilityCommands(root)
	app.addChangelogCommands(root)
	app.addConfigCommands(root)
	app.addCpCommands(cg)
	app.addCreateCommands(cg)
	app.addDoctorCommands(root)
	app.addGatewayCommands(root)
	app.addGpusCommands(root)
	app.addGroupsCommands(cg)
	app.addInferenceCommands(root)
	app.addInstancesCommands(cg)
	app.addLoginCommands(root)
	app.addLogoutCommands(root)
	app.addLogsCommands(root, cg)
	app.addOrgsCommands(root)
	app.addQueuesCommands(root)
	app.addQuotasCommands(root)
	app.addScaleCommands(cg)
	app.addSFTPCommands(cg)
	app.addSSHCommands(cg)
	app.addSSHKeyCommands(root)
	app.addUseCommands(root)
	app.addVersionCommands(root)
	app.addWebhookSecretCommands(root)
	app.addWhoamiCommands(root)
	app.prepareTree(root)
	app.registerCompletions(root)
	return root
}

// Run runs one command line and returns the process exit code. Errors are
// printed to stderr: in json mode as a one-line {"error": {...}} document,
// in table mode as "saladcloud: error: <message>" and, when the error has a
// fix, "hint: <fix>" under it.
func Run(ctx context.Context, d *Deps) int {
	app := newApp(d)
	rootCmd := New(app) //nolint:contextcheck // New only builds the tree; commands get ctx from ExecuteContextC below
	if err := errors.Join(app.treeErrs...); err != nil {
		ce := clierr.New(clierr.CodeGeneric, clierr.KindInternal, "the command tree is incomplete: %v", err)
		app.printError(ce)
		return ce.Code
	}
	rootCmd.SetArgs(app.Args)
	cmd, err := rootCmd.ExecuteContextC(ctx)
	if err == nil {
		return clierr.CodeOK
	}
	app.adoptOutputFlag(cmd)
	app.adoptOutputArg(app.Args)
	var ce *clierr.Error
	if !errors.As(err, &ce) {
		// Anything that escapes without a *clierr.Error came from cobra's
		// own parsing (unknown command, bad args, flag errors that bypass
		// the flag error func) — those are usage errors by definition,
		// because command RunE funcs always return *clierr.Error.
		ce = suggestUnknownCommand(rootCmd, err.Error(), app.Args)
	}
	if canceledByCtrlC(ctx, ce) {
		ce = clierr.New(clierr.CodeGeneric, clierr.KindInterrupted, "%s canceled", commandPath(cmd))
	}
	app.printError(ce)
	return ce.Code
}

// noteTreeErr records an error from building the command tree.
func (app *App) noteTreeErr(err error) {
	if err != nil {
		app.treeErrs = append(app.treeErrs, err)
	}
}

// apiOptions is how the run's API clients are built. Every client is built from them, so this is where the run
// warns, once, about base-URL variables that send its requests to another machine.
func (app *App) apiOptions() api.Options {
	app.warnRemoteBaseURLs()
	return api.Options{Getenv: app.Env.Getenv, Debug: app.debugLevel, DebugOut: app.errw(), Backoff: nil, Now: nil}
}

// warnRemoteBaseURLs writes one warning: line, in every output mode, for each base-URL variable that names a
// server on another machine: that server is sent the key. The variables are test knobs, so one set in a shell
// that is not running a test was most likely put there by something else (a project's env file, a CI setting).
func (app *App) warnRemoteBaseURLs() {
	overrides := api.RemoteBaseURLs(app.Env.Getenv)
	if len(overrides) == 0 || !app.baseURLWarned.CompareAndSwap(false, true) {
		return
	}
	for _, o := range overrides {
		fprintf(app.errw(), "warning: %s is set, so requests and the key they carry go to %s, not to SaladCloud\n",
			o.Env, strconv.Quote(o.Host))
	}
}

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
	if cmd == nil || !cmd.HasParent() {
		return "saladcloud"
	}
	return strings.TrimPrefix(cmd.CommandPath(), cmd.Root().Name()+" ")
}

// prepareTree finishes the command tree before a run. cobra adds its
// completion command inside ExecuteC; adding it first lets requireVerbs make
// a bare 'completion' a usage error like every other noun, and wrapArgs give
// its verbs the CLI's usage messages.
func (app *App) prepareTree(root *cobra.Command) {
	root.InitDefaultCompletionCmd()
	for _, c := range root.Commands() {
		if contextFreeCommands()[c.Name()] {
			app.contextFree(c)
		}
	}
	requireVerbs(root)
	app.wrapArgs(root)
}

// contextFreeCommands are the top-level commands that read neither the
// organization nor the project, with everything under them.
func contextFreeCommands() map[string]bool {
	return map[string]bool{
		"agent": true, "ai-gateway": true, "changelog": true, "completion": true, cmdNameConfig: true,
		cmdNameLogout: true, "orgs": true, "ssh-key": true, "use": true, "version": true,
	}
}

// adoptOutputFlag sets the output mode from the --output value cobra parsed
// for cmd when PersistentPreRunE never got to: cobra validates positional
// arguments before the pre-run, so without this an argument error would
// report in table form under -o json. An invalid value is left alone, so
// that error still reports in table form. Errors raised before the flag was
// parsed are adoptOutputArg's.
func (app *App) adoptOutputFlag(cmd *cobra.Command) {
	if cmd == nil || app.printer.Machine() {
		return
	}
	if f := cmd.Flag("output"); f != nil && f.Changed {
		if cerr := app.printer.SetMode(f.Value.String()); cerr != nil {
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
func (app *App) adoptOutputArg(args []string) {
	if app.printer.Machine() {
		return
	}
	if mode := lastOutputArg(args); mode != "" {
		if cerr := app.printer.SetMode(mode); cerr != nil {
			return // not reached: mode is one of the three modes
		}
	}
}

// lastOutputArg is the last valid -o/--output value on the raw command line args, ignoring the words after "--",
// or "" when there is none.
func lastOutputArg(args []string) string {
	mode := ""
	for i := 0; i < len(args); i++ {
		if args[i] == "--" {
			break
		}
		v, consumed := outputArgValue(args[i], args[i+1:])
		i += consumed
		if v == globalOutputJSON || v == globalOutputYAML || v == globalOutputTable {
			mode = v
		}
	}
	return mode
}

// outputArgValue is the -o/--output value the argument a carries, or for a bare -o or --output, the next argument,
// the first of rest, with 1 for the one argument it consumed (0 otherwise). An argument that is not -o carries "".
func outputArgValue(a string, rest []string) (string, int) {
	switch {
	case a == "-o" || a == "--output":
		if len(rest) == 0 {
			return "", 0
		}
		return rest[0], 1
	case strings.HasPrefix(a, "--output="):
		return strings.TrimPrefix(a, "--output="), 0
	case strings.HasPrefix(a, "-o") && !strings.HasPrefix(a, "--"):
		return strings.TrimPrefix(strings.TrimPrefix(a, "-o"), "="), 0
	}
	return "", 0
}

func (app *App) printError(e *clierr.Error) {
	// Errors are the one-line JSON document in every machine mode, yaml
	// included, so a script parses one error shape.
	if app.printer.Machine() {
		if payload, err := output.Marshal(map[string]*clierr.Error{"error": e}); err == nil {
			fprintln(app.Stderr, string(output.ASCII(payload)))
			return
		}
	}
	fprintln(app.Stderr, "saladcloud: error: "+e.Message)
	if h := e.HintText(); h != "" {
		fprintln(app.Stderr, "hint: "+h)
	}
}

// wrapErr coerces any error into a *clierr.Error so nothing escapes a RunE
// as a plain error (Execute would then misreport it as a usage error).
func wrapErr(err error) error {
	if err == nil {
		return nil
	}
	if ce, ok := errors.AsType[*clierr.Error](err); ok {
		return ce
	}
	return clierr.New(clierr.CodeGeneric, clierr.KindAPIError, "%v", err)
}

// NewClient is the helper command files use to build an API client: it
// reads the --org/--project flags, resolves configuration, and constructs
// the client (which requires an API key but not org/project).
func (app *App) NewClient(cmd *cobra.Command) (*api.Client, config.Resolved, *clierr.Error) {
	res, err := config.Resolve(app.Env, app.flags.org, app.flags.project)
	if err != nil {
		path := app.configPath()
		return nil, config.Resolved{}, clierr.New(
			clierr.CodeGeneric,
			clierr.KindAPIError,
			"cannot read the config file %s: %s",
			path,
			readReason(err),
		).WithHint("fix or delete it")
	}
	if cerr := app.newClientCheckNames(cmd, &res); cerr != nil {
		return nil, res, cerr
	}
	keyFrom := originOf(res.APIKeySource, "", envAPIKey)
	if res.APIKey != "" {
		key, ok := normalizeKey(res.APIKey)
		if !ok {
			return nil, res, malformedKey(labelAPIKey, key, keyFrom)
		}
		res.APIKey = key
	}
	app.assumedWarned.Store(false)
	client, cerr := api.New(&res, app.apiOptions())
	if cerr != nil {
		return nil, res, cerr
	}
	app.hookClient(cmd.Context(), client, &res)
	return client, res, nil
}

// hookClient gives client, built from res, the checks every command's client makes: the assumed-project warning, the
// error for a rejected key, and the organization check after a 404 under the organization, so a client for another
// project reports a key or an organization the way the command's own client does.
func (app *App) hookClient(ctx context.Context, client *api.Client, res *config.Resolved) {
	app.warnOnAssumedProject(client, res)
	keyFrom := originOf(res.APIKeySource, "", envAPIKey)
	saved := app.savedConfig()
	client.OnRejected = func() *clierr.Error { return keyRejected(res.APIKey, keyFrom, saved.APIKey) }
	client.OnOrgGone = func() *clierr.Error { return app.organizationGone(ctx, res) }
}

// newClientCheckNames is NewClient's check of the resolved names: a name from a flag or variable that cannot be a URL
// name fails before any request, and so does a saved one that looks like a key; each only where the command reads
// it.
func (app *App) newClientCheckNames(cmd *cobra.Command, res *config.Resolved) *clierr.Error {
	for _, n := range []struct {
		kind, name, source, flag, env string
		reads                         bool
	}{
		{
			contextKindOrganization, res.Organization, res.OrganizationSource,
			"--" + globalFlagOrg, envOrganization, readsOrg(cmd),
		},
		{
			contextKindProject, res.Project, res.ProjectSource,
			"--" + globalFlagProject, envProject, readsProject(cmd),
		},
	} {
		switch {
		case !n.reads || n.name == "":
		case n.source == resolveSourceFlag || n.source == resolveSourceEnv:
			if cerr := app.checkName(n.kind, n.name, originOf(n.source, n.flag, n.env)); cerr != nil {
				return cerr
			}
		case n.source == resolveSourceConfig && app.looksLikeKey(n.name):
			return keyLikeName(n.kind, resolveSourceConfig)
		}
	}
	return nil
}

// readsProject reports whether cmd uses the project: organization-scoped
// commands hide the --project they cannot use.
func readsProject(cmd *cobra.Command) bool {
	f := cmd.Flag(globalFlagProject) // as readsOrg
	return f != nil && !f.Hidden
}

// readsOrg reports whether cmd reads the organization: --org is its own,
// not shadowed by contextFree.
func readsOrg(cmd *cobra.Command) bool {
	f := cmd.Flag(globalFlagOrg) // the nearest definition, merged into cmd.Flags() yet or not
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
func (app *App) organizationGone(ctx context.Context, res *config.Resolved) *clierr.Error {
	from := originOf(res.OrganizationSource, "--org", envOrganization)
	e := clierr.New(
		clierr.CodeNotFound,
		clierr.KindNotFound,
		`organization "%s"%s not found`,
		res.Organization,
		fromTag(from),
	).WithStatus(http.StatusNotFound)
	var names []string
	if app.atTerminal() {
		names = app.keyOrgNames(ctx, res.APIKey)
	}
	fix := ""
	switch from {
	case envOrganization:
		fix = "fix or remove SALAD_ORGANIZATION"
	case resolveSourceConfig:
		fix = "to switch, run saladcloud use <org>"
	}
	if h := hintClauses(app.orgsClause(names), fix); h != "" {
		e = e.WithHint("%s", h)
	}
	return e
}

// keyOrgNames is the names of the organizations key can see, nil when they cannot be listed.
func (app *App) keyOrgNames(ctx context.Context, key string) []string {
	c, cerr := api.New(
		&config.Resolved{
			APIKey:              key,
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
		app.apiOptions(),
	)
	if cerr != nil {
		return nil
	}
	list, lerr := c.OrganizationListing(ctx)
	if lerr != nil || list == nil {
		return nil
	}
	return list.OrgNames()
}

// warnOnAssumedProject makes c print the assumed-project warning, once per
// command, the first time it uses an assumed project.
func (app *App) warnOnAssumedProject(c *api.Client, resolved *config.Resolved) {
	res := *resolved // the configuration as of this call; the warning prints later
	c.OnAssumedProject = func() {
		if app.assumedWarned.CompareAndSwap(false, true) {
			fprintln(app.errw(), app.assumedProjectWarning(&res))
		}
	}
}

// assumedProjectWarning is the stderr line a project-scoped command prints
// when its project is the "default" fallback rather than one that was set.
func (app *App) assumedProjectWarning(res *config.Resolved) string {
	if res.SkippedProject != "" {
		return fmt.Sprintf(
			`warning: using project "%s": none was set for organization "%s" `+
				`(the saved project "%s" is for "%s"); pass --project or set SALAD_PROJECT`,
			res.Project,
			res.Organization,
			app.shownName(res.SkippedProject),
			app.shownName(res.SkippedProjectOrg),
		)
	}
	return fmt.Sprintf(
		`warning: using project "%s" because none was set; `+
			`pass --project or set SALAD_PROJECT to choose one`,
		res.Project,
	)
}
