// The create command: build a container group from flags, a definition
// file, a copy of an existing group (--from, see createfrom.go), or any
// combination (base from --from and/or -f, flags applied on top,
// docker-run style).

package cli

import (
	"encoding/json"
	"fmt"
	"io"
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

// validateWaitAutostart rejects --wait together with --no-autostart: a group
// created stopped never reaches "running", so the wait could only ever time
// out. Pre-flight, before any file read or API call.
func validateWaitAutostart(o *createOptions) *clierr.Error {
	if o.wait.wait && o.flags.NoAutostart {
		return clierr.New(clierr.CodeUsage, clierr.KindUsage,
			"--wait cannot be combined with --no-autostart (a group created stopped never becomes running)")
	}
	return nil
}

// validateGroupName pre-validates a group name client-side so a bad name
// fails fast (exit 6, the API's own rule) before any request.
func validateGroupName(name string) *clierr.Error { return validateResourceName("group", name) }

// validateResourceName applies the API's resource name rule (shared by
// container groups and queues) to name; noun names the resource in the
// message.
func validateResourceName(noun, name string) *clierr.Error {
	if !groupNameRe.MatchString(name) {
		return clierr.New(clierr.CodeInvalidInput, clierr.KindInvalidInput,
			`invalid %s name "%s"`, noun, name).
			WithHint(
				"use 2-63 characters: lowercase letters, digits and hyphens, starting with a letter and ending with a letter or digit", //nolint:lll // one literal, so validation_test.go reads the hint at its call site
			)
	}
	return nil
}

// buildCreateRequest applies create's flags on top of a base request
// (from -f or empty), then fills defaults into fields that are
// still unset. It is pure: GPU name resolution goes through opts.ResolveGpu.
func (app *App) buildCreateRequest(
	base *containergroups.ContainerGroupCreationRequest,
	opts *createFlags,
) (containergroups.ContainerGroupCreationRequest, *clierr.Error) {
	req := *base

	if !opts.HasBase && opts.Image == "" {
		return req, clierr.New(clierr.CodeUsage, clierr.KindUsage,
			"missing required flag: --image <image> (or start from a base definition with -f <file>)")
	}

	// Flags override the base, docker-run style. The steps run in this
	// order, so the same command line always fails the same check first.
	steps := []func(*containergroups.ContainerGroupCreationRequest, *createFlags) *clierr.Error{
		createApplyContainerFlags,
		createApplyCountryFlags,
		app.createApplyQueueFlags,
		createApplySchedulingFlags,
		createApplyEnvFlags,
		createApplyGpuFlags,
		createApplyNetworkingFlags,
	}
	for _, step := range steps {
		if cerr := step(&req, opts); cerr != nil {
			return req, cerr
		}
	}

	createFillDefaults(&req)
	return req, nil
}

// createContainer is req's container configuration, created empty when the
// base had none.
func createContainer(req *containergroups.ContainerGroupCreationRequest) *containergroups.ContainerConfiguration {
	if req.Container == nil {
		req.Container = &containergroups.ContainerConfiguration{}
	}
	return req.Container
}

// createResources is req's container resource requirements, created empty
// (with the container) when the base had none.
func createResources(
	req *containergroups.ContainerGroupCreationRequest,
) *containergroups.CreateContainerResourceRequirements {
	c := createContainer(req)
	if c.Resources == nil {
		c.Resources = &containergroups.CreateContainerResourceRequirements{}
	}
	return c.Resources
}

// createApplyContainerFlags applies the group's name and display name and
// its container's image, command and resources (--cpu, --memory, --storage).
func createApplyContainerFlags(req *containergroups.ContainerGroupCreationRequest, opts *createFlags) *clierr.Error {
	if opts.Name != "" {
		req.Name = ptr.To(opts.Name)
	}
	if opts.DisplayName != "" {
		req.DisplayName = ptr.To(opts.DisplayName)
	}
	if opts.Image != "" {
		createContainer(req).Image = ptr.To(opts.Image)
	}
	if opts.Command != nil {
		createContainer(req).Command = &util.Nullable[[]string]{Value: opts.Command}
	}
	if opts.CPUSet {
		createResources(req).Cpu = ptr.To(opts.CPU)
	}
	if opts.MemorySet {
		createResources(req).Memory = ptr.To(opts.Memory)
	}
	if opts.StorageSet {
		if opts.Storage < 1 {
			return clierr.New(
				clierr.CodeInvalidInput,
				clierr.KindInvalidInput,
				"invalid --storage %d: must be at least 1 (GB)",
				opts.Storage,
			)
		}
		createResources(req).StorageAmount = ptr.To(opts.Storage * bytesPerGB)
	}
	return nil
}

// createApplyCountryFlags replaces the base's country codes with the
// --country values, normalized, when any were given.
func createApplyCountryFlags(req *containergroups.ContainerGroupCreationRequest, opts *createFlags) *clierr.Error {
	if len(opts.Countries) == 0 {
		return nil
	}
	codes, cerr := normalizeCountries(opts.Countries)
	if cerr != nil {
		return cerr
	}
	req.CountryCodes = make([]shared.CountryCode, len(codes))
	for i, c := range codes {
		req.CountryCodes[i] = shared.CountryCode(c)
	}
	return nil
}

// createQueueFlagsGiven reports whether --queue, --queue-port and
// --queue-path were given; giving some of them but not all is a usage error.
func createQueueFlagsGiven(opts *createFlags) (bool, *clierr.Error) {
	if opts.Queue == "" && !opts.QueuePortSet && opts.QueuePath == "" {
		return false, nil
	}
	if opts.Queue == "" || !opts.QueuePortSet || opts.QueuePath == "" {
		return false, clierr.New(
			clierr.CodeUsage,
			clierr.KindUsage,
			"--queue, --queue-port and --queue-path go together: the queue, and the port and path its jobs are delivered to", //nolint:lll // one literal, so validation_test.go reads the message at its call site
		)
	}
	return true, nil
}

// createApplyQueueFlags connects the group to the --queue job queue, at
// --queue-port and --queue-path, when the three were given.
func (app *App) createApplyQueueFlags(
	req *containergroups.ContainerGroupCreationRequest,
	opts *createFlags,
) *clierr.Error {
	given, cerr := createQueueFlagsGiven(opts)
	if cerr != nil || !given {
		return cerr
	}
	if opts.QueuePort < 1 || opts.QueuePort > 65535 {
		return clierr.New(clierr.CodeInvalidInput, clierr.KindInvalidInput,
			"invalid --queue-port %d: must be between 1 and 65535", opts.QueuePort)
	}
	// The API takes any string as the path, so a rewritten one would be
	// stored and every job posted to it.
	if app.underGitBash() && msysDrivePath.MatchString(opts.QueuePath) {
		return clierr.New(
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
	return nil
}

// createApplySchedulingFlags applies --replicas, --priority,
// --restart-policy and --no-autostart.
func createApplySchedulingFlags(req *containergroups.ContainerGroupCreationRequest, opts *createFlags) *clierr.Error {
	if opts.ReplicasSet {
		req.Replicas = ptr.To(opts.Replicas)
	}
	if opts.Priority != "" {
		p, cerr := parseGroupPriorityFlag(opts.Priority)
		if cerr != nil {
			return cerr
		}
		createContainer(req).Priority = &util.Nullable[shared.ContainerGroupPriority]{Value: p}
	}
	if opts.RestartPolicySet {
		rp, cerr := createParseRestartPolicy(opts.RestartPolicy)
		if cerr != nil {
			return cerr
		}
		req.RestartPolicy = &rp
	}
	if opts.NoAutostart {
		req.AutostartPolicy = ptr.To(false)
	}
	return nil
}

// parseGroupPriorityFlag is a --priority value (create's or update's) as the
// API's priority enum.
func parseGroupPriorityFlag(value string) (shared.ContainerGroupPriority, *clierr.Error) {
	switch p := shared.ContainerGroupPriority(value); p {
	case shared.CONTAINER_GROUP_PRIORITY_HIGH, shared.CONTAINER_GROUP_PRIORITY_MEDIUM,
		shared.CONTAINER_GROUP_PRIORITY_LOW, shared.CONTAINER_GROUP_PRIORITY_BATCH:
		return p, nil
	default:
		return "", clierr.New(clierr.CodeInvalidInput, clierr.KindInvalidInput,
			`invalid --priority "%s"`, value).WithHint("use high, medium, low or batch")
	}
}

// createParseRestartPolicy is the --restart-policy value as the API's
// restart policy enum.
func createParseRestartPolicy(value string) (shared.ContainerRestartPolicy, *clierr.Error) {
	switch rp := shared.ContainerRestartPolicy(value); rp {
	case shared.CONTAINER_RESTART_POLICY_ALWAYS, shared.CONTAINER_RESTART_POLICY_ON_FAILURE,
		shared.CONTAINER_RESTART_POLICY_NEVER:
		return rp, nil
	default:
		return "", clierr.New(clierr.CodeInvalidInput, clierr.KindInvalidInput,
			`invalid --restart-policy "%s"`, value).WithHint("use always, on_failure or never")
	}
}

// createApplyEnvFlags sets each -e KEY=VALUE on the container.
func createApplyEnvFlags(req *containergroups.ContainerGroupCreationRequest, opts *createFlags) *clierr.Error {
	if len(opts.Env) == 0 {
		return nil
	}
	c := createContainer(req)
	if c.EnvironmentVariables == nil {
		c.EnvironmentVariables = make(map[string]*string, len(opts.Env))
	}
	for _, kv := range opts.Env {
		if cerr := checkGroupEnvFlag(kv); cerr != nil {
			return cerr
		}
		k, v, _ := strings.Cut(kv, "=")
		c.EnvironmentVariables[k] = ptr.To(v) // flag wins over the base, per key
	}
	return nil
}

// checkGroupEnvFlag checks a -e value (create's or update's): KEY=VALUE,
// split at the first "=", with a name before it.
func checkGroupEnvFlag(kv string) *clierr.Error {
	if k, _, ok := strings.Cut(kv, "="); !ok || k == "" {
		return clierr.New(clierr.CodeUsage, clierr.KindUsage, `invalid --env "%s"`, kv).
			WithHint("write it as KEY=VALUE")
	}
	return nil
}

// createApplyGpuFlags replaces the container's GPU classes with the --gpu
// values, resolved to class UUIDs, when any were given.
func createApplyGpuFlags(req *containergroups.ContainerGroupCreationRequest, opts *createFlags) *clierr.Error {
	if len(opts.Gpus) == 0 {
		return nil
	}
	ids, cerr := resolveGroupGpuFlags(opts.Gpus, opts.ResolveGpu)
	if cerr != nil {
		return cerr
	}
	createResources(req).GpuClasses = ids
	return nil
}

// resolveGroupGpuFlags is the --gpu values (create's and update's) as GPU
// class UUIDs: a UUID is kept as given, and any other value goes through
// resolve, which is nil when name resolution is unavailable.
func resolveGroupGpuFlags(
	gpus []string,
	resolve func(value string) (string, *clierr.Error),
) ([]string, *clierr.Error) {
	ids := make([]string, 0, len(gpus))
	for _, g := range gpus {
		if isUUID(g) {
			ids = append(ids, g)
			continue
		}
		if resolve == nil {
			return nil, clierr.New(clierr.CodeInvalidInput, clierr.KindInvalidInput,
				`--gpu "%s" is not a GPU class UUID, and name resolution is unavailable`, g)
		}
		id, cerr := resolve(g)
		if cerr != nil {
			return nil, cerr
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// createApplyNetworkingFlags exposes --port over the container gateway
// (http with auth on, unless the base already says otherwise) and applies
// --no-auth.
func createApplyNetworkingFlags(
	req *containergroups.ContainerGroupCreationRequest,
	opts *createFlags,
) *clierr.Error {
	if opts.PortSet {
		if opts.Port < 1 || opts.Port > 65535 {
			return clierr.New(clierr.CodeInvalidInput, clierr.KindInvalidInput,
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
			return clierr.New(clierr.CodeUsage, clierr.KindUsage,
				"--no-auth requires --port (or a base definition with networking)")
		}
		req.Networking.Auth = ptr.To(false)
	}
	return nil
}

// createDefaultMemoryMB is the memory, in MB, a container gets when neither
// the base nor --memory sets it; it is also --memory's default.
const createDefaultMemoryMB = 1024

// createFillDefaults fills only fields still unset after base + flags.
// cpu/memory defaults apply only when a container exists at all (no
// container means the request is invalid regardless — validateCreateRequest
// reports it).
func createFillDefaults(req *containergroups.ContainerGroupCreationRequest) {
	if req.Container != nil {
		r := createResources(req)
		if r.Cpu == nil {
			r.Cpu = ptr.To(int64(1))
		}
		if r.Memory == nil {
			r.Memory = ptr.To(int64(createDefaultMemoryMB))
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
	var subNames, subIDs []string // the substring matches
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
			subNames, subIDs = append(subNames, name), append(subIDs, id)
		}
	}
	switch len(subIDs) {
	case 1:
		return subIDs[0], nil
	case 0:
		return "", unknownGpuClassError(value, classes)
	default:
		return "", ambiguousGpuClassError(value, subNames)
	}
}

// gpuClassesNamedInHint is how many of the org's GPU classes the unknown GPU
// class hint names at most.
const gpuClassesNamedInHint = 5

// unknownGpuClassError is resolveGpuClassName's error for a value no class
// matches; its hint names up to gpuClassesNamedInHint of the org's classes.
func unknownGpuClassError(value string, classes []organizationdata.GpuClass) *clierr.Error {
	names := make([]string, 0, gpuClassesNamedInHint)
	for i := range classes {
		if n := strings.TrimSpace(ptr.Deref(classes[i].Name)); n != "" {
			names = append(names, `"`+n+`"`)
		}
		if len(names) == gpuClassesNamedInHint {
			break
		}
	}
	hint := "to list them, run saladcloud gpus list"
	if len(names) > 0 {
		hint += "; valid names include " + strings.Join(names, ", ")
	}
	return clierr.New(clierr.CodeInvalidInput, clierr.KindInvalidInput,
		`unknown GPU class "%s"`, value).WithHint("%s", hint)
}

// ambiguousGpuClassError is resolveGpuClassName's error for a value that
// several class names contain; it lists them.
func ambiguousGpuClassError(value string, matches []string) *clierr.Error {
	names := make([]string, len(matches))
	for i, m := range matches {
		names[i] = `"` + m + `"`
	}
	return clierr.New(clierr.CodeInvalidInput, clierr.KindInvalidInput,
		`ambiguous GPU class "%s": matches %s`, value, strings.Join(names, ", ")).
		WithHint("name one of them, or to list them, run saladcloud gpus list")
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
		"note: jobs from queue %s reach this group only if its image runs the job queue worker "+
			"beside your HTTP server; without it the jobs stay pending and nothing reports why; "+
			"to add the worker, see %s\n",
		queue,
		queueWorkerDocs,
	)
}

// queueWorkerDocs is the platform's page on building the worker into an image.
const queueWorkerDocs = "https://docs.salad.com/container-engine/how-to-guides/job-processing/queue-worker"

// createOptions are create's flags.
type createOptions struct {
	file         string
	from         string
	allowUnknown bool
	flags        createFlags
	wait         waitOptions
}

// addCreateOverrideFlags binds the flags that override the base request to o.
func addCreateOverrideFlags(cmd *cobra.Command, o *createFlags) {
	f := cmd.Flags()
	f.StringVar(&o.Image, "image", "", "container image (required unless -f or --from provides one)")
	f.Int64Var(&o.CPU, "cpu", 1, "vCPU cores")
	f.Int64Var(&o.Memory, "memory", createDefaultMemoryMB, "memory in MB")
	f.Int64Var(&o.Storage, "storage", 0, "storage in GB (default: server default)")
	f.StringArrayVar(
		&o.Countries,
		"country",
		nil,
		"country code the group may run in, repeatable (for example us; default: any)",
	)
	f.StringArrayVar(
		&o.Gpus,
		"gpu",
		nil,
		"GPU class UUID or name, repeatable (names resolved via the org's GPU class list)",
	)
	f.Int64Var(&o.Replicas, "replicas", 1, "container instance count")
	f.StringArrayVarP(
		&o.Env,
		"env",
		"e",
		nil,
		"environment variable KEY=VALUE, repeatable (overrides the base per key)",
	)
	f.Int64Var(&o.Port, "port", 0, "expose this container port over the container gateway (http, auth on)")
	f.BoolVar(
		&o.NoAuth,
		"no-auth",
		false,
		"disable auth on the container gateway (requires --port or networking in the base)",
	)
	f.StringVar(&o.Priority, "priority", "", "scheduling priority: high|medium|low|batch (default: server default)")
	f.StringVar(&o.RestartPolicy, "restart-policy", "always", "restart policy: always|on_failure|never")
	f.BoolVar(&o.NoAutostart, "no-autostart", false, "create the group stopped instead of starting it")
	f.StringVar(&o.DisplayName, "display-name", "", "human-readable display name")
	f.StringVar(
		&o.Queue,
		"queue",
		"",
		"connect the group to this job queue in the project (with --queue-port and --queue-path)",
	)
	f.Int64Var(&o.QueuePort, "queue-port", 0, "container port the queue's jobs are delivered to")
	f.StringVar(&o.QueuePath, "queue-path", "", "HTTP path the queue's jobs are delivered to, for example /generate")
}

// collectCreateFlags is a copy of the override flags bound to bound, with the
// ones given on the command line marked set, for a group named name.
func collectCreateFlags(cmd *cobra.Command, bound *createFlags, name string, hasBase bool) createFlags {
	f := cmd.Flags()
	o := *bound
	o.Name = name
	o.HasBase = hasBase
	o.CPUSet = f.Changed("cpu")
	o.MemorySet = f.Changed("memory")
	o.StorageSet = f.Changed("storage")
	o.QueuePortSet = f.Changed("queue-port")
	o.ReplicasSet = f.Changed("replicas")
	o.PortSet = f.Changed("port")
	o.RestartPolicySet = f.Changed("restart-policy")
	o.Command, o.ResolveGpu = nil, nil
	return o
}

// createBase is the request create starts from, before its flags apply:
// empty, or loaded from -f and/or --from.
//
// raw and unknowns are non-empty only on the --allow-unknown-fields path:
// raw is the user's document as written, unknowns the fields the typed
// struct cannot carry. Those fields are grafted back by deep-merging the
// built request (flags + defaults, which always win) over raw and sending
// the merged document raw.
type createBase struct {
	req containergroups.ContainerGroupCreationRequest
	// given is true when the base came from -f or --from; without one,
	// --image is required.
	given    bool
	raw      []byte
	unknowns []input.Unknown
}

// runCreate is the tail of create: apply the override flags to the base,
// pre-validate, create, and emit the group. The API client is built lazily
// so pure usage errors (missing --image, bad --env) surface before any
// auth/config requirement.
func (app *App) runCreate(cmd *cobra.Command, o *createOptions, base *createBase, name string, command []string) error {
	getClient := app.createLazyClient(cmd)
	opts := collectCreateFlags(cmd, &o.flags, name, base.given)
	opts.Command = command
	opts.ResolveGpu = createLazyGpuResolver(cmd, getClient)

	req, cerr := app.buildCreateRequest(&base.req, &opts)
	if cerr != nil {
		return cerr
	}
	if verr := validateCreateRequest(&req); verr != nil {
		return verr
	}
	c, cerr := getClient()
	if cerr != nil {
		return cerr
	}
	if qerr := createCheckQueue(cmd, c, &req); qerr != nil {
		return qerr
	}
	sent, cerr := app.createRequestBody(&req, base.raw, base.unknowns)
	if cerr != nil {
		return cerr
	}
	group, cerr := c.CreateGroupRaw(cmd.Context(), sent)
	if cerr != nil {
		return cerr
	}
	if q := req.QueueConnection; q != nil {
		noteQueueWorker(app.errw(), ptr.Deref(q.QueueName))
	}
	// Same "did it actually happen?" check the update path runs: the API
	// accepts some fields and silently drops them (container.image_caching),
	// returning 201 either way.
	warnUnappliedCreateFields(app.errw(), findUnappliedCreateFields(sent, group))
	group, err := app.createAwaitRunning(cmd, o, c, group, &req)
	if err != nil {
		return err
	}
	return wrapErr(app.printer.Emit(group, func(w *output.Writer) { writeGroupDetail(w, group) }))
}

// createLazyClient returns create's API client getter: the client is built
// on first use, so pure usage errors surface before any auth or config
// requirement, and then reused.
func (app *App) createLazyClient(cmd *cobra.Command) func() (*api.Client, *clierr.Error) {
	var client *api.Client
	return func() (*api.Client, *clierr.Error) {
		if client != nil {
			return client, nil
		}
		c, _, cerr := app.NewClient(cmd)
		if cerr != nil {
			return nil, cerr
		}
		client = c
		return client, nil
	}
}

// createLazyGpuResolver resolves --gpu names against the organization's GPU
// classes, fetched once, on the first non-UUID --gpu, through the client
// getClient builds then.
func createLazyGpuResolver(
	cmd *cobra.Command,
	getClient func() (*api.Client, *clierr.Error),
) func(value string) (string, *clierr.Error) {
	var classes []organizationdata.GpuClass
	return func(value string) (string, *clierr.Error) {
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
}

// createCheckQueue checks that the queue req connects to exists in the
// target project: the API would accept the connection and the group would
// never receive a job.
func createCheckQueue(
	cmd *cobra.Command,
	c *api.Client,
	req *containergroups.ContainerGroupCreationRequest,
) *clierr.Error {
	q := req.QueueConnection
	if q == nil || q.QueueName == nil {
		return nil
	}
	if _, qerr := c.GetQueue(cmd.Context(), *q.QueueName); qerr != nil {
		return queueNotFound(qerr, *q.QueueName, c)
	}
	return nil
}

// createRequestBody is the document create sends: the effective request —
// flags merged over the file — which is what the created group gets compared
// against afterward. Using the file alone would report every flag override
// as a dropped write. It is encoded with MarshalRequest, not [json.Marshal]:
// the SDK's omitempty would drop an empty list the file or the --from source
// set, and the API rejects an HTTP probe without its (possibly empty)
// headers list. With --allow-unknown-fields it warns about the unknowns and
// deep-merges the request over baseRaw, so they are sent as written.
func (app *App) createRequestBody(
	req *containergroups.ContainerGroupCreationRequest,
	baseRaw []byte,
	unknowns []input.Unknown,
) (json.RawMessage, *clierr.Error) {
	sent, err := output.MarshalRequest(*req)
	if err != nil {
		return nil, clierr.New(clierr.CodeGeneric, clierr.KindInternal, "cannot marshal request: %v", err)
	}
	if len(unknowns) == 0 {
		return sent, nil
	}
	warnCreateUnknowns(app.errw(), unknowns)
	return input.MergeJSON(baseRaw, sent)
}

// createAwaitRunning is create's --wait (validated pre-flight in RunE): it
// blocks until the new group runs and returns the settled document, so the
// caller emits only then — the document must describe the settled state, and
// stdout must stay empty if the wait fails (timeout, interrupt). Without
// --wait it returns group, the create response, unchanged.
func (app *App) createAwaitRunning(
	cmd *cobra.Command,
	o *createOptions,
	c *api.Client,
	group *shared.ContainerGroup,
	req *containergroups.ContainerGroupCreationRequest,
) (*shared.ContainerGroup, error) {
	wait, timeout, cerr := o.wait.values()
	if cerr != nil {
		return nil, cerr
	}
	if !wait {
		return group, nil
	}
	name := ptr.Deref(group.Name)
	if name == "" {
		name = ptr.Deref(req.Name) // defensive: the response should echo it
	}
	// fromActive: the create response is pending with autostart on, so a
	// stopped group from here on was never started, not merely slow.
	return app.runStatusWait(cmd, c, name, string(shared.CONTAINER_GROUP_STATUS_RUNNING), true, timeout)
}

// addCreateCommands adds create.go's commands to the tree.
func (app *App) addCreateCommands(cg *cobra.Command) {
	var opts createOptions
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
		Example: "  # Flags only: the common case\n" +
			"  saladcloud container-groups create my-app --image ghcr.io/me/app:latest --replicas 3 --port 8080 " +
			`-e MODE=prod --gpu "RTX 3060 (12 GB)"

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
			if pos, _ := splitCommand(cmd, args); len(pos) > 1 {
				return fmt.Errorf("%w at most 1 arg(s), received %d", errAccepts, len(pos))
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return app.runCreateCommand(cmd, &opts, args)
		},
	}
	createCmd.Flags().
		StringVarP(&opts.file, "file", "f", "", "container group definition file (.json/.yaml; \"-\" reads stdin)")
	createCmd.Flags().
		StringVar(&opts.from, "from", "", "copy an existing group in this organization: <group> or <project>/<group>")
	addCreateOverrideFlags(createCmd, &opts.flags)
	addAllowUnknownFlag(createCmd, &opts.allowUnknown)
	addWaitFlags(createCmd, "the group is running", &opts.wait)
	cg.AddCommand(createCmd)
}

// runCreateCommand is create's RunE: check the container command and the
// flags pre-flight, before any file or API work, then load the base (--from,
// -f, or none) and hand it to runCreate.
func (app *App) runCreateCommand(cmd *cobra.Command, opts *createOptions, args []string) error {
	args, command := splitCommand(cmd, args)
	if cerr := app.validateCommand(command); cerr != nil {
		return cerr
	}
	warnCommand(app.errw(), cmd, command.words)
	// Pre-flight flag validation, before any file or API work.
	if _, _, cerr := opts.wait.values(); cerr != nil {
		return cerr
	}
	if cerr := validateWaitAutostart(opts); cerr != nil {
		return cerr
	}
	name := ""
	if len(args) == 1 {
		name = args[0]
	}
	var base createBase
	switch {
	case opts.from != "":
		from, cerr := app.loadFromBase(cmd, opts, name, app.errw())
		if cerr != nil {
			return cerr
		}
		base = from
	case opts.file != "":
		raw, unknowns, cerr := app.createLoadFile(opts, &base.req)
		if cerr != nil {
			return cerr
		}
		base.given, base.raw, base.unknowns = true, raw, unknowns
	}
	return app.runCreate(cmd, opts, &base, name, command.words)
}

// createLoadFile loads the -f definition into base. With
// --allow-unknown-fields it returns the document as written and the fields
// base cannot carry; without, an unknown field is an error.
func (app *App) createLoadFile(
	opts *createOptions,
	base *containergroups.ContainerGroupCreationRequest,
) ([]byte, []input.Unknown, *clierr.Error) {
	raw, u, cerr := input.LoadLenient(opts.file, app.Stdin, base)
	if cerr != nil {
		return nil, nil, cerr
	}
	if opts.allowUnknown {
		return raw, u, nil
	}
	// Strict: load leniently, then reject with per-field advice (create-only /
	// response-only fields get a real explanation instead of the escape-hatch
	// hint).
	return nil, nil, app.rejectUnknowns(u)
}
