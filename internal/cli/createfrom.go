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
	"github.com/saladtechnologies/salad-cloud-cli/internal/config"
	"github.com/saladtechnologies/salad-cloud-cli/internal/input"
	"github.com/saladtechnologies/salad-cloud-cli/internal/output"
	"github.com/saladtechnologies/salad-cloud-cli/internal/ptr"
)

// flagProvenance is the source config.Resolved records for a value given on
// the command line.
const flagProvenance = "flag"

// fromRef is --from's reference: the source group, and its project ("" for the current one).
type fromRef struct {
	project, group string
}

// parseFromRef splits --from's [<project>/]<group> into the project and the
// group. The project is "" when the reference names only a group (the
// current project). A third segment would read as <org>/<project>/<group>,
// and copies never cross organizations.
func parseFromRef(ref string) (fromRef, *clierr.Error) {
	project, group, hasProject := strings.Cut(ref, "/")
	switch {
	case hasProject && strings.Contains(group, "/"):
		return fromRef{}, clierr.New(
			clierr.CodeUsage,
			clierr.KindUsage,
			`invalid --from "%s"`,
			ref,
		).WithHint("give <group> or <project>/<group>; --from stays within the organization")
	case hasProject:
		if project == "" {
			return fromRef{}, clierr.New(
				clierr.CodeUsage,
				clierr.KindUsage,
				`invalid --from "%s": the project before the / is empty`,
				ref,
			)
		}
	default:
		project, group = "", project
	}
	if group == "" {
		return fromRef{}, clierr.New(
			clierr.CodeUsage,
			clierr.KindUsage,
			`invalid --from "%s": no container group named`,
			ref,
		)
	}
	return fromRef{project: project, group: group}, nil
}

// checkCopyTarget rejects a copy that can only fail on a name conflict: one
// from sourceProject into the same targetProject under the source's name (or
// no name, which keeps it).
func checkCopyTarget(sourceProject, targetProject, name, source string) *clierr.Error {
	if sourceProject == targetProject && (name == "" || name == source) {
		return clierr.New(clierr.CodeUsage, clierr.KindUsage,
			"give the copy a new name: 'create <new-group> --from %s' (names are unique within a project)", source)
	}
	return nil
}

// errUnexpectedGroupDocument is a group document that did not decode to a
// JSON object.
var errUnexpectedGroupDocument = errors.New("unexpected group document")

// copyDocument renders g as a creation request document. The priority
// relocation is applied and every field the creation schema does not know
// (id, current_state, readme, networking.dns, …) is removed. autostart_policy
// is removed as well: the API consumes it once a group starts, so the
// source's stored value says nothing about how it was created, and the copy
// takes create's default (or --no-autostart). A displayName other than ""
// replaces the source's display name (see copyDisplayName).
func copyDocument(g *shared.ContainerGroup, displayName string) (json.RawMessage, error) {
	// MarshalRequest, not toTree: the API returns empty lists ([] probe
	// headers among them) and the copy must send them back.
	b, err := output.MarshalRequest(g)
	if err != nil {
		return nil, fmt.Errorf("encoding the source group: %w", err)
	}
	var tree any
	if err = json.Unmarshal(b, &tree); err != nil {
		return nil, fmt.Errorf("decoding the source group: %w", err)
	}
	obj, ok := tree.(map[string]any)
	if !ok {
		return nil, errUnexpectedGroupDocument
	}
	copyRelocateResponseFields(obj)
	delete(obj, "autostart_policy")
	var dropped []string
	requestType := reflect.TypeFor[containergroups.ContainerGroupCreationRequest]()
	filtered, ok := input.FilterTree(obj, requestType, "", &dropped).(map[string]any)
	if !ok {
		return nil, errUnexpectedGroupDocument
	}
	if displayName != "" {
		filtered["display_name"] = displayName
	}
	doc, err := json.Marshal(filtered)
	if err != nil {
		return nil, fmt.Errorf("encoding the copy: %w", err)
	}
	return doc, nil
}

// copyDisplayName is the display name a copy of g named newName gets when no
// --display-name is given: newName, when the copy gets a new name, so the
// portal does not show two groups under one display name; otherwise "" (the
// source's display name stays).
func copyDisplayName(g *shared.ContainerGroup, newName string) string {
	if newName != "" && newName != ptr.Deref(g.Name) {
		return newName
	}
	return ""
}

// copyRelocateResponseFields moves each field a group response holds at a
// different path from the creation request (priority) to the request's
// path, unless obj already has a value there.
func copyRelocateResponseFields(obj map[string]any) {
	for reqPath, respPath := range requestToResponsePath() {
		v, present := lookupPath(obj, respPath)
		if !present {
			continue
		}
		if _, already := lookupPath(obj, reqPath); !already {
			setPath(obj, reqPath, v)
		}
		deletePath(obj, respPath)
	}
}

// publicRegistries are the hosts whose images pull without credentials as
// far as the CLI can tell; a copy of an image from one needs no note.
func publicRegistries() map[string]bool {
	return map[string]bool{"docker.io": true, "ghcr.io": true, "public.ecr.aws": true, "quay.io": true}
}

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
	return publicRegistries()[strings.ToLower(first)]
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
	fprintln(
		w,
		"note: registry credentials are never returned by the API, so they are not copied; for a private image, "+
			"pass them with -f <file> holding container.registry_authentication",
	)
}

// loadFromBase builds create's base for --from (opts.from) and a group named
// name: the source group, copied, with the -f document (if any) deep-merged
// over it. Its raw document and unknowns are set only on the
// --allow-unknown-fields path, as for -f alone.
func (app *App) loadFromBase(
	cmd *cobra.Command,
	opts *createOptions,
	name string,
	notes io.Writer,
) (createBase, *clierr.Error) {
	source, group, cerr := app.fromSourceProject(cmd, opts.from, name)
	if cerr != nil {
		return createBase{}, cerr
	}

	var fileRaw json.RawMessage
	var unknowns []input.Unknown
	if opts.file != "" {
		var probe containergroups.ContainerGroupCreationRequest
		raw, u, lerr := input.LoadLenient(opts.file, app.Stdin, &probe)
		if lerr != nil {
			return createBase{}, lerr
		}
		if !opts.allowUnknown {
			if uerr := app.rejectUnknowns(u); uerr != nil {
				return createBase{}, uerr
			}
		}
		fileRaw, unknowns = raw, u
	}

	g, cerr := app.fromFetchSource(cmd, &source, group)
	if cerr != nil {
		return createBase{}, cerr
	}
	req, merged, cerr := fromCopyBase(cmd, g, name, fileRaw)
	if cerr != nil {
		return createBase{}, cerr
	}
	noteRegistryCredentials(notes, fileRaw, fromCopyImage(cmd, &req, opts.flags.Image))
	base := createBase{req: req, given: true, raw: nil, unknowns: nil}
	if opts.allowUnknown && len(unknowns) > 0 {
		base.raw, base.unknowns = merged, unknowns
	}
	return base, nil
}

// fromSourceProject resolves --from's reference to the source group and the
// configuration for its project (the current one unless the reference
// names one), after checking that the copy can get a name of its own.
func (app *App) fromSourceProject(
	cmd *cobra.Command,
	ref, name string,
) (config.Resolved, string, *clierr.Error) {
	from, cerr := parseFromRef(ref)
	if cerr != nil {
		return config.Resolved{}, "", cerr
	}
	project, group := from.project, from.group
	client, res, cerr := app.NewClient(cmd)
	if cerr != nil {
		return config.Resolved{}, "", cerr
	}
	if oerr := client.RequireOrg(); oerr != nil {
		return config.Resolved{}, "", oerr
	}
	explicitProject := project != ""
	if !explicitProject {
		project = client.Project()
	}
	if terr := checkCopyTarget(project, client.Project(), name, group); terr != nil {
		return config.Resolved{}, "", terr
	}
	r := res
	r.Project = project
	if explicitProject {
		r.ProjectSource = flagProvenance
	}
	return r, group, nil
}

// fromFetchSource gets the source group from its project, warning first
// when that project was assumed.
func (app *App) fromFetchSource(
	cmd *cobra.Command,
	source *config.Resolved,
	group string,
) (*shared.ContainerGroup, *clierr.Error) {
	src, cerr := api.New(source, app.apiOptions())
	if cerr != nil {
		return nil, cerr
	}
	app.warnOnAssumedProject(src, source)
	g, cerr := src.GetGroup(cmd.Context(), group)
	if cerr != nil {
		return nil, groupNotFound(cerr, group, src.Org(), src.Project())
	}
	return g, nil
}

// fromCopyBase is the copy of g as create's base request, with the -f
// document (fileRaw, may be empty) deep-merged over it, and the merged
// document it was decoded from.
func fromCopyBase(
	cmd *cobra.Command,
	g *shared.ContainerGroup,
	name string,
	fileRaw json.RawMessage,
) (containergroups.ContainerGroupCreationRequest, json.RawMessage, *clierr.Error) {
	var base containergroups.ContainerGroupCreationRequest
	displayName := ""
	if !cmd.Flags().Changed("display-name") {
		displayName = copyDisplayName(g, name)
	}
	doc, err := copyDocument(g, displayName)
	if err != nil {
		return base, nil, clierr.New(clierr.CodeGeneric, clierr.KindInternal, "cannot copy group: %v", err)
	}
	merged := doc
	if len(fileRaw) > 0 {
		var cerr *clierr.Error
		if merged, cerr = input.MergeJSON(doc, fileRaw); cerr != nil {
			return base, nil, cerr
		}
	}
	if err = json.Unmarshal(merged, &base); err != nil {
		return base, nil, clierr.New(clierr.CodeGeneric, clierr.KindInternal, "cannot copy group: %v", err)
	}
	return base, merged, nil
}

// fromCopyImage is the image the copy will run: --image when given, else
// the base's.
func fromCopyImage(
	cmd *cobra.Command,
	base *containergroups.ContainerGroupCreationRequest,
	imageFlag string,
) string {
	if cmd.Flags().Changed("image") {
		return imageFlag
	}
	if base.Container != nil {
		return ptr.Deref(base.Container.Image)
	}
	return ""
}
