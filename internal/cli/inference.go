// The inference plumbing: `saladcloud inference` mirrors the managed
// inference-endpoints API surface noun-for-noun. Plumbing is cheap and
// generic; flagship porcelain (transcribe.go) composes over it client-side.

package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/saladtechnologies/salad-cloud-sdk-go/pkg/inferenceendpoints"
	"github.com/spf13/cobra"

	"github.com/saladtechnologies/salad-cloud-cli/internal/api"
	"github.com/saladtechnologies/salad-cloud-cli/internal/clierr"
	"github.com/saladtechnologies/salad-cloud-cli/internal/output"
	"github.com/saladtechnologies/salad-cloud-cli/internal/ptr"
)

// jobActionResult is the Emit payload for the bodiless job verb (cancel).
type jobActionResult struct {
	Endpoint string `json:"endpoint"`
	ID       string `json:"id"`
	Action   string `json:"action"`
	OK       bool   `json:"ok"`
}

// endpointNotFound rewrites a 404 on an endpoint-level call with the
// endpoint's identity and the discovery hint.
func endpointNotFound(cerr *clierr.Error, endpoint, org string) *clierr.Error {
	return describeNotFound(cerr, "to list the endpoints, run saladcloud inference list",
		`inference endpoint "%s" not found in organization %s`, endpoint, org)
}

// jobNotFound rewrites a 404 on a job-level call with the job's identity.
func jobNotFound(cerr *clierr.Error, endpoint, id string) *clierr.Error {
	return describeNotFound(cerr, "to list its jobs, run saladcloud inference jobs list --endpoint "+endpoint,
		`job "%s" not found on endpoint "%s"`, id, endpoint)
}

// jobStatusString renders a job's status ("-" when the API omits it).
func jobStatusString(j *inferenceendpoints.InferenceEndpointJob) string {
	if j == nil || j.Status == nil {
		return "-"
	}
	return string(*j.Status)
}

// lastEventSuffix renders " (last event: <action> @ <time>)" for failure
// messages and the job tables, or "" when the job carries no events.
func lastEventSuffix(j *inferenceendpoints.InferenceEndpointJob) string {
	if j == nil || len(j.Events) == 0 {
		return ""
	}
	e := &j.Events[len(j.Events)-1]
	action := "-"
	if e.Action != nil {
		action = string(*e.Action)
	}
	return fmt.Sprintf(" (last event: %s @ %s)", action, ptr.Deref(e.Time))
}

// compactJSON renders a decoded JSON value ("input"/"output" are `any` in the
// SDK) as one compact line, truncated to limit bytes (0 = no truncation).
// nil renders as "-".
func compactJSON(v any, limit int) string {
	if v == nil {
		return "-"
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	s := string(b)
	if limit > 0 {
		s = truncateRunes(s, limit)
	}
	return s
}

// truncateRunes caps s at limit bytes without splitting a UTF-8 rune,
// appending "..." when it truncates.
func truncateRunes(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	cut := limit
	for cut > 0 && s[cut]&0xC0 == 0x80 {
		cut--
	}
	return s[:cut] + "..."
}

// oneLine collapses all whitespace runs (including newlines) to single
// spaces so multi-line descriptions fit a table cell.
func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// writeJobDetail writes the two-column key/value table for one job. Input
// and output are compact JSON truncated to 200 bytes; the note row points
// at -o json for the full document.
func writeJobDetail(w io.Writer, job *inferenceendpoints.InferenceEndpointJob) {
	row := func(k, v string) { fmt.Fprintf(w, "%s\t%s\n", k, v) }
	row("ID", ptr.Deref(job.Id))
	row("Endpoint", ptr.Deref(job.InferenceEndpointName))
	row("Status", jobStatusString(job))
	row("Created", timeCell(job.CreateTime))
	row("Updated", timeCell(job.UpdateTime))
	if wh := ptr.Deref(job.Webhook); wh != "" {
		row("Webhook", wh)
	}
	writeJobBody(row, job.Input, job.Output, len(job.Events), lastEventSuffix(job))
}

// writeJobBody writes the rows both job tables share: input and output as
// compact JSON cut to 200 bytes, the events row, and the note pointing at
// -o json only when something was cut.
func writeJobBody(row func(k, v string), input, out any, events int, lastEvent string) {
	in, outCell := compactJSON(input, 200), compactJSON(out, 200)
	row("Input", in)
	row("Output", outCell)
	if events > 0 {
		row("Events", fmt.Sprintf("%d%s", events, lastEvent))
	}
	// Compact JSON never ends in "..." unless it was cut.
	if strings.HasSuffix(in, "...") || strings.HasSuffix(outCell, "...") {
		row("Note", "Input and output are cut short here; -o json shows the full document")
	}
}

// --- job --wait machinery (shared with the transcribe porcelain) ---

// jobWaitDefaultTimeout is `inference jobs create --wait`'s default --timeout.
const jobWaitDefaultTimeout = 10 * time.Minute

// addJobWaitFlags registers --wait/--timeout on jobs create. The values are
// read via the shared waitFlagValues (waitfor.go).
func addJobWaitFlags(cmd *cobra.Command) {
	cmd.Flags().
		Bool("wait", false, "block until the job reaches a terminal status (succeeded exits 0; failed/cancelled exit 1)")
	cmd.Flags().Duration("timeout", jobWaitDefaultTimeout, "give up on --wait after this long")
}

// jobWaitDeps are waitForJobTerminal's dependencies, injectable for tests.
// J is the polled job type: an inference job or a queue job, which share
// one document shape ({input, metadata?, webhook?} plus status and events).
type jobWaitDeps[J any] struct {
	get func(ctx context.Context) (*J, *clierr.Error)
	// view reads the job's status and its last event ("" when none).
	view  func(*J) (status, lastEvent string)
	now   func() time.Time
	sleep func(ctx context.Context, d time.Duration) error // returns ctx.Err() when cancelled
	// progress receives the table-mode progress lines and transient-error
	// warnings; nil means os.Stderr.
	progress io.Writer
	// label prefixes the progress line, e.g. `waiting for job "x"` or
	// "transcribing".
	label string
	// where places the job in messages: `on endpoint "transcribe"`.
	where string
	// check is the command that inspects the job, for the timeout message.
	check string
}

// inferenceJobView is jobWaitDeps.view for inference jobs.
func inferenceJobView(j *inferenceendpoints.InferenceEndpointJob) (string, string) {
	return jobStatusString(j), lastEventSuffix(j)
}

// waitForJobTerminal polls d.get until the job's status is terminal, the
// context is cancelled, or timeout elapses. The first poll is immediate,
// then one every poll interval. succeeded returns (job, nil); failed and
// cancelled return the job AND an exit-1 error naming the status and last
// event. Transient API errors warn on stderr and keep polling (same policy
// as waitForGroupStatus); in table mode a progress line is written whenever
// the observed status changes, and repeated every progressRepeat while it
// does not.
func waitForJobTerminal[J any](
	ctx context.Context,
	d jobWaitDeps[J],
	id string,
	timeout, poll time.Duration,
) (*J, *clierr.Error) {
	stderr := d.progress
	if stderr == nil {
		stderr = os.Stderr
	}
	start := d.now()
	deadline := start.Add(timeout)
	lastStatus := "-"
	lastProgress := ""
	var lastPrinted time.Time
	for {
		job, cerr := d.get(ctx)
		switch {
		case cerr != nil && ctx.Err() != nil:
			return nil, jobWaitInterrupted()
		case cerr != nil:
			switch cerr.Code {
			case clierr.CodeNotFound, clierr.CodeAuth, clierr.CodeUsage, clierr.CodeInvalidInput:
				return nil, cerr
			default:
				// Transient (network, 5xx, rate limit): note it, keep polling.
				fmt.Fprintf(stderr, "warning: %s (still waiting)\n", cerr.Message)
			}
		default:
			status, lastEvent := d.view(job)
			lastStatus = status
			switch inferenceendpoints.Status(status) {
			case inferenceendpoints.STATUS_SUCCEEDED:
				return job, nil
			case inferenceendpoints.STATUS_FAILED, inferenceendpoints.STATUS_CANCELLED:
				return job, clierr.New(clierr.CodeGeneric, jobEndKind(status),
					"job %s %s %s%s", id, d.where, status, lastEvent)
			}
			if output.Mode() == "table" {
				// The elapsed suffix is excluded from the change check;
				// otherwise every poll would print.
				line := fmt.Sprintf("%s: %s", d.label, status)
				if now := d.now(); line != lastProgress || now.Sub(lastPrinted) >= progressRepeat {
					lastProgress, lastPrinted = line, now
					fmt.Fprintf(stderr, "%s (%s elapsed)\n", line, d.now().Sub(start).Round(time.Second))
				}
			}
		}
		if !d.now().Before(deadline) {
			return nil, clierr.New(clierr.CodeGeneric, clierr.KindWaitTimeout,
				"timed out after %s waiting for job %s %s (last status: %s)", timeout, id, d.where, lastStatus).
				WithHint("retry with a longer --timeout, or to check on the job, run %s; the job continues server-side", d.check)
		}
		if err := d.sleep(ctx, poll); err != nil {
			return nil, jobWaitInterrupted()
		}
	}
}

// jobEndKind is the error kind for a job that reached a terminal status
// other than succeeded.
func jobEndKind(status string) clierr.Kind {
	switch inferenceendpoints.Status(status) {
	case inferenceendpoints.STATUS_FAILED:
		return clierr.KindJobFailed
	case inferenceendpoints.STATUS_CANCELLED:
		return clierr.KindJobCancelled
	}
	return clierr.KindAPIError
}

// jobWaitInterrupted is the SIGINT/cancel result: not success, and not
// silence, because the job keeps running server-side.
func jobWaitInterrupted() *clierr.Error {
	return clierr.New(clierr.CodeGeneric, clierr.KindInterrupted, "wait interrupted; the job continues server-side")
}

// ctxSleep sleeps for d or until ctx is cancelled (returning ctx.Err()).
func ctxSleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// liveJobWaitDeps builds the production jobWaitDeps for one inference job.
func liveJobWaitDeps(
	client *api.Client,
	endpoint, id, label string,
) jobWaitDeps[inferenceendpoints.InferenceEndpointJob] {
	return jobWaitDeps[inferenceendpoints.InferenceEndpointJob]{
		get: func(ctx context.Context) (*inferenceendpoints.InferenceEndpointJob, *clierr.Error) {
			job, cerr := client.GetInferenceJob(ctx, endpoint, id)
			return job, jobNotFound(cerr, endpoint, id)
		},
		view:  inferenceJobView,
		now:   time.Now,
		sleep: ctxSleep,
		label: label,
		where: "on endpoint " + endpoint,
		check: fmt.Sprintf("saladcloud inference jobs get %s --endpoint %s", id, endpoint),
	}
}

// --- command tree ---

// inferenceCmd is both the product area and its resource: an endpoint is
// what 'inference list' lists, so it is the one singular noun that takes
// list/get. Porcelain verbs (transcribe.go) are added under it, each named
// exactly after the endpoint it drives.
var inferenceCmd = &cobra.Command{
	Use:   "inference",
	Short: "List managed inference endpoints and run jobs on them (transcription and others)",
	Long: `Interact with the organization's managed inference endpoints.

  saladcloud inference list | get <endpoint>          the endpoints
  saladcloud inference jobs ... --endpoint <endpoint> jobs on any endpoint
  saladcloud inference <endpoint> ...                 one step per endpoint

Inference endpoints belong to the organization, so no project is involved.
inference list fetches every page; inference jobs list fetches up to
--limit jobs (default 100; 0 for all, up to 10,000).

Each endpoint the CLI knows has a porcelain command named exactly after it
(inference transcribe, inference transcription-lite) whose flags are the
endpoint's input fields. An endpoint newer than this CLI, or one whose name
is reserved here (list, get, jobs, help, completion), has no porcelain
command and is reached with inference jobs create --endpoint <name>.`,
}

// endpointInputFields returns the property names of an endpoint's input
// schema (a JSON Schema document carried as a string), sorted; nil when the
// endpoint publishes none.
func endpointInputFields(schema *string) []string {
	if schema == nil {
		return nil
	}
	var doc struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if json.Unmarshal([]byte(*schema), &doc) != nil {
		return nil
	}
	names := make([]string, 0, len(doc.Properties))
	for k := range doc.Properties {
		names = append(names, k)
	}
	slices.Sort(names)
	return names
}

// addEndpointFlag registers the required --endpoint flag on a jobs verb.
func addEndpointFlag(cmd *cobra.Command) {
	cmd.Flags().String("endpoint", "", "the inference endpoint (required; saladcloud inference list shows them)")
	_ = cmd.MarkFlagRequired("endpoint")
	annotate(cmd, annotationFlagForArg, "endpoint")
}

// endpointFlag reads --endpoint.
func endpointFlag(cmd *cobra.Command) string {
	e, _ := cmd.Flags().GetString("endpoint")
	return e
}

func init() {
	inferenceCmd.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List the available inference endpoints",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, _, cerr := NewClient(cmd)
			if cerr != nil {
				return cerr
			}
			endpoints, cerr := client.ListInferenceEndpoints(cmd.Context())
			if cerr != nil {
				return cerr
			}
			// The list envelope alone: the collection's paging keys are
			// always null once every page is read.
			payload := struct {
				Items []inferenceendpoints.InferenceEndpoint `json:"items"`
			}{endpoints.Items}
			if payload.Items == nil {
				payload.Items = []inferenceendpoints.InferenceEndpoint{}
			}
			return wrapErr(output.Emit(payload, func(w io.Writer) {
				fmt.Fprintln(w, "NAME\tDISPLAY NAME\tDESCRIPTION")
				for i := range endpoints.Items {
					e := &endpoints.Items[i]
					fmt.Fprintf(w, "%s\t%s\t%s\n",
						ptr.Deref(e.Name), ptr.Deref(e.DisplayName),
						truncateRunes(oneLine(ptr.Deref(e.Description)), 60))
				}
			}))
		},
	})

	inferenceCmd.AddCommand(&cobra.Command{
		Use:   "get <endpoint>",
		Short: "Show one inference endpoint, its price and its input fields",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, _, cerr := NewClient(cmd)
			if cerr != nil {
				return cerr
			}
			endpoint, cerr := client.GetInferenceEndpoint(cmd.Context(), args[0])
			if cerr != nil {
				return endpointNotFound(cerr, args[0], client.Org())
			}
			return wrapErr(output.Emit(endpoint, func(w io.Writer) {
				row := func(k, v string) { fmt.Fprintf(w, "%s\t%s\n", k, v) }
				row("Name", ptr.Deref(endpoint.Name))
				row("Display name", ptr.Deref(endpoint.DisplayName))
				if org := ptr.Deref(endpoint.OrganizationName); org != "" {
					row("Organization", org)
				}
				row("Price", oneLine(ptr.Deref(endpoint.PriceDescription)))
				row("Description", oneLine(ptr.Deref(endpoint.Description)))
				inputs := "- (no input schema published; see the readme in -o json)"
				if names := endpointInputFields(endpoint.InputSchema); len(names) > 0 {
					inputs = strings.Join(names, ", ")
				}
				row("Inputs", inputs)
				row("Note", "use -o json for the input/output schemas and the readme")
			}))
		},
	})

	jobsCmd := &cobra.Command{
		Use:     "jobs",
		Aliases: []string{"job"},
		Short:   "Queue and inspect jobs on an inference endpoint",
		Long: `Queue and inspect jobs on any inference endpoint. The job is the positional
argument and the endpoint is --endpoint:

    saladcloud inference jobs list --endpoint transcribe
    saladcloud inference jobs get <id> --endpoint transcribe`,
	}

	jobsListCmd := &cobra.Command{
		Use:   "list --endpoint <endpoint>",
		Short: "List the organization's jobs on an endpoint, newest first",
		Long: `List the organization's jobs on an endpoint, newest first. --limit keeps
the newest N (default 100; 0 for all, up to 10,000); a stderr note says when
older jobs were left out.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			endpoint := endpointFlag(cmd)
			limit, _ := cmd.Flags().GetInt("limit")
			if limit < 0 {
				return clierr.New(
					clierr.CodeUsage,
					clierr.KindUsage,
					"--limit must be 0 (all) or positive (got %d)",
					limit,
				)
			}
			client, _, cerr := NewClient(cmd)
			if cerr != nil {
				return cerr
			}
			jobs, more, cerr := client.ListInferenceJobs(cmd.Context(), endpoint, limit)
			if cerr != nil {
				return endpointNotFound(cerr, endpoint, client.Org())
			}
			payload := struct {
				Items []inferenceendpoints.InferenceEndpointJob `json:"items"`
			}{jobs}
			err := wrapErr(output.Emit(payload, func(w io.Writer) {
				fmt.Fprintln(w, "ID\tSTATUS\tCREATED\tUPDATED")
				for i := range jobs {
					j := &jobs[i]
					fmt.Fprintf(w, "%s\t%s\t%s\t%s\n",
						ptr.Deref(j.Id), jobStatusString(j),
						timeCell(j.CreateTime), timeCell(j.UpdateTime))
				}
			}))
			if err != nil {
				return err
			}
			if len(jobs) == 0 && output.Mode() == "table" {
				fmt.Fprintf(os.Stderr, "no jobs on endpoint %s\n", endpoint)
			}
			jobsLimitNote(os.Stderr, len(jobs), limit, api.InferenceJobsCap, more)
			return nil
		},
	}
	addEndpointFlag(jobsListCmd)
	jobsListCmd.Flags().
		Int("limit", 100, fmt.Sprintf("how many of the newest jobs to list (0 = all, up to %d)", api.InferenceJobsCap))
	jobsCmd.AddCommand(jobsListCmd)

	jobsGetCmd := &cobra.Command{
		Use:   "get <id> --endpoint <endpoint>",
		Short: "Show one job (input/output truncated; -o json for the full document)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			endpoint := endpointFlag(cmd)
			if cerr := requireJobUUID(args[0], "saladcloud inference jobs list --endpoint "+endpoint); cerr != nil {
				return cerr
			}
			client, _, cerr := NewClient(cmd)
			if cerr != nil {
				return cerr
			}
			job, cerr := client.GetInferenceJob(cmd.Context(), endpoint, args[0])
			if cerr != nil {
				return jobNotFound(cerr, endpoint, args[0])
			}
			return wrapErr(output.Emit(job, func(w io.Writer) { writeJobDetail(w, job) }))
		},
	}
	addEndpointFlag(jobsGetCmd)
	jobsCmd.AddCommand(jobsGetCmd)

	jobsCreateCmd := &cobra.Command{
		Use:   "create --endpoint <endpoint> (-f <file> | --input '<json>')",
		Short: "Queue a job on an inference endpoint (--wait blocks until it finishes)",
		Long: `Queue a job on an inference endpoint. This reaches every endpoint, including
ones newer than this CLI that have no porcelain command yet.

Exactly one input source is required:
  -f <file>   a full job prototype document {input, metadata?, webhook?}
              as JSON or YAML ("-" reads stdin)
  --input     the input object alone, as inline JSON; it is wrapped into
              the prototype for you

With --wait the command polls the job every 5s until it reaches a terminal
status: succeeded emits the final job document and exits 0; failed or
cancelled exits 1 with the job's status and last event. --timeout (default
10m) caps the wait; the job keeps running server-side on timeout.`,
		Example: `  # Inline input (bash, zsh, PowerShell 7.3 or later; older PowerShell drops the inner quotes, so use -f)
  saladcloud inference jobs create --endpoint transcribe --input '{"url": "https://example.com/meeting.mp3", "language_code": "en"}' --wait

  # Full prototype from a file (input + webhook)
  saladcloud inference jobs create --endpoint transcribe -f job.yaml`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			endpoint := endpointFlag(cmd)
			file, _ := cmd.Flags().GetString("file")
			inputJSON, _ := cmd.Flags().GetString("input")
			// Pre-flight validation, before any file or API work.
			if (file == "") == (inputJSON == "") {
				return clierr.New(clierr.CodeUsage, clierr.KindUsage,
					"exactly one of -f <file> or --input '<json>' is required")
			}
			wait, timeout, cerr := waitFlagValues(cmd)
			if cerr != nil {
				return cerr
			}
			var proto inferenceendpoints.InferenceEndpointJobPrototype
			if file != "" {
				if cerr = loadJobFile(file, &proto); cerr != nil {
					return cerr
				}
				if proto.Input == nil {
					return clierr.New(clierr.CodeInvalidInput, clierr.KindInvalidInput,
						"invalid job prototype: input is required")
				}
			} else {
				if !json.Valid([]byte(inputJSON)) {
					return clierr.New(clierr.CodeInvalidInput, clierr.KindInvalidInput,
						"invalid --input: not valid JSON")
				}
				// RawMessage goes on the wire byte-for-byte inside the prototype.
				proto.Input = json.RawMessage(inputJSON)
			}

			client, _, cerr := NewClient(cmd)
			if cerr != nil {
				return cerr
			}
			job, cerr := client.CreateInferenceJob(cmd.Context(), endpoint, proto)
			if cerr != nil {
				return endpointNotFound(cerr, endpoint, client.Org())
			}
			if !wait {
				return wrapErr(output.Emit(job, func(w io.Writer) { writeJobDetail(w, job) }))
			}
			ctx := cmd.Context() // cancelled by the one signal handler in Execute
			id := ptr.Deref(job.Id)
			d := liveJobWaitDeps(client, endpoint, id, "waiting for job "+id)
			final, cerr := waitForJobTerminal(ctx, d, id, timeout, waitPoll)
			if cerr != nil {
				return cerr
			}
			return wrapErr(output.Emit(final, func(w io.Writer) { writeJobDetail(w, final) }))
		},
	}
	addEndpointFlag(jobsCreateCmd)
	jobsCreateCmd.Flags().
		StringP("file", "f", "", "job prototype file {input, metadata?, webhook?} (.json/.yaml; \"-\" reads stdin)")
	jobsCreateCmd.Flags().String("input", "", "the job input object as inline JSON (wrapped into the prototype)")
	addJobWaitFlags(jobsCreateCmd)
	jobsCmd.AddCommand(jobsCreateCmd)

	jobsCancelCmd := &cobra.Command{
		Use:   "cancel <id> --endpoint <endpoint>",
		Short: "Cancel a job on an inference endpoint",
		Long: `Cancel a job on an inference endpoint. The API accepts the request and
answers with no body; to see the job's status afterwards, run
saladcloud inference jobs get <id> --endpoint <endpoint>.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			endpoint := endpointFlag(cmd)
			if cerr := requireJobUUID(args[0], "saladcloud inference jobs list --endpoint "+endpoint); cerr != nil {
				return cerr
			}
			client, _, cerr := NewClient(cmd)
			if cerr != nil {
				return cerr
			}
			if cerr := client.CancelInferenceJob(cmd.Context(), endpoint, args[0]); cerr != nil {
				return jobNotFound(cerr, endpoint, args[0])
			}
			v := jobActionResult{Endpoint: endpoint, ID: args[0], Action: "cancelled", OK: true}
			return wrapErr(output.Emit(v, func(w io.Writer) {
				fmt.Fprintf(w, "Job %s on endpoint %s cancelled\n", v.ID, v.Endpoint)
			}))
		},
	}
	addEndpointFlag(jobsCancelCmd)
	jobsCmd.AddCommand(jobsCancelCmd)
	inferenceCmd.AddCommand(jobsCmd)

	orgScoped(inferenceCmd)
	Register(inferenceCmd)
}

// jobsLimitNote writes the stderr note for a job listing that left older
// jobs out: at the ceiling, the most one listing returns, in every mode (with no
// limit, or a limit above the ceiling, the caller asked for more than it got);
// below it, in table mode, when --limit kept the newest and more exist.
func jobsLimitNote(w io.Writer, shown, limit, ceiling int, more bool) {
	switch {
	case !more:
	case (limit == 0 || limit > ceiling) && shown >= ceiling:
		fmt.Fprintf(
			w,
			"note: showing the newest %d jobs, the most one listing returns; older jobs are not shown\n",
			ceiling,
		)
	case output.Mode() == "table":
		fmt.Fprintf(
			w,
			"note: showing the newest %d jobs; older ones exist\nhint: for more, raise --limit; 0 means all, up to %d\n",
			shown,
			ceiling,
		)
	}
}
