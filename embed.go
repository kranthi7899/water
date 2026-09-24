// Package water is the module root. It exists to embed the twins/ and
// themes/ trees so the binary is self-contained. The `all:` prefix includes
// dot-files.
package water

import "embed"

//go:embed themes
var themesFS embed.FS

//go:embed all:twins
var twinsFS embed.FS

// TwinsFS returns the embedded twins tree, rooted at the repository root
// (open "twins/<id>/twin.yaml").
func TwinsFS() embed.FS { return twinsFS }

// ThemesFS returns the embedded themes tree, rooted at the repository root
// (open "themes/<name>.yaml").
func ThemesFS() embed.FS { return themesFS }
