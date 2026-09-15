package voice

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
)

func TestOpenAIRequiresExplicitMeteredOptIn(t *testing.T) {
	v := NewOpenAI(OpenAIOptions{APIKey: "test", Role: "ceo", Player: func(context.Context, string) error { return nil }})
	if v.Available() {
		t.Fatal("metered voice was available without explicit opt-in")
	}
	if err := v.Speak(context.Background(), "hello"); err == nil || !strings.Contains(err.Error(), "allow_metered") {
		t.Fatalf("opt-in error = %v", err)
	}
}

func TestOpenAISendsRoleProfileAndPlaysReturnedAudio(t *testing.T) {
	var got map[string]any
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("authorization = %q", r.Header.Get("Authorization"))
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Error(err)
		}
		return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Header: make(http.Header), Body: io.NopCloser(bytes.NewReader([]byte("fake-mp3")))}, nil
	})}
	var played []byte
	v := NewOpenAI(OpenAIOptions{
		APIKey: "test-key", Role: "design", AllowMetered: true, Endpoint: "https://voice.test/v1/audio/speech", HTTPClient: client,
		Player: func(_ context.Context, path string) error { b, err := os.ReadFile(path); played = b; return err },
	})
	if err := v.Speak(context.Background(), "A completed reply."); err != nil {
		t.Fatal(err)
	}
	if got["voice"] != "coral" || got["model"] != "gpt-4o-mini-tts" || got["input"] != "A completed reply." {
		t.Fatalf("speech request = %#v", got)
	}
	if instructions, _ := got["instructions"].(string); !strings.Contains(instructions, "Warm") {
		t.Fatalf("design instructions = %q", instructions)
	}
	if string(played) != "fake-mp3" {
		t.Fatalf("played audio = %q", played)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestSpeechChunksPreserveAllText(t *testing.T) {
	text := strings.Repeat("a", 4090) + ". " + strings.Repeat("b", 32)
	chunks := speechChunks(text, 4096)
	if len(chunks) != 2 || strings.Join(chunks, " ") != text {
		t.Fatalf("chunks lost text: %#v", chunks)
	}
	for _, c := range chunks {
		if len([]rune(c)) > 4096 {
			t.Fatalf("oversize chunk: %d", len([]rune(c)))
		}
	}
}
