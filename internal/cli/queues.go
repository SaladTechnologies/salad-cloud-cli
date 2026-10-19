// Job queues: project-scoped queues a container group's instances pull jobs
// from. There is no porcelain here: a queue job's output is whatever the
// user's own container returns, so the plumbing is the whole surface.

package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/saladtechnologies/salad-cloud-sdk-go/pkg/queues"
	"github.com/saladtechnologies/salad-cloud-sdk-go/pkg/shared"
	"github.com/saladtechnologies/salad-cloud-sdk-go/pkg/util"
	"github.com/spf13/cobra"

	"github.com/saladtechnologies/salad-cloud-cli/internal/api"
	"github.com/saladtechnologies/salad-cloud-cli/internal/clierr"
	"github.com/saladtechnologies/salad-cloud-cli/internal/output"
	"github.com/saladtechnologies/salad-cloud-cli/internal/ptr"
)

// queueActionResult is the Emit payload for queues delete.
type queueActionResult struct {
	Name   string `json:"name"`
	Action string `json:"action"`
	OK     bool   `json:"ok"`
}

// queueJobActionResult is the Emit payload for queues jobs cancel.
type queueJobActionResult struct {
	Queue  string `json:"queue"`
	ID     string `json:"id"`
	Action string `json:"action"`
	OK     bool   `json:"ok"`
}

// queueNotFound rewrites a 404 on a queue-level call with the queue's
// identity and the discovery hint.
func queueNotFound(cerr *clierr.Error, queue string, c *api.Client) *clierr.Error {
	return describeNotFound(cerr, "to list the queues, run saladcloud queues list",
		`queue "%s" not found in %s/%s`, queue, c.Org(), c.Project())
}

// queueJobNotFound rewrites a 404 on a job-level call.
func queueJobNotFound(cerr *clierr.Error, queue, id string) *clierr.Error {
	return describeNotFound(cerr, "to list its jobs, run saladcloud queues jobs list --queue "+queue,
		`job "%s" not found in queue "%s"`, id, queue)
}

// queueJobNotCancelable turns the API's bare 409 to a cancel into what
// happened: the job has already finished. It reads the job for its status;
// only the message changes, so the exit code, kind and HTTP status stay the
// 409's.
func queueJobNotCancelable(
	ctx context.Context,
	client *api.Client,
	cerr *clierr.Error,
	queue, id string,
) *clierr.Error {
	why := "it has already finished"
	if j, gerr := client.GetQueueJob(ctx, queue, id); gerr == nil && j.Status != nil {
		why = fmt.Sprintf("its status is already %s", *j.Status)
	}
	out := *cerr
	out.Message = fmt.Sprintf("job %s in queue %s cannot be canceled: %s", id, queue, why)
	return &out
}

// requireJobUUID rejects a job ID that is not a full UUID before any
// request: jobs, unlike instances, are not resolved by prefix, and the API
// answers a prefix with a 400 that does not say so.
func requireJobUUID(id, listCmd string) *clierr.Error {
	if isUUID(id) {
		return nil
	}
	return clierr.New(clierr.CodeInvalidInput, clierr.KindInvalidInput,
		`job IDs must be full UUIDs, not "%s"`, id).WithHint("to list them, run %s", listCmd)
}

// queueJobStatus renders a queue job's status ("-" when absent).
func queueJobStatus(j *queues.QueueJob) string {
	if j == nil || j.Status == nil {
		return "-"
	}
	return string(*j.Status)
}

// queueJobView is jobWaitDeps.view for queue jobs.
func queueJobView(j *queues.QueueJob) (string, string) {
	if j == nil || len(j.Events) == 0 {
		return queueJobStatus(j), ""
	}
	e := &j.Events[len(j.Events)-1]
	action := "-"
	if e.Action != nil {
		action = string(*e.Action)
	}
	return queueJobStatus(j), fmt.Sprintf(" (last event: %s @ %s)", action, ptr.Deref(e.Time))
}

// writeQueueDetail writes the key/value table for one queue.
func writeQueueDetail(w *output.Writer, q *queues.Queue) {
	row := func(k, v string) { w.Printf("%s\t%s\n", k, v) }
	row("Name", ptr.Deref(q.Name))
	row("Display name", ptr.Deref(q.DisplayName))
	row("Description", orDash(oneLine(ptr.Deref(q.Description))))
	row("Queue length", int64Cell(q.CurrentQueueLength))
	groups := make([]string, 0, len(q.ContainerGroups))
	for _, g := range q.ContainerGroups {
		groups = append(groups, ptr.Deref(g.Name))
	}
	row("Container groups", orDash(strings.Join(groups, ", ")))
	row("Created", timeCell(q.CreateTime))
	row("Updated", timeCell(q.UpdateTime))
}

// writeQueueJobDetail writes the key/value table for one queue job.
func writeQueueJobDetail(w *output.Writer, j *queues.QueueJob) {
	row := func(k, v string) { w.Printf("%s\t%s\n", k, v) }
	row("ID", ptr.Deref(j.Id))
	row("Status", queueJobStatus(j))
	row("Created", timeCell(j.CreateTime))
	row("Updated", timeCell(j.UpdateTime))
	if wh := ptr.Deref(j.Webhook); wh != "" {
		row("Webhook", wh)
	}
	_, last := queueJobView(j)
	writeJobBody(row, j.Input, j.Output, len(j.Events), last)
}

// addQueueFlag registers the required --queue flag on a queue jobs verb.
func addQueueFlag(cmd *cobra.Command) {
	cmd.Flags().String("queue", "", "the queue (required; saladcloud queues list shows them)")
	noteTreeErr(cmd.MarkFlagRequired("queue"))
	annotate(cmd, annotationFlagForArg, "queue")
}

// queueFlag reads --queue.
func queueFlag(cmd *cobra.Command) (string, *clierr.Error) {
	fr := readFlags(cmd.Flags())
	q := fr.String("queue")
	return q, fr.Err()
}

// queueFields reads --display-name and --description into the values to
// send; set reports whether either was passed.
func queueFields(fr *flagReader) (displayName, description *string, set bool) {
	if fr.Changed("display-name") {
		v := fr.String("display-name")
		displayName = &v
	}
	if fr.Changed("description") {
		v := fr.String("description")
		description = &v
	}
	return displayName, description, displayName != nil || description != nil
}

func addQueueFieldFlags(cmd *cobra.Command) {
	cmd.Flags().String("display-name", "", "human-readable display name")
	cmd.Flags().String("description", "", "notes about the queue (up to 500 characters)")
}

func init() {
	queuesCmd := &cobra.Command{
		Use:     "queues",
		Aliases: []string{"queue"},
		Short:   "Create and manage job queues and their jobs",
		Long: `Job queues hold jobs for a container group's instances to work through. A
queue belongs to a project; a container group is connected to one at create
time with container-groups create --queue <name> --queue-port <n>
--queue-path <path>, and its instances then receive the queue's jobs over
HTTP on that port and path. The image must run the job queue worker beside
the HTTP server: the worker takes the jobs and forwards them. Without it the
group runs and the jobs stay pending, and nothing reports why.

Jobs are submitted with queues jobs create --queue <name>. A job's output
is whatever your container returns.`,
	}

	createCmd := &cobra.Command{
		Use:   "create <name> [--display-name <n>] [--description <d>]",
		Short: "Create a job queue in the current project",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if cerr := validateResourceName("queue", args[0]); cerr != nil {
				return cerr
			}
			fr := readFlags(cmd.Flags())
			displayName, description, _ := queueFields(fr)
			if cerr := fr.Err(); cerr != nil {
				return cerr
			}
			client, _, cerr := NewClient(cmd)
			if cerr != nil {
				return cerr
			}
			q, cerr := client.CreateQueue(cmd.Context(), queues.QueuePrototype{
				Name: ptr.To(args[0]), DisplayName: displayName, Description: description,
			})
			if cerr != nil {
				return cerr
			}
			return wrapErr(app.printer.Emit(q, func(w *output.Writer) { writeQueueDetail(w, q) }))
		},
	}
	addQueueFieldFlags(createCmd)
	queuesCmd.AddCommand(createCmd)

	queuesCmd.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List the current project's job queues",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, _, cerr := NewClient(cmd)
			if cerr != nil {
				return cerr
			}
			list, cerr := client.ListQueues(cmd.Context())
			if cerr != nil {
				return cerr
			}
			payload := struct {
				Items []queues.Queue `json:"items"`
			}{list.Items}
			if payload.Items == nil {
				payload.Items = []queues.Queue{}
			}
			err := wrapErr(app.printer.Emit(payload, func(w *output.Writer) {
				w.Println("NAME\tDISPLAY NAME\tLENGTH\tGROUPS\tCREATED")
				for i := range list.Items {
					q := &list.Items[i]
					w.Printf("%s\t%s\t%s\t%s\t%s\n", ptr.Deref(q.Name), ptr.Deref(q.DisplayName),
						int64Cell(q.CurrentQueueLength), queueGroupsCell(q.ContainerGroups), timeCell(q.CreateTime))
				}
			}))
			if err != nil {
				return err
			}
			if len(payload.Items) == 0 && app.printer.Mode() == "table" {
				fprintf(
					errw(),
					"no queues in %s/%s\nhint: to create one, run saladcloud queues create <name>\n",
					client.Org(),
					client.Project(),
				)
			}
			return nil
		},
	})

	queuesCmd.AddCommand(&cobra.Command{
		Use:   "get <name>",
		Short: "Show one job queue",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, _, cerr := NewClient(cmd)
			if cerr != nil {
				return cerr
			}
			q, cerr := client.GetQueue(cmd.Context(), args[0])
			if cerr != nil {
				return queueNotFound(cerr, args[0], client)
			}
			return wrapErr(app.printer.Emit(q, func(w *output.Writer) { writeQueueDetail(w, q) }))
		},
	})

	updateCmd := &cobra.Command{
		Use:   "update <name> [--display-name <n>] [--description <d>]",
		Short: "Change a job queue's display name or description",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			fr := readFlags(cmd.Flags())
			displayName, description, set := queueFields(fr)
			if cerr := fr.Err(); cerr != nil {
				return cerr
			}
			if !set {
				return clierr.New(
					clierr.CodeUsage,
					clierr.KindUsage,
					"nothing to update: pass --display-name and/or --description",
				)
			}
			var patch queues.QueuePatch
			if displayName != nil {
				patch.DisplayName = &util.Nullable[string]{Value: *displayName}
			}
			if description != nil {
				patch.Description = &util.Nullable[string]{Value: *description}
			}
			client, _, cerr := NewClient(cmd)
			if cerr != nil {
				return cerr
			}
			q, cerr := client.UpdateQueue(cmd.Context(), args[0], patch)
			if cerr != nil {
				return queueNotFound(cerr, args[0], client)
			}
			return wrapErr(app.printer.Emit(q, func(w *output.Writer) { writeQueueDetail(w, q) }))
		},
	}
	addQueueFieldFlags(updateCmd)
	queuesCmd.AddCommand(updateCmd)

	queuesCmd.AddCommand(&cobra.Command{
		Use:   "delete <name>",
		Short: "Delete a job queue",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, _, cerr := NewClient(cmd)
			if cerr != nil {
				return cerr
			}
			if cerr := client.DeleteQueue(cmd.Context(), args[0]); cerr != nil {
				return queueNotFound(cerr, args[0], client)
			}
			v := queueActionResult{Name: args[0], Action: "deleted", OK: true}
			return wrapErr(app.printer.Emit(v, func(w *output.Writer) {
				w.Printf("Queue %s deleted\n", v.Name)
			}))
		},
	})

	queuesCmd.AddCommand(newQueueJobsCmd())
	Register(queuesCmd)
}

// newQueueJobsCmd builds 'queues jobs': the job is the positional, the
// queue is --queue.
func newQueueJobsCmd() *cobra.Command {
	jobsCmd := &cobra.Command{
		Use:     "jobs",
		Aliases: []string{"job"},
		Short:   "Submit and inspect the jobs in a queue",
		Long: `Submit and inspect the jobs in a queue. The job is the positional argument
and the queue is --queue:

    saladcloud queues jobs list --queue my-queue
    saladcloud queues jobs get <id> --queue my-queue`,
	}

	listCmd := &cobra.Command{
		Use:   "list --queue <queue>",
		Short: "List the jobs in a queue, newest first",
		Long: `List the jobs in a queue, newest first. --limit keeps the newest N (default
100; 0 for all, up to 10,000); a stderr note says when older jobs were left
out. The API lists queue jobs in job ID order, not by time, so finding the
newest means reading pages and sorting them. To spare the key's request
allowance, a listing with a limit reads at most 1,000 jobs (or the limit, if
larger) and says so on stderr when the queue may hold more; --limit 0 reads
every job, up to 10,000.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			fr := readFlags(cmd.Flags())
			queue, cerr := queueFlag(cmd)
			if cerr != nil {
				return cerr
			}
			limit := fr.Int("limit")
			if cerr = fr.Err(); cerr != nil {
				return cerr
			}
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
			jobs, more, partial, cerr := client.ListQueueJobs(cmd.Context(), queue, limit)
			if cerr != nil {
				return queueNotFound(cerr, queue, client)
			}
			payload := struct {
				Items []queues.QueueJob `json:"items"`
			}{jobs}
			err := wrapErr(app.printer.Emit(payload, func(w *output.Writer) {
				w.Println("ID\tSTATUS\tCREATED\tUPDATED")
				for i := range jobs {
					j := &jobs[i]
					fprintf(
						w,
						"%s\t%s\t%s\t%s\n",
						ptr.Deref(j.Id),
						queueJobStatus(j),
						timeCell(j.CreateTime),
						timeCell(j.UpdateTime),
					)
				}
			}))
			if err != nil {
				return err
			}
			if len(jobs) == 0 && app.printer.Mode() == "table" {
				fprintf(errw(), "no jobs in queue %s\n", queue)
			}
			jobsLimitNote(errw(), len(jobs), limit, api.QueueJobsCap, more)
			if partial {
				fprintf(
					errw(),
					"note: read the first %d jobs only; the queue may hold more, and the API lists jobs in no time order, so these are the newest of those read; --limit 0 reads all (up to %d)\n",
					max(limit, api.QueueJobsBudget),
					api.QueueJobsCap,
				)
			}
			return nil
		},
	}
	addQueueFlag(listCmd)
	listCmd.Flags().
		Int("limit", 100, fmt.Sprintf("how many of the newest jobs to list; a limit reads at most %d jobs (or the limit), 0 reads all, up to %d", api.QueueJobsBudget, api.QueueJobsCap))
	jobsCmd.AddCommand(listCmd)

	getCmd := &cobra.Command{
		Use:   "get <id> --queue <queue>",
		Short: "Show one queue job (input/output truncated; -o json for the full document)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			queue, cerr := queueFlag(cmd)
			if cerr != nil {
				return cerr
			}
			if cerr = requireJobUUID(args[0], "saladcloud queues jobs list --queue "+queue); cerr != nil {
				return cerr
			}
			client, _, cerr := NewClient(cmd)
			if cerr != nil {
				return cerr
			}
			job, cerr := client.GetQueueJob(cmd.Context(), queue, args[0])
			if cerr != nil {
				return queueJobNotFound(cerr, queue, args[0])
			}
			return wrapErr(app.printer.Emit(job, func(w *output.Writer) { writeQueueJobDetail(w, job) }))
		},
	}
	addQueueFlag(getCmd)
	jobsCmd.AddCommand(getCmd)

	createCmd := &cobra.Command{
		Use:   "create --queue <queue> (-f <file> | --input '<json>') [--webhook <url>]",
		Short: "Submit a job to a queue (--wait blocks until it finishes)",
		Long: `Submit a job to a queue.

Exactly one input source is required:
  -f <file>   a full job prototype document {input, metadata?, webhook?}
              as JSON or YAML ("-" reads stdin)
  --input     the input object alone, as inline JSON; it is wrapped into
              the prototype for you

--webhook sets (or overrides) the URL notified when the job finishes; the
delivery is signed with the organization's webhook secret (saladcloud
webhook-secret get shows it).

With --wait the command polls the job every 5s until it reaches a terminal
status: succeeded emits the final job document and exits 0; failed or
canceled exits 1 with the job's status and last event. --timeout (default
10m) caps the wait; the job stays queued or running on timeout.`,
		Example: `  # Inline input, wait for the result (bash, zsh, PowerShell 7.3 or later; older PowerShell drops the inner quotes, so use -f)
  saladcloud queues jobs create --queue my-queue --input '{"prompt": "hello"}' --wait

  # Full prototype from a file, with a webhook
  saladcloud queues jobs create --queue my-queue -f job.yaml --webhook https://example.com/hook`,
		Args: cobra.NoArgs,
		RunE: runQueueJobCreate,
	}
	addQueueFlag(createCmd)
	createCmd.Flags().
		StringP("file", "f", "", "job prototype file {input, metadata?, webhook?} (.json/.yaml; \"-\" reads stdin)")
	createCmd.Flags().String("input", "", "the job input as inline JSON (wrapped into the prototype)")
	createCmd.Flags().String("webhook", "", "URL notified when the job finishes (overrides the file's webhook)")
	addJobWaitFlags(createCmd)
	jobsCmd.AddCommand(createCmd)

	cancelCmd := &cobra.Command{
		Use:   "cancel <id> --queue <queue>",
		Short: "Cancel a pending or running queue job",
		Long: `Cancel a queue job. A pending job never runs. A running job is marked
canceled and its output is discarded, but the instance working on it runs
it to the end, so a handler with side effects should be idempotent. A job
that has finished cannot be canceled (exit 1).`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			queue, cerr := queueFlag(cmd)
			if cerr != nil {
				return cerr
			}
			if cerr = requireJobUUID(args[0], "saladcloud queues jobs list --queue "+queue); cerr != nil {
				return cerr
			}
			client, _, cerr := NewClient(cmd)
			if cerr != nil {
				return cerr
			}
			if cerr := client.CancelQueueJob(cmd.Context(), queue, args[0]); cerr != nil {
				if cerr.HTTPStatus != nil && *cerr.HTTPStatus == http.StatusConflict {
					return queueJobNotCancelable(cmd.Context(), client, cerr, queue, args[0])
				}
				return queueJobNotFound(cerr, queue, args[0])
			}
			v := queueJobActionResult{Queue: queue, ID: args[0], Action: "canceled", OK: true}
			return wrapErr(app.printer.Emit(v, func(w *output.Writer) {
				w.Printf("Job %s in queue %s canceled\n", v.ID, v.Queue)
			}))
		},
	}
	addQueueFlag(cancelCmd)
	jobsCmd.AddCommand(cancelCmd)
	return jobsCmd
}

// queueGroupsCell renders the GROUPS cell of `queues list`: the count of
// connected groups, or "-" when the list is empty. The API returns an empty
// container_groups list even while a connected group is running
// (docs/notes.md), so an empty list means unknown, not zero.
func queueGroupsCell(groups []shared.ContainerGroup) string {
	if len(groups) == 0 {
		return "-"
	}
	return strconv.Itoa(len(groups))
}

func runQueueJobCreate(cmd *cobra.Command, _ []string) error {
	fr := readFlags(cmd.Flags())
	queue, cerr := queueFlag(cmd)
	if cerr != nil {
		return cerr
	}
	file := fr.String("file")
	inputJSON := fr.String("input")
	wh := fr.String("webhook")
	if cerr = fr.Err(); cerr != nil {
		return cerr
	}
	if (file == "") == (inputJSON == "") {
		return clierr.New(
			clierr.CodeUsage,
			clierr.KindUsage,
			"exactly one of -f <file> or --input '<json>' is required",
		)
	}
	wait, timeout, cerr := waitFlagValues(cmd)
	if cerr != nil {
		return cerr
	}
	var proto queues.QueueJobPrototype
	if file != "" {
		if cerr = loadJobFile(file, &proto); cerr != nil {
			return cerr
		}
		if proto.Input == nil {
			return clierr.New(
				clierr.CodeInvalidInput,
				clierr.KindInvalidInput,
				"invalid job prototype: input is required",
			)
		}
	} else {
		if !json.Valid([]byte(inputJSON)) {
			return clierr.New(clierr.CodeInvalidInput, clierr.KindInvalidInput, "invalid --input: not valid JSON")
		}
		proto.Input = json.RawMessage(inputJSON)
	}
	if cmd.Flags().Changed("webhook") {
		proto.Webhook = &wh
	}

	client, _, cerr := NewClient(cmd)
	if cerr != nil {
		return cerr
	}
	job, cerr := client.CreateQueueJob(cmd.Context(), queue, proto)
	if cerr != nil {
		return queueNotFound(cerr, queue, client)
	}
	if !wait {
		return wrapErr(app.printer.Emit(job, func(w *output.Writer) { writeQueueJobDetail(w, job) }))
	}
	ctx := cmd.Context() // canceled by the one signal handler in Execute
	id := ptr.Deref(job.Id)
	d := jobWaitDeps[queues.QueueJob]{
		get: func(ctx context.Context) (*queues.QueueJob, *clierr.Error) {
			j, err := client.GetQueueJob(ctx, queue, id)
			return j, queueJobNotFound(err, queue, id)
		},
		view:     queueJobView,
		now:      time.Now,
		sleep:    ctxSleep,
		label:    "waiting for job " + id,
		where:    "in queue " + queue,
		check:    fmt.Sprintf("saladcloud queues jobs get %s --queue %s", id, queue),
		progress: nil,
	}
	final, cerr := waitForJobTerminal(ctx, d, id, timeout, waitPoll)
	if cerr != nil {
		return cerr
	}
	return wrapErr(app.printer.Emit(final, func(w *output.Writer) { writeQueueJobDetail(w, final) }))
}
