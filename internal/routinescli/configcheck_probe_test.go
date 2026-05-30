package routinescli

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/whyiyhw/seek/internal/config"
)

// TestConfigCheckProbe_TemplateDelivered guards the bug where
// `cron config check --probe` built its WebhookTarget without the
// Template field, so a format="template" webhook rendered to "" → invalid
// JSON → probe failed. Asserts the template is passed through, rendered
// with the test values, and delivered as valid JSON.
func TestConfigCheckProbe_TemplateDelivered(t *testing.T) {
	withTestHome(t)
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	writeTestConfig(t, config.Config{PushWebhooks: []config.PushWebhook{
		{URL: srv.URL, Format: "template", Template: `{"t":"{{title}}","b":"{{body}}"}`},
	}})

	var out bytes.Buffer
	if err := Run([]string{"config", "check", "--probe"}, &out, &bytes.Buffer{}); err != nil {
		t.Fatalf("probe should succeed, got %v\nout=%s", err, out.String())
	}
	if !json.Valid([]byte(gotBody)) {
		t.Fatalf("delivered body is not valid JSON (template not passed through?): %q", gotBody)
	}
	var p map[string]string
	if err := json.Unmarshal([]byte(gotBody), &p); err != nil {
		t.Fatal(err)
	}
	if p["t"] != "seek webhook test" {
		t.Errorf("template not rendered with the test title; body=%q", gotBody)
	}
}
