package gateway

import (
	"errors"
	"io/fs"

	"water"
	"water/internal/brand"
)

// addBrandPayloadFields adds template_version, signature_hash and
// asset_hashes to payload before a gmail.send_message envelope is proposed
// (docs/slices/BRAND.md task 9, the shared foundation's item 3: "the
// approved payload hash covers the body, the template version, the
// signature config and the asset hashes"). approvals.PayloadHash
// (internal/approvals/queue.go) already hashes whatever map it's given, so
// putting these three keys into the payload here -- and nowhere else -- is
// the entire mechanism: editing twins/<twinID>/brand/signature.yaml,
// bumping brand.TemplateVersion, or changing one of the three embedded
// assets changes one of these values, which changes PayloadHash, which
// voids any approval already proposed with the old value, by construction.
//
// These three keys are real, if functionally unused, gmail.send_message
// arguments now (gmail.go's sendMessageSchema), not stripped before
// execution -- found live (2026-09-28) that stripping them broke a more
// fundamental invariant: the gate re-hashes the exact args a claimed
// envelope executes with and refuses unless that equals the envelope's own
// PayloadHash (Claim's "an approval covers exactly what you saw" check), so
// whatever gets hashed at Propose time must reach the connector completely
// unmodified at execute time. asset_hashes is brand.CombinedAssetHash(), a
// single string, not a per-asset map, because connectors.Property has no
// "object" type to declare a map with (internal/connectors/schema.go).
//
// fsys is the embedded tree to load twins/<twinID>/brand/signature.yaml
// from -- every real call site passes water.TwinsFS() (see
// addBrandPayloadFieldsForTwin below); it is a parameter, rather than
// addBrandPayloadFields reaching for water.TwinsFS() itself, purely so a
// test can pass an in-memory fstest.MapFS and prove that two different
// signature.yaml contents produce two different signature_hash values
// (and therefore two different approvals.PayloadHash results) without
// needing two different compiled binaries. twinID selects which twin's
// file to load (twins/<twinID>/brand/signature.yaml, the same
// embedded-tree-plus-path convention internal/cli/twin.go's
// loadTwinManifest/internal/brand.LoadSignature use). It mutates and
// returns the same map (a nil payload is treated as empty, matching
// approvals.PayloadHash's own nil handling) so call sites can wire this
// directly into the Propose call:
//
//	payload, err := addBrandPayloadFieldsForTwin(d.cfg.Manifest.ID, payload)
//	if err != nil { ... }
//	env, err := d.cfg.Approvals.Propose(ctx, approvals.Envelope{Payload: payload, ...})
//
// A twin with no twins/<twinID>/brand/signature.yaml at all (the demo twin,
// a --twin-selected one such as Slice E's counterparty, or a synthetic test
// manifest with no embedded twins/<id> directory) has no brand config to
// cover yet -- addBrandPayloadFields leaves payload untouched and returns a
// nil error for that specific case, rather than refusing every
// gmail.send_message proposal for a twin that was never given brand
// assets. Any other failure (malformed YAML, a schema violation, a hashing
// error) refuses the whole proposal instead: a load or hash failure once a
// twin does have a signature.yaml never falls back to a silent zero-value
// signature_hash, matching brand.LoadSignature's own fail-loudly contract.
func addBrandPayloadFields(fsys fs.FS, twinID string, payload map[string]any) (map[string]any, error) {
	if payload == nil {
		payload = map[string]any{}
	}
	sig, err := brand.LoadSignature(fsys, "twins/"+twinID+"/brand/signature.yaml")
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return payload, nil
		}
		return nil, err
	}
	sigHash, err := sig.Hash()
	if err != nil {
		return nil, err
	}
	assetHash, err := brand.CombinedAssetHash()
	if err != nil {
		return nil, err
	}
	payload["template_version"] = brand.TemplateVersion
	payload["signature_hash"] = sigHash
	payload["asset_hashes"] = assetHash
	return payload, nil
}

// addBrandPayloadFieldsForTwin is addBrandPayloadFields over the real
// embedded tree (water.TwinsFS()) -- the one every non-test call site in
// this package uses.
func addBrandPayloadFieldsForTwin(twinID string, payload map[string]any) (map[string]any, error) {
	return addBrandPayloadFields(water.TwinsFS(), twinID, payload)
}
