package brand

import (
	"strings"
	"testing"
)

func testSignature(withCTA bool) Signature {
	sig := Signature{
		Name:    "Alex Morgan",
		Title:   "CEO",
		Company: "Example Holdings",
		Signoff: "Best,",
		Links: []Link{
			{Label: "example.com", URL: "https://example.com"},
		},
	}
	if withCTA {
		sig.CTA = &Link{Label: "Book a time to talk", URL: "https://example.com/book"}
	}
	return sig
}

func countOccurrences(s, substr string) int {
	return strings.Count(s, substr)
}

func TestRenderEmailWithCTA(t *testing.T) {
	body := "<p>Hi Elena,</p><p>Thanks for flagging the issue.</p>"
	sig := testSignature(true)
	htmlOut, textOut, err := RenderEmail(body, sig)
	if err != nil {
		t.Fatalf("RenderEmail: %v", err)
	}

	for _, cid := range []string{"cid:water-header", "cid:water-koi", "cid:water-glass"} {
		if !strings.Contains(htmlOut, cid) {
			t.Errorf("html output missing %q", cid)
		}
	}
	if strings.Contains(textOut, "cid:") {
		t.Errorf("text output must not reference cid: images, got:\n%s", textOut)
	}

	disclosure := "Sent by Water, an AI assistant, on behalf of Alex Morgan."
	if n := countOccurrences(htmlOut, disclosure); n != 1 {
		t.Errorf("html disclosure appears %d times, want exactly 1:\n%s", n, htmlOut)
	}
	if n := countOccurrences(textOut, disclosure); n != 1 {
		t.Errorf("text disclosure appears %d times, want exactly 1:\n%s", n, textOut)
	}

	for _, want := range []string{sig.Name, sig.Title, sig.Company, sig.Signoff, sig.Links[0].Label, sig.CTA.Label} {
		if !strings.Contains(htmlOut, want) {
			t.Errorf("html output missing signature field %q", want)
		}
		if !strings.Contains(textOut, want) {
			t.Errorf("text output missing signature field %q", want)
		}
	}
	if !strings.Contains(htmlOut, sig.CTA.URL) {
		t.Errorf("html output missing CTA url %q", sig.CTA.URL)
	}

	if !strings.Contains(htmlOut, "Hi Elena,") || !strings.Contains(htmlOut, "Thanks for flagging the issue.") {
		t.Errorf("html output missing body content:\n%s", htmlOut)
	}
	if !strings.Contains(textOut, "Hi Elena,") || !strings.Contains(textOut, "Thanks for flagging the issue.") {
		t.Errorf("text output missing body content:\n%s", textOut)
	}
}

// TestRenderEmailNoCTAProducesNoCTAMarkup is the specific regression the
// brief calls out: a nil CTA must produce no CTA markup at all, never an
// empty button.
func TestRenderEmailNoCTAProducesNoCTAMarkup(t *testing.T) {
	body := "<p>No CTA here.</p>"
	sig := testSignature(false)
	if sig.CTA != nil {
		t.Fatal("test setup: sig.CTA should be nil")
	}
	htmlOut, textOut, err := RenderEmail(body, sig)
	if err != nil {
		t.Fatalf("RenderEmail: %v", err)
	}

	// The CTA label/url used by the "with CTA" case must never appear here.
	for _, absent := range []string{"Book a time to talk", "example.com/book"} {
		if strings.Contains(htmlOut, absent) {
			t.Errorf("html output contains CTA markup %q despite nil CTA:\n%s", absent, htmlOut)
		}
		if strings.Contains(textOut, absent) {
			t.Errorf("text output contains CTA markup %q despite nil CTA:\n%s", absent, textOut)
		}
	}

	disclosure := "Sent by Water, an AI assistant, on behalf of Alex Morgan."
	if n := countOccurrences(htmlOut, disclosure); n != 1 {
		t.Errorf("html disclosure appears %d times, want exactly 1", n)
	}
	if n := countOccurrences(textOut, disclosure); n != 1 {
		t.Errorf("text disclosure appears %d times, want exactly 1", n)
	}
}

func TestRenderEmailAltText(t *testing.T) {
	htmlOut, _, err := RenderEmail("<p>Body.</p>", testSignature(true))
	if err != nil {
		t.Fatalf("RenderEmail: %v", err)
	}
	if !strings.Contains(htmlOut, `alt="Water. Trust the flow."`) {
		t.Error("html output missing header alt text")
	}
	if !strings.Contains(htmlOut, `alt="Water"`) {
		t.Error("html output missing footer koi alt text")
	}
	if !strings.Contains(htmlOut, `bgcolor="#0B0B14"`) {
		t.Error("html output missing the ink bgcolor fallback on the glass-band cell")
	}
}

func TestRenderEmailBodyNotDoubleEscaped(t *testing.T) {
	// Body is caller-sanitized HTML; RenderEmail must not re-escape it into
	// visible &lt;p&gt; tags.
	htmlOut, _, err := RenderEmail("<p>Already <strong>sanitized</strong> HTML.</p>", testSignature(false))
	if err != nil {
		t.Fatalf("RenderEmail: %v", err)
	}
	if strings.Contains(htmlOut, "&lt;p&gt;") {
		t.Errorf("html output double-escaped the body:\n%s", htmlOut)
	}
	if !strings.Contains(htmlOut, "<strong>sanitized</strong>") {
		t.Errorf("html output missing expected inline markup:\n%s", htmlOut)
	}
}

func TestHtmlToText(t *testing.T) {
	in := `<p>Hi Elena,</p><p>See <a href="https://example.com/x">this link</a> and:</p><ul><li>one</li><li>two</li></ul><p>Thanks &amp; regards.</p>`
	out := htmlToText(in)
	for _, want := range []string{
		"Hi Elena,",
		"this link (https://example.com/x)",
		"- one",
		"- two",
		"Thanks & regards.",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("htmlToText output missing %q, got:\n%s", want, out)
		}
	}
	if strings.Contains(out, "<") || strings.Contains(out, ">") {
		t.Errorf("htmlToText left raw tag characters:\n%s", out)
	}
}
