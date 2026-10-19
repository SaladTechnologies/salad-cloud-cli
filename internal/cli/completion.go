// Dynamic shell completion. Cobra's `completion bash|zsh|fish|powershell`
// command writes the scripts; this file teaches them the CLI's names:
// container groups, instances, inference endpoints, queues, GPU classes,
// organizations, and the values of enum flags.
//
// Completion runs inside the user's shell on every Tab, so it is silent: it
// builds its own client (no project-fallback note, no warnings), gives up
// after completionTimeout, and turns any error into no suggestions, so a
// missing key or an offline machine never breaks the shell.

package cli

import (
	"context"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/saladtechnologies/salad-cloud-cli/internal/api"
	"github.com/saladtechnologies/salad-cloud-cli/internal/config"
	"github.com/saladtechnologies/salad-cloud-cli/internal/logsq"
	"github.com/saladtechnologies/salad-cloud-cli/internal/ptr"
)

// completionTimeout bounds the API calls one Tab makes.
const completionTimeout = 3 * time.Second

// completer lists candidate values for one completion.
type completer func(ctx context.Context, c *api.Client, cmd *cobra.Command, args []string) []string

// complete wraps a completer as a cobra completion function: it builds the
// silent client from the root's --org/--project, and returns no candidates
// (and no file completion) on any failure.
func complete(f completer) func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	return func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		fr := readFlags(cmd.Root().PersistentFlags())
		org, project := fr.String("org"), fr.String("project")
		if fr.Err() != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		res, err := config.Resolve(procEnv, org, project)
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		client, cerr := api.New(res, procEnv.Getenv)
		if cerr != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		ctx, cancel := context.WithTimeout(context.Background(), completionTimeout)
		defer cancel()
		return filterPrefix(f(ctx, client, cmd, args), toComplete), cobra.ShellCompDirectiveNoFileComp
	}
}

// staticValues completes a fixed set of values.
func staticValues(values ...string) func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	return func(_ *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		return filterPrefix(values, toComplete), cobra.ShellCompDirectiveNoFileComp
	}
}

// firstArgOnly limits a positional completer to the first argument.
func firstArgOnly(
	f func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective),
) func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	return func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		if len(args) > 0 {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		return f(cmd, args, toComplete)
	}
}

func filterPrefix(values []string, prefix string) []string {
	var out []string
	for _, v := range values {
		if strings.HasPrefix(strings.ToLower(v), strings.ToLower(prefix)) {
			out = append(out, v)
		}
	}
	slices.Sort(out)
	return out
}

func groupNames(ctx context.Context, c *api.Client, _ *cobra.Command, _ []string) []string {
	groups, cerr := c.ListGroups(ctx)
	if cerr != nil {
		return nil
	}
	var names []string
	for i := range groups.Items {
		names = append(names, ptr.Deref(groups.Items[i].Name))
	}
	return names
}

// instanceIDs lists the instances of the group the command names: -c, or
// the positional group of ssh/sftp.
func instanceIDs(ctx context.Context, c *api.Client, cmd *cobra.Command, args []string) []string {
	group := ""
	if f := cmd.Flags().Lookup("container-group"); f != nil {
		group = f.Value.String()
	} else if len(args) > 0 {
		group = args[0]
	}
	if group == "" {
		return nil
	}
	coll, cerr := c.ListInstances(ctx, group)
	if cerr != nil {
		return nil
	}
	var ids []string
	for i := range coll.Instances {
		ids = append(ids, ptr.Deref(coll.Instances[i].Id))
	}
	return ids
}

func endpointNames(ctx context.Context, c *api.Client, _ *cobra.Command, _ []string) []string {
	coll, cerr := c.ListInferenceEndpoints(ctx)
	if cerr != nil {
		return nil
	}
	var names []string
	for i := range coll.Items {
		names = append(names, ptr.Deref(coll.Items[i].Name))
	}
	return names
}

func queueNames(ctx context.Context, c *api.Client, _ *cobra.Command, _ []string) []string {
	coll, cerr := c.ListQueues(ctx)
	if cerr != nil {
		return nil
	}
	var names []string
	for i := range coll.Items {
		names = append(names, ptr.Deref(coll.Items[i].Name))
	}
	return names
}

func gpuClassNames(ctx context.Context, c *api.Client, _ *cobra.Command, _ []string) []string {
	list, cerr := c.ListGpuClasses(ctx)
	if cerr != nil {
		return nil
	}
	var names []string
	for i := range list.Items {
		names = append(names, strings.TrimSpace(ptr.Deref(list.Items[i].Name)))
	}
	return names
}

func orgNames(ctx context.Context, c *api.Client, _ *cobra.Command, _ []string) []string {
	list, cerr := c.ListOrganizations(ctx)
	if cerr != nil || !list.Usable() {
		return nil
	}
	return list.OrgNames()
}

var registerCompletionsOnce sync.Once

// registerCompletions attaches the completion functions to the tree, once:
// positional arguments by command path, flags by name wherever they occur.
func registerCompletions(root *cobra.Command) {
	registerCompletionsOnce.Do(func() {
		positional := map[string]completer{
			"container-groups get":                  groupNames,
			"container-groups update":               groupNames,
			"container-groups start":                groupNames,
			"container-groups stop":                 groupNames,
			"container-groups delete":               groupNames,
			"container-groups scale":                groupNames,
			"container-groups logs":                 groupNames,
			"container-groups ssh":                  groupNames,
			"container-groups sftp":                 groupNames,
			"container-groups instances get":        instanceIDs,
			"container-groups instances update":     instanceIDs,
			"container-groups instances restart":    instanceIDs,
			"container-groups instances recreate":   instanceIDs,
			"container-groups instances reallocate": instanceIDs,
			"inference get":                         endpointNames,
			"queues get":                            queueNames,
			"queues update":                         queueNames,
			"queues delete":                         queueNames,
			"gpus get":                              gpuClassNames,
			"orgs get":                              orgNames,
			"use":                                   orgNames,
		}
		flags := map[string]completer{
			"container-group": groupNames,
			"from":            groupNames,
			"instance":        instanceIDs,
			"endpoint":        endpointNames,
			"queue":           queueNames,
			"gpu":             gpuClassNames,
		}
		enums := map[string][]string{
			"priority":       {"high", "medium", "low", "batch"},
			"restart-policy": {"always", "on_failure", "never"},
			"severity":       logsq.Severities,
			"status":         {"pending", "running", "stopped", "succeeded", "failed", "deploying"},
		}
		noteTreeErr(root.RegisterFlagCompletionFunc("output", staticValues("table", "json", "yaml")))
		noteTreeErr(root.RegisterFlagCompletionFunc("debug", staticValues("body")))
		noteTreeErr(root.RegisterFlagCompletionFunc("org", complete(orgNames)))

		var walk func(c *cobra.Command)
		walk = func(c *cobra.Command) {
			path := strings.TrimPrefix(c.CommandPath(), root.Name()+" ")
			if f, ok := positional[path]; ok && c.ValidArgsFunction == nil {
				c.ValidArgsFunction = firstArgOnly(complete(f))
			}
			for name, f := range flags {
				if c.LocalFlags().Lookup(name) != nil {
					noteTreeErr(c.RegisterFlagCompletionFunc(name, complete(f)))
				}
			}
			for name, values := range enums {
				if c.LocalFlags().Lookup(name) != nil {
					noteTreeErr(c.RegisterFlagCompletionFunc(name, staticValues(values...)))
				}
			}
			for _, sub := range c.Commands() {
				walk(sub)
			}
		}
		walk(root)
	})
}
