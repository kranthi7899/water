package backend

import (
	"encoding/json"
	"testing"
)

// TestClaudeAttachmentScenario checks the exact wire shape used for the two
// visual inputs people use in the chat: a PDF deck and a screenshot. This is
// intentionally a protocol test, not a mock of what a model might say: it
// proves the content reaches Claude as document/image blocks.
func TestClaudeAttachmentScenario(t *testing.T) {
	line := streamJSONUserMessage(Request{
		Prompt: "write a brief from the deck and describe the screenshot",
		Attachments: []Attachment{
			{Name: "demo-script.pdf", Kind: "document", MediaType: "application/pdf", Data: []byte("%PDF-1.4")},
			{Name: "screen.png", Kind: "image", MediaType: "image/png", Data: []byte{0x89, 'P', 'N', 'G'}},
		},
	})
	var got struct {
		Message struct {
			Content []struct {
				Type   string         `json:"type"`
				Text   string         `json:"text"`
				Source map[string]any `json:"source"`
			} `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal([]byte(line), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Message.Content) != 3 || got.Message.Content[0].Text == "" {
		t.Fatalf("content = %+v", got.Message.Content)
	}
	for i, want := range []struct{ kind, media string }{{"document", "application/pdf"}, {"image", "image/png"}} {
		block := got.Message.Content[i+1]
		if block.Type != want.kind || block.Source["media_type"] != want.media || block.Source["type"] != "base64" {
			t.Fatalf("block %d = %+v, want %s/%s base64", i+1, block, want.kind, want.media)
		}
	}
}
