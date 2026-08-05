package server

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// TestNewNodeFormDefaultsToRealFullDuplexRepeaterMode is the direct
// regression test for a real incident: the setup wizard's own default
// Duplex value was "1", which -- verified directly against
// AllStarLink's own rpt.conf documentation -- is actually half-duplex
// simplex with no repeated audio at all, silently handing every new
// node created via this wizard the wrong mode while labeling it as if
// it were the normal full-duplex repeater setup, unless the operator
// happened to change the dropdown themselves.
func TestNewNodeFormDefaultsToRealFullDuplexRepeaterMode(t *testing.T) {
	s := newTemplateTestServer(t)
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "http://example.com/nodes/new", nil)
	s.handleNodeNewForm(w, r)

	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, `value="2" selected`) {
		t.Errorf("duplex=2 (the real full-duplex normal repeater mode) is not the selected default, body:\n%s", body)
	}
	if strings.Contains(body, `value="1" selected`) {
		t.Error("duplex=1 (real half-duplex/no-repeat mode) must not be the default -- it was the source of the bug this test guards")
	}
}

// TestDuplexOptionsMatchVerifiedAppRptMeaning pins every duplex
// value/label pair on the node edit page against the meaning verified
// directly from AllStarLink's own rpt.conf documentation. Direct
// regression coverage for a real incident: the previous mapping wrote
// duplex=4 (full duplex, repeats audio except during autopatch) for the
// option labeled "Simplex ... with status tones", making a node
// selected for simplex-only operation instead transmit repeated audio
// back out while the operator was still transmitting.
func TestDuplexOptionsMatchVerifiedAppRptMeaning(t *testing.T) {
	s := newTemplateTestServer(t)
	cases := []struct {
		duplex string
		label  string
	}{
		{"0", "Simplex — one frequency, no status tones"},
		{"1", "Simplex — one frequency, with status tones"},
		{"2", "Full repeater — normal setup, with status tones"},
		{"3", "Full repeater — with status tones, but doesn't repeat audio"},
		{"4", "Full repeater — normal setup, but no repeat during autopatch calls"},
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "http://example.com/nodes/new", nil)
	s.handleNodeNewForm(w, r)
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	body := w.Body.String()
	for _, c := range cases {
		want := `value="` + c.duplex + `"`
		idx := strings.Index(body, want)
		if idx == -1 {
			t.Errorf("duplex=%s option not found at all", c.duplex)
			continue
		}
		// The label immediately follows this exact option tag, before the
		// next "</option>" -- confirms the value and label weren't
		// separated/reordered relative to each other.
		snippet := body[idx:]
		end := strings.Index(snippet, "</option>")
		if end == -1 || !strings.Contains(snippet[:end], c.label) {
			t.Errorf("duplex=%s does not render with label %q, got option snippet: %q", c.duplex, c.label, snippet[:min(end+1, len(snippet))])
		}
	}
}
