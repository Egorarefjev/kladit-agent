package policy

import "testing"

func TestPolicy(t *testing.T) {
	for _, input := range []string{`{"version":1}`, `{"version":1,"capabilities":{}}`, `{"version":1,"capabilities":{"metrics.read":false}}`} {
		p, err := parse([]byte(input))
		if err != nil {
			t.Fatal(err)
		}
		if p.Require("metrics.read") == nil {
			t.Fatal("missing/false permission allowed")
		}
	}
	p, err := parse([]byte(`{"version":1,"capabilities":{"metrics.read":true}}`))
	if err != nil || p.Require("metrics.read") != nil {
		t.Fatal("explicit permission denied", err)
	}
	if p.Require("terminal") == nil {
		t.Fatal("unknown capability allowed")
	}
}

func TestInvalidPolicy(t *testing.T) {
	for _, input := range []string{``, `null`, `{}`, `{"version":2}`, `{"version":1,"extra":1}`, `{"Version":1}`, `{"version":1,"capabilities":{"terminal":true}}`, `{"version":1,"capabilities":{"metrics.read":"true"}}`, `{"version":1,"capabilities":null}`, `{"version":1,"capabilities":{"metrics.read":null}}`, `{"version":1,"version":1}`, `{"version":1,"capabilities":{"metrics.read":false,"metrics.read":true}}`, `{"version":1} {}`} {
		if _, err := parse([]byte(input)); err == nil {
			t.Fatalf("accepted %s", input)
		}
	}
	if _, err := Load("/nonexistent/kladit-policy.json"); err == nil {
		t.Fatal("missing file accepted")
	}
}
