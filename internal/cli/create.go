// The create command: build a container group from flags, a definition
// file, a copy of an existing group (--from, see createfrom.go), or any
// combination (base from --from and/or -f, flags applied on top,
// docker-run style).

package cli

import (
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"

	"github.com/saladtechnologies/salad-cloud-sdk-go/pkg/containergroups"
	"github.com/saladtechnologies/salad-cloud-sdk-go/pkg/organizationdata"
	"github.com/saladtechnologies/salad-cloud-sdk-go/pkg/shared"
	"github.com/saladtechnologies/salad-cloud-sdk-go/pkg/util"
	"github.com/spf13/cobra"

	"github.com/saladtechnologies/salad-cloud-cli/internal/api"
	"github.com/saladtechnologies/salad-cloud-cli/internal/clierr"
	"github.com/saladtechnologies/salad-cloud-cli/internal/input"
	"github.com/saladtechnologies/salad-cloud-cli/internal/output"
	"github.com/saladtechnologies/salad-cloud-cli/internal/ptr"
)

// createFlags carries the create command's flag values into
// buildCreateRequest. The *Set fields record whether the flag was actually
// passed (cobra's Changed), so base-provided values are only overridden
// deliberately and defaults only fill fields nothing else set.
type createFlags struct {
	Name             string // positional <group>; "" keeps the base's name
	DisplayName      string
	Image            string
	CPU              int64
	CPUSet           bool
	Memory           int64
	MemorySet        bool
	Storage          int64 // GB
	StorageSet       bool
	Countries        []string
	Queue            string
	QueuePort        int64
	QueuePortSet     bool
	QueuePath        string
	Replicas         int64
	ReplicasSet      bool
	Gpus             []string
	Env              []string
	Port             int64
	PortSet          bool
	NoAuth           bool
	Priority         string
	RestartPolicy    string
	RestartPolicySet bool
	NoAutostart      bool
	// Command is the container command after "--"; nil when none was given
	// (validateCommand has already rejected "--" with nothing after it).
	Command []string
	// HasBase is true when the request started from -f; without
	// a base, --image is required.
	HasBase bool
	// ResolveGpu maps a non-UUID --gpu value to a GPU class UUID. Injected
	// so buildCreateRequest stays pure and tests stay offline; only called
	// when --gpu is given with a value that is not already a UUID.
	ResolveGpu func(value string) (string, *clierr.Error)
}

var uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// isUUID reports whether s is an 8-4-4-4-12 hex UUID.
func isUUID(s string) bool { return uuidRe.MatchString(s) }

// groupNameRe is the API's container group name rule (the SDK's `pattern`
// tag on name): 2-63 chars — the pattern's own {0,61} core implies the
// length bounds — lowercase letters/digits/hyphens, starting with a letter
// and ending with a letter or digit.
var groupNameRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,61}[a-z0-9]$`)

// validateGroupName pre-validates a group name client-side so a bad name
// fails fast (exit 6, the API's own rule) before any request.
// validateWaitAutostart rejects --wait together with --no-autostart: a group
// created stopped never reaches "running", so the wait could only ever time
// out. Pre-flight, before any file read or API call.
func validateWaitAutostart(wait, noAutostart bool) *clierr.Error {
	if wait && noAutostart {
		return clierr.New(clierr.CodeUsage, clierr.KindUsage,
			"--wait cannot be combined with --no-autostart (a group created stopped never becomes running)")
	}
	return nil
}

func validateGroupName(name string) *clierr.Error { return validateResourceName("group", name) }

// validateResourceName applies the API's resource name rule (shared by
// container groups and queues) to name; noun names the resource in the
// message.
func validateResourceName(noun, name string) *clierr.Error {
	if !groupNameRe.MatchString(name) {
		return clierr.New(clierr.CodeInvalidInput, clierr.KindInvalidInput,
			`invalid %s name "%s"`, noun, name).
			WithHint("use 2-63 characters: lowercase letters, digits and hyphens, starting with a letter and ending with a letter or digit")
	}
	return nil
}

// buildCreateRequest applies create's flags on top of a base request
// (from -f or empty), then fills defaults into fields that are
// still unset. It is pure: GPU name resolution goes through opts.ResolveGpu.
func buildCreateRequest(
	base containergroups.ContainerGroupCreationRequest,
	opts createFlags,
) (containergroups.ContainerGroupCreationRequest, *clierr.Error) {
	req := base

	if !opts.HasBase && opts.Image == "" {
		return req, clierr.New(clierr.CodeUsage, clierr.KindUsage,
			"missing required flag: --image <image> (or start from a base definition with -f <file>)")
	}

	container := func() *containergroups.ContainerConfiguration {
		if req.Container == nil {
			req.Container = &containergroups.ContainerConfiguration{}
		}
		return req.Container
	}
	resources := func() *containergroups.CreateContainerResourceRequirements {
		c := container()
		if c.Resources == nil {
			c.Resources = &containergroups.CreateContainerResourceRequirements{}
		}
		return c.Resources
	}

	// Flags override the base, docker-run style.
	if opts.Name != "" {
		req.Name = ptr.To(opts.Name)
	}
	if opts.DisplayName != "" {
		req.DisplayName = ptr.To(opts.DisplayName)
	}
	if opts.Image != "" {
		container().Image = ptr.To(opts.Image)
	}
	if opts.Command != nil {
		container().Command = &util.Nullable[[]string]{Value: opts.Command}
	}
	if opts.CPUSet {
		resources().Cpu = ptr.To(opts.CPU)
	}
	if opts.MemorySet {
		resources().Memory = ptr.To(opts.Memory)
	}
	if opts.StorageSet {
		if opts.Storage < 1 {
			return req, clierr.New(
				clierr.CodeInvalidInput,
				clierr.KindInvalidInput,
				"invalid --storage %d: must be at least 1 (GB)",
				opts.Storage,
			)
		}
		resources().StorageAmount = ptr.To(opts.Storage * bytesPerGB)
	}
	if len(opts.Countries) > 0 {
		codes, cerr := normalizeCountries(opts.Countries)
		if cerr != nil {
			return req, cerr
		}
		req.CountryCodes = make([]shared.CountryCode, len(codes))
		for i, c := range codes {
			req.CountryCodes[i] = shared.CountryCode(c)
		}
	}
	if opts.Queue != "" || opts.QueuePortSet || opts.QueuePath != "" {
		if opts.Queue == "" || !opts.QueuePortSet || opts.QueuePath == "" {
			return req, clierr.New(
				clierr.CodeUsage,
				clierr.KindUsage,
				"--queue, --queue-port and --queue-path go together: the queue, and the port and path its jobs are delivered to",
			)
		}
		if opts.QueuePort < 1 || opts.QueuePort > 65535 {
			return req, clierr.New(clierr.CodeInvalidInput, clierr.KindInvalidInput,
				"invalid --queue-port %d: must be between 1 and 65535", opts.QueuePort)
		}
		// The API takes any string as the path, so a rewritten one would be
		// stored and every job posted to it.
		if underGitBash() && msysDrivePath.MatchString(opts.QueuePath) {
			return req, clierr.New(
				clierr.CodeUsage,
				clierr.KindUsage,
				`--queue-path "%s" was rewritten by Git Bash, which turns a leading / into a Windows path`,
				opts.QueuePath,
			).
				WithHint("run the command again with MSYS_NO_PATHCONV=1 in front of it, or from PowerShell or cmd")
		}
		req.QueueConnection = &shared.ContainerGroupQueueConnection{
			QueueName: ptr.To(opts.Queue), Port: ptr.To(opts.QueuePort), Path: ptr.To(opts.QueuePath),
		}
	}
	if opts.ReplicasSet {
		req.Replicas = ptr.To(opts.Replicas)
	}

	if opts.Priority != "" {
		switch p := shared.ContainerGroupPriority(opts.Priority); p {
		case shared.CONTAINER_GROUP_PRIORITY_HIGH, shared.CONTAINER_GROUP_PRIORITY_MEDIUM,
			shared.CONTAINER_GROUP_PRIORITY_LOW, shared.CONTAINER_GROUP_PRIORITY_BATCH:
			container().Priority = &util.Nullable[shared.ContainerGroupPriority]{Value: p}
		default:
			return req, clierr.New(clierr.CodeInvalidInput, clierr.KindInvalidInput,
				`invalid --priority "%s"`, opts.Priority).WithHint("use high, medium, low or batch")
		}
	}

	if opts.RestartPolicySet {
		switch rp := shared.ContainerRestartPolicy(opts.RestartPolicy); rp {
		case shared.CONTAINER_RESTART_POLICY_ALWAYS, shared.CONTAINER_RESTART_POLICY_ON_FAILURE,
			shared.CONTAINER_RESTART_POLICY_NEVER:
			req.RestartPolicy = &rp
		default:
			return req, clierr.New(clierr.CodeInvalidInput, clierr.KindInvalidInput,
				`invalid --restart-policy "%s"`, opts.RestartPolicy).WithHint("use always, on_failure or never")
		}
	}

	if opts.NoAutostart {
		req.AutostartPolicy = ptr.To(false)
	}

	if len(opts.Env) > 0 {
		c := container()
		if c.EnvironmentVariables == nil {
			c.EnvironmentVariables = make(map[string]*string, len(opts.Env))
		}
		for _, kv := range opts.Env {
			k, v, ok := strings.Cut(kv, "=")
			if !ok || k == "" {
				return req, clierr.New(clierr.CodeUsage, clierr.KindUsage, `invalid --env "%s"`, kv).
					WithHint("write it as KEY=VALUE")
			}
			c.EnvironmentVariables[k] = ptr.To(v) // flag wins over the base, per key
		}
	}

	if len(opts.Gpus) > 0 {
		ids := make([]string, 0, len(opts.Gpus))
		for _, g := range opts.Gpus {
			if isUUID(g) {
				ids = append(ids, g)
				continue
			}
			if opts.ResolveGpu == nil {
				return req, clierr.New(clierr.CodeInvalidInput, clierr.KindInvalidInput,
					`--gpu "%s" is not a GPU class UUID, and name resolution is unavailable`, g)
			}
			id, cerr := opts.ResolveGpu(g)
			if cerr != nil {
				return req, cerr
			}
			ids = append(ids, id)
		}
		resources().GpuClasses = ids
	}

	if opts.PortSet {
		if opts.Port < 1 || opts.Port > 65535 {
			return req, clierr.New(clierr.CodeInvalidInput, clierr.KindInvalidInput,
				"invalid --port %d: must be between 1 and 65535", opts.Port)
		}
		if req.Networking == nil {
			req.Networking = &containergroups.CreateContainerGroupNetworking{}
		}
		n := req.Networking
		n.Port = ptr.To(opts.Port)
		if n.Protocol == nil {
			p := shared.CONTAINER_NETWORKING_PROTOCOL_HTTP
			n.Protocol = &p
		}
		if n.Auth == nil {
			n.Auth = ptr.To(true)
		}
	}
	if opts.NoAuth {
		if req.Networking == nil {
			return req, clierr.New(clierr.CodeUsage, clierr.KindUsage,
				"--no-auth requires --port (or a base definition with networking)")
		}
		req.Networking.Auth = ptr.To(false)
	}

	// Defaults fill only fields still unset after base + flags. cpu/memory
	// defaults apply only when a container exists at all (no container means
	// the request is invalid regardless — validateCreateRequest reports it).
	if req.Container != nil {
		r := resources()
		if r.Cpu == nil {
			r.Cpu = ptr.To(int64(1))
		}
		if r.Memory == nil {
			r.Memory = ptr.To(int64(1024))
		}
	}
	if req.Replicas == nil {
		req.Replicas = ptr.To(int64(1))
	}
	if req.RestartPolicy == nil {
		rp := shared.CONTAINER_RESTART_POLICY_ALWAYS
		req.RestartPolicy = &rp
	}
	if req.AutostartPolicy == nil {
		req.AutostartPolicy = ptr.To(true)
	}
	// Priority is deliberately NOT defaulted: omitted means the server picks.

	return req, nil
}

// resolveGpuClassName resolves a GPU class name (a --gpu value, or the class
// 'gpus get' takes) against the org's GPU class list: case-insensitive exact
// name match first, then a unique substring match. Ambiguous or unknown
// names are CodeInvalidInput with candidates; the messages name the class,
// not a flag, because the input may be either.
// Both the input and the stored names are whitespace-trimmed first — the
// API has returned class names with a leading space (e.g. " RTX 5080
// (16 GB)"), which must not break an exact match.
func resolveGpuClassName(value string, classes []organizationdata.GpuClass) (string, *clierr.Error) {
	lower := strings.ToLower(strings.TrimSpace(value))
	type match struct{ name, id string }
	var subs []match
	for i := range classes {
		name := strings.TrimSpace(ptr.Deref(classes[i].Name))
		id := ptr.Deref(classes[i].Id)
		if id == "" || name == "" {
			continue
		}
		if strings.EqualFold(name, lower) {
			return id, nil
		}
		if strings.Contains(strings.ToLower(name), lower) {
			subs = append(subs, match{name, id})
		}
	}
	switch len(subs) {
	case 1:
		return subs[0].id, nil
	case 0:
		names := make([]string, 0, 5)
		for i := range classes {
			if n := strings.TrimSpace(ptr.Deref(classes[i].Name)); n != "" {
				names = append(names, `"`+n+`"`)
			}
			if len(names) == 5 {
				break
			}
		}
		hint := "to list them, run saladcloud gpus list"
		if len(names) > 0 {
			hint += "; valid names include " + strings.Join(names, ", ")
		}
		return "", clierr.New(clierr.CodeInvalidInput, clierr.KindInvalidInput,
			`unknown GPU class "%s"`, value).WithHint("%s", hint)
	default:
		names := make([]string, len(subs))
		for i, m := range subs {
			names[i] = `"` + m.name + `"`
		}
		return "", clierr.New(clierr.CodeInvalidInput, clierr.KindInvalidInput,
			`ambiguous GPU class "%s": matches %s`, value, strings.Join(names, ", ")).
			WithHint("name one of them, or to list them, run saladcloud gpus list")
	}
}

// msysDrivePath matches what Git Bash (MSYS) makes of a flag value such as
// --queue-path /generate before the CLI sees it: C:/Program Files/Git/generate.
// No HTTP path starts with a drive letter.
var msysDrivePath = regexp.MustCompile(`^[A-Za-z]:[/\\]`)

// noteQueueWorker says what nothing else will once a group is connected to
// a queue: jobs reach it only through the job queue worker in its image, and
// a missing worker is silent (the group runs, the jobs stay pending).
func noteQueueWorker(w io.Writer, queue string) {
	fprintf(
		w,
		"note: jobs from queue %s reach this group only if its image runs the job queue worker beside your HTTP server; "+
			"without it the jobs stay pending and nothing reports why; to add the worker, see %s\n",
		queue,
		queueWorkerDocs,
	)
}

// queueWorkerDocs is the platform's page on building the worker into an image.
const queueWorkerDocs = "https://docs.salad.com/container-engine/how-to-guides/job-processing/queue-worker"

// addCreateOverrideFlags registers the flags shared by create and
// (everything that overrides the base request).
func addCreateOverrideFlags(cmd *cobra.Command) {
	f := cmd.Flags()
	f.String("image", "", "container image (required unless -f or --from provides one)")
	f.Int64("cpu", 1, "vCPU cores")
	f.Int64("memory", 1024, "memory in MB")
	f.Int64("storage", 0, "storage in GB (default: server default)")
	f.StringArray("country", nil, "country code the group may run in, repeatable (for example us; default: any)")
	f.StringArray("gpu", nil, "GPU class UUID or name, repeatable (names resolved via the org's GPU class list)")
	f.Int64("replicas", 1, "container instance count")
	f.StringArrayP("env", "e", nil, "environment variable KEY=VALUE, repeatable (overrides the base per key)")
	f.Int64("port", 0, "expose this container port over the container gateway (http, auth on)")
	f.Bool("no-auth", false, "disable auth on the container gateway (requires --port or networking in the base)")
	f.String("priority", "", "scheduling priority: high|medium|low|batch (default: server default)")
	f.String("restart-policy", "always", "restart policy: always|on_failure|never")
	f.Bool("no-autostart", false, "create the group stopped instead of starting it")
	f.String("display-name", "", "human-readable display name")
	f.String("queue", "", "connect the group to this job queue in the project (with --queue-port and --queue-path)")
	f.Int64("queue-port", 0, "container port the queue's jobs are delivered to")
	f.String("queue-path", "", "HTTP path the queue's jobs are delivered to, for example /generate")
}

// collectCreateFlags reads the shared override flags into a createFlags.
func collectCreateFlags(cmd *cobra.Command, name string, hasBase bool) (createFlags, *clierr.Error) {
	f := readFlags(cmd.Flags())
	str := func(n string) string { return f.String(n) }
	i64 := func(n string) int64 { return f.Int64(n) }
	b := func(n string) bool { return f.Bool(n) }
	arr := func(n string) []string { return f.StringArray(n) }
	opts := createFlags{
		Name:             name,
		DisplayName:      str("display-name"),
		Image:            str("image"),
		CPU:              i64("cpu"),
		CPUSet:           f.Changed("cpu"),
		Memory:           i64("memory"),
		MemorySet:        f.Changed("memory"),
		Storage:          i64("storage"),
		StorageSet:       f.Changed("storage"),
		Countries:        arr("country"),
		Queue:            str("queue"),
		QueuePort:        i64("queue-port"),
		QueuePortSet:     f.Changed("queue-port"),
		QueuePath:        str("queue-path"),
		Replicas:         i64("replicas"),
		ReplicasSet:      f.Changed("replicas"),
		Gpus:             arr("gpu"),
		Env:              arr("env"),
		Port:             i64("port"),
		PortSet:          f.Changed("port"),
		NoAuth:           b("no-auth"),
		Priority:         str("priority"),
		RestartPolicy:    str("restart-policy"),
		RestartPolicySet: f.Changed("restart-policy"),
		NoAutostart:      b("no-autostart"),
		HasBase:          hasBase,
		Command:          nil,
		ResolveGpu:       nil,
	}
	return opts, f.Err()
}

// runCreate is the tail of create: apply the
// override flags to the base, pre-validate, create, and emit the group.
// The API client is built lazily so pure usage errors (missing --image,
// bad --env) surface before any auth/config requirement.
//
// baseRaw/unknowns are non-empty only on the --allow-unknown-fields path:
// baseRaw is the user's document as written, unknowns the fields the typed
// struct cannot carry. Those fields are grafted back by deep-merging the
// built request (flags + defaults, which always win) over baseRaw and
// sending the merged document raw.
func runCreate(
	cmd *cobra.Command,
	base containergroups.ContainerGroupCreationRequest,
	hasBase bool,
	name string,
	command []string,
	baseRaw []byte,
	unknowns []input.Unknown,
) error {
	var client *api.Client
	getClient := func() (*api.Client, *clierr.Error) {
		if client != nil {
			return client, nil
		}
		c, _, cerr := NewClient(cmd)
		if cerr != nil {
			return nil, cerr
		}
		client = c
		return client, nil
	}

	opts, cerr := collectCreateFlags(cmd, name, hasBase)
	if cerr != nil {
		return cerr
	}
	opts.Command = command
	var classes []organizationdata.GpuClass // fetched once, on first non-UUID --gpu
	opts.ResolveGpu = func(value string) (string, *clierr.Error) {
		if classes == nil {
			c, err := getClient()
			if err != nil {
				return "", err
			}
			list, err := c.ListGpuClasses(cmd.Context())
			if err != nil {
				return "", err
			}
			classes = list.Items
		}
		return resolveGpuClassName(value, classes)
	}

	req, cerr := buildCreateRequest(base, opts)
	if cerr != nil {
		return cerr
	}
	if cerr = validateCreateRequest(&req); cerr != nil {
		return cerr
	}
	c, cerr := getClient()
	if cerr != nil {
		return cerr
	}
	// The queue must exist in the target project: the API would accept the
	// connection and the group would never receive a job.
	if q := req.QueueConnection; q != nil && q.QueueName != nil {
		if _, qerr := c.GetQueue(cmd.Context(), *q.QueueName); qerr != nil {
			return queueNotFound(qerr, *q.QueueName, c)
		}
	}
	// sent is the effective request document — flags merged over the file —
	// which is what the created group gets compared against below. Using the
	// file alone would report every flag override as a dropped write. It is
	// encoded with MarshalRequest, not json.Marshal: the SDK's omitempty
	// would drop an empty list the file or the --from source set, and the API
	// rejects an HTTP probe without its (possibly empty) headers list.
	sent, err := output.MarshalRequest(req)
	if err != nil {
		return clierr.New(clierr.CodeGeneric, clierr.KindInternal, "cannot marshal request: %v", err)
	}
	if len(unknowns) > 0 {
		warnUnknowns(os.Stderr, unknowns, true)
		if sent, cerr = input.MergeJSON(baseRaw, sent); cerr != nil {
			return cerr
		}
	}
	group, cerr := c.CreateGroupRaw(cmd.Context(), sent)
	if cerr != nil {
		return cerr
	}
	if q := req.QueueConnection; q != nil {
		noteQueueWorker(os.Stderr, ptr.Deref(q.QueueName))
	}
	// Same "did it actually happen?" check the update path runs: the API
	// accepts some fields and silently drops them (container.image_caching),
	// returning 201 either way.
	warnUnappliedCreateFields(os.Stderr, findUnappliedCreateFields(sent, group))
	// --wait (validated pre-flight in RunE): block until the new group runs,
	// and only then emit — the document must describe the settled state, and
	// stdout must stay empty if the wait fails (timeout, interrupt). Without
	// --wait the create response is emitted immediately, as before.
	wait, timeout, cerr := waitFlagValues(cmd)
	if cerr != nil {
		return cerr
	}
	if wait {
		name = ptr.Deref(group.Name)
		if name == "" {
			name = ptr.Deref(req.Name) // defensive: the response should echo it
		}
		// fromActive: the create response is pending with autostart on, so a
		// stopped group from here on was never started, not merely slow.
		final, err := runStatusWait(cmd, c, name, "running", true, timeout)
		if err != nil {
			return err
		}
		group = final
	}
	return wrapErr(output.Emit(group, func(w *output.Writer) { writeGroupDetail(w, group) }))
}

func init() {
	createCmd := &cobra.Command{
		Use:   "create [<group>] [-- <command>...]",
		Short: "Create a container group from flags, a definition file, or an existing group",
		Long: `Create a container group.

The request starts from a base (a JSON/YAML definition from -f, or empty),
then flags apply on top of it, docker-run style.
Defaults (cpu 1, memory 1024, replicas 1, restart-policy always, autostart
on) fill only fields that are still unset afterward. Without -f or
--from, --image is required.

--from copies an existing group in the same organization: <group> from the
current project, or <project>/<group> from another one. The copy keeps the
source's settings, including restart policy and queue connection, and takes
the new name as its display name unless --display-name is given. -f then
merges over the copy and flags apply on top. The API never returns registry
credentials, so a copy of a private image needs them again: pass a -f file
holding container.registry_authentication. The copy autostarts like any
new group; add --no-autostart to create it stopped.

autostart_policy is consumed, not kept: once the group starts (pending to
deploying), the API sets the stored value to false. A get after that
reports autostart_policy: false for a group created with autostart on;
current_state.status, not autostart_policy, says whether it is running.

--gpu accepts a GPU class UUID or a name; names resolve case-insensitively
against the organization's GPU class list (exact match first, then a unique
substring match).

Words after -- are the container command (container.command), passed as
your shell split them. The command replaces the image's ENTRYPOINT and CMD
together, so it starts with the program: -- python app.py --port 8080, not
-- --port 8080. Unlike cg ssh, there is no shell: for a script, give
-- bash -c '<script>'. Put saladcloud's own flags (-o, --wait, ...) before --.`,
		Example: `  # Flags only: the common case
  saladcloud container-groups create my-app --image ghcr.io/me/app:latest --replicas 3 --port 8080 -e MODE=prod --gpu "RTX 3060 (12 GB)"

  # From a YAML/JSON definition file
  saladcloud container-groups create -f examples/hello-cpu.yaml

  # File as the base, flags override on top
  saladcloud container-groups create my-copy -f examples/sleep-cpu.json --replicas 2 -e DEBUG=1

  # Copy an existing group, changing the image, and create it stopped
  saladcloud container-groups create my-app-v2 --from my-app --image ghcr.io/me/app:v2 --no-autostart

  # Copy a group from another project into the current one
  saladcloud container-groups create my-app --from staging/my-app

  # From JSON on stdin, targeting an explicit org/project
  cat my-app.json | saladcloud container-groups create -f - --org my-org --project default

  # Create and block until the group is running (scripts/CI)
  saladcloud container-groups create my-app --image ghcr.io/me/app:latest --wait --timeout 15m

  # Override the image's command: keep it running to debug startup over ssh
  saladcloud container-groups create my-debug --image ghcr.io/me/app:latest -- sleep 2147483647`,
		Args: func(cmd *cobra.Command, args []string) error {
			// The one optional positional is <group>; the words after -- are
			// the container command and are not counted.
			if pos, _, _ := splitCommand(cmd, args); len(pos) > 1 {
				return fmt.Errorf("%w at most 1 arg(s), received %d", errAccepts, len(pos))
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			fr := readFlags(cmd.Flags())
			noAutostart := fr.Bool("no-autostart")
			file := fr.String("file")
			allowUnknown := fr.Bool("allow-unknown-fields")
			from := fr.String("from")
			if cerr := fr.Err(); cerr != nil {
				return cerr
			}
			args, command, commandGiven := splitCommand(cmd, args)
			if cerr := validateCommand(command, commandGiven); cerr != nil {
				return cerr
			}
			warnCommand(os.Stderr, cmd, command)
			// Pre-flight flag validation, before any file or API work.
			wait, _, cerr := waitFlagValues(cmd)
			if cerr != nil {
				return cerr
			}
			if cerr = validateWaitAutostart(wait, noAutostart); cerr != nil {
				return cerr
			}
			var base containergroups.ContainerGroupCreationRequest
			var baseRaw []byte
			var unknowns []input.Unknown
			hasBase := false
			name := ""
			if len(args) == 1 {
				name = args[0]
			}
			if from != "" {
				base, baseRaw, unknowns, cerr = loadFromBase(cmd, from, file, allowUnknown, name, os.Stderr)
				if cerr != nil {
					return cerr
				}
				return runCreate(cmd, base, true, name, command, baseRaw, unknowns)
			}
			if file != "" {
				if allowUnknown {
					raw, u, err := input.LoadLenient(file, &base)
					if err != nil {
						return err
					}
					baseRaw, unknowns = raw, u
				} else {
					// Strict: load leniently, then reject with per-field
					// advice (create-only / response-only fields get a
					// real explanation instead of the escape-hatch hint).
					_, u, err := input.LoadLenient(file, &base)
					if err != nil {
						return err
					}
					if cerr := rejectUnknowns(u); cerr != nil {
						return cerr
					}
				}
				hasBase = true
			}
			return runCreate(cmd, base, hasBase, name, command, baseRaw, unknowns)
		},
	}
	createCmd.Flags().StringP("file", "f", "", "container group definition file (.json/.yaml; \"-\" reads stdin)")
	createCmd.Flags().String("from", "", "copy an existing group in this organization: <group> or <project>/<group>")
	addCreateOverrideFlags(createCmd)
	addAllowUnknownFlag(createCmd)
	addWaitFlags(createCmd, "the group is running")
	RegisterContainerGroups(createCmd)
}
