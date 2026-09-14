package guards_test

import (
	"fmt"
	"testing/fstest"
)

// tree builds an in-memory agents fs with the given roles. The first role is
// the singleton orchestrator unless overridden via opts.
type roleSpec struct {
	slug         string
	singleton    bool
	orchestrator bool
	soul         string
}

func tree(specs ...roleSpec) fstest.MapFS {
	m := fstest.MapFS{}
	for _, s := range specs {
		m[s.slug+"/role.yaml"] = &fstest.MapFile{Data: []byte(fmt.Sprintf(
			"schema: 1\nname: %q\nslug: %q\nsingleton: %v\norchestrator: %v\n", s.slug, s.slug, s.singleton, s.orchestrator))}
		soul := s.soul
		if soul == "" {
			soul = "I am " + s.slug + "."
		}
		m[s.slug+"/soul.md"] = &fstest.MapFile{Data: []byte("---\nschema: 1\n---\n" + soul + "\n")}
		m[s.slug+"/experience.md"] = &fstest.MapFile{Data: []byte("---\nschema: 1\nstatus: unwritten\n---\n<!-- blank -->\n")}
		m[s.slug+"/memory/session.md"] = &fstest.MapFile{Data: []byte("---\nschema: 1\n---\n")}
	}
	return m
}

func standardTree() fstest.MapFS {
	return tree(
		roleSpec{slug: "ceo", singleton: true, orchestrator: true},
		roleSpec{slug: "cfo"},
		roleSpec{slug: "cto"},
		roleSpec{slug: "fourth"},
	)
}
