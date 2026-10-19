package cli

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/saladtechnologies/salad-cloud-sdk-go/pkg/organizationdata"
	"github.com/spf13/cobra"

	"github.com/saladtechnologies/salad-cloud-cli/internal/api"
	"github.com/saladtechnologies/salad-cloud-cli/internal/clierr"
)

// updateFlags are the create flags that make sense on an existing group.
// Create-only fields (the name, restart policy, autostart, gateway auth) have
// no flag here: the API cannot change them after create.
type updateFlags struct {
	Image, Priority, DisplayName            string
	CPU, Memory, Replicas, Port             int64
	ImageSet, PrioritySet, DisplayNameSet   bool
	CPUSet, MemorySet, ReplicasSet, PortSet bool
	Gpus                                    []string
	Storage                                 int64 // GB
	StorageSet                              bool
	Countries                               []string
	Env                                     []string                                   // KEY=VALUE, merged into the group's current variables
	Command                                 []string                                   // the container command after "--"; nil when none was given
	ResolveGpu                              func(value string) (string, *clierr.Error) // injected; nil when no --gpu is a name
}

// any reports whether at least one update flag was given.
func (f updateFlags) any() bool {
	return f.ImageSet || f.PrioritySet || f.DisplayNameSet || f.CPUSet || f.MemorySet ||
		f.ReplicasSet || f.PortSet || len(f.Gpus) > 0 || len(f.Env) > 0 || f.StorageSet || len(f.Countries) > 0 ||
		f.Command != nil
}

// addUpdateFlags binds the update flags to u.
func addUpdateFlags(cmd *cobra.Command, u *updateFlags) {
	f := cmd.Flags()
	f.StringVar(&u.Image, "image", "", "container image")
	f.Int64Var(&u.CPU, "cpu", 0, "vCPU cores")
	f.Int64Var(&u.Memory, "memory", 0, "memory in MB")
	f.Int64Var(&u.Storage, "storage", 0, "storage in GB")
	f.StringArrayVar(
		&u.Countries,
		"country",
		nil,
		"country code the group may run in, repeatable; replaces the group's countries",
	)
	f.StringArrayVar(&u.Gpus, "gpu", nil, "GPU class UUID or name, repeatable; replaces the group's GPU classes")
	f.Int64Var(
		&u.Replicas,
		"replicas",
		0,
		"container instance count (0-500); puts scheduled scaling on hold, like scale",
	)
	f.StringArrayVarP(
		&u.Env,
		"env",
		"e",
		nil,
		"environment variable KEY=VALUE, repeatable; merged into the group's current variables",
	)
	f.StringVar(&u.Priority, "priority", "", "scheduling priority: high|medium|low|batch (ignored on CPU-only groups)")
	f.Int64Var(&u.Port, "port", 0, "container gateway port")
	f.StringVar(&u.DisplayName, "display-name", "", "human-readable display name")
}

// collectUpdateFlags is u, the bound update flags, with the ones given on the
// command line marked set.
func collectUpdateFlags(cmd *cobra.Command, u updateFlags) updateFlags {
	f := cmd.Flags()
	u.StorageSet = f.Changed("storage")
	u.ImageSet, u.CPUSet, u.MemorySet = f.Changed("image"), f.Changed("cpu"), f.Changed("memory")
	u.ReplicasSet, u.PrioritySet = f.Changed("replicas"), f.Changed("priority")
	u.PortSet, u.DisplayNameSet = f.Changed("port"), f.Changed("display-name")
	return u
}

// validateUpdateFlags checks the flag values before any file or API work,
// so bad input exits 2 or 6 even with no API key set.
func validateUpdateFlags(u updateFlags) *clierr.Error {
	if u.ImageSet && u.Image == "" {
		return clierr.New(clierr.CodeUsage, clierr.KindUsage, "--image cannot be empty")
	}
	if u.CPUSet && u.CPU < 1 {
		return clierr.New(
			clierr.CodeInvalidInput,
			clierr.KindInvalidInput,
			"invalid --cpu %d: must be at least 1",
			u.CPU,
		)
	}
	if u.MemorySet && u.Memory < 1 {
		return clierr.New(
			clierr.CodeInvalidInput,
			clierr.KindInvalidInput,
			"invalid --memory %d: must be at least 1 (MB)",
			u.Memory,
		)
	}
	if u.StorageSet && u.Storage < 1 {
		return clierr.New(
			clierr.CodeInvalidInput,
			clierr.KindInvalidInput,
			"invalid --storage %d: must be at least 1 (GB)",
			u.Storage,
		)
	}
	if _, cerr := normalizeCountries(u.Countries); cerr != nil {
		return cerr
	}
	if u.ReplicasSet {
		if cerr := validateScaleReplicas(u.Replicas); cerr != nil {
			return cerr
		}
	}
	if u.PrioritySet {
		switch u.Priority {
		case "high", "medium", "low", "batch":
		default:
			return clierr.New(clierr.CodeInvalidInput, clierr.KindInvalidInput,
				`invalid --priority "%s"`, u.Priority).WithHint("use high, medium, low or batch")
		}
	}
	if u.PortSet && (u.Port < 1 || u.Port > 65535) {
		return clierr.New(
			clierr.CodeInvalidInput,
			clierr.KindInvalidInput,
			"invalid --port %d: must be between 1 and 65535",
			u.Port,
		)
	}
	for _, kv := range u.Env {
		if k, _, ok := strings.Cut(kv, "="); !ok || k == "" {
			return clierr.New(clierr.CodeUsage, clierr.KindUsage, `invalid --env "%s"`, kv).
				WithHint("write it as KEY=VALUE")
		}
	}
	return nil
}

// buildUpdatePatch lays the flags over the patch file (fileRaw may be nil)
// and returns the merge patch to send; flags win over the file. With -e,
// currentEnv is the group's present variables: the API replaces the whole
// environment_variables map on every write, so the patch carries the full
// merged map — current values, then the file's changes (null removes a
// variable), then the flags. Without -e, the file's map is sent as written.
//
// intent is what the caller asked for, for the post-write checks: the wire
// patch plus an explicit null for each variable the file removed. The wire
// map must omit a removed variable rather than null it, but the checks need
// to know the removal was requested, or they report it as a side effect.
func buildUpdatePatch(
	fileRaw []byte,
	u updateFlags,
	currentEnv map[string]*string,
) (wire, intent json.RawMessage, cerr *clierr.Error) {
	doc := make(map[string]any)
	if len(fileRaw) > 0 {
		dec := json.NewDecoder(bytes.NewReader(fileRaw))
		dec.UseNumber() // keep the file's numbers exactly as written
		if err := dec.Decode(&doc); err != nil {
			return nil, nil, clierr.New(
				clierr.CodeInvalidInput,
				clierr.KindInvalidInput,
				"patch file is not a JSON object: %v",
				err,
			)
		}
	}
	if u.ImageSet {
		setPath(doc, "container.image", u.Image)
	}
	if u.Command != nil {
		setPath(doc, "container.command", u.Command)
	}
	if u.CPUSet {
		setPath(doc, "container.resources.cpu", u.CPU)
	}
	if u.MemorySet {
		setPath(doc, "container.resources.memory", u.Memory)
	}
	if u.StorageSet {
		setPath(doc, "container.resources.storage_amount", u.Storage*bytesPerGB)
	}
	if len(u.Countries) > 0 {
		codes, cerr := normalizeCountries(u.Countries)
		if cerr != nil {
			return nil, nil, cerr
		}
		doc["country_codes"] = codes
	}
	if len(u.Gpus) > 0 {
		ids := make([]string, 0, len(u.Gpus))
		for _, g := range u.Gpus {
			if isUUID(g) {
				ids = append(ids, g)
				continue
			}
			if u.ResolveGpu == nil {
				return nil, nil, clierr.New(clierr.CodeInvalidInput, clierr.KindInvalidInput,
					`--gpu "%s" is not a GPU class UUID, and name resolution is unavailable`, g)
			}
			id, rerr := u.ResolveGpu(g)
			if rerr != nil {
				return nil, nil, rerr
			}
			ids = append(ids, id)
		}
		setPath(doc, "container.resources.gpu_classes", ids)
	}
	if u.PrioritySet {
		setPath(doc, "container.priority", u.Priority)
	}
	if u.ReplicasSet {
		doc["replicas"] = u.Replicas
	}
	if u.PortSet {
		setPath(doc, "networking.port", u.Port)
	}
	if u.DisplayNameSet {
		doc["display_name"] = u.DisplayName
	}
	var removed []string // variables the file set to null, under -e
	if len(u.Env) > 0 {
		merged := make(map[string]any)
		for k, v := range currentEnv {
			if v != nil {
				merged[k] = *v
			}
		}
		if c, ok := doc["container"].(map[string]any); ok {
			if fileEnv, ok := c["environment_variables"].(map[string]any); ok {
				for k, v := range fileEnv {
					if v == nil {
						delete(merged, k)
						removed = append(removed, k)
					} else {
						merged[k] = v
					}
				}
			}
		}
		for _, kv := range u.Env {
			k, v, _ := strings.Cut(kv, "=")
			merged[k] = v
		}
		setPath(doc, "container.environment_variables", merged)
	}
	wire, err := json.Marshal(doc)
	if err != nil {
		return nil, nil, clierr.New(clierr.CodeGeneric, clierr.KindInternal, "cannot encode the patch: %v", err)
	}
	if len(removed) == 0 {
		return wire, wire, nil
	}
	container, ok := doc["container"].(map[string]any)
	env, envOK := container["environment_variables"].(map[string]any)
	if !ok || !envOK {
		return nil, nil, clierr.New(
			clierr.CodeGeneric,
			clierr.KindInternal,
			"cannot encode the patch: no environment variables to remove from",
		)
	}
	for _, k := range removed {
		env[k] = nil
	}
	intent, err = json.Marshal(doc)
	if err != nil {
		return nil, nil, clierr.New(clierr.CodeGeneric, clierr.KindInternal, "cannot encode the patch: %v", err)
	}
	return wire, intent, nil
}

// lazyGpuResolver resolves --gpu names against the organization's GPU
// classes, listing them once, on the first name that is not a UUID.
func lazyGpuResolver(cmd *cobra.Command, client *api.Client) func(string) (string, *clierr.Error) {
	var classes []organizationdata.GpuClass
	return func(value string) (string, *clierr.Error) {
		if classes == nil {
			list, cerr := client.ListGpuClasses(cmd.Context())
			if cerr != nil {
				return "", cerr
			}
			classes = list.Items
		}
		return resolveGpuClassName(value, classes)
	}
}

// envOf reads container.environment_variables from a group document.
func envOf(group any) map[string]*string {
	b, err := json.Marshal(group)
	if err != nil {
		return nil
	}
	var g struct {
		Container struct {
			Env map[string]*string `json:"environment_variables"`
		} `json:"container"`
	}
	if json.Unmarshal(b, &g) != nil {
		return nil
	}
	return g.Container.Env
}
