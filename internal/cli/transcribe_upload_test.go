package cli

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/saladtechnologies/salad-cloud-cli/internal/clierr"
)

// When the job cannot be created, nothing will read the local file's S4
// upload, so transcribe deletes it rather than leave a signed copy behind.
func TestTranscribeDeletesUploadWhenJobCreateFails(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Method+" "+r.URL.Path)
		mu.Unlock()
		switch {
		case r.Method == http.MethodPut:
			w.Write([]byte(`{"url":"http://` + r.Host + r.URL.Path + `?token=t"}`))
		case r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/jobs"):
			w.WriteHeader(http.StatusPaymentRequired)
			w.Write([]byte(`{"detail":"insufficient balance"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	stubAPI(t, nil)
	t.Setenv("SALAD_CLI_API_BASE_URL", srv.URL)
	t.Setenv("SALAD_CLI_STORAGE_BASE_URL", srv.URL)
	audio := filepath.Join(t.TempDir(), "a.wav")
	if err := os.WriteFile(audio, []byte("RIFF....WAVE"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := runRoot(t, "inference", "transcribe", audio, "-o", "json")
	if cerr, ok := err.(*clierr.Error); !ok || cerr.Code == clierr.CodeOK {
		t.Fatalf("transcribe = %v, want the job-create error", err)
	}
	var put, del string
	for _, s := range seen {
		switch {
		case strings.HasPrefix(s, "PUT "):
			put = strings.TrimPrefix(s, "PUT ")
		case strings.HasPrefix(s, "DELETE "):
			del = strings.TrimPrefix(s, "DELETE ")
		}
	}
	if put == "" || del != put {
		t.Errorf("requests = %v; want the uploaded file deleted after the failed create", seen)
	}
}

func TestTranscriptionKind(t *testing.T) {
	failed := clierr.New(clierr.CodeGeneric, clierr.KindJobFailed, "job failed")
	if got := transcriptionKind(failed); got.Kind != clierr.KindTranscriptionFailed || got.Code != clierr.CodeGeneric {
		t.Errorf("failed job = %s/%d, want transcription_failed/1", got.Kind, got.Code)
	}
	for _, k := range []clierr.Kind{clierr.KindJobCancelled, clierr.KindWaitTimeout, clierr.KindInterrupted} {
		if got := transcriptionKind(clierr.New(clierr.CodeGeneric, k, "x")); got.Kind != k {
			t.Errorf("%s became %s", k, got.Kind)
		}
	}
}

// TestTranscribeFailedJobIsTranscriptionFailed drives transcribe against a
// stub endpoint whose job ends failed: the exit is the transcription's own
// kind, not the generic job_failed, which is the wiring transcriptionKind's
// unit test cannot see.
func TestTranscribeFailedJobIsTranscriptionFailed(t *testing.T) {
	const id = "8a2f13ba-01c2-4a29-9c4d-52c7e5a44e37"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/inference-endpoints/transcribe/jobs"):
			w.WriteHeader(http.StatusCreated)
			w.Write([]byte(`{"id":"` + id + `","status":"pending","events":[]}`))
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/jobs/"+id):
			w.Write(
				[]byte(
					`{"id":"` + id + `","status":"failed","events":[{"action":"failed","time":"2026-10-03T00:00:00Z"}]}`,
				),
			)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	stubAPI(t, nil)
	t.Setenv("SALAD_CLI_API_BASE_URL", srv.URL)
	_, err := runRoot(t, "inference", "transcribe", "https://example.com/a.wav", "-o", "json")
	cerr, ok := err.(*clierr.Error)
	if !ok || cerr.Kind != clierr.KindTranscriptionFailed || cerr.Code != clierr.CodeGeneric {
		t.Errorf("transcribe of a failed job = %v, want (1, transcription_failed)", err)
	}
}
