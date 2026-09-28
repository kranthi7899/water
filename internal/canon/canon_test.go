package canon

import "testing"

func TestKeyOrderDoesNotChangeHash(t *testing.T) {
	a, _ := Hash(map[string]any{"b": 1, "a": map[string]any{"y": "x", "x": []any{1, "z"}}})
	b, _ := Hash(map[string]any{"a": map[string]any{"x": []any{1, "z"}, "y": "x"}, "b": 1})
	if a != b {
		t.Fatal("key order changed the hash")
	}
	c, _ := Hash(map[string]any{"a": map[string]any{"x": []any{1, "z"}, "y": "x "}, "b": 1})
	if a == c {
		t.Fatal("a one-character edit kept the hash")
	}
	got, _ := JSON(struct {
		Z int    `json:"z"`
		A string `json:"a"`
	}{1, "q"})
	if string(got) != `{"a":"q","z":1}` {
		t.Fatalf("struct fields not sorted: %s", got)
	}
}
