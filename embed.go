// Package water is the module root. It exists to embed the agents/ tree so that
// adding a role is adding a folder — never a code change. The `all:` prefix
// includes dot-files such as .index.json. Themes are embedded the same way:
// adding a fifth role's look is adding themes/<slug>.yaml.
package water

import "embed"

//go:embed all:agents
var agentsFS embed.FS

//go:embed themes
var themesFS embed.FS

//go:embed all:twins
var twinsFS embed.FS

// TwinsFS returns the embedded twins tree, rooted at the repository root
// (open "twins/<id>/twin.yaml").
func TwinsFS() embed.FS { return twinsFS }

// AgentsFS returns the embedded persona tree. The returned FS is rooted at the
// repository root; callers should fs.Sub(AgentsFS(), "agents").
func AgentsFS() embed.FS { return agentsFS }

// ThemesFS returns the embedded themes tree, rooted at the repository root
// (open "themes/<name>.yaml").
func ThemesFS() embed.FS { return themesFS }
