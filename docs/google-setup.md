# Connecting water to Google (Calendar, Gmail, Drive)

This is a one-time setup, about 10 minutes, for a **personal Gmail account**. Water reads Calendar, Gmail and Drive/Docs/Sheets — read-only — through your own Google Cloud project, so there's no third-party server in between.

## 1. Create a Google Cloud project

1. Go to <https://console.cloud.google.com/projectcreate>.
2. Name it anything (e.g. "water-personal"). **No billing account is needed** for this.

## 2. Enable the three APIs

With the new project selected, visit each of these and click **Enable**:

- <https://console.cloud.google.com/apis/library/calendar-json.googleapis.com>
- <https://console.cloud.google.com/apis/library/gmail.googleapis.com>
- <https://console.cloud.google.com/apis/library/drive.googleapis.com>

## 3. Configure the OAuth consent screen

Go to <https://console.cloud.google.com/apis/credentials/consent>.

1. User type: **External**.
2. Fill in the app name, your email as the support email, and your email again as the developer contact.
3. Scopes: skip this screen — water requests scopes itself during `water connect google`.
4. **Test users**: add your own Google account's email address.
5. **Publishing status: set it to "In production"**, and **do not click "Submit for verification."**
   - This matters. A personal ("External") OAuth client left in **Testing** mode has its refresh tokens **expire after 7 days** — water would silently stop working weekly. **In production** (unverified) keeps issuing long-lived refresh tokens; only apps that request sensitive/restricted scopes for *other people* need Google's verification, and this app only reads your own data with your own consent.
   - Google will show a "Google hasn't verified this app" warning the first time you connect. That's expected for an unverified app — see step 5 below for how to get past it.

## 4. Create a Desktop OAuth client

Go to <https://console.cloud.google.com/apis/credentials>, click **Create credentials → OAuth client ID**.

- Application type: **Desktop app** (not "Web application" — water's loopback flow needs this type).
- Name it anything.
- Click **Create**, then **Download JSON**. Save it somewhere like `~/Downloads/client_secret.json`.

## 5. Connect water

```sh
water connect google --client-file ~/Downloads/client_secret.json
```

This opens your browser (and prints the URL, in case it doesn't). You'll see:

- **"Google hasn't verified this app"** — click **Advanced**, then **Go to water-personal (unsafe)**. This is Google's standard warning for any unverified OAuth app; it's safe here because you created the app yourself and it only touches your own account.
- A consent screen listing Calendar, Gmail and Drive read access. Approve it, and **tick every checkbox** — water refuses to connect if any of the three scopes was left unchecked.

On success, water prints `connected: water.google/ceo` and stores the credential in the macOS Keychain. Nothing in that output or in any log is ever the token itself.

## 6. Check it

```sh
water connect google --status   # forces a live token refresh
water doctor                    # shows "google: connected (water.google/ceo)"
```

## Using it

```sh
water daemon                                   # starts the background sync too
water ask "what's on my schedule today?"       # answered from the store, no model call
water ask "summarize my unread email from today"
water audit verify                             # every call the gate ran is in the hash-chained log
```

## Disconnecting

```sh
water connect google --revoke
```

This revokes the refresh token at Google (so it stops working everywhere, not just for water) and deletes it from the Keychain.

## Troubleshooting

- **"reconnect: run `water connect google`"** — Google rejected the stored refresh token (`invalid_grant`). This happens if you revoke water's access from <https://myaccount.google.com/permissions>, or if the consent screen was left in Testing mode and the token expired after 7 days (see step 3). Just run `water connect google --client-file ...` again.
- **"client file is for a Web application client"** — you created the wrong OAuth client type in step 4. Delete it and create a **Desktop app** client instead.
- **"access not granted for ...; connect again and tick every box"** — one of the three scopes wasn't approved. Reconnect and check every box on the consent screen.
