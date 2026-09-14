package voice

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"testing"
)

func TestOSDegradesWithClearMessage(t *testing.T) {
	o := &OS{Look: func(string) (string, error) { return "", errors.New("nope") }}
	o.detect()
	if o.Available() {
		t.Fatal("no binary should mean unavailable")
	}
	err := o.Speak(context.Background(), "hello")
	if err == nil || !strings.Contains(err.Error(), "unavailable") && !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("absence message unclear: %v", err)
	}
	if _, err := o.Listen(context.Background()); !errors.Is(err, ErrListenUnavailable) {
		t.Fatal("listen must be a documented no-op")
	}
	if runtime.GOOS == "darwin" {
		real := NewOS()
		if !real.Available() {
			t.Skip("say not found")
		}
		if real.Name() != "os" {
			t.Fatal("name")
		}
	}
}
