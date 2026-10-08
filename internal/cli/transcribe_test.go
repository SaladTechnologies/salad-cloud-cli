package cli

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/saladtechnologies/salad-cloud-cli/internal/clierr"
)

// parsedTranscribeCmd returns a porcelain command for endpoint with args
// parsed into its flags.
func parsedTranscribeCmd(t *testing.T, endpoint string, fields []inputField, args ...string) *cobra.Command {
	t.Helper()
	cmd := newTranscribeCmd(endpoint, fields, "")
	if err := cmd.ParseFlags(args); err != nil {
		t.Fatalf("parse %v: %v", args, err)
	}
	return cmd
}

// A flag that is not set is not sent: the input carries url plus what the
// user passed, so the endpoint's own defaults govern the rest.
func TestBuildTranscribeInputSendsOnlySetFlags(t *testing.T) {
	tests := []struct {
		name  string
		args  []string
		extra string
		want  map[string]any
	}{
		{"nothing set", nil, "", map[string]any{"url": "u"}},
		{"set flags, including an explicit false", []string{"--language-code", "de", "--diarization", "--sentence-level-timestamps=false", "--summarize", "100"}, "",
			map[string]any{"url": "u", "language_code": "de", "diarization": true, "sentence_level_timestamps": false, "summarize": int64(100)}},
		{"input-extra wins over the flags", []string{"--language-code", "en"}, `{"language_code": "fr", "word_level_timestamps": true}`,
			map[string]any{"url": "u", "language_code": "fr", "word_level_timestamps": true}},
		// A list field is a repeated flag, joined with commas for the
		// endpoint's one string; a value is never split.
		{"list fields repeat and join", []string{"--llm-translation", "german", "--llm-translation", "french",
			"--custom-vocabulary", "Salad", "--classification-labels", "a,b"}, "",
			map[string]any{"url": "u", "llm_translation": "german,french", "custom_vocabulary": "Salad", "classification_labels": "a,b"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := parsedTranscribeCmd(t, "transcribe", transcribeFields, tt.args...)
			got, cerr := buildTranscribeInput("u", transcribeFields, cmd.Flags(), tt.extra)
			if cerr != nil {
				t.Fatalf("buildTranscribeInput: %v", cerr)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("input = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestBuildTranscribeInputRejectsBadExtra(t *testing.T) {
	cmd := parsedTranscribeCmd(t, "transcribe", transcribeFields)
	for _, extra := range []string{"{not json", `"a string"`, "[1,2]"} {
		_, cerr := buildTranscribeInput("u", transcribeFields, cmd.Flags(), extra)
		if cerr == nil {
			t.Fatalf("buildTranscribeInput(extra=%q) = nil error, want invalid_input", extra)
		}
		if cerr.Code != clierr.CodeInvalidInput {
			t.Errorf("Code = %d for extra %q, want %d", cerr.Code, extra, clierr.CodeInvalidInput)
		}
	}
}

// Each porcelain command's flags are exactly its endpoint's input fields,
// kebab-cased: lite rejects a full-only flag by itself, and the two never
// spell one input differently.
func TestTranscribeFlagsAreTheEndpointFields(t *testing.T) {
	if len(transcribeFields)+1 != 20 || len(transcriptionLiteFields)+1 != 9 {
		t.Errorf("fields with url: transcribe %d, transcription-lite %d; want 20 and 9",
			len(transcribeFields)+1, len(transcriptionLiteFields)+1)
	}
	full := map[string]inputField{}
	for _, f := range transcribeFields {
		full[f.name] = f
		if f.flag() != strings.ReplaceAll(f.name, "_", "-") {
			t.Errorf("flag %q is not the kebab-cased field %q", f.flag(), f.name)
		}
	}
	for _, f := range transcriptionLiteFields {
		if g, ok := full[f.name]; !ok || g.kind != f.kind {
			t.Errorf("lite field %q is not a full-tier field of the same type", f.name)
		}
	}
	lite := newTranscribeCmd("transcription-lite", transcriptionLiteFields, "")
	if err := lite.ParseFlags([]string{"--summarize", "10"}); err == nil {
		t.Error("transcription-lite accepted --summarize, a full-tier-only field")
	}
	for _, name := range []string{"language", "diarize", "lite"} {
		if lite.Flags().Lookup(name) != nil {
			t.Errorf("--%s is a removed spelling", name)
		}
	}
}

func TestIsHTTPURLDispatch(t *testing.T) {
	for s, want := range map[string]bool{
		"https://example.com/a.mp3": true,
		"http://example.com/a.mp3":  true,
		"./meeting.mp3":             false,
		"meeting.mp3":               false,
		"C:\\audio\\meeting.mp3":    false,
		"ftp://example.com/a.mp3":   false,
	} {
		if got := isHTTPURL(s); got != want {
			t.Errorf("isHTTPURL(%q) = %t, want %t", s, got, want)
		}
	}
}

func TestTranscribeMime(t *testing.T) {
	for path, want := range map[string]string{
		"meeting.mp3":     "audio/mpeg",
		"clip.WAV":        "audio/wav",
		"note.m4a":        "audio/x-m4a",
		"talk.mp4":        "video/mp4",
		"weird.xyz":       "application/octet-stream",
		"noextension":     "application/octet-stream",
		"dir/nested.flac": "audio/flac",
	} {
		if got := transcribeMime(path); got != want {
			t.Errorf("transcribeMime(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestTranscribeRemoteName(t *testing.T) {
	now := time.Unix(1756800000, 0)
	got := transcribeRemoteName("testdata/meeting.mp3", now)
	want := "saladcloud-cli/transcribe/meeting-1756800000.mp3"
	if got != want {
		t.Errorf("transcribeRemoteName = %q, want %q", got, want)
	}
	// No extension: no trailing dot, timestamp still appended.
	if got := transcribeRemoteName("audio", now); got != "saladcloud-cli/transcribe/audio-1756800000" {
		t.Errorf("transcribeRemoteName(no ext) = %q", got)
	}
}

func TestTranscriptText(t *testing.T) {
	srtDoc := "1\n00:00:00,000 --> 00:00:01,000\nhello\n"
	// The endpoint returns the SRT under "srt_content" (verified live).
	out := map[string]any{"text": "hello world", "srt_content": srtDoc}
	if got, cerr := transcriptText(out, false, false); cerr != nil || got != "hello world" {
		t.Errorf("transcriptText(text) = %q, %v; want the text field", got, cerr)
	}
	if got, cerr := transcriptText(out, true, false); cerr != nil || got != srtDoc {
		t.Errorf("transcriptText(srt) = %q, %v; want the srt_content field", got, cerr)
	}
	// The legacy "srt" spelling is accepted too.
	if got, cerr := transcriptText(map[string]any{"srt": srtDoc}, true, false); cerr != nil || got != srtDoc {
		t.Errorf("transcriptText(legacy srt) = %q, %v; want the srt field", got, cerr)
	}
	// srt requested but absent: an error, NOT a silent fallback to the text —
	// a script piping to an .srt file must not receive a transcript instead.
	if got, cerr := transcriptText(map[string]any{"text": "t"}, true, false); cerr == nil {
		t.Errorf("transcriptText(srt missing) = %q, nil; want an error", got)
	} else if !strings.Contains(cerr.Message, "srt_content") {
		t.Errorf("srt-missing error %q does not name srt_content", cerr.Message)
	}
	// Neither field: compact JSON of the whole output.
	if got, cerr := transcriptText(map[string]any{"words": []any{"a"}}, false, false); cerr != nil || got != `{"words":["a"]}` {
		t.Errorf("transcriptText(no text) = %q, %v; want compact JSON", got, cerr)
	} // --return-as-file prints the result URL; none is an error, not the text.
	if got, cerr := transcriptText(map[string]any{"text": "t", "url": "https://r/x.json"}, false, true); cerr != nil || got != "https://r/x.json" {
		t.Errorf("transcriptText(file) = %q, %v; want the url", got, cerr)
	}
	if _, cerr := transcriptText(map[string]any{"text": "t"}, false, true); cerr == nil {
		t.Error("transcriptText(file, no url) = nil error, want one")
	}
}

func TestJobOutputError(t *testing.T) {
	// The API marks some failed transcriptions `succeeded` with the failure
	// embedded in the output; the CLI must surface it as a real error.
	msg, code := jobOutputError(map[string]any{
		"duration": 0, "error": "File can not be downloaded", "error_code": "download_failed",
	})
	if msg != "File can not be downloaded" || code != "download_failed" {
		t.Errorf("jobOutputError = (%q, %q), want the embedded error", msg, code)
	}
	if msg, code := jobOutputError(map[string]any{"text": "fine"}); msg != "" || code != "" {
		t.Errorf("jobOutputError(clean output) = (%q, %q), want empty", msg, code)
	}
	if msg, code := jobOutputError("not a map"); msg != "" || code != "" {
		t.Errorf("jobOutputError(non-map) = (%q, %q), want empty", msg, code)
	}
}

func TestHumanSize(t *testing.T) {
	for n, want := range map[int64]string{
		512:             "512 B",
		2048:            "2.0 KB",
		3 * 1024 * 1024: "3.0 MB",
	} {
		if got := humanSize(n); got != want {
			t.Errorf("humanSize(%d) = %q, want %q", n, got, want)
		}
	}
}

// TestListFieldsHelpNamesOnlyTheEndpointsFlags: each porcelain command's
// help names the list flags its own endpoint takes, and no others.
func TestListFieldsHelpNamesOnlyTheEndpointsFlags(t *testing.T) {
	for _, ep := range transcribeEndpoints {
		help := listFieldsHelp(ep.fields)
		isList := map[string]bool{}
		for _, f := range ep.fields {
			isList["--"+f.flag()] = f.kind == fieldList
		}
		for _, name := range []string{"--llm-translation", "--srt-translation", "--custom-vocabulary", "--classification-labels"} {
			if got := strings.Contains(help, name); got != isList[name] {
				t.Errorf("%s: help names %s = %v, endpoint takes it as a list = %v", ep.name, name, got, isList[name])
			}
		}
	}
}
