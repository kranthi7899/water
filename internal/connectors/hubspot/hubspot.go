// Package hubspot is a real, read-only HubSpot connector: it lists CRM
// deals and contacts through HubSpot's REST API (api.hubapi.com),
// authenticated with a private-app access token generated in HubSpot's own
// UI (Settings > Integrations > Private Apps; no OAuth flow needed for a
// personal/free-tier account). Its function names and Normalize output
// shapes match internal/connectors/fake.HubSpot exactly, so anything
// already built against the demo fake (twins/ceo-demo's investor_request
// card, its tests) works unchanged against this real connector once a token
// is configured.
package hubspot

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"
	"time"

	"water/internal/connectors"
	"water/internal/connectors/tokenapi"
	"water/internal/gate/permit"
	"water/internal/store"
	"water/internal/twins"
	"water/internal/vault"
)

// Service and Account name the vault entry `water connect hubspot` writes.
const (
	Service = "water.hubspot"
	Account = "ceo"
)

const apiHost = "api.hubapi.com"

// pageSize bounds one page of HubSpot's cursor-paginated list endpoints;
// maxPages bounds how many pages Invoke will follow so a very large CRM
// cannot loop this forever.
const (
	pageSize = 100
	maxPages = 20
)

// Deal is the compact shape Invoke returns for list_deals and Normalize
// consumes, matching fake.HubSpotDeal's fields (plus the same computed
// AmountUSD field the fake adds — see dealOutput below).
type Deal struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Stage       string `json:"stage"`
	AmountCents int64  `json:"amount_cents"`
	CloseDate   string `json:"close_date"` // RFC 3339
	Owner       string `json:"owner"`
	Company     string `json:"company"`
}

// dealOutput is what list_deals actually returns to the model. AmountUSD is
// computed here, in code, from AmountCents — mirroring
// internal/connectors/fake.HubSpot's dealOutput exactly and for the same
// reason: a model reading a raw amount_cents integer has no reliable reason
// to divide by 100 before treating it as a dollar figure.
type dealOutput struct {
	Deal
	AmountUSD float64 `json:"amount_usd"`
}

// Contact is the compact shape Invoke returns for list_contacts, matching
// fake.HubSpotContact's fields.
type Contact struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Email   string `json:"email"`
	Phone   string `json:"phone,omitempty"`
	Company string `json:"company"`
	Title   string `json:"title"`
}

// wireObject is HubSpot's generic CRM object shape (deals and contacts both
// come back this way: an id plus a flat properties map). Associations is
// only populated for the deals list call (requested via the associations
// query param) and carries each deal's linked company ids — HubSpot deals
// have no "company" property of their own; the company comes from this
// association, resolved to a name via a separate batch read.
type wireObject struct {
	ID           string            `json:"id"`
	Properties   map[string]string `json:"properties"`
	Associations *struct {
		Companies *struct {
			Results []struct {
				ID string `json:"id"`
			} `json:"results"`
		} `json:"companies"`
	} `json:"associations"`
}

type wirePage struct {
	Results []wireObject `json:"results"`
	Paging  *struct {
		Next *struct {
			After string `json:"after"`
		} `json:"next"`
	} `json:"paging"`
}

// wireBatchReadResponse is crm/v3/objects/{type}/batch/read's response
// shape: no pagination, just every requested object's properties.
type wireBatchReadResponse struct {
	Results []wireObject `json:"results"`
}

// batchReadMax is HubSpot's own cap on ids per batch/read call.
const batchReadMax = 100

// HubSpot is the connector. opts is nil in production and set in tests to
// point at an httptest server.
type HubSpot struct {
	opts *tokenapi.Options
}

// New builds the production connector.
func New() *HubSpot { return &HubSpot{} }

// NewWithOptions builds a connector against overridden tokenapi options, for
// tests.
func NewWithOptions(o *tokenapi.Options) *HubSpot { return &HubSpot{opts: o} }

func (*HubSpot) Name() string { return "hubspot" }

func (*HubSpot) Credential() (string, string) { return Service, Account }

func (*HubSpot) Functions() []connectors.Function {
	return []connectors.Function{
		{Name: "list_deals", Description: "List CRM deals, optionally filtered by keyword.", Level: twins.R, Risk: connectors.RiskLow, External: true,
			Schema: connectors.Schema{Properties: map[string]connectors.Property{"query": {Type: "string", Description: "keyword filter over deal name/company"}}}},
		{Name: "list_contacts", Description: "List CRM contacts, optionally filtered by keyword.", Level: twins.R, Risk: connectors.RiskLow, External: true,
			Schema: connectors.Schema{Properties: map[string]connectors.Property{"query": {Type: "string", Description: "keyword filter over name/company"}}}},
	}
}

func (h *HubSpot) client(cred vault.Secret) (*tokenapi.Client, error) {
	return tokenapi.FromSecret(cred, tokenapi.BearerAuth, apiHost, h.opts)
}

func (h *HubSpot) Invoke(ctx context.Context, p permit.Permit) (json.RawMessage, error) {
	v, err := p.Open()
	if err != nil {
		return nil, err
	}
	cl, err := h.client(v.Credential)
	if err != nil {
		return nil, fmt.Errorf("hubspot: %w; run `water connect hubspot --token <TOKEN>`", err)
	}
	switch v.Function {
	case "list_deals":
		return h.listDeals(ctx, cl, v)
	case "list_contacts":
		return h.listContacts(ctx, cl, v)
	}
	return nil, fmt.Errorf("hubspot: unknown function %q", v.Function)
}

func (h *HubSpot) listPage(ctx context.Context, cl *tokenapi.Client, objectType string, properties []string, associations []string) ([]wireObject, error) {
	endpoint := "https://" + apiHost + "/crm/v3/objects/" + objectType
	var out []wireObject
	after := ""
	for page := 0; page < maxPages; page++ {
		q := url.Values{"limit": {strconv.Itoa(pageSize)}, "properties": {strings.Join(properties, ",")}}
		if len(associations) > 0 {
			q.Set("associations", strings.Join(associations, ","))
		}
		if after != "" {
			q.Set("after", after)
		}
		var resp wirePage
		if _, err := cl.GetJSON(ctx, endpoint, q, nil, &resp); err != nil {
			return nil, err
		}
		out = append(out, resp.Results...)
		if resp.Paging == nil || resp.Paging.Next == nil || resp.Paging.Next.After == "" {
			break
		}
		after = resp.Paging.Next.After
	}
	return out, nil
}

// companyNames resolves a set of company object ids to their "name"
// property via crm/v3/objects/companies/batch/read, chunked at
// batchReadMax ids per call (HubSpot's own limit). An id that fails to
// resolve (deleted company, no read access) is simply left out of the
// result rather than failing the whole deals listing.
func (h *HubSpot) companyNames(ctx context.Context, cl *tokenapi.Client, ids []string) (map[string]string, error) {
	out := map[string]string{}
	for start := 0; start < len(ids); start += batchReadMax {
		end := min(start+batchReadMax, len(ids))
		inputs := make([]map[string]string, 0, end-start)
		for _, id := range ids[start:end] {
			inputs = append(inputs, map[string]string{"id": id})
		}
		var resp wireBatchReadResponse
		body := map[string]any{"inputs": inputs, "properties": []string{"name"}}
		if _, err := cl.PostJSON(ctx, "https://"+apiHost+"/crm/v3/objects/companies/batch/read", nil, body, &resp); err != nil {
			return nil, err
		}
		for _, r := range resp.Results {
			out[r.ID] = r.Properties["name"]
		}
	}
	return out, nil
}

func (h *HubSpot) listDeals(ctx context.Context, cl *tokenapi.Client, v permit.Call) (json.RawMessage, error) {
	query := strings.ToLower(tokenapi.ArgString(v.Args, "query"))
	objs, err := h.listPage(ctx, cl, "deals", []string{"dealname", "dealstage", "amount", "closedate", "hubspot_owner_id"}, []string{"companies"})
	if err != nil {
		return nil, err
	}

	var companyIDs []string
	seen := map[string]bool{}
	for _, o := range objs {
		if o.Associations == nil || o.Associations.Companies == nil {
			continue
		}
		for _, c := range o.Associations.Companies.Results {
			if !seen[c.ID] {
				seen[c.ID] = true
				companyIDs = append(companyIDs, c.ID)
			}
		}
	}
	var names map[string]string
	if len(companyIDs) > 0 {
		names, err = h.companyNames(ctx, cl, companyIDs)
		if err != nil {
			return nil, err
		}
	}

	var views []dealOutput
	for _, o := range objs {
		d := toDeal(o, names)
		if query != "" && !matchesAny(query, strings.ToLower(d.Name), strings.ToLower(d.Company), strings.ToLower(d.Stage)) {
			continue
		}
		views = append(views, dealOutput{Deal: d, AmountUSD: float64(d.AmountCents) / 100})
	}
	if views == nil {
		views = []dealOutput{}
	}
	return json.Marshal(views)
}

func (h *HubSpot) listContacts(ctx context.Context, cl *tokenapi.Client, v permit.Call) (json.RawMessage, error) {
	query := strings.ToLower(tokenapi.ArgString(v.Args, "query"))
	objs, err := h.listPage(ctx, cl, "contacts", []string{"firstname", "lastname", "email", "phone", "company", "jobtitle"}, nil)
	if err != nil {
		return nil, err
	}
	var out []Contact
	for _, o := range objs {
		c := toContact(o)
		if query != "" && !matchesAny(query, strings.ToLower(c.Name), strings.ToLower(c.Company), strings.ToLower(c.Title)) {
			continue
		}
		out = append(out, c)
	}
	if out == nil {
		out = []Contact{}
	}
	return json.Marshal(out)
}

// matchesAny reports whether any whitespace-separated word of query appears
// as a substring of any haystack (the same word-substring rule the fake
// connector uses).
func matchesAny(query string, haystacks ...string) bool {
	for _, w := range strings.Fields(query) {
		for _, h := range haystacks {
			if strings.Contains(h, w) {
				return true
			}
		}
	}
	return false
}

// amountCents converts HubSpot's "amount" property (a decimal-dollar
// string, e.g. "42000" or "42000.50") to integer cents, matching
// fake.HubSpotDeal.AmountCents' units.
func amountCents(s string) int64 {
	if s == "" {
		return 0
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return int64(math.Round(f * 100))
}

// toDeal builds a Deal from one wire object plus the company-id-to-name map
// companyNames already resolved (see (*HubSpot).companyNames): a deal has no
// "company" property of its own, only an association to a company object.
func toDeal(o wireObject, companyNames map[string]string) Deal {
	p := o.Properties
	company := ""
	if o.Associations != nil && o.Associations.Companies != nil {
		for _, c := range o.Associations.Companies.Results {
			if name := companyNames[c.ID]; name != "" {
				company = name
				break
			}
		}
	}
	return Deal{
		ID:          o.ID,
		Name:        p["dealname"],
		Stage:       p["dealstage"],
		AmountCents: amountCents(p["amount"]),
		// HubSpot returns closedate as an RFC 3339 timestamp (with
		// milliseconds); Normalize parses it with parseHubSpotTime, which
		// handles that.
		CloseDate: p["closedate"],
		Owner:     p["hubspot_owner_id"],
		Company:   company,
	}
}

func toContact(o wireObject) Contact {
	p := o.Properties
	name := strings.TrimSpace(p["firstname"] + " " + p["lastname"])
	return Contact{
		ID:      o.ID,
		Name:    name,
		Email:   p["email"],
		Phone:   p["phone"],
		Company: p["company"],
		Title:   p["jobtitle"],
	}
}

// parseHubSpotTime parses a HubSpot date/datetime property, trying RFC 3339
// with fractional seconds (what closedate normally carries,
// "2026-10-08T00:00:00.000Z") before plain RFC 3339, and returns the zero
// time for anything else rather than erroring — a missing or malformed date
// degrades this one field instead of failing the whole listing.
func parseHubSpotTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t.UTC()
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC()
	}
	return time.Time{}
}

// Normalize maps deals onto store.Transaction and contacts onto
// store.Contact, exactly as internal/connectors/fake.HubSpot.Normalize does
// (including repurposing Account to carry the deal's Stage — see that
// file's own comment; store.Transaction has no dedicated pipeline-stage
// field).
func (h *HubSpot) Normalize(fn string, raw json.RawMessage) ([]store.Record, error) {
	switch fn {
	case "list_deals":
		var deals []Deal
		if err := json.Unmarshal(raw, &deals); err != nil {
			return nil, err
		}
		var out []store.Record
		for _, d := range deals {
			closed := parseHubSpotTime(d.CloseDate)
			out = append(out, &store.Transaction{
				Meta:         store.Meta{Source: h.Name(), SourceID: d.ID, External: true},
				Account:      d.Stage,
				AmountMinor:  d.AmountCents,
				Currency:     "USD",
				Counterparty: d.Company,
				Description:  d.Name,
				PostedAt:     closed.UTC(),
			})
		}
		return out, nil
	case "list_contacts":
		var contacts []Contact
		if err := json.Unmarshal(raw, &contacts); err != nil {
			return nil, err
		}
		var out []store.Record
		for _, c := range contacts {
			out = append(out, &store.Contact{
				Meta:  store.Meta{Source: h.Name(), SourceID: c.ID, External: true},
				Name:  c.Name,
				Email: c.Email,
				Phone: c.Phone,
				Org:   c.Company,
				Title: c.Title,
			})
		}
		return out, nil
	}
	return nil, nil
}

// CheckStatus does a minimal, cheap, read-only live call (one contact page)
// to confirm a stored token actually authenticates and has at least
// contacts read scope. `water connect hubspot --status` uses it.
func CheckStatus(ctx context.Context, s vault.Secret) error {
	return checkStatusWithOptions(ctx, s, nil)
}

// checkStatusWithOptions is CheckStatus parameterized over tokenapi
// options, so a test can point it at an httptest server.
func checkStatusWithOptions(ctx context.Context, s vault.Secret, o *tokenapi.Options) error {
	cl, err := tokenapi.FromSecret(s, tokenapi.BearerAuth, apiHost, o)
	if err != nil {
		return err
	}
	var resp wirePage
	_, err = cl.GetJSON(ctx, "https://"+apiHost+"/crm/v3/objects/contacts", url.Values{"limit": {"1"}}, nil, &resp)
	return err
}
