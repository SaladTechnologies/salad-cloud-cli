package clierr

import (
	"encoding/json"
	"testing"
)

// The error document is written outside output.Emit, so its keys are
// present by tag: hint is null without a fix, http_status without an HTTP
// response.
func TestErrorDocumentKeys(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		err  *Error
		want string
	}{
		{
			"usage", New(CodeUsage, KindUsage, "bad flag"),
			`{"exit_code":2,"kind":"usage","message":"bad flag","hint":null,"http_status":null}`,
		},
		{
			"http", HTTP(CodeNotFound, KindNotFound, 404, "not found"),
			`{"exit_code":4,"kind":"not_found","message":"not found","hint":null,"http_status":404}`,
		},
		{
			"hint", New(CodeUsage, KindUsage, "no organization set").WithHint("pass --org %s", "my-org"),
			`{"exit_code":2,"kind":"usage","message":"no organization set","hint":"pass --org ` +
				`my-org","http_status":null}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			data, err := json.Marshal(tc.err)
			if err != nil {
				t.Fatal(err)
			}
			if string(data) != tc.want {
				t.Errorf("got %s, want %s", data, tc.want)
			}
		})
	}
}

// TestCanceled: the error a canceled request returns is interrupted, exit
// 1, and recognizable; a copy keeps the mark, a new error does not have it.
func TestCanceled(t *testing.T) {
	t.Parallel()
	c := Canceled()
	if c.Code != CodeGeneric || c.Kind != KindInterrupted || !c.IsCancel() {
		t.Errorf("Canceled() = %+v", c)
	}
	copied := *c
	if !copied.IsCancel() {
		t.Error("a copy lost the mark")
	}
	if New(CodeGeneric, KindInterrupted, "wait interrupted").IsCancel() {
		t.Error("an ordinary interrupted error reads as a canceled request")
	}
	var none *Error
	if none.IsCancel() || New(CodeUsage, KindUsage, "x").HintText() != "" {
		t.Error("nil or hintless error misreports")
	}
}
