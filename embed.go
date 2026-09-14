// Package water is the module root. It exists to embed the agents/ tree so that
// adding a role is adding a folder — never a code change. The `all:` prefix
// includes dot-files such as .index.json and .gitkeep.
package water

import "embed"

//go:embed all:agents
var agentsFS embed.FS

// AgentsFS returns the embedded persona tree. The returned FS is rooted at the
// repository root; callers should fs.Sub(AgentsFS(), "agents").
func AgentsFS() embed.FS { return agentsFS }
