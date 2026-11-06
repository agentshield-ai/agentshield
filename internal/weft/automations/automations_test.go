package automations

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agentshield-ai/agentshield/internal/weft/egress"
	"github.com/agentshield-ai/agentshield/internal/weft/store"
)

func allowLoopback(t *testing.T) {
	prev := egress.SetDefault(egress.MustNew(egress.Config{Allow: []string{"127.0.0.1", "::1", "localhost"}}))
	t.Cleanup(func() { egress.SetDefault(prev) })
}

func newRunner(t *testing.T, secret SecretFunc) *Runner {
	st, err := store.Open(filepath.Join(t.TempDir(), "w.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return New(st, secret, nil)
}

func obj(pid string) *store.Obj { return &store.Obj{ProjectID: pid, ObjectID: "auto"} }

// I7: values substituted into a Slack message are escaped, so event data
// (a feedback type, a payload) cannot inject links or mentions; the
// template's own text is kept.
func TestSlackMessagesEscapeSubstitutedValues(t *testing.T) {
	allowLoopback(t)
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &got)
	}))
	defer srv.Close()
	r := newRunner(t, nil)
	ev := map[string]any{"type": "feedback_created", "feedback_type": "x", "span_id": "<!channel> <https://evil.example|click> & co",
		"payload": map[string]any{"note": "<@U123>"}}
	a := &Automation{Name: "n<b>", Trigger: Trigger{Type: "feedback_created"},
		Action: Action{Type: "slack", URL: srv.URL, Message: "<https://weft.example|Open> {span_id} {payload}"}}
	if _, _, err := r.deliver(context.Background(), obj("e/p"), a, ev, false); err != nil {
		t.Fatal(err)
	}
	want := "<https://weft.example|Open> &lt;!channel&gt; &lt;https://evil.example|click&gt; &amp; co {"
	if txt, _ := got["text"].(string); !strings.HasPrefix(txt, want) || strings.Contains(txt, "<@U123>") {
		t.Fatalf("text = %q\nwant prefix %q", txt, want)
	}
	// The default message escapes too.
	a.Action.Message = ""
	if _, _, err := r.deliver(context.Background(), obj("e/p"), a, ev, false); err != nil {
		t.Fatal(err)
	}
	if txt := got["text"].(string); strings.Contains(txt, "<!channel>") || strings.Contains(txt, "n<b>") {
		t.Fatalf("default text = %q", txt)
	}
	// Webhooks (JSON) get the values unchanged.
	if Render("{a}", map[string]any{"a": "<x>"}) != "<x>" {
		t.Fatal("Render escaped")
	}
}

// E2: when the webhook URL comes from a secret, delivery errors do not
// contain it; header values are validated when the automation is checked.
func TestSecretWebhookErrorsAreGeneric(t *testing.T) {
	allowLoopback(t)
	const secretURL = "http://127.0.0.1:1/services/T000/B000/SECRETTOKEN"
	r := newRunner(t, func(context.Context, string, string) (string, error) { return secretURL, nil })
	a := &Automation{Trigger: Trigger{Type: "feedback_created"}, Action: Action{Type: "slack", URLSecret: "SLACK_HOOK"}}
	_, _, err := r.deliver(context.Background(), obj("e/p"), a, map[string]any{"type": "feedback_created"}, true)
	if err == nil || strings.Contains(err.Error(), "SECRETTOKEN") || strings.Contains(err.Error(), "127.0.0.1") || !strings.Contains(err.Error(), "SLACK_HOOK") {
		t.Fatalf("error = %v", err)
	}
	// A header with a newline is refused at validation, and never reaches
	// the transport (whose error would print the URL).
	a.Action.Headers = map[string]string{"X-Bad": "a\nb"}
	if err := a.Validate(); err == nil || !strings.Contains(err.Error(), "X-Bad") {
		t.Fatalf("validate: %v", err)
	}
	_, _, err = r.deliver(context.Background(), obj("e/p"), a, map[string]any{"type": "feedback_created"}, true)
	if err == nil || strings.Contains(err.Error(), "SECRETTOKEN") {
		t.Fatalf("deliver with a bad header: %v", err)
	}
	for _, bad := range []map[string]string{{"Bad Name": "v"}, {"X": "\r"}, {"": "v"}} {
		a.Action.Headers = bad
		if a.Validate() == nil {
			t.Fatalf("accepted headers %v", bad)
		}
	}
	a.Action.Headers = map[string]string{"Authorization": "Bearer x", "X-Tab": "a\tb"}
	if err := a.Validate(); err != nil {
		t.Fatal(err)
	}
}
