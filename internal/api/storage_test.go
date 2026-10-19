package api

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/saladtechnologies/salad-cloud-cli/internal/clierr"
	"github.com/saladtechnologies/salad-cloud-cli/internal/ptr"
)

// TestUploadFile pins the S4 upload wire contract: PUT to the org files path
// (folder slashes preserved), multipart/form-data with the file bytes and the
// mimeType/sign/signatureExp fields, the Salad-Api-Key header, and the
// {"url": ...} response parsed.
func TestUploadFile(t *testing.T) {
	t.Parallel()
	fileBytes := []byte("fake-mp3-bytes")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		checkRequest(t, r, http.MethodPut, "/organizations/my-org/files/saladcloud-cli/transcribe/meeting-123.mp3")
		if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "multipart/form-data") {
			t.Errorf("Content-Type = %q, want multipart/form-data", ct)
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatalf("ParseMultipartForm: %v", err)
		}
		for field, want := range map[string]string{"mimeType": "audio/mpeg", "sign": "true", "signatureExp": "86400"} {
			if got := r.FormValue(field); got != want {
				t.Errorf("%s = %q, want %s", field, got, want)
			}
		}
		f, _, err := r.FormFile("file")
		if err != nil {
			t.Fatalf("FormFile: %v", err)
		}
		defer func() { reportErr(t, f.Close()) }()
		got := should(io.ReadAll(f))(t)
		if !bytes.Equal(got, fileBytes) {
			t.Errorf("file part = %q, want %q", got, fileBytes)
		}
		w.Header().Set("Content-Type", "application/json")
		respond(
			t,
			w,
			`{"url": "https://storage-api.salad.com/organizations/my-org/files/saladcloud-cli/transcribe/meeting-123.mp3?token=abc"}`,
		)
	}))
	t.Cleanup(srv.Close)

	c := newRestTestClient(t, srv.URL) // API base URL is irrelevant here
	url, cerr := c.UploadFile(t.Context(),
		"saladcloud-cli/transcribe/meeting-123.mp3", bytes.NewReader(fileBytes), "audio/mpeg", true, 86400)
	if cerr != nil {
		t.Fatalf("UploadFile: %v", cerr)
	}
	if !strings.Contains(url, "token=abc") {
		t.Errorf("url = %q, want the server's signed URL", url)
	}
}

// TestUploadFileUnsigned: without sign, the sign/signatureExp fields must be
// absent entirely (not sent as "false"/"0").
func TestUploadFileUnsigned(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatalf("ParseMultipartForm: %v", err)
		}
		if _, ok := r.MultipartForm.Value["sign"]; ok {
			t.Error("sign field sent, want absent for an unsigned upload")
		}
		if _, ok := r.MultipartForm.Value["signatureExp"]; ok {
			t.Error("signatureExp field sent, want absent for an unsigned upload")
		}
		w.Header().Set("Content-Type", "application/json")
		respond(t, w, `{"url": "https://storage-api.salad.com/x"}`)
	}))
	t.Cleanup(srv.Close)

	c := newRestTestClient(t, srv.URL)
	_, cerr := c.UploadFile(
		t.Context(),
		"a.bin",
		strings.NewReader("x"),
		"application/octet-stream",
		false,
		0,
	)
	if cerr != nil {
		t.Fatalf("UploadFile: %v", cerr)
	}
}

func TestUploadFileNon2xxMapsStatus(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		respond(t, w, `{"title": "Forbidden"}`)
	}))
	t.Cleanup(srv.Close)

	c := newRestTestClient(t, srv.URL)
	_, cerr := c.UploadFile(t.Context(), "a.mp3", strings.NewReader("x"), "audio/mpeg", true, 60)
	if cerr == nil {
		t.Fatal("UploadFile = nil error, want auth")
	}
	if cerr.Code != clierr.CodeAuth || cerr.Kind != clierr.KindAuth {
		t.Errorf("error = (%d, %s), want (%d, auth)", cerr.Code, cerr.Kind, clierr.CodeAuth)
	}
	if ptr.Deref(cerr.HTTPStatus) != http.StatusForbidden {
		t.Errorf("HTTPStatus = %d, want 403", ptr.Deref(cerr.HTTPStatus))
	}
}
