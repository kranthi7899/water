// Package water is the module root. It exists to embed the twins/ tree so
// the binary is self-contained. The `all:` prefix includes dot-files.
package water

import "embed"

//go:embed all:twins
var twinsFS embed.FS

// TwinsFS returns the embedded twins tree, rooted at the repository root
// (open "twins/<id>/twin.yaml").
func TwinsFS() embed.FS { return twinsFS }
