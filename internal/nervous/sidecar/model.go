// Package sidecar supervises the Tier 1 FunctionGemma llama-server process,
// pulls its pinned model file, and tracks the eval-gate record that must pass
// before Tier 1 is allowed to answer. See docs/functiongemma.md and
// docs/slices/R.md §10.
package sidecar

import "fmt"

// FunctionGemma GGUF pin.
//
// Verified live against the Hugging Face API on 2026-09-24 (task R-17):
//
//	GET https://huggingface.co/api/models/ggml-org/functiongemma-270m-it-GGUF?blobs=true
//
// ggml-org is Hugging Face's own llama.cpp/GGUF-conversion org — the
// "official-or-ggml-org" conversion docs/slices/R.md's Risks §11 asks for,
// converted straight from google/functiongemma-270m-it. The repo publishes
// exactly two quantizations: bf16 (~543 MB, essentially unquantized) and
// q8_0 (~292 MB). Q8_0 is picked: it is the only genuinely CPU-friendly,
// quantized option the repo offers (there is no Q4/Q5/Q6 build here), and at
// 8-bit it is effectively lossless for a 270M-parameter model, which matters
// more than shaving another ~150 MB off a model this small.
//
// ModelSHA256 is the LFS object's sha256 from the API's blobs listing,
// cross-checked against the `X-Linked-ETag` header on the file's `resolve`
// redirect (both agreed). ModelRevision is the repo's HEAD commit ("sha"
// field in the model API response) at verification time, pinned so a later
// push to the repo can't silently change what gets downloaded.
const (
	// ModelRepo is the Hugging Face repo id hosting the GGUF conversion.
	ModelRepo = "ggml-org/functiongemma-270m-it-GGUF"
	// ModelFile is the exact filename within ModelRepo to download.
	ModelFile = "functiongemma-270m-it-q8_0.gguf"
	// ModelRevision pins the repo commit the file is fetched from.
	ModelRevision = "2566ce14aedfc14fdd0de955ba67346425e67126"
	// ModelSHA256 is the expected sha256 of ModelFile, verified before the
	// downloaded file is ever renamed into place.
	ModelSHA256 = "83940d4dd9676710856f43523bed096164a595a96f6b34771610a03937de5270"
	// ModelSizeBytes is the file's size in bytes, from the same API listing.
	ModelSizeBytes = 291557792

	// ModelFileName is the name the file is stored under once installed
	// (renamed from the ".part" name after sha256 verification succeeds).
	ModelFileName = "functiongemma.gguf"
	// ModelPartFileName is the name used for the in-progress download.
	ModelPartFileName = ModelFileName + ".part"
)

// ModelDownloadURL is the pinned, revision-locked download URL for the
// FunctionGemma GGUF file.
func ModelDownloadURL() string {
	return fmt.Sprintf("https://huggingface.co/%s/resolve/%s/%s", ModelRepo, ModelRevision, ModelFile)
}
