package store

import (
	"encoding/json"
	"time"
)

// This file's six types are the people roster (twins/<id>/seed/people.yaml,
// loaded by internal/roster): who works on what. They follow records.go's
// exact normalized-record shape (Meta, db tags, Table()) and get Upsert/
// Get/List for free from its generic machinery. Source is always "seed";
// SourceID is the yaml's own id for that entry. Relationships between them
// (team membership, leadership, project allocation, client ownership) are
// never columns here — they live in the links table (links.go), so a new
// relationship kind never needs a schema change.

type Person struct {
	Meta
	Name string `db:"name"`
	Role string `db:"role"`
	// HomeTeam is the team's source_id. also_on_teams (a person can belong
	// to more than one) is a member_of link instead, since a single column
	// cannot hold a set.
	HomeTeam string `db:"home_team"`
	// Identities is a JSON object, e.g. {"linear_owner_label":"Theo"}: how
	// each connector refers to this person. Use Identity to read one key
	// rather than unmarshaling this directly.
	Identities string `db:"identities"`
}

func (*Person) Table() string { return "people" }

// Identity returns identities' value for key ("" if absent or the stored
// JSON is malformed — a resolution miss, not a caller-visible error, since
// a seed file's identities are trusted input already validated at load
// time).
func (p Person) Identity(key string) string {
	if p.Identities == "" {
		return ""
	}
	var m map[string]string
	if err := json.Unmarshal([]byte(p.Identities), &m); err != nil {
		return ""
	}
	return m[key]
}

type Team struct {
	Meta
	Name      string `db:"name"`
	LinearKey string `db:"linear_key"`
}

func (*Team) Table() string { return "teams" }

type Project struct {
	Meta
	Name string `db:"name"`
	// LinearProject is the Linear project key (e.g. "P-CRA-1") issues and
	// PRs carry, for matching a Linear item to this project.
	LinearProject string    `db:"linear_project"`
	StartAt       time.Time `db:"start_at"`
	TargetAt      time.Time `db:"target_at"`
}

func (*Project) Table() string { return "projects" }

type Client struct {
	Meta
	Name    string `db:"name"`
	Product string `db:"product"`
	// MRRMinor/PotentialMRRMinor are USD cents, matching Transaction's own
	// AmountMinor convention (money is never a float column).
	MRRMinor          int64     `db:"mrr_minor"`
	PotentialMRRMinor int64     `db:"potential_mrr_minor"`
	Status            string    `db:"status"`
	RenewalAt         time.Time `db:"renewal_at"`
}

func (*Client) Table() string { return "clients" }

type Vendor struct {
	Meta
	Name         string    `db:"name"`
	Product      string    `db:"product"`
	MonthlyMinor int64     `db:"monthly_minor"`
	RenewalAt    time.Time `db:"renewal_at"`
}

func (*Vendor) Table() string { return "vendors" }

// OrgContact is the roster's own investor/candidate-style entries — not
// Contact (records.go), which is a real CRM connector's synced business
// contact and has a different shape entirely.
type OrgContact struct {
	Meta
	Name    string `db:"name"`
	Role    string `db:"role"`
	ForTeam string `db:"for_team"`
	Stage   string `db:"stage"`
}

func (*OrgContact) Table() string { return "org_contacts" }
