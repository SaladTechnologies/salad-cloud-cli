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

// jobVerbClient makes the first two steps of a job verb that names a job: requireJobUUID's check of id, and the API
// client.
func (app *App) jobVerbClient(cmd *cobra.Command, id, listCmd string) (*api.Client, *clierr.Error) {
	if cerr := requireJobUUID(id, listCmd); cerr != nil {
		return nil, cerr
	}
	client, _, cerr := app.NewClient(cmd)
	return client, cerr
}

// queueJobStatus renders a queue job's status ("-" when absent).
func queueJobStatus(j *queues.QueueJob) string {
	if j == nil || j.Status == nil {
		return "-"
	}
	return string(*j.Status)
}

// queueJobView is jobWaitDeps.view for queue jobs.
func queueJobView(j *queues.QueueJob) jobWaitView {
	if j == nil || len(j.Events) == 0 {
		return jobWaitView{status: queueJobStatus(j), lastEvent: ""}
	}
	e := &j.Events[len(j.Events)-1]
	action := "-"
	if e.Action != nil {
		action = string(*e.Action)
	}
	return jobWaitView{
		status:    queueJobStatus(j),
		lastEvent: fmt.Sprintf(" (last event: %s @ %s)", action, ptr.Deref(e.Time)),
	}
}

// writeQueueDetail writes the key/value table for one queue.
func writeQueueDetail(w *output.Writer, q *queues.Queue) {
	row := func(k, v string) { w.Printf("%s\t%s\n", k, v) }
	row("Name", ptr.Deref(q.Name))
	row("Display name", ptr.Deref(q.DisplayName))
	row("Description", orDash(oneLine(ptr.Deref(q.Description))))
	row("Queue length", int64Cell(q.CurrentQueueLength))
	groups := make([]string, 0, len(q.ContainerGroups))
	for i := range q.ContainerGroups {
		groups = append(groups, ptr.Deref(q.ContainerGroups[i].Name))
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
	writeJobBody(row, j.Input, j.Output, len(j.Events), queueJobView(j).lastEvent)
}

// addQueueFlag registers the required --queue flag on a queue jobs verb.
func (app *App) addQueueFlag(cmd *cobra.Command) {
	cmd.Flags().
		StringVar(&app.flags.queue, ancestorFlagQueue, "", "the queue (required; saladcloud queues list shows them)")
	app.noteTreeErr(cmd.MarkFlagRequired(ancestorFlagQueue))
	annotate(cmd, annotationFlagForArg, ancestorFlagQueue)
}

// queueFieldOptions are --display-name and --description.
type queueFieldOptions struct {
	displayName, description string
}

// queueFieldValues are --display-name and --description as the values to send, each nil when not passed.
type queueFieldValues struct {
	displayName, description *string
}

// set reports whether either field was passed.
func (v queueFieldValues) set() bool {
	return v.displayName != nil || v.description != nil
}

// queueFields is --display-name and --description as the values to send.
func queueFields(cmd *cobra.Command, o queueFieldOptions) queueFieldValues {
	v := queueFieldValues{displayName: nil, description: nil}
	if cmd.Flags().Changed("display-name") {
		v.displayName = &o.displayName
	}
	if cmd.Flags().Changed("description") {
		v.description = &o.description
	}
	return v
}

func addQueueFieldFlags(cmd *cobra.Command, o *queueFieldOptions) {
	cmd.Flags().StringVar(&o.displayName, "display-name", "", "human-readable display name")
	cmd.Flags().StringVar(&o.description, "description", "", "notes about the queue (up to 500 characters)")
}

// addQueuesCommands adds queues.go's commands to the tree.
func (app *App) addQueuesCommands(root *cobra.Command) {
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
	queuesCmd.AddCommand(app.newQueuesCreateCmd())
	queuesCmd.AddCommand(app.newQueuesListCmd())
	queuesCmd.AddCommand(app.newQueuesGetCmd())
	queuesCmd.AddCommand(app.newQueuesUpdateCmd())
	queuesCmd.AddCommand(app.newQueuesDeleteCmd())
	queuesCmd.AddCommand(app.newQueueJobsCmd())
	root.AddCommand(queuesCmd)
}

// newQueuesCreateCmd is 'saladcloud queues create <name>'.
func (app *App) newQueuesCreateCmd() *cobra.Command {
	var fields queueFieldOptions
	cmd := &cobra.Command{
		Use:   "create <name> [--display-name <n>] [--description <d>]",
		Short: "Create a job queue in the current project",
		Args:  cobra.ExactArgs(1),
		RunE:  func(cmd *cobra.Command, args []string) error { return app.runQueuesCreate(cmd, args[0], fields) },
	}
	addQueueFieldFlags(cmd, &fields)
	return cmd
}

// runQueuesCreate creates the queue name in the current project.
func (app *App) runQueuesCreate(cmd *cobra.Command, name string, fields queueFieldOptions) error {
	if cerr := validateResourceName("queue", name); cerr != nil {
		return cerr
	}
	values := queueFields(cmd, fields)
	displayName, description := values.displayName, values.description
	client, _, cerr := app.NewClient(cmd)
	if cerr != nil {
		return cerr
	}
	q, cerr := client.CreateQueue(cmd.Context(), queues.QueuePrototype{
		Name: new(name), DisplayName: displayName, Description: description,
	})
	if cerr != nil {
		return cerr
	}
	return wrapErr(app.printer.Emit(q, func(w *output.Writer) { writeQueueDetail(w, q) }))
}

// newQueuesListCmd is 'saladcloud queues list'.
func (app *App) newQueuesListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   cmdUseList,
		Short: "List the current project's job queues",
		Args:  cobra.NoArgs,
		RunE:  func(cmd *cobra.Command, _ []string) error { return app.runQueuesList(cmd) },
	}
}

// runQueuesList lists the current project's queues.
func (app *App) runQueuesList(cmd *cobra.Command) error {
	client, _, cerr := app.NewClient(cmd)
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
		payload.Items = []queues.Queue{} //nolint:revive // the JSON contract renders none as [], not null
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
	if len(payload.Items) == 0 && app.printer.Mode() == globalOutputTable {
		fprintf(
			app.errw(),
			"no queues in %s/%s\nhint: to create one, run saladcloud queues create <name>\n",
			client.Org(),
			client.Project(),
		)
	}
	return nil
}

// newQueuesGetCmd is 'saladcloud queues get <name>'.
func (app *App) newQueuesGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "get <name>",
		Short: "Show one job queue",
		Args:  cobra.ExactArgs(1),
		RunE:  func(cmd *cobra.Command, args []string) error { return app.runQueuesGet(cmd, args[0]) },
	}
}

// runQueuesGet shows the queue name.
func (app *App) runQueuesGet(cmd *cobra.Command, name string) error {
	client, _, cerr := app.NewClient(cmd)
	if cerr != nil {
		return cerr
	}
	q, cerr := client.GetQueue(cmd.Context(), name)
	if cerr != nil {
		return queueNotFound(cerr, name, client)
	}
	return wrapErr(app.printer.Emit(q, func(w *output.Writer) { writeQueueDetail(w, q) }))
}

// newQueuesUpdateCmd is 'saladcloud queues update <name>'.
func (app *App) newQueuesUpdateCmd() *cobra.Command {
	var fields queueFieldOptions
	cmd := &cobra.Command{
		Use:   "update <name> [--display-name <n>] [--description <d>]",
		Short: "Change a job queue's display name or description",
		Args:  cobra.ExactArgs(1),
		RunE:  func(cmd *cobra.Command, args []string) error { return app.runQueuesUpdate(cmd, args[0], fields) },
	}
	addQueueFieldFlags(cmd, &fields)
	return cmd
}

// runQueuesUpdate patches the display name and description of the queue name with the ones given.
func (app *App) runQueuesUpdate(cmd *cobra.Command, name string, fields queueFieldOptions) error {
	values := queueFields(cmd, fields)
	displayName, description := values.displayName, values.description
	if !values.set() {
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
	client, _, cerr := app.NewClient(cmd)
	if cerr != nil {
		return cerr
	}
	q, cerr := client.UpdateQueue(cmd.Context(), name, patch)
	if cerr != nil {
		return queueNotFound(cerr, name, client)
	}
	return wrapErr(app.printer.Emit(q, func(w *output.Writer) { writeQueueDetail(w, q) }))
}

// newQueuesDeleteCmd is 'saladcloud queues delete <name>'.
func (app *App) newQueuesDeleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "delete <name>",
		Short: "Delete a job queue",
		Args:  cobra.ExactArgs(1),
		RunE:  func(cmd *cobra.Command, args []string) error { return app.runQueuesDelete(cmd, args[0]) },
	}
}

// runQueuesDelete deletes the queue name.
func (app *App) runQueuesDelete(cmd *cobra.Command, name string) error {
	client, _, cerr := app.NewClient(cmd)
	if cerr != nil {
		return cerr
	}
	if cerr := client.DeleteQueue(cmd.Context(), name); cerr != nil {
		return queueNotFound(cerr, name, client)
	}
	v := queueActionResult{Name: name, Action: "deleted", OK: true}
	return wrapErr(app.printer.Emit(v, func(w *output.Writer) {
		w.Printf("Queue %s deleted\n", v.Name)
	}))
}

// newQueueJobsCmd builds 'queues jobs': the job is the positional, the
// queue is --queue.
func (app *App) newQueueJobsCmd() *cobra.Command {
	jobsCmd := &cobra.Command{
		Use:     cmdUseJobs,
		Aliases: []string{"job"},
		Short:   "Submit and inspect the jobs in a queue",
		Long: `Submit and inspect the jobs in a queue. The job is the positional argument
and the queue is --queue:

    saladcloud queues jobs list --queue my-queue
    saladcloud queues jobs get <id> --queue my-queue`,
	}
	jobsCmd.AddCommand(app.newQueueJobsListCmd())
	jobsCmd.AddCommand(app.newQueueJobsGetCmd())
	jobsCmd.AddCommand(app.newQueueJobsCreateCmd())
	jobsCmd.AddCommand(app.newQueueJobsCancelCmd())
	return jobsCmd
}

// newQueueJobsListCmd is 'saladcloud queues jobs list --queue <queue>'.
func (app *App) newQueueJobsListCmd() *cobra.Command {
	var limit int
	cmd := &cobra.Command{
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
		RunE: func(cmd *cobra.Command, _ []string) error { return app.runQueueJobsList(cmd, limit) },
	}
	app.addQueueFlag(cmd)
	cmd.Flags().IntVar(&limit, "limit", jobsDefaultLimit, fmt.Sprintf(
		"how many of the newest jobs to list; a limit reads at most %d jobs (or the limit), 0 reads all, up to %d",
		api.QueueJobsBudget,
		api.QueueJobsCap,
	))
	return cmd
}

// runQueueJobsList lists the newest limit jobs in --queue (0 for all).
func (app *App) runQueueJobsList(cmd *cobra.Command, limit int) error {
	queue := app.flags.queue
	if limit < 0 {
		return clierr.New(
			clierr.CodeUsage,
			clierr.KindUsage,
			"--limit must be 0 (all) or positive (got %d)",
			limit,
		)
	}
	client, _, cerr := app.NewClient(cmd)
	if cerr != nil {
		return cerr
	}
	listing, cerr := client.ListQueueJobs(cmd.Context(), queue, limit)
	if cerr != nil {
		return queueNotFound(cerr, queue, client)
	}
	jobs, more, partial := listing.Jobs, listing.More, listing.Partial
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
	if len(jobs) == 0 && app.printer.Mode() == globalOutputTable {
		fprintf(app.errw(), "no jobs in queue %s\n", queue)
	}
	app.jobsLimitNote(app.errw(), len(jobs), limit, api.QueueJobsCap, more)
	if partial {
		fprintf(
			app.errw(),
			"note: read the first %d jobs only; the queue may hold more, and the API lists jobs in no time order, "+
				"so these are the newest of those read; --limit 0 reads all (up to %d)\n",
			max(limit, api.QueueJobsBudget),
			api.QueueJobsCap,
		)
	}
	return nil
}

// newQueueJobsGetCmd is 'saladcloud queues jobs get <id> --queue <queue>'.
func (app *App) newQueueJobsGetCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "get <id> --queue <queue>",
		Short: "Show one queue job (input/output truncated; -o json for the full document)",
		Args:  cobra.ExactArgs(1),
		RunE:  func(cmd *cobra.Command, args []string) error { return app.runQueueJobsGet(cmd, args[0]) },
	}
	app.addQueueFlag(cmd)
	return cmd
}

// runQueueJobsGet shows job id in --queue.
func (app *App) runQueueJobsGet(cmd *cobra.Command, id string) error {
	queue := app.flags.queue
	client, cerr := app.jobVerbClient(cmd, id, "saladcloud queues jobs list --queue "+queue)
	if cerr != nil {
		return cerr
	}
	job, cerr := client.GetQueueJob(cmd.Context(), queue, id)
	if cerr != nil {
		return queueJobNotFound(cerr, queue, id)
	}
	return wrapErr(app.printer.Emit(job, func(w *output.Writer) { writeQueueJobDetail(w, job) }))
}

// newQueueJobsCreateCmd is 'saladcloud queues jobs create --queue <queue>'.
func (app *App) newQueueJobsCreateCmd() *cobra.Command {
	var o jobCreateOptions
	cmd := &cobra.Command{
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
		Example: `  # Inline input, wait for the result (bash, zsh, PowerShell 7.3 or later; ` +
			`older PowerShell drops the inner quotes, so use -f)
  saladcloud queues jobs create --queue my-queue --input '{"prompt": "hello"}' --wait

  # Full prototype from a file, with a webhook
  saladcloud queues jobs create --queue my-queue -f job.yaml --webhook https://example.com/hook`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return app.runQueueJobCreate(cmd, &o) },
	}
	app.addQueueFlag(cmd)
	cmd.Flags().StringVarP(&o.file, "file", "f", "",
		"job prototype file {input, metadata?, webhook?} (.json/.yaml; \"-\" reads stdin)")
	cmd.Flags().
		StringVar(&o.input, "input", "", "the job input as inline JSON (wrapped into the prototype)")
	cmd.Flags().
		StringVar(&o.webhook, "webhook", "", "URL notified when the job finishes (overrides the file's webhook)")
	addJobWaitFlags(cmd, &o.wait)
	return cmd
}

// newQueueJobsCancelCmd is 'saladcloud queues jobs cancel <id> --queue <queue>'.
func (app *App) newQueueJobsCancelCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cancel <id> --queue <queue>",
		Short: "Cancel a pending or running queue job",
		Long: `Cancel a queue job. A pending job never runs. A running job is marked
canceled and its output is discarded, but the instance working on it runs
it to the end, so a handler with side effects should be idempotent. A job
that has finished cannot be canceled (exit 1).`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error { return app.runQueueJobsCancel(cmd, args[0]) },
	}
	app.addQueueFlag(cmd)
	return cmd
}

// runQueueJobsCancel cancels job id in --queue.
func (app *App) runQueueJobsCancel(cmd *cobra.Command, id string) error {
	queue := app.flags.queue
	client, cerr := app.jobVerbClient(cmd, id, "saladcloud queues jobs list --queue "+queue)
	if cerr != nil {
		return cerr
	}
	if cerr := client.CancelQueueJob(cmd.Context(), queue, id); cerr != nil {
		if cerr.HTTPStatus != nil && *cerr.HTTPStatus == http.StatusConflict {
			return queueJobNotCancelable(cmd.Context(), client, cerr, queue, id)
		}
		return queueJobNotFound(cerr, queue, id)
	}
	v := queueJobActionResult{Queue: queue, ID: id, Action: "canceled", OK: true}
	return wrapErr(app.printer.Emit(v, func(w *output.Writer) {
		w.Printf("Job %s in queue %s canceled\n", v.ID, v.Queue)
	}))
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

// runQueueJobCreate submits a job to --queue and, with --wait, waits for it to end.
func (app *App) runQueueJobCreate(cmd *cobra.Command, o *jobCreateOptions) error {
	queue := app.flags.queue
	if (o.file == "") == (o.input == "") {
		return clierr.New(
			clierr.CodeUsage,
			clierr.KindUsage,
			"exactly one of -f <file> or --input '<json>' is required",
		)
	}
	wait, timeout, cerr := o.wait.values()
	if cerr != nil {
		return cerr
	}
	proto, cerr := app.queueJobPrototype(cmd, o)
	if cerr != nil {
		return cerr
	}

	client, _, cerr := app.NewClient(cmd)
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
	final, cerr := waitForJobTerminal(ctx, app, liveQueueJobWaitDeps(client, queue, id), id, timeout, waitPoll)
	if cerr != nil {
		return cerr
	}
	return wrapErr(app.printer.Emit(final, func(w *output.Writer) { writeQueueJobDetail(w, final) }))
}

// queueJobPrototype is the job prototype 'queues jobs create' submits: the -f file, which must carry an input, or
// the --input JSON wrapped into one, with --webhook, when given, as its webhook.
func (app *App) queueJobPrototype(cmd *cobra.Command, o *jobCreateOptions) (queues.QueueJobPrototype, *clierr.Error) {
	var proto queues.QueueJobPrototype
	if o.file != "" {
		if cerr := app.loadJobFile(o.file, &proto); cerr != nil {
			return proto, cerr
		}
		if proto.Input == nil {
			return proto, clierr.New(
				clierr.CodeInvalidInput,
				clierr.KindInvalidInput,
				"invalid job prototype: input is required",
			)
		}
	} else {
		if !json.Valid([]byte(o.input)) {
			return proto, clierr.New(
				clierr.CodeInvalidInput,
				clierr.KindInvalidInput,
				"invalid --input: not valid JSON",
			)
		}
		proto.Input = json.RawMessage(o.input)
	}
	if cmd.Flags().Changed("webhook") {
		wh := o.webhook
		proto.Webhook = &wh
	}
	return proto, nil
}

// liveQueueJobWaitDeps builds the production jobWaitDeps for job id in queue.
func liveQueueJobWaitDeps(client *api.Client, queue, id string) jobWaitDeps[queues.QueueJob] {
	return jobWaitDeps[queues.QueueJob]{
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
}
