// Package roster loads twins/<id>/seed/people.yaml — the people roster,
// the source of truth for who works on what — into the store as normalized
// records (Person, Team, Project, Client, Vendor, OrgContact) connected by
// typed links (member_of, leads, allocated_to with a fraction, owns_client;
// internal/store/links.go). Loading is idempotent: it runs once at daemon
// startup (buildTwinDepsFS), safe to re-run on every restart, and a twin
// with no seed/people.yaml simply has no roster — this is additive data,
// never required for a twin to load.
//
// Connectors never define people themselves; they resolve to these
// records. Hours invested are always derived at query time (HoursInvested)
// from an allocation fraction and a project's start date — never stored,
// since a stored number would silently go stale the moment a week passes.
package roster

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"time"

	"gopkg.in/yaml.v3"

	"water/internal/store"
)

const dateLayout = "2006-01-02"

// --- Raw YAML shape (yamlDoc mirrors the file exactly; Parse validates and
// cross-references it before Load ever writes to the store) ---

type yamlDoc struct {
	Company  yamlCompany   `yaml:"company"`
	Teams    []yamlTeam    `yaml:"teams"`
	Projects []yamlProject `yaml:"projects"`
	People   []yamlPerson  `yaml:"people"`
	External yamlExternal  `yaml:"external"`
}

type yamlCompany struct {
	Name                 string `yaml:"name"`
	Headcount            int    `yaml:"headcount"`
	BlendedHourlyCostUSD int    `yaml:"blended_hourly_cost_usd"`
}

type yamlTeam struct {
	ID        string `yaml:"id"`
	Name      string `yaml:"name"`
	LinearKey string `yaml:"linear_key"`
}

type yamlProject struct {
	ID     string `yaml:"id"`
	Name   string `yaml:"name"`
	Team   string `yaml:"team"`
	Linear string `yaml:"linear"`
	Start  string `yaml:"start"`
	Target string `yaml:"target"`
}

type yamlPerson struct {
	ID          string             `yaml:"id"`
	Name        string             `yaml:"name"`
	Role        string             `yaml:"role"`
	HomeTeam    string             `yaml:"home_team"`
	AlsoOnTeams []string           `yaml:"also_on_teams"`
	Leads       []string           `yaml:"leads"`
	Allocation  map[string]float64 `yaml:"allocation"`
	OwnsClients []string           `yaml:"owns_clients"`
	Identities  map[string]string  `yaml:"identities"`
}

type yamlExternal struct {
	Clients  []yamlClient  `yaml:"clients"`
	Vendors  []yamlVendor  `yaml:"vendors"`
	Contacts []yamlContact `yaml:"contacts"`
}

type yamlClient struct {
	ID              string `yaml:"id"`
	Name            string `yaml:"name"`
	Product         string `yaml:"product"`
	Owner           string `yaml:"owner"`
	MRRUSD          int64  `yaml:"mrr_usd"`
	PotentialMRRUSD int64  `yaml:"potential_mrr_usd"`
	Status          string `yaml:"status"`
	Renewal         string `yaml:"renewal"`
}

type yamlVendor struct {
	ID         string `yaml:"id"`
	Name       string `yaml:"name"`
	Product    string `yaml:"product"`
	MonthlyUSD int64  `yaml:"monthly_usd"`
	Renewal    string `yaml:"renewal"`
}

type yamlContact struct {
	ID      string `yaml:"id"`
	Name    string `yaml:"name"`
	Role    string `yaml:"role"`
	ForTeam string `yaml:"for_team"`
	Stage   string `yaml:"stage"`
}

// Roster is a fully parsed and cross-validated people.yaml, ready to write
// to a store via Write. Parse never touches a store; Load (Parse + Write)
// is what a daemon startup path calls.
type Roster struct {
	doc yamlDoc
}

// Parse strictly decodes and cross-validates b. Unknown keys are errors —
// a typo in an id, not just an unknown field, must never silently produce
// an empty roster or a dropped relationship. Every cross-reference (a
// project's team, a person's allocation target, an owns_clients entry, a
// client's owner) is checked against the ids actually defined in the same
// document.
func Parse(b []byte) (*Roster, error) {
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	var doc yamlDoc
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("roster: %w", err)
	}

	teamIDs := map[string]bool{}
	for _, t := range doc.Teams {
		if t.ID == "" || t.Name == "" {
			return nil, fmt.Errorf("roster: team with empty id or name")
		}
		if teamIDs[t.ID] {
			return nil, fmt.Errorf("roster: duplicate team id %q", t.ID)
		}
		teamIDs[t.ID] = true
	}

	projectIDs := map[string]bool{}
	for _, p := range doc.Projects {
		if p.ID == "" || p.Name == "" {
			return nil, fmt.Errorf("roster: project with empty id or name")
		}
		if projectIDs[p.ID] {
			return nil, fmt.Errorf("roster: duplicate project id %q", p.ID)
		}
		projectIDs[p.ID] = true
		if p.Team != "" && !teamIDs[p.Team] {
			return nil, fmt.Errorf("roster: project %q names unknown team %q", p.ID, p.Team)
		}
		if p.Start != "" {
			if _, err := time.Parse(dateLayout, p.Start); err != nil {
				return nil, fmt.Errorf("roster: project %q start: %w", p.ID, err)
			}
		}
		if p.Target != "" {
			if _, err := time.Parse(dateLayout, p.Target); err != nil {
				return nil, fmt.Errorf("roster: project %q target: %w", p.ID, err)
			}
		}
	}

	clientIDs := map[string]string{} // id -> owner, for the owns_clients cross-check below
	for _, c := range doc.External.Clients {
		if c.ID == "" || c.Name == "" {
			return nil, fmt.Errorf("roster: client with empty id or name")
		}
		if _, dup := clientIDs[c.ID]; dup {
			return nil, fmt.Errorf("roster: duplicate client id %q", c.ID)
		}
		clientIDs[c.ID] = c.Owner
		if c.Renewal != "" {
			if _, err := time.Parse(dateLayout, c.Renewal); err != nil {
				return nil, fmt.Errorf("roster: client %q renewal: %w", c.ID, err)
			}
		}
	}

	for _, v := range doc.External.Vendors {
		if v.ID == "" || v.Name == "" {
			return nil, fmt.Errorf("roster: vendor with empty id or name")
		}
		if v.Renewal != "" {
			if _, err := time.Parse(dateLayout, v.Renewal); err != nil {
				return nil, fmt.Errorf("roster: vendor %q renewal: %w", v.ID, err)
			}
		}
	}

	for _, c := range doc.External.Contacts {
		if c.ID == "" || c.Name == "" {
			return nil, fmt.Errorf("roster: contact with empty id or name")
		}
	}

	personIDs := map[string]bool{}
	for _, p := range doc.People {
		if p.ID == "" || p.Name == "" {
			return nil, fmt.Errorf("roster: person with empty id or name")
		}
		if personIDs[p.ID] {
			return nil, fmt.Errorf("roster: duplicate person id %q", p.ID)
		}
		personIDs[p.ID] = true
		if p.HomeTeam != "" && !teamIDs[p.HomeTeam] {
			return nil, fmt.Errorf("roster: person %q names unknown home_team %q", p.ID, p.HomeTeam)
		}
		for _, t := range p.AlsoOnTeams {
			if !teamIDs[t] {
				return nil, fmt.Errorf("roster: person %q names unknown also_on_teams team %q", p.ID, t)
			}
		}
		for _, target := range p.Leads {
			if !teamIDs[target] && !projectIDs[target] {
				return nil, fmt.Errorf("roster: person %q leads unknown team/project %q", p.ID, target)
			}
		}
		for proj := range p.Allocation {
			if !projectIDs[proj] {
				return nil, fmt.Errorf("roster: person %q is allocated to unknown project %q", p.ID, proj)
			}
		}
		for _, cl := range p.OwnsClients {
			owner, ok := clientIDs[cl]
			if !ok {
				return nil, fmt.Errorf("roster: person %q owns unknown client %q", p.ID, cl)
			}
			if owner != p.ID {
				return nil, fmt.Errorf("roster: person %q lists owns_clients %q, but that client's own owner is %q — the seed file disagrees with itself", p.ID, cl, owner)
			}
		}
	}
	for _, c := range doc.External.Clients {
		if c.Owner != "" && !personIDs[c.Owner] {
			return nil, fmt.Errorf("roster: client %q names unknown owner %q", c.ID, c.Owner)
		}
	}

	return &Roster{doc: doc}, nil
}

// Load reads twins/<id>/seed/people.yaml from fsys and writes it into st.
// A missing seed directory or file is not an error — the roster is
// additive, optional data, and most twins (ceo-demo, counterparty) have
// none. A present-but-malformed file fails loudly, exactly like a bad
// twin.yaml or decisions/*.yaml, since a startup that silently ran with a
// broken roster would be worse than one that refused to start.
func Load(ctx context.Context, fsys fs.FS, id string, st *store.Store) error {
	b, err := fs.ReadFile(fsys, path.Join("twins", id, "seed", "people.yaml"))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("roster: %w", err)
	}
	r, err := Parse(b)
	if err != nil {
		return err
	}
	return r.Write(ctx, st)
}

// Write upserts every record and link in r into st. Idempotent: safe to
// call on every daemon startup, since Upsert and AddLink both replace
// in place rather than duplicating.
func (r *Roster) Write(ctx context.Context, st *store.Store) error {
	for _, t := range r.doc.Teams {
		if err := st.Upsert(ctx, &store.Team{
			Meta: store.Meta{Source: "seed", SourceID: t.ID},
			Name: t.Name, LinearKey: t.LinearKey,
		}); err != nil {
			return fmt.Errorf("roster: team %q: %w", t.ID, err)
		}
	}

	for _, p := range r.doc.Projects {
		start, _ := time.Parse(dateLayout, p.Start)   // validated in Parse
		target, _ := time.Parse(dateLayout, p.Target) // validated in Parse
		if err := st.Upsert(ctx, &store.Project{
			Meta: store.Meta{Source: "seed", SourceID: p.ID},
			Name: p.Name, LinearProject: p.Linear, StartAt: start, TargetAt: target,
		}); err != nil {
			return fmt.Errorf("roster: project %q: %w", p.ID, err)
		}
		if p.Team != "" {
			if err := st.AddLink(ctx, store.Link{Kind: store.LinkMemberOf, FromType: "project", FromID: p.ID, ToType: "team", ToID: p.Team}); err != nil {
				return fmt.Errorf("roster: project %q member_of %q: %w", p.ID, p.Team, err)
			}
		}
	}

	for _, c := range r.doc.External.Clients {
		renewal, _ := time.Parse(dateLayout, c.Renewal) // validated in Parse; zero if absent
		if err := st.Upsert(ctx, &store.Client{
			Meta: store.Meta{Source: "seed", SourceID: c.ID},
			Name: c.Name, Product: c.Product,
			MRRMinor: c.MRRUSD * 100, PotentialMRRMinor: c.PotentialMRRUSD * 100,
			Status: c.Status, RenewalAt: renewal,
		}); err != nil {
			return fmt.Errorf("roster: client %q: %w", c.ID, err)
		}
		if c.Owner != "" {
			if err := st.AddLink(ctx, store.Link{Kind: store.LinkOwnsClient, FromType: "person", FromID: c.Owner, ToType: "client", ToID: c.ID}); err != nil {
				return fmt.Errorf("roster: client %q owns_client %q: %w", c.ID, c.Owner, err)
			}
		}
	}

	for _, v := range r.doc.External.Vendors {
		renewal, _ := time.Parse(dateLayout, v.Renewal)
		if err := st.Upsert(ctx, &store.Vendor{
			Meta: store.Meta{Source: "seed", SourceID: v.ID},
			Name: v.Name, Product: v.Product, MonthlyMinor: v.MonthlyUSD * 100, RenewalAt: renewal,
		}); err != nil {
			return fmt.Errorf("roster: vendor %q: %w", v.ID, err)
		}
	}

	for _, c := range r.doc.External.Contacts {
		if err := st.Upsert(ctx, &store.OrgContact{
			Meta: store.Meta{Source: "seed", SourceID: c.ID},
			Name: c.Name, Role: c.Role, ForTeam: c.ForTeam, Stage: c.Stage,
		}); err != nil {
			return fmt.Errorf("roster: contact %q: %w", c.ID, err)
		}
	}

	for _, p := range r.doc.People {
		idJSON, err := json.Marshal(p.Identities)
		if err != nil {
			return fmt.Errorf("roster: person %q identities: %w", p.ID, err)
		}
		if err := st.Upsert(ctx, &store.Person{
			Meta: store.Meta{Source: "seed", SourceID: p.ID},
			Name: p.Name, Role: p.Role, HomeTeam: p.HomeTeam, Identities: string(idJSON),
		}); err != nil {
			return fmt.Errorf("roster: person %q: %w", p.ID, err)
		}
		if p.HomeTeam != "" {
			if err := st.AddLink(ctx, store.Link{Kind: store.LinkMemberOf, FromType: "person", FromID: p.ID, ToType: "team", ToID: p.HomeTeam}); err != nil {
				return fmt.Errorf("roster: person %q member_of %q: %w", p.ID, p.HomeTeam, err)
			}
		}
		for _, t := range p.AlsoOnTeams {
			if err := st.AddLink(ctx, store.Link{Kind: store.LinkMemberOf, FromType: "person", FromID: p.ID, ToType: "team", ToID: t}); err != nil {
				return fmt.Errorf("roster: person %q member_of %q: %w", p.ID, t, err)
			}
		}
		for _, target := range p.Leads {
			toType := "team"
			if _, isProject := findProject(r.doc.Projects, target); isProject {
				toType = "project"
			}
			if err := st.AddLink(ctx, store.Link{Kind: store.LinkLeads, FromType: "person", FromID: p.ID, ToType: toType, ToID: target}); err != nil {
				return fmt.Errorf("roster: person %q leads %q: %w", p.ID, target, err)
			}
		}
		for proj, fraction := range p.Allocation {
			if err := st.AddLink(ctx, store.Link{Kind: store.LinkAllocated, FromType: "person", FromID: p.ID, ToType: "project", ToID: proj, Fraction: fraction}); err != nil {
				return fmt.Errorf("roster: person %q allocated_to %q: %w", p.ID, proj, err)
			}
		}
		// owns_client links are written once, from the client's own owner
		// field above — p.OwnsClients was already cross-checked in Parse to
		// agree with it, so writing it again here would just be the same
		// edge a second time.
	}

	return nil
}

func findProject(projects []yamlProject, id string) (yamlProject, bool) {
	for _, p := range projects {
		if p.ID == id {
			return p, true
		}
	}
	return yamlProject{}, false
}
