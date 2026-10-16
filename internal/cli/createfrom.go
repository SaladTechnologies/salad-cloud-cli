// create --from: a new container group copied from an existing one in the
// same organization. The source group's settings become create's base
// request, -f merges over that, and the usual create flags apply on top.

package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"

	"github.com/saladtechnologies/salad-cloud-sdk-go/pkg/containergroups"
	"github.com/saladtechnologies/salad-cloud-sdk-go/pkg/shared"
	"github.com/spf13/cobra"

	"github.com/saladtechnologies/salad-cloud-cli/internal/api"
	"github.com/saladtechnologies/salad-cloud-cli/internal/clierr"
	"github.com/saladtechnologies/salad-cloud-cli/internal/input"
	"github.com/saladtechnologies/salad-cloud-cli/internal/output"
	"github.com/saladtechnologies/salad-cloud-cli/internal/ptr"
)

// parseFromRef splits --from's [<project>/]<group>. project is "" when the
// reference names only a group (the current project). A third segment would
// read as <org>/<project>/<group>, and copies never cross organizations.
func parseFromRef(ref string) (project, group string, cerr *clierr.Error) {
	parts := strings.Split(ref, "/")
	switch {
	case len(parts) > 2:
		return "", "", clierr.New(
			clierr.CodeUsage,
			clierr.KindUsage,
			`invalid --from "%s"`,
			ref,
		).WithHint("give <group> or <project>/<group>; --from stays within the organization")
	case len(parts) == 2:
		project, group = parts[0], parts[1]
		if project == "" {
			return "", "", clierr.New(
				clierr.CodeUsage,
				clierr.KindUsage,
				`invalid --from "%s": the project before the / is empty`,
				ref,
			)
		}
	default:
		group = parts[0]
	}
	if group == "" {
		return "", "", clierr.New(
			clierr.CodeUsage,
			clierr.KindUsage,
			`invalid --from "%s": no container group named`,
			ref,
		)
	}
	return project, group, nil
}

// checkCopyTarget rejects a copy that can only fail on a name conflict: one
// into the source's own project under the source's name (or no name, which
// keeps it).
func checkCopyTarget(sameProject bool, name, source string) *clierr.Error {
	if sameProject && (name == "" || name == source) {
		return clierr.New(clierr.CodeUsage, clierr.KindUsage,
			"give the copy a new name: 'create <new-group> --from %s' (names are unique within a project)", source)
	}
	return nil
}

// copyDocument renders g as a creation request document. The priority
// relocation is applied and every field the creation schema does not know
// (id, current_state, readme, networking.dns, …) is removed. autostart_policy
// is removed as well: the API consumes it once a group starts, so the
// source's stored value says nothing about how it was created, and the copy
// takes create's default (or --no-autostart). When the copy gets a new name
// and no --display-name, its display name becomes that name, so the portal
// does not show two groups under one display name.
func copyDocument(g *shared.ContainerGroup, newName string, displayNameSet bool) (json.RawMessage, error) {
	// MarshalRequest, not toTree: the API returns empty lists ([] probe
	// headers among them) and the copy must send them back.
	b, err := output.MarshalRequest(g)
	if err != nil {
		return nil, err
	}
	var tree any
	if err := json.Unmarshal(b, &tree); err != nil {
		return nil, err
	}
	obj, ok := tree.(map[string]any)
	if !ok {
		return nil, errors.New("unexpected group document")
	}
	for reqPath, respPath := range requestToResponsePath {
		if v, present := lookupPath(obj, respPath); present {
			if _, already := lookupPath(obj, reqPath); !already {
				setPath(obj, reqPath, v)
			}
			deletePath(obj, respPath)
		}
	}
	delete(obj, "autostart_policy")
	var dropped []string
	filtered, ok := input.FilterTree(obj, reflect.TypeFor[containergroups.ContainerGroupCreationRequest](), "", &dropped).(map[string]any)
	if !ok {
		return nil, errors.New("unexpected group document")
	}
	if newName != "" && newName != ptr.Deref(g.Name) && !displayNameSet {
		filtered["display_name"] = newName
	}
	return json.Marshal(filtered)
}

// publicRegistries are the hosts whose images pull without credentials as
// far as the CLI can tell; a copy of an image from one needs no note.
var publicRegistries = map[string]bool{"docker.io": true, "ghcr.io": true, "public.ecr.aws": true, "quay.io": true}

// imageOnPublicRegistry reports whether image names one of the public
// registries: an image with no registry host (busybox:1.36, library/x) is on
// Docker Hub. Anything else, or an empty image, may be private.
func imageOnPublicRegistry(image string) bool {
	if image == "" {
		return false
	}
	first, _, hasSlash := strings.Cut(image, "/")
	if !hasSlash || !strings.ContainsAny(first, ".:") && first != "localhost" {
		return true // Docker Hub: no registry host in the reference
	}
	return publicRegistries[strings.ToLower(first)]
}

// noteRegistryCredentials says that registry credentials do not come across,
// unless the -f document supplies them or the image is on a public registry.
// The API never returns them, so the CLI cannot tell whether the source had
// any; a copy of a private image without them never pulls.
func noteRegistryCredentials(w io.Writer, fileRaw json.RawMessage, image string) {
	if imageOnPublicRegistry(image) {
		return
	}
	if len(fileRaw) > 0 {
		var doc any
		if json.Unmarshal(fileRaw, &doc) == nil {
			if _, has := lookupPath(doc, "container.registry_authentication"); has {
				return
			}
		}
	}
	fmt.Fprintln(
		w,
		"note: registry credentials are never returned by the API, so they are not copied; for a private image, "+
			"pass them with -f <file> holding container.registry_authentication",
	)
}

// loadFromBase builds create's base request for --from: the source group,
// copied, with the -f document (if any) deep-merged over it. baseRaw and
// unknowns are set only on the --allow-unknown-fields path, as for -f alone.
func loadFromBase(cmd *cobra.Command, ref, file string, allowUnknown bool, name string, notes io.Writer) (
	base containergroups.ContainerGroupCreationRequest, baseRaw []byte, unknowns []input.Unknown, cerr *clierr.Error,
) {
	project, group, cerr := parseFromRef(ref)
	if cerr != nil {
		return base, nil, nil, cerr
	}
	client, res, cerr := NewClient(cmd)
	if cerr != nil {
		return base, nil, nil, cerr
	}
	if cerr := client.RequireOrg(); cerr != nil {
		return base, nil, nil, cerr
	}
	explicitProject := project != ""
	if !explicitProject {
		project = client.Project()
	}
	if cerr := checkCopyTarget(project == client.Project(), name, group); cerr != nil {
		return base, nil, nil, cerr
	}

	var fileRaw json.RawMessage
	if file != "" {
		var probe containergroups.ContainerGroupCreationRequest
		raw, u, lerr := input.LoadLenient(file, &probe)
		if lerr != nil {
			return base, nil, nil, lerr
		}
		if !allowUnknown {
			if cerr := rejectUnknowns(u); cerr != nil {
				return base, nil, nil, cerr
			}
		}
		fileRaw, unknowns = raw, u
	}

	r := res
	r.Project = project
	if explicitProject {
		r.ProjectSource = "flag"
	}
	src, cerr := api.New(r)
	if cerr != nil {
		return base, nil, nil, cerr
	}
	warnOnAssumedProject(src, r)
	g, cerr := src.GetGroup(cmd.Context(), group)
	if cerr != nil {
		return base, nil, nil, groupNotFound(cerr, group, src.Org(), src.Project())
	}
	doc, err := copyDocument(g, name, cmd.Flags().Changed("display-name"))
	if err != nil {
		return base, nil, nil, clierr.New(clierr.CodeGeneric, clierr.KindInternal, "cannot copy group: %v", err)
	}
	merged := doc
	if len(fileRaw) > 0 {
		if merged, cerr = input.MergeJSON(doc, fileRaw); cerr != nil {
			return base, nil, nil, cerr
		}
	}
	if err := json.Unmarshal(merged, &base); err != nil {
		return base, nil, nil, clierr.New(clierr.CodeGeneric, clierr.KindInternal, "cannot copy group: %v", err)
	}
	image := ""
	if base.Container != nil {
		image = ptr.Deref(base.Container.Image)
	}
	if cmd.Flags().Changed("image") {
		image, _ = cmd.Flags().GetString("image")
	}
	noteRegistryCredentials(notes, fileRaw, image)
	if allowUnknown && len(unknowns) > 0 {
		baseRaw = merged
	} else {
		unknowns = nil
	}
	return base, baseRaw, unknowns, nil
}
