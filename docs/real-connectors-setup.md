# Setting up the real GitHub, Linear and HubSpot connectors

These three connectors (`internal/connectors/github`, `internal/connectors/
linear`, `internal/connectors/hubspot`) are real, read-only integrations —
not the in-memory demo fakes (`internal/connectors/fake`, used only by
`water daemon --demo`). Each is optional and independent: the daemon starts
fine with none, some or all three configured. An unconfigured one simply
answers "not connected" the moment the twin tries to call it; nothing about
startup depends on any of them.

`twins/ceo/twin.yaml` grants all five functions (`github.list_prs`,
`github.list_issues`, `linear.list_issues`, `hubspot.list_deals`,
`hubspot.list_contacts`) at level R (read-only) unconditionally, so they are
always valid tool calls for the twin — whether or not a token is stored
determines whether calling one actually does anything.

## GitHub (`water connect github`)

The owner already has a real GitHub account (`kranthi7899`) and this very
repo, so this is the one to set up and test live tonight.

1. **Generate a token.** Either works; a fine-grained token is the more
   precise, GitHub-recommended option for a single-repo integration like
   this one:
   - **Fine-grained token (recommended):** GitHub.com → your avatar →
     **Settings** → **Developer settings** → **Personal access tokens** →
     **Fine-grained tokens** → **Generate new token**. Set:
     - **Resource owner:** your account (or the org that owns the repo).
     - **Repository access:** "Only select repositories" → pick the repo
       water should read.
     - **Permissions → Repository permissions:** set **Pull requests** to
       **Read-only** and **Issues** to **Read-only**. (GitHub adds
       **Contents: Metadata — Read-only** automatically; that's fine and
       needed.)
     - Generate, then copy the token (starts `github_pat_`). GitHub only
       shows it once.
   - **Classic token (simpler, broader):** **Settings** → **Developer
     settings** → **Personal access tokens** → **Tokens (classic)** →
     **Generate new token (classic)**. Scope: **`public_repo`** if the repo
     is public, or the full **`repo`** scope if it's private (classic
     tokens can't grant read-only access to just PRs/issues on a private
     repo — `repo` is the minimum that reaches a private repo's pulls and
     issues). Copy the token (starts `ghp_`).
2. **Store it:**
   ```
   water connect github --token <the token you copied>
   ```
   This writes it to the macOS Keychain (`water.github`/`ceo`) via the same
   `internal/vault` mechanism every other connector's credential uses. The
   token is never printed back or logged.
3. **Tell water which repo to read** (there is no default):
   ```
   water config set github.repo kranthi7899/<repo-name>
   ```
   (`owner/name`, exactly as it appears in the repo's GitHub URL.)
4. **Verify it's actually reachable:**
   ```
   water connect github --status
   ```
   This calls `GET /rate_limit` — the one GitHub endpoint every valid token
   can call regardless of granted scopes, so `--status` confirms the token
   authenticates without needing `github.repo` to be set yet.
5. **Try it live:** with the daemon running (`water daemon`), `water ask
   "what pull requests are open on our repo?"` should exercise
   `github.list_prs` for real. `water doctor` also reports GitHub's status —
   configured or not, and (if configured) the same live `/rate_limit` check.
6. **To disconnect:** `water connect github --revoke` removes the token from
   the Keychain. It does not call back to GitHub — GitHub tokens can't be
   revoked by a third party's API call, only from GitHub's own UI (the same
   **Settings → Developer settings** page, or **Settings → Applications**
   for a fine-grained token) if you want the token itself invalidated, not
   just removed from water.

## Linear (`water connect linear`)

The owner likely doesn't have a Linear workspace yet. Skip this until there
is one to test against; the code is complete and covered by tests using
`httptest` fixtures, so nothing here needs the daemon restarted once a real
key is added.

1. **Generate a personal API key.** In Linear: **Settings** (click your
   workspace avatar, bottom left) → **Security & access** → **Personal API
   keys** → **New API key**. Give it a label (e.g. "water"), no scope
   picker exists for personal keys — they carry your own full read/write
   access to your workspace, so treat it like a password. Copy it (starts
   `lin_api_`); Linear only shows it once.
2. **Store it:**
   ```
   water connect linear --token <the key you copied>
   ```
3. **Verify:**
   ```
   water connect linear --status
   ```
   This runs the minimal GraphQL query `{ viewer { id } }` against
   `https://api.linear.app/graphql`.
4. **Try it live:** `water ask "what's in progress on Linear?"` should
   exercise `linear.list_issues`.
5. **To disconnect:** `water connect linear --revoke` removes the local
   copy. Revoke the key itself (if you want it invalidated everywhere) from
   the same **Security & access → Personal API keys** page.

## HubSpot (`water connect hubspot`)

Same situation as Linear — set this up once there's a real HubSpot account
to point at (a free-tier HubSpot CRM account works fine).

1. **Create a private app.** In HubSpot: **Settings** (gear icon) →
   **Integrations** → **Private Apps** → **Create a private app**. Give it a
   name (e.g. "water"). Under the **Scopes** tab, add these read scopes:
   - `crm.objects.deals.read`
   - `crm.objects.companies.read` (deals reference their company only by
     association, not a plain property — water resolves the company name
     via this scope; without it, deals still list but with an empty company
     field)
   - `crm.objects.contacts.read`
   Click **Create app**, confirm, then copy the **access token** it shows
   you (starts `pat-na1-` or similar). HubSpot only shows it once.
2. **Store it:**
   ```
   water connect hubspot --token <the token you copied>
   ```
3. **Verify:**
   ```
   water connect hubspot --status
   ```
   This fetches one page of contacts (`GET /crm/v3/objects/contacts?
   limit=1`) as a minimal reachability check.
4. **Try it live:** `water ask "what deals are in the pipeline?"` should
   exercise `hubspot.list_deals`; a HubSpot-sourced `investor_request` card
   (`water decisions list`) needs a matching deal in your CRM to trigger.
5. **To disconnect:** `water connect hubspot --revoke` removes the local
   copy. Delete or deactivate the private app itself from HubSpot's
   **Private Apps** page if you want the token invalidated everywhere.

## Notes for all three

- None of this needs a daemon restart to take effect for `--token`/
  `--status`/`--revoke` themselves (they write straight to the Keychain via
  `water connect`), but the daemon does need to be running for `water ask`
  to actually reach the connector, and it re-reads the Keychain on every
  call rather than caching a missing credential — so connecting a token
  while the daemon is already running works immediately, no restart needed.
- Every call is read-only (level R in `twins/ceo/twin.yaml`); none of these
  three connectors can send, post, create or delete anything.
- Every response from these three services is marked `External: true`
  (someone else's/another organization's data), the same as email and
  calendar content from other people — it is treated as untrusted input
  wherever it flows into a decision or a drafted message.
