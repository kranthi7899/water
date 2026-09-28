package brand

import (
	"strings"
	"testing"
)

func TestCSSCustomPropertiesRoundTrip(t *testing.T) {
	css := CSSCustomProperties()
	for _, tok := range Tokens {
		want := "--" + tok.Name + ": " + tok.Hex + ";"
		if !strings.Contains(css, want) {
			t.Errorf("CSSCustomProperties() missing %q\ngot:\n%s", want, css)
		}
	}
	// The dozen tokens documents.md §2 names, exactly.
	want := map[string]string{
		"ink": "#0B0B14", "paper": "#F6F7FA", "white": "#FFFFFF",
		"blue-sky": "#6CCBFF", "blue": "#3B94F2", "blue-deep": "#2D5FB8",
		"navy": "#173561", "teal": "#1BA79A", "violet": "#6D5BD0",
		"amber": "#E0A13A", "coral": "#E2634E", "slate": "#6B7280",
	}
	if len(Tokens) != len(want) {
		t.Fatalf("Tokens has %d entries, want %d", len(Tokens), len(want))
	}
	for _, tok := range Tokens {
		hex, ok := want[tok.Name]
		if !ok {
			t.Errorf("unexpected token %q", tok.Name)
			continue
		}
		if tok.Hex != hex {
			t.Errorf("token %q hex = %q, want %q", tok.Name, tok.Hex, hex)
		}
		if got, ok := Hex(tok.Name); !ok || got != hex {
			t.Errorf("Hex(%q) = %q, %v, want %q, true", tok.Name, got, ok, hex)
		}
	}
}

func TestHexUnknownToken(t *testing.T) {
	if _, ok := Hex("not-a-token"); ok {
		t.Fatal("Hex(unknown) reported ok=true")
	}
}

func TestAssetHashStableAndNonEmpty(t *testing.T) {
	for _, name := range []string{AssetHeader, AssetKoi, AssetGlass} {
		h1, err := AssetHash(name)
		if err != nil {
			t.Fatalf("AssetHash(%q): %v", name, err)
		}
		if h1 == "" {
			t.Fatalf("AssetHash(%q) is empty", name)
		}
		h2, err := AssetHash(name)
		if err != nil {
			t.Fatalf("AssetHash(%q) second call: %v", name, err)
		}
		if h1 != h2 {
			t.Fatalf("AssetHash(%q) not stable: %q != %q", name, h1, h2)
		}
	}
	// Different assets must not collide.
	hHeader, _ := AssetHash(AssetHeader)
	hKoi, _ := AssetHash(AssetKoi)
	hGlass, _ := AssetHash(AssetGlass)
	if hHeader == hKoi || hHeader == hGlass || hKoi == hGlass {
		t.Fatalf("distinct assets hashed equal: header=%s koi=%s glass=%s", hHeader, hKoi, hGlass)
	}
}

func TestAssetHashUnknownName(t *testing.T) {
	if _, err := AssetHash("not-a-real-asset"); err == nil {
		t.Fatal("AssetHash(unknown) returned nil error")
	}
}

func TestAssetBytesUnknownName(t *testing.T) {
	if _, err := AssetBytes("not-a-real-asset"); err == nil {
		t.Fatal("AssetBytes(unknown) returned nil error")
	}
}

func TestTemplateVersionSet(t *testing.T) {
	if TemplateVersion == "" {
		t.Fatal("TemplateVersion is empty")
	}
}
