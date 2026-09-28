package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"testing/fstest"

	"water"
	"water/internal/approvals"
	"water/internal/audit"
	"water/internal/backend"
	"water/internal/brand"
	"water/internal/connectors"
	"water/internal/gate"
	"water/internal/store"
	"water/internal/twins"
	"water/internal/vault"
)

// sigYAML is a minimal, valid twins/<id>/brand/signature.yaml body (see
// internal/brand.Signature's schema) with name as its only varying field,
// so two calls with different names are two different, both-valid
// signature configs.
func sigYAML(name string) string {
	return "name: " + name + "\ntitle: CEO\ncompany: Water\nsignoff: \"Best,\"\n"
}

// TestAddBrandPayloadFields_SignatureEditChangesPayloadHash is
// docs/slices/BRAND.md task 9's concrete proof for the signature config:
// the same starting payload, run through addBrandPayloadFields twice with
// only the loaded signature.yaml's content changed in between, ends up
// with two different signature_hash values and therefore two different
// approvals.PayloadHash results -- an already-proposed approval is voided
// by a later signature edit, by construction.
func TestAddBrandPayloadFields_SignatureEditChangesPayloadHash(t *testing.T) {
	basePayload := func() map[string]any {
		return map[string]any{"to": []string{"a@x.com"}, "subject": "s", "body": "b"}
	}

	fsysBefore := fstest.MapFS{"twins/ceo/brand/signature.yaml": {Data: []byte(sigYAML("Alex Morgan"))}}
	fsysAfter := fstest.MapFS{"twins/ceo/brand/signature.yaml": {Data: []byte(sigYAML("Jordan Lee"))}}

	before, err := addBrandPayloadFields(fsysBefore, "ceo", basePayload())
	if err != nil {
		t.Fatalf("before: %v", err)
	}
	after, err := addBrandPayloadFields(fsysAfter, "ceo", basePayload())
	if err != nil {
		t.Fatalf("after: %v", err)
	}
	if before["signature_hash"] == after["signature_hash"] || before["signature_hash"] == "" {
		t.Fatalf("signature_hash before=%v after=%v, want different non-empty values", before["signature_hash"], after["signature_hash"])
	}
	hashBefore, err := approvals.PayloadHash(before)
	if err != nil {
		t.Fatal(err)
	}
	hashAfter, err := approvals.PayloadHash(after)
	if err != nil {
		t.Fatal(err)
	}
	if hashBefore == hashAfter {
		t.Fatalf("PayloadHash unchanged (%s) across a signature edit, want it to differ", hashBefore)
	}

	// template_version and asset_hashes are present and identical (only the
	// signature changed), confirming the diff is attributable to exactly
	// the field that changed.
	if before["template_version"] != after["template_version"] {
		t.Fatalf("template_version changed unexpectedly: %v vs %v", before["template_version"], after["template_version"])
	}
}

// TestAddBrandPayloadFields_TemplateVersionEditChangesPayloadHash proves
// the same thing for template_version: two payloads that agree on
// everything else but disagree on template_version hash differently,
// exactly what a hand-bumped brand.TemplateVersion (after a template edit)
// would produce for an approval proposed under the old version.
func TestAddBrandPayloadFields_TemplateVersionEditChangesPayloadHash(t *testing.T) {
	fsys := fstest.MapFS{"twins/ceo/brand/signature.yaml": {Data: []byte(sigYAML("Alex Morgan"))}}
	payload, err := addBrandPayloadFields(fsys, "ceo", map[string]any{"to": []string{"a@x.com"}, "subject": "s", "body": "b"})
	if err != nil {
		t.Fatal(err)
	}
	older := map[string]any{}
	for k, v := range payload {
		older[k] = v
	}
	older["template_version"] = "v0-before-a-template-edit"

	hashOlder, err := approvals.PayloadHash(older)
	if err != nil {
		t.Fatal(err)
	}
	hashCurrent, err := approvals.PayloadHash(payload)
	if err != nil {
		t.Fatal(err)
	}
	if hashOlder == hashCurrent {
		t.Fatalf("PayloadHash unchanged across a template_version edit, want it to differ")
	}
}

// TestAddBrandPayloadFields_AssetEditChangesPayloadHash proves the same
// thing for asset_hashes: changing one of the three embedded assets'
// recorded hash (what internal/brand.AssetHash would return after the
// asset's bytes changed) changes PayloadHash, covering docs/slices/BRAND.md
// task 9's third case.
func TestAddBrandPayloadFields_AssetEditChangesPayloadHash(t *testing.T) {
	fsys := fstest.MapFS{"twins/ceo/brand/signature.yaml": {Data: []byte(sigYAML("Alex Morgan"))}}
	payload, err := addBrandPayloadFields(fsys, "ceo", map[string]any{"to": []string{"a@x.com"}, "subject": "s", "body": "b"})
	if err != nil {
		t.Fatal(err)
	}
	assets, ok := payload["asset_hashes"].(string)
	if !ok || assets == "" {
		t.Fatalf("asset_hashes = %#v, want a non-empty combined hash string", payload["asset_hashes"])
	}
	edited := map[string]any{}
	for k, v := range payload {
		edited[k] = v
	}
	edited["asset_hashes"] = "0000000000000000000000000000000000000000000000000000000000000000"

	hashOriginal, err := approvals.PayloadHash(payload)
	if err != nil {
		t.Fatal(err)
	}
	hashEdited, err := approvals.PayloadHash(edited)
	if err != nil {
		t.Fatal(err)
	}
	if hashOriginal == hashEdited {
		t.Fatalf("PayloadHash unchanged across an asset_hashes edit, want it to differ")
	}
}

// TestAddBrandPayloadFields_NoSignatureForTwinIsANoOp: a twin with no
// twins/<id>/brand/signature.yaml (the fstest.MapFS is empty, mirroring the
// demo twin or a synthetic test manifest) leaves the payload untouched
// rather than refusing the proposal.
func TestAddBrandPayloadFields_NoSignatureForTwinIsANoOp(t *testing.T) {
	payload := map[string]any{"to": []string{"a@x.com"}, "subject": "s", "body": "b"}
	got, err := addBrandPayloadFields(fstest.MapFS{}, "ceo-demo", payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := got["template_version"]; ok {
		t.Fatalf("payload = %#v, want no brand fields added for a twin with no signature.yaml", got)
	}
}

// TestAddBrandPayloadFields_MalformedSignatureFailsLoudly: a
// signature.yaml that exists but fails brand.LoadSignature's schema (e.g. a
// missing required field) refuses the whole call rather than silently
// omitting the brand fields -- the "no file at all" case above is the only
// one addBrandPayloadFields treats as a no-op.
func TestAddBrandPayloadFields_MalformedSignatureFailsLoudly(t *testing.T) {
	fsys := fstest.MapFS{"twins/ceo/brand/signature.yaml": {Data: []byte("name: \"\"\n")}}
	if _, err := addBrandPayloadFields(fsys, "ceo", map[string]any{}); err == nil {
		t.Fatal("want an error for a signature.yaml that fails schema validation, got nil")
	}
}

// ceoDraftManifest mirrors draftTestManifest (artifact_test.go) but under
// the real twin id ("ceo"), so a test through the real HTTP draft-submit
// path exercises addBrandPayloadFieldsForTwin against the real embedded
// twins/ceo/brand/signature.yaml -- not just the fstest.MapFS unit tests
// above.
const ceoDraftManifest = `
id: ceo
name: CEO twin
usage: {window: 1h, model_calls: 50, auto_model_calls: 10}
connectors:
  - name: gmail
    functions:
      - {name: list_messages, level: R}
      - {name: draft_message, level: D}
      - {name: draft_for_review, level: D}
      - {name: send_message, level: A}
`

// newCEODraftHarness is newDraftHarness (artifact_test.go) with
// ceoDraftManifest in place of draftTestManifest.
func newCEODraftHarness(t *testing.T) *harness {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "water.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	log, err := audit.Open(filepath.Join(dir, "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { log.Close() })
	q := approvals.NewQueue(st, log)
	gm := &fakeGmail{}
	reg, err := connectors.NewRegistry(gm)
	if err != nil {
		t.Fatal(err)
	}
	m, err := twins.Parse([]byte(ceoDraftManifest))
	if err != nil {
		t.Fatal(err)
	}
	g, err := gate.New(gate.Config{Manifest: m, Registry: reg, Approvals: q, Audit: log, Vault: vault.NewMemory(), Store: st})
	if err != nil {
		t.Fatal(err)
	}
	fb := backend.NewFake("fake")
	clients, err := LoadClients(filepath.Join(dir, "clients.json"))
	if err != nil {
		t.Fatal(err)
	}
	tok, err := clients.EnsureCLI()
	if err != nil {
		t.Fatal(err)
	}
	d := New(Config{Manifest: m, Store: st, Audit: log, Approvals: q, Gate: g, Registry: reg, Backend: fb, Clients: clients,
		SocketPath: "unused-in-http-tests.sock", Nervous: testNervous(t, m, st)})
	srv := httptest.NewServer(d.Mux())
	t.Cleanup(srv.Close)
	return &harness{d: d, srv: srv, token: tok, st: st, log: log, q: q, fake: fb, dir: dir}
}

// TestSubmitDraftForRealCEOTwinCarriesBrandFields is the end-to-end wiring
// check: submitting a gmail.send_message draft under the real "ceo" twin id
// (not a synthetic test manifest) produces an envelope whose payload
// carries template_version/signature_hash/asset_hashes matching
// internal/brand's own values for the real, committed
// twins/ceo/brand/signature.yaml and the three embedded assets.
func TestSubmitDraftForRealCEOTwinCarriesBrandFields(t *testing.T) {
	h := newCEODraftHarness(t)
	ctx := context.Background()
	d, err := h.st.CreateDraft(ctx, store.Draft{Template: "reply", To: "a@x.com", Subject: "s", Body: "b"})
	if err != nil {
		t.Fatal(err)
	}
	resp := h.post(t, "/v1/drafts/"+d.ID+"/submit", `{"to":"a@x.com","subject":"s","body":"b"}`, h.token)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var out struct {
		Envelope struct {
			Payload map[string]any `json:"payload"`
		} `json:"envelope"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	pl := out.Envelope.Payload

	wantSig, err := brand.LoadSignature(water.TwinsFS(), "twins/ceo/brand/signature.yaml")
	if err != nil {
		t.Fatal(err)
	}
	wantSigHash, err := wantSig.Hash()
	if err != nil {
		t.Fatal(err)
	}
	if pl["template_version"] != brand.TemplateVersion {
		t.Fatalf("template_version = %v, want %v", pl["template_version"], brand.TemplateVersion)
	}
	if pl["signature_hash"] != wantSigHash {
		t.Fatalf("signature_hash = %v, want %v", pl["signature_hash"], wantSigHash)
	}
	wantAssetHash, err := brand.CombinedAssetHash()
	if err != nil {
		t.Fatal(err)
	}
	if pl["asset_hashes"] != wantAssetHash {
		t.Fatalf("asset_hashes = %v, want %v", pl["asset_hashes"], wantAssetHash)
	}
}

// TestApprovedGmailSendWithBrandFieldsActuallyExecutes is a real bug found
// and fixed during review (2026-09-28), not a hypothetical: every test
// above proves the brand payload fields reach a *proposed* envelope, but
// none of them ever approved and executed one, so nothing caught that the
// gate's Schema.Validate (internal/connectors/schema.go) refuses any key a
// function's schema doesn't declare. The owner's own real test send hit
// this live: the approval read back correctly (showing asset_hashes/
// signature_hash/template_version, proving the payload extension worked),
// but deciding "yes" came back "denied by gate: gmail.send_message:
// unexpected argument \"asset_hashes\"" -- the email never sent. A first
// fix attempt (stripping the three keys before execution) made things
// worse in a subtler way: it broke Claim's "an approval covers exactly
// what you saw" re-hash check, since the executed args then hashed
// differently from the approved envelope ("payload changed after
// approval"). The real fix is gmail.go's sendMessageSchema, which declares
// all three as real (if functionally unused) string arguments, so the
// exact payload that was hashed at Propose time reaches Schema.Validate
// and Claim unmodified at execute time. This test
// proves the full propose -> approve -> execute path now actually works,
// not just that the payload gets built correctly.
func TestApprovedGmailSendWithBrandFieldsActuallyExecutes(t *testing.T) {
	h := newCEODraftHarness(t)
	ctx := context.Background()
	d, err := h.st.CreateDraft(ctx, store.Draft{Template: "reply", To: "a@x.com", Subject: "s", Body: "b"})
	if err != nil {
		t.Fatal(err)
	}
	resp := h.post(t, "/v1/drafts/"+d.ID+"/submit", `{"to":"a@x.com","subject":"s","body":"b"}`, h.token)
	var proposed struct {
		Envelope struct {
			ID          string `json:"id"`
			PayloadHash string `json:"payload_hash"`
			Payload     struct {
				AssetHashes string `json:"asset_hashes"`
			} `json:"payload"`
		} `json:"envelope"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&proposed); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if proposed.Envelope.Payload.AssetHashes == "" {
		t.Fatalf("proposed envelope missing asset_hashes, got %+v -- test setup didn't reproduce the real condition", proposed.Envelope.Payload)
	}

	decideBody, err := json.Marshal(map[string]string{"payload_hash": proposed.Envelope.PayloadHash, "reply": "yes"})
	if err != nil {
		t.Fatal(err)
	}
	dResp := h.post(t, "/v1/approvals/"+proposed.Envelope.ID+"/decision", string(decideBody), h.token)
	defer dResp.Body.Close()
	var decided struct {
		Executed bool   `json:"executed"`
		Error    string `json:"error"`
		Envelope struct {
			Status string `json:"status"`
			Reason string `json:"reason"`
		} `json:"envelope"`
	}
	if err := json.NewDecoder(dResp.Body).Decode(&decided); err != nil {
		t.Fatal(err)
	}
	if !decided.Executed || decided.Envelope.Status != "executed" {
		t.Fatalf("approved gmail.send_message with brand payload fields was not executed: status=%q reason=%q error=%q",
			decided.Envelope.Status, decided.Envelope.Reason, decided.Error)
	}
}
