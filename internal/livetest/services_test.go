//go:build live

// The platform services past container groups: transcription, the AI Gateway, and queues.

package livetest

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

// TestTranscribe covers the inference path end to end. Cost is per audio
// hour, so a few seconds of speech is negligible.
//
// A transcription job can fail transiently, with no error detail in the job
// document, so a single failure is retried once before the test is believed.
func TestTranscribe(t *testing.T) { //nolint:paralleltest // writes to the live account, one at a time
	audio := os.Getenv("SALAD_CLI_TEST_AUDIO")
	if audio == "" {
		t.Skip("SALAD_CLI_TEST_AUDIO unset (path to a short wav); skipping transcription")
	}
	phrase := os.Getenv("SALAD_CLI_TEST_PHRASE")
	if phrase == "" {
		phrase = "quick brown fox"
	}
	r := transcribeRetried(t, audio)
	if !strings.Contains(strings.ToLower(r.Stdout), strings.ToLower(phrase)) {
		t.Errorf("transcript %q does not contain %q", r.Stdout, phrase)
	}

	// --srt must produce an actual SRT document, not the plain transcript.
	srt := run(t, "inference", "transcribe", audio, "--srt")
	if srt.Code == 0 && !strings.Contains(srt.Stdout, "-->") {
		t.Errorf("--srt produced no cue timestamps: %s", srt.Stdout)
	}

	// A failed transcription must exit nonzero with stdout empty — the API
	// reports such jobs as "succeeded" with the error inside the payload.
	bad := run(t, "inference", "transcribe", "https://example.com/nope.mp3")
	if bad.Code == 0 {
		t.Errorf("an undownloadable URL exited 0; stdout: %s", bad.Stdout)
	}
	if strings.TrimSpace(bad.Stdout) != "" {
		t.Errorf("failed transcription must leave stdout empty, got: %s", bad.Stdout)
	}

	transcribeLite(t, audio, phrase)
}

// transcribeRetried transcribes audio, retrying a failed job once, and fails the test when both attempts fail.
func transcribeRetried(t *testing.T, audio string) result {
	t.Helper()
	var r result
	for attempt := 1; attempt <= 2; attempt++ {
		r = run(t, "inference", "transcribe", audio)
		if r.Code == 0 {
			break
		}
		t.Logf("transcribe attempt %d failed (exit %d): %s", attempt, r.Code, r.Stderr)
	}
	if r.Code != 0 {
		t.Fatalf("transcribe failed twice: %s", r.Stderr)
	}
	return r
}

// transcribeLite checks the lite tier: its JSON drops the upload's signed URL, and its transcript holds phrase.
func transcribeLite(t *testing.T, audio, phrase string) {
	t.Helper()
	// -o json never hands out the upload's signed URL: the token is removed
	// (and the upload itself deleted) once the transcript is back.
	js := run(t, "inference", "transcription-lite", audio, "-o", "json")
	if js.Code == 0 {
		in := valueAs[map[string]any](oneDoc(t, js)["input"])
		if u := valueAs[string](in["url"]); strings.Contains(u, "?") {
			t.Errorf("input.url kept its signed query string: %s", u)
		}
	}

	// The lite tier is its own porcelain command, named after its endpoint.
	lite := run(t, "inference", "transcription-lite", audio)
	if lite.Code == 0 && !strings.Contains(strings.ToLower(lite.Stdout), strings.ToLower(phrase)) {
		t.Errorf("transcription-lite transcript %q does not contain %q", lite.Stdout, phrase)
	}
}

// TestTranscribeLanguageAndStream pins two behaviors only a live run can
// confirm: language_code omitted or null detects the language (it is not
// 'en'), and audio_stream_index is honored. It needs a non-English clip,
// because English audio cannot tell detection from a forced en. A
// single-track clip makes stream 1 invalid.
func TestTranscribeLanguageAndStream(t *testing.T) { //nolint:paralleltest // writes to the live account, one at a time
	audio := os.Getenv("SALAD_CLI_TEST_AUDIO_FOREIGN")
	if audio == "" {
		t.Skip("SALAD_CLI_TEST_AUDIO_FOREIGN unset (path to a short single-track non-English clip); skipping")
	}
	omitted := transcribeDoc(t, audio)
	null := transcribeDoc(t, audio, "--input-extra", `{"language_code": null}`)
	forced := transcribeDoc(t, audio, "--language-code", "en")

	if in := valueAs[map[string]any](null["input"]); in == nil {
		t.Errorf("null job has no input: %v", null)
	} else if v, has := in["language_code"]; !has || v != nil {
		t.Errorf("null was not preserved in the echoed input: %v", in)
	}
	if transcriptText(omitted) == "" {
		t.Fatalf("omitted language_code returned no transcript: %v", omitted)
	}
	if transcriptText(null) != transcriptText(omitted) {
		t.Errorf(
			"null and omitted language_code differ:\nnull:    %s\nomitted: %s",
			transcriptText(null),
			transcriptText(omitted),
		)
	}
	if transcriptText(forced) == transcriptText(omitted) {
		t.Errorf(
			"forcing en changed nothing, so omitted may default to en, or the clip is English: %s",
			transcriptText(forced),
		)
	}

	transcribeMissingStream(t, audio)
}

// transcribeMissingStream checks that a stream the single-track clip audio lacks fails the job, which proves
// audio_stream_index is read.
func transcribeMissingStream(t *testing.T, audio string) {
	t.Helper()
	bad := run(t, "inference", "transcribe", audio, "--audio-stream-index", "1")
	if bad.Code == 0 {
		t.Errorf("--audio-stream-index 1 on a single-track clip exited 0; stdout: %s", bad.Stdout)
	}
	if !strings.Contains(bad.Stderr, "invalid_audio_stream") {
		t.Errorf("stderr should carry invalid_audio_stream: %s", bad.Stderr)
	}
	if strings.TrimSpace(bad.Stdout) != "" {
		t.Errorf("failed transcription must leave stdout empty, got: %s", bad.Stdout)
	}
}

// transcribeDoc is transcribe's JSON document for audio with the extra args. Transient job failures are retried
// once, as in TestTranscribe.
func transcribeDoc(t *testing.T, audio string, args ...string) map[string]any {
	t.Helper()
	var r result
	for attempt := 1; attempt <= 2; attempt++ {
		r = run(t, append([]string{"inference", "transcribe", audio, "-o", "json"}, args...)...)
		if r.Code == 0 {
			return oneDoc(t, r)
		}
		t.Logf("transcribe %v attempt %d failed (exit %d): %s", args, attempt, r.Code, r.Stderr)
	}
	t.Fatalf("transcribe %v failed twice: %s", args, r.Stderr)
	return nil
}

// transcriptText is the trimmed output.text of a transcription job document.
func transcriptText(d map[string]any) string {
	out := valueAs[map[string]any](d["output"])
	s := valueAs[string](out["text"])
	return strings.TrimSpace(s)
}

// TestGateway smoke-tests the AI Gateway commands. It needs a gateway key,
// which is a separate credential, so it skips without one.
func TestGateway(t *testing.T) {
	t.Parallel()
	if os.Getenv("SALAD_AI_GATEWAY_API_KEY") == "" {
		t.Skip("SALAD_AI_GATEWAY_API_KEY unset; skipping the AI Gateway smoke test")
	}
	m := run(t, "ai-gateway", "models", "list", "-o", "json")
	if m.Code != 0 {
		t.Fatalf("gateway models list exit %d: %s", m.Code, m.Stderr)
	}
	doc := oneDoc(t, m)
	data := valueAs[[]any](doc["items"])
	if len(data) == 0 {
		t.Fatalf("gateway models listed nothing: %s", m.Stdout)
	}
	r := run(t, "ai-gateway", "test", "--prompt", "Reply with the single word: pong", "-o", "json")
	if r.Code != 0 {
		t.Fatalf("gateway test exit %d: %s", r.Code, r.Stderr)
	}
	res := oneDoc(t, r)
	if res["ok"] != any(true) || res["reply"] == "" {
		t.Errorf("gateway test = %v", res)
	}
	gatewayWrongKey(t)
}

// gatewayErrorDoc is the part of an error document gatewayWrongKey reads.
type gatewayErrorDoc struct {
	Error gatewayErrorBody `json:"error"`
}

// gatewayErrorBody is a gatewayErrorDoc's error.
type gatewayErrorBody struct {
	Kind       string `json:"kind"`
	Message    string `json:"message"`
	HTTPStatus *int   `json:"http_status"`
}

// gatewayWrongKey checks that a wrong key is an auth failure with empty stdout. The model list is public and accepts
// any key, so the check is test's completion, which the gateway rejects before it runs (not billed).
func gatewayWrongKey(t *testing.T) {
	t.Helper()
	bad := runEnv(
		t,
		map[string]string{"SALAD_AI_GATEWAY_API_KEY": "not-a-real-key"},
		"ai-gateway",
		"test",
		"-o",
		"json",
	)
	if bad.Code != 3 || strings.TrimSpace(bad.Stdout) != "" {
		t.Errorf("wrong gateway key: exit %d stdout %q, want 3 and empty", bad.Code, bad.Stdout)
	}
	var errDoc gatewayErrorDoc
	if err := json.Unmarshal([]byte(strings.TrimSpace(bad.Stderr)), &errDoc); err != nil {
		t.Fatalf("wrong gateway key: stderr is not one error document: %s", bad.Stderr)
	}
	if want := "AI Gateway key ****...-key (from SALAD_AI_GATEWAY_API_KEY) was rejected"; errDoc.Error.Kind != "auth" ||
		errDoc.Error.Message != want || errDoc.Error.HTTPStatus == nil || *errDoc.Error.HTTPStatus != 401 {
		t.Errorf("wrong gateway key: %s; want kind auth, %q, http_status 401", bad.Stderr, want)
	}
}

// TestQueues drives the queue plumbing without a worker: a job submitted to
// a queue with no container group stays pending, so it can be read back and
// canceled. Queues cost nothing.
func TestQueues(t *testing.T) { //nolint:paralleltest // writes to the live account, one at a time
	q := name("q")
	r := run(t, "queues", "create", q, "--display-name", "Live test queue", "-o", "json")
	if r.Code != 0 {
		t.Fatalf("queues create exit %d: %s", r.Code, r.Stderr)
	}
	t.Cleanup(func() { _ = run(t, "queues", "delete", q) })
	if doc := oneDoc(t, r); doc["name"] != q {
		t.Errorf("created queue name = %v", doc["name"])
	}
	queuesReadUpdate(t, q)
	queuesJobs(t, q)
	queuesConnectGroup(t, q)
}

// queuesReadUpdate reads queue q back and updates its description.
func queuesReadUpdate(t *testing.T, q string) {
	t.Helper()
	if g := run(t, "queues", "get", q, "-o", "json"); g.Code != 0 {
		t.Errorf("queues get exit %d: %s", g.Code, g.Stderr)
	}
	u := run(
		t,
		"queues",
		"update",
		q,
		"--description",
		"updated",
		"-o",
		"json",
	)
	if u.Code != 0 ||
		oneDoc(t, u)["description"] != "updated" {
		t.Errorf("queues update exit %d: %s %s", u.Code, u.Stdout, u.Stderr)
	}
}

// queuesJobs submits a job to q, which has no container group, so it stays pending: read it back, list it, and
// cancel it.
func queuesJobs(t *testing.T, q string) {
	t.Helper()
	job := run(t, "queues", "jobs", "create", "--queue", q, "--input", `{"n": 1}`, "-o", "json")
	if job.Code != 0 {
		t.Fatalf("queues jobs create exit %d: %s", job.Code, job.Stderr)
	}
	id := valueAs[string](oneDoc(t, job)["id"])
	if id == "" {
		t.Fatalf("queue job has no id: %s", job.Stdout)
	}
	if g := run(t, "queues", "jobs", "get", id, "--queue", q, "-o", "json"); g.Code != 0 {
		t.Errorf("queues jobs get exit %d: %s", g.Code, g.Stderr)
	}
	l := run(
		t,
		"queues",
		"jobs",
		"list",
		"--queue",
		q,
		"-o",
		"json",
	)
	if l.Code != 0 ||
		!strings.Contains(l.Stdout, id) {
		t.Errorf("queues jobs list exit %d does not list %s: %s", l.Code, id, l.Stdout)
	}
	if d := run(t, "queues", "jobs", "cancel", id, "--queue", q, "-o", "json"); d.Code != 0 {
		t.Errorf("queues jobs cancel (before dispatch) exit %d: %s", d.Code, d.Stderr)
	}
}

// queuesConnectGroup checks create --queue: a missing queue is not found, and a group created on q carries its
// queue connection, the worker note, and its queue autoscaler or a warning that the API dropped it.
func queuesConnectGroup(t *testing.T, q string) {
	t.Helper()
	// create --queue checks the queue exists before creating anything.
	missing := run(t, "container-groups", "create", name("qmiss"), "--image", "docker.io/library/busybox:1.36",
		"--no-autostart", "--queue", prefix+"-no-such-queue", "--queue-port", "8080", "--queue-path", "/job")
	if missing.Code != 4 {
		t.Errorf("create --queue <missing> exit %d, want 4: %s", missing.Code, missing.Stderr)
	}
	time.Sleep(createSpacing)
	g := name("qcg")
	auto := liveTempFile(
		t,
		"auto.json",
		`{"queue_autoscaler":{"min_replicas":0,"max_replicas":1,"desired_queue_length":1}}`,
	)
	cg := run(t, "container-groups", "create", g, "--image", "docker.io/library/busybox:1.36", "--no-autostart",
		"--queue", q, "--queue-port", "8080", "--queue-path", "/job", "-f", auto, "-o", "json")
	if cg.Code != 0 {
		t.Fatalf("create --queue exit %d: %s", cg.Code, cg.Stderr)
	}
	conn := valueAs[map[string]any](oneDoc(t, cg)["queue_connection"])
	if conn["queue_name"] != q || conn["port"] != float64(8080) || conn["path"] != "/job" {
		t.Errorf("queue_connection = %v", conn)
	}
	// Nothing reports an image without the worker, so create says it.
	if !strings.Contains(cg.Stderr, "job queue worker") {
		t.Errorf("create --queue stderr has no worker note: %q", cg.Stderr)
	}
	// The API can drop queue_autoscaler on create; either it kept it, or
	// create said it did not.
	if oneDoc(t, cg)["queue_autoscaler"] == nil && !strings.Contains(cg.Stderr, "queue_autoscaler") {
		t.Errorf("queue_autoscaler dropped without a warning: %q", cg.Stderr)
	}
	_ = run(t, "container-groups", "delete", g)
}
