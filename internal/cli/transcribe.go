// The transcription porcelain: 'inference transcribe' and 'inference
// transcription-lite', pure client-side composition over the inference
// plumbing (plus one S4 upload for local files). Each command is named
// exactly after its endpoint and its flags are that endpoint's input fields;
// everything here could be done with 'saladcloud inference jobs create
// --endpoint <name>', just less pleasantly.

package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/saladtechnologies/salad-cloud-sdk-go/pkg/inferenceendpoints"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/saladtechnologies/salad-cloud-cli/internal/clierr"
	"github.com/saladtechnologies/salad-cloud-cli/internal/output"
	"github.com/saladtechnologies/salad-cloud-cli/internal/ptr"
)

const (
	transcribeSignedURLExp = int64(24 * 60 * 60) // 24h signed-URL expiry for uploads
	transcribeWaitTimeout  = 30 * time.Minute    // default --timeout (long audio)
)

// keptUploadNote says, on stderr, that a failed job's upload stays in S4.
func keptUploadNote(remote string) {
	if remote != "" {
		fmt.Fprintf(os.Stderr, "note: the upload stays in S4 as %s for inspection\n", remote)
	}
}

// redactUploadURL removes the signed URL's query string (its token) from a
// job's input.url, so -o json does not hand out a working link to the file.
func redactUploadURL(job *inferenceendpoints.InferenceEndpointJob) {
	in, ok := job.Input.(map[string]any)
	if !ok {
		return
	}
	if u, ok := in["url"].(string); ok {
		if before, _, ok := strings.Cut(u, "?"); ok {
			in["url"] = before
		}
	}
}

// isHTTPURL reports whether s is an http(s) URL (vs a local file path).
func isHTTPURL(s string) bool {
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
}

// transcribeMimeTypes maps audio/video file extensions to MIME types for the
// S4 upload; unknown extensions fall back to application/octet-stream.
var transcribeMimeTypes = map[string]string{
	".mp3":  "audio/mpeg",
	".wav":  "audio/wav",
	".m4a":  "audio/x-m4a",
	".mp4":  "video/mp4",
	".ogg":  "audio/ogg",
	".flac": "audio/flac",
	".aac":  "audio/aac",
	".webm": "video/webm",
}

// transcribeMime returns the upload MIME type for a local file path.
func transcribeMime(path string) string {
	if m, ok := transcribeMimeTypes[strings.ToLower(filepath.Ext(path))]; ok {
		return m
	}
	return "application/octet-stream"
}

// transcribeRemoteName builds the S4 remote name for one upload:
// saladcloud-cli/transcribe/<basename>-<unixtime><ext>. The timestamp keeps
// repeat runs of the same file from overwriting each other.
func transcribeRemoteName(localPath string, now time.Time) string {
	base := filepath.Base(localPath)
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	return fmt.Sprintf("saladcloud-cli/transcribe/%s-%d%s", stem, now.Unix(), ext)
}

// humanSize renders a byte count compactly (1.5 MB style).
func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGT"[exp])
}

// resultURLKeys are where a return_as_file job's output carries the result
// URL: output.url, in place of the inline text.
var resultURLKeys = []string{"url"}

// transcriptText extracts the table-mode stdout payload from a succeeded
// job's output: the SRT document when requested, else the text field, else
// the whole output as compact JSON (the output schema is modeled tolerantly
// — it is `any` on the wire). The endpoint returns the SRT under
// "srt_content"; "srt" is accepted too, liberally. When
// --srt was asked for and no SRT field is present, that is an error, not a
// silent fallback to the plain text — scripts piping to an .srt file must
// not receive a transcript instead.
func transcriptText(out any, wantSRT, wantFile bool) (string, *clierr.Error) {
	m, isMap := out.(map[string]any)
	if isMap && wantFile {
		for _, key := range resultURLKeys {
			if s, ok := m[key].(string); ok && s != "" {
				return s, nil
			}
		}
		return "", clierr.New(clierr.CodeGeneric, clierr.KindAPIError,
			"the job succeeded, but its output has no result URL").WithHint("inspect the job document with -o json")
	}
	if isMap && wantSRT {
		for _, key := range []string{"srt_content", "srt"} {
			if s, ok := m[key].(string); ok && s != "" {
				return s, nil
			}
		}
		return "", clierr.New(
			clierr.CodeGeneric,
			clierr.KindAPIError,
			"the job succeeded, but its output has no SRT document (expected output.srt_content)",
		).WithHint("inspect the job document with -o json")
	}
	if isMap {
		if s, ok := m["text"].(string); ok && s != "" {
			return s, nil
		}
	}
	return compactJSON(out, 0), nil
}

// jobOutputError extracts the error message and code a terminal job's output
// may embed: a failed job can return `succeeded` with output.error set (e.g.
// an undownloadable URL), and a failed transcription has to be a nonzero
// exit either way.
func jobOutputError(out any) (msg, code string) {
	m, ok := out.(map[string]any)
	if !ok {
		return "", ""
	}
	msg, _ = m["error"].(string)
	code, _ = m["error_code"].(string)
	return msg, code
}

// fieldKind is the JSON type of one porcelain input field.
type fieldKind int

const (
	fieldBool fieldKind = iota
	fieldString
	fieldInt
	// fieldList is a field the endpoint takes as one comma-separated
	// string: the flag is repeatable, one value each, and the CLI joins them.
	fieldList
)

// inputField is one input field of an endpoint's schema, exposed as a flag
// named after it, kebab-cased, so two endpoints never spell one input
// differently.
type inputField struct {
	name string // the schema field, e.g. language_code
	kind fieldKind
	help string
}

// flag is the field's flag name.
func (f inputField) flag() string { return strings.ReplaceAll(f.name, "_", "-") }

// transcribeFields are the transcribe endpoint's input fields besides url
// (the positional), from its published input schema.
var transcribeFields = []inputField{
	{
		"language_code",
		fieldString,
		"language of the audio, for example en or de (omit to detect it automatically; setting it forces that language)",
	},
	{"audio_stream_index", fieldInt, "zero-based audio stream to transcribe in multitrack media"},
	{"multichannel", fieldBool, "preserve audio channels for channel-based diarization"},
	{"return_as_file", fieldBool, "return a temporary result URL instead of inline output (table mode prints the URL)"},
	{"word_level_timestamps", fieldBool, "include word segments"},
	{"sentence_level_timestamps", fieldBool, "include sentence-level timestamps"},
	{"diarization", fieldBool, "add speaker labels to word segments"},
	{"sentence_diarization", fieldBool, "add speaker labels to sentence segments"},
	{"srt", fieldBool, "produce an SRT subtitle document (table mode prints it instead of the text)"},
	{"translate", fieldString, "Whisper translation: to_eng translates to English"},
	{
		"llm_translation",
		fieldList,
		"a language to translate the transcript into, repeatable (--llm-translation german --llm-translation french)",
	},
	{"srt_translation", fieldList, "a language to translate the SRT into, repeatable"},
	{"custom_prompt", fieldString, "an instruction for processing the transcript"},
	{"custom_vocabulary", fieldList, "a domain term to improve accuracy, repeatable"},
	{"summarize", fieldInt, "summarize the transcript in at most this many words (0-2000)"},
	{"overall_classification", fieldBool, "classify the transcript (see --classification-labels)"},
	{"classification_labels", fieldList, "a label for --overall-classification, repeatable"},
	{"overall_sentiment_analysis", fieldBool, "classify the transcript's sentiment"},
	{"enhanced_accuracy", fieldBool, "use a language-specific fine-tuned model when one is available"},
}

// transcriptionLiteFields are the transcription-lite endpoint's input
// fields besides url, from its published input schema.
var transcriptionLiteFields = []inputField{
	{"language_code", fieldString, "language of the audio, for example en or de (endpoint default: en)"},
	{"return_as_file", fieldBool, "return the result as a file (table mode prints the URL)"},
	{"sentence_level_timestamps", fieldBool, "include sentence-level timestamps"},
	{"word_level_timestamps", fieldBool, "include word-level timestamps"},
	{"diarization", fieldBool, "separate speakers in multi-speaker audio"},
	{"sentence_diarization", fieldBool, "sentence-level speaker diarization"},
	{"srt", fieldBool, "produce an SRT subtitle document (table mode prints it instead of the text)"},
	{"translate", fieldString, "translate the transcription to English"},
}

// buildTranscribeInput builds the job input: url plus every field flag the
// user set (Changed, not the zero value), so the endpoint's own defaults
// govern everything else and the request is the one 'inference jobs create
// --input' would send for the same intent. --input-extra is merged last,
// key by key, and wins.
func buildTranscribeInput(
	url string,
	fields []inputField,
	flags *pflag.FlagSet,
	inputExtra string,
) (map[string]any, *clierr.Error) {
	in := map[string]any{"url": url}
	for _, f := range fields {
		if !flags.Changed(f.flag()) {
			continue
		}
		switch f.kind {
		case fieldBool:
			v, _ := flags.GetBool(f.flag())
			in[f.name] = v
		case fieldString:
			v, _ := flags.GetString(f.flag())
			in[f.name] = v
		case fieldInt:
			v, _ := flags.GetInt64(f.flag())
			in[f.name] = v
		case fieldList:
			v, _ := flags.GetStringArray(f.flag())
			in[f.name] = strings.Join(v, ",")
		}
	}
	if inputExtra != "" {
		var extra map[string]any
		if err := json.Unmarshal([]byte(inputExtra), &extra); err != nil {
			return nil, clierr.New(clierr.CodeInvalidInput, clierr.KindInvalidInput,
				"invalid --input-extra: %v (must be a JSON object)", err)
		}
		maps.Copy(in, extra)
	}
	return in, nil
}

// transcribeEndpoints are the endpoints with a transcription porcelain
// command, each named exactly after its endpoint.
var transcribeEndpoints = []struct {
	name   string
	fields []inputField
	short  string
}{
	{"transcribe", transcribeFields, "Transcribe an audio or video file on the transcribe endpoint"},
	{
		"transcription-lite",
		transcriptionLiteFields,
		"Transcribe an audio or video file on the cheaper transcription-lite endpoint",
	},
}

func init() {
	for _, ep := range transcribeEndpoints {
		inferenceCmd.AddCommand(newTranscribeCmd(ep.name, ep.fields, ep.short))
	}
}

// signedLinkNote warns that a job document read with 'inference jobs get'
// carries a working link to the uploaded file: the CLI shows the API's own
// document unedited (the output rule in AGENTS.md), so the warning goes
// where the CLI sends people to it.
const signedLinkNote = "the job document's input.url is a working link to your uploaded file until it expires; remove it before sharing the output"

// listFieldsHelp is the help sentence on an endpoint's list fields, naming
// only the ones it takes, on lines of its own; "" when it takes none.
func listFieldsHelp(fields []inputField) string {
	var flags []string
	for _, f := range fields {
		if f.kind == fieldList {
			flags = append(flags, "--"+f.flag())
		}
	}
	if len(flags) == 0 {
		return ""
	}
	sentence := "A field the endpoint takes as a comma-separated list (" + strings.Join(flags, ", ") +
		") is a repeatable flag, one value each, joined with commas for you."
	var b strings.Builder
	width := 0
	for word := range strings.FieldsSeq(sentence) {
		switch {
		case width == 0:
		case width+1+len(word) > 74:
			b.WriteString("\n")
			width = 0
		default:
			b.WriteString(" ")
			width++
		}
		b.WriteString(word)
		width += len(word)
	}
	return "\n" + b.String()
}

// newTranscribeCmd builds the porcelain command for one transcription
// endpoint: the same implementation, with that endpoint's input fields as
// flags, so a flag the endpoint does not take is rejected by cobra itself.
func newTranscribeCmd(endpoint string, fields []inputField, short string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   endpoint + " <file-or-url>",
		Short: short,
		Long: `Transcribe an audio or video file in one step, on the inference endpoint
"` + endpoint + `". Everything here can be done with saladcloud inference jobs
create --endpoint ` + endpoint + `; this command uploads, queues, waits and
prints for you.

The argument is an http(s) URL (used as-is) or a local file path. Local
files are uploaded to the organization's S4 storage (Salad Simple Storage
Service) first, under saladcloud-cli/transcribe/, with a signed URL that
expires after 24 hours. The upload is deleted once the transcript is back;
it stays when the job fails (so you can inspect it), with --no-wait (the job
still needs it), and with --keep-upload. The signed URL's token is removed
from the job's input.url in -o json unless --keep-upload.

Each input field of the endpoint is a flag named after it. A flag you do
not set is not sent, so the endpoint's own defaults apply.` + listFieldsHelp(fields) + `
--input-extra merges raw JSON over the input for fields newer than this CLI.

By default the command waits for the job (polling every 5s, --timeout
default 30m) and prints the transcript text to stdout (the SRT document
with --srt, the result URL with --return-as-file). With -o json it emits
the full final job document instead. --no-wait queues the job and prints
its id; to follow up, run saladcloud inference jobs get <id> --endpoint
` + endpoint + `.

Transcription is billed per audio hour (see SaladCloud pricing); to see the
endpoint's price row, run saladcloud inference get ` + endpoint + `.`,
		Example: `  # Transcribe a local recording (uploads to S4, waits, prints the text)
  saladcloud inference ` + endpoint + ` ./meeting.mp3

  # Subtitles for a video already on the web
  saladcloud inference ` + endpoint + ` https://example.com/talk.mp4 --srt

  # Queue without waiting, then poll it yourself
  saladcloud inference ` + endpoint + ` ./meeting.mp3 --no-wait
  saladcloud inference jobs get <job-id> --endpoint ` + endpoint,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTranscribe(cmd, args[0], endpoint, fields)
		},
	}
	f := cmd.Flags()
	for _, fd := range fields {
		switch fd.kind {
		case fieldBool:
			f.Bool(fd.flag(), false, fd.help)
		case fieldString:
			f.String(fd.flag(), "", fd.help)
		case fieldInt:
			f.Int64(fd.flag(), 0, fd.help)
		case fieldList:
			f.StringArray(fd.flag(), nil, fd.help)
		}
	}
	f.String("input-extra", "", "extra input fields as a JSON object, merged over the built input (extra wins)")
	f.Bool("no-wait", false, "queue the job and exit instead of waiting for the transcript")
	f.Bool(
		"keep-upload",
		false,
		"keep a local file's S4 upload after the transcript is back (and show its signed URL in -o json)",
	)
	f.Duration("timeout", transcribeWaitTimeout, "give up waiting after this long (the job keeps running server-side)")
	return cmd
}

func runTranscribe(cmd *cobra.Command, source, endpoint string, fields []inputField) error {
	flags := cmd.Flags()
	inputExtra, _ := flags.GetString("input-extra")
	noWait, _ := flags.GetBool("no-wait")
	keepUpload, _ := flags.GetBool("keep-upload")
	timeout, _ := flags.GetDuration("timeout")
	srt := flags.Changed("srt")
	if srt {
		srt, _ = flags.GetBool("srt")
	}
	asFile := flags.Changed("return-as-file")
	if asFile {
		asFile, _ = flags.GetBool("return-as-file")
	}

	// Pre-flight validation, before any upload or API work.
	if timeout <= 0 {
		return clierr.New(clierr.CodeUsage, clierr.KindUsage, "--timeout must be greater than zero (got %s)", timeout)
	}
	if flags.Lookup("summarize") != nil {
		if n, _ := flags.GetInt64("summarize"); n < 0 {
			return clierr.New(
				clierr.CodeInvalidInput,
				clierr.KindInvalidInput,
				"invalid --summarize %d: the endpoint takes 0 to 2000",
				n,
			)
		}
	}
	// Validate the input (and --input-extra's JSON) now, so a bad value
	// fails before any upload.
	if _, cerr := buildTranscribeInput(source, fields, flags, inputExtra); cerr != nil {
		return cerr
	}
	isURL := isHTTPURL(source)
	var fileSize int64
	if !isURL {
		fi, err := os.Stat(source)
		switch {
		case err != nil:
			return clierr.New(clierr.CodeInvalidInput, clierr.KindInvalidInput,
				`audio file "%s" not found`, source).WithHint("pass a local file path or an http(s) URL")
		case fi.IsDir():
			return clierr.New(clierr.CodeInvalidInput, clierr.KindInvalidInput,
				`"%s" is a directory, not an audio file`, source)
		}
		fileSize = fi.Size()
	}

	client, _, cerr := NewClient(cmd)
	if cerr != nil {
		return cerr
	}
	if cerr = client.RequireOrg(); cerr != nil {
		return cerr
	}

	url := source
	remote := "" // the S4 name of a local file's upload
	if !isURL {
		fh, err := os.Open(source)
		if err != nil {
			return clierr.New(clierr.CodeInvalidInput, clierr.KindInvalidInput, `cannot read "%s": %v`, source, err)
		}
		defer fh.Close()
		remote = transcribeRemoteName(source, time.Now())
		url, cerr = client.UploadFile(
			cmd.Context(),
			remote,
			fh,
			transcribeMime(source),
			true,
			transcribeSignedURLExp,
		)
		if cerr != nil {
			return cerr
		}
		if output.Mode() == "table" {
			fmt.Fprintf(os.Stderr, "uploaded %s (%s) to S4\n", source, humanSize(fileSize))
		}
	}

	in, cerr := buildTranscribeInput(url, fields, flags, inputExtra)
	if cerr != nil {
		return cerr // unreachable (validated pre-flight), kept for safety
	}
	job, cerr := client.CreateInferenceJob(cmd.Context(), endpoint,
		inferenceendpoints.InferenceEndpointJobPrototype{Input: in})
	if cerr != nil {
		// No job will read the upload: delete it, unless asked to keep it
		// or the delete fails, and then say where it is.
		if remote != "" && (keepUpload || client.DeleteFile(cmd.Context(), remote) != nil) {
			keptUploadNote(remote)
		}
		return endpointNotFound(cerr, endpoint, client.Org())
	}
	id := ptr.Deref(job.Id)

	if remote != "" && !keepUpload {
		redactUploadURL(job)
	}
	if noWait {
		if remote != "" && output.Mode() == "table" {
			fmt.Fprintf(os.Stderr, "note: the upload stays in S4 as %s; the queued job reads it\n", remote)
		}
		return wrapErr(output.Emit(job, func(w io.Writer) {
			fmt.Fprintf(w, "Job %s queued on endpoint %s (status: %s)\n", id, endpoint, jobStatusString(job))
			if remote != "" {
				fmt.Fprintf(os.Stderr, "note: %s\n", signedLinkNote)
			}
			fmt.Fprintf(
				os.Stderr,
				"hint: to check on it, run saladcloud inference jobs get %s --endpoint %s\n",
				id,
				endpoint,
			)
		}))
	}

	ctx := cmd.Context() // cancelled by the one signal handler in Execute
	d := liveJobWaitDeps(client, endpoint, id, "transcribing")
	final, cerr := waitForJobTerminal(ctx, d, id, timeout, waitPoll)
	if cerr != nil {
		keptUploadNote(remote)
		return transcriptionKind(cerr)
	}
	if remote != "" && !keepUpload {
		redactUploadURL(final)
	}
	// The API can mark a failed transcription `succeeded` with the failure
	// embedded in the output — treat that as the failure it is (stdout stays
	// empty, scripts can trust the exit code).
	if errMsg, errCode := jobOutputError(final.Output); errMsg != "" || errCode != "" {
		detail := errMsg
		if detail == "" {
			detail = "transcription failed"
		}
		if errCode != "" {
			detail = fmt.Sprintf("%s (error_code: %s)", detail, errCode)
		}
		keptUploadNote(remote)
		if remote != "" {
			detail += " (" + signedLinkNote + ")"
		}
		return clierr.New(
			clierr.CodeGeneric,
			clierr.KindTranscriptionFailed,
			"%s",
			detail,
		).WithHint("to see the full job document, run saladcloud inference jobs get %s --endpoint %s -o json", id, endpoint)
	}
	text, cerr := transcriptText(final.Output, srt, asFile)
	if cerr != nil {
		keptUploadNote(remote)
		return cerr
	}
	if remote != "" && !keepUpload {
		if derr := client.DeleteFile(cmd.Context(), remote); derr != nil {
			fmt.Fprintf(os.Stderr, "warning: could not delete the S4 upload %s: %s\n", remote, derr.Message)
		}
	}
	// Table mode prints the transcript itself; -o json emits the full job.
	return wrapErr(output.Emit(final, func(w io.Writer) {
		fmt.Fprintln(w, text)
	}))
}

// transcriptionKind gives a transcription job that ended failed the kind
// transcribe documents, transcription_failed, as a job that succeeded with
// an error in its output already has; every other error is unchanged.
func transcriptionKind(cerr *clierr.Error) *clierr.Error {
	if cerr != nil && cerr.Kind == clierr.KindJobFailed {
		cerr.Kind = clierr.KindTranscriptionFailed
	}
	return cerr
}
