package cli

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/saladtechnologies/salad-cloud-cli/internal/clierr"
)

// Canceling a finished job: the API answers a bare 409 Conflict; the CLI
// says the job has finished and in what status, keeping the 409's exit code
// and kind.
func TestQueueJobCancelFinishedSaysWhy(t *testing.T) {
	t.Parallel()
	app := newTestRun(t)
	const id = "30add6e3-6f35-49a1-9905-dfc21805a783"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/queues/q/jobs/"+id) {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusConflict)
			respond(t, w, `{"title":"Conflict","status":409}`)
			return
		}
		respond(t, w, `{"id":"`+id+`","status":"succeeded","events":[]}`)
	}))
	t.Cleanup(srv.Close)
	app.setenv("SALAD_CLI_API_BASE_URL", srv.URL)
	app.setenv("SALAD_CLI_CONFIG_DIR", t.TempDir())
	app.setenv("SALAD_API_KEY", "salad_cloud_user_test0000")
	app.setenv("SALAD_ORGANIZATION", "my-org")
	app.setenv("SALAD_PROJECT", "default")

	_, err := app.runRoot(t, "queues", "jobs", "cancel", id, "--queue", "q", "-o", "json")
	var cerr *clierr.Error
	ok := errors.As(err, &cerr)
	if !ok {
		t.Fatalf("err = %v, want a *clierr.Error", err)
	}
	if !strings.Contains(cerr.Message, `cannot be canceled: its status is already succeeded`) ||
		cerr.Code != clierr.CodeGeneric ||
		cerr.HTTPStatus == nil ||
		*cerr.HTTPStatus != http.StatusConflict {
		t.Errorf("err = %+v (code %d), want the finished-job message with the 409's exit 1", cerr.Message, cerr.Code)
	}
}
