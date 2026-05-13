# BeaconGate Apps Script forwarder

`Code.gs` is the Google Apps Script web app that makes the
`appsscript` transport's "looks like Google" property work. The
BeaconGate client posts encrypted batches to your deployment of this
script at `https://script.google.com/macros/s/{DEPLOYMENT_ID}/exec`;
the script forwards them as binary to your BeaconGate VPS server.

## Why this script does base64

BeaconGate's design keeps the VPS server **binary-only** across both
transports (`https` and `appsscript`); the server has no transport
awareness. Apps Script, by contrast, exposes `e.postData.contents`
as a JavaScript string. To bridge those constraints without
contaminating the server, the wire body the client sends is text-safe
**base64**, and this script handles the text↔binary boundary itself:

- Inbound: decode base64 → binary → forward to the VPS.
- Outbound: receive binary from the VPS → base64-encode → return to
  the client.

The cost is ~10 lines of JavaScript executed inside the Apps Script
VM. Moving this boundary into the server would push base64 awareness
into a layer that has no business knowing about transports.

## Setup

1. Open <https://script.google.com/> and create a new project.
2. Replace the default `Code.gs` content with this directory's
   `Code.gs`.
3. **Edit the `RELAY_URL` constant** (line 26) to point at your
   BeaconGate VPS's tunnel endpoint, e.g.
   `'http://1.2.3.4:8080/tunnel'` or `'https://relay.example.com/tunnel'`.
4. Deploy:
   - Deploy → New deployment → Select type → **Web app**
   - Description: anything (e.g. `BeaconGate v1.1`)
   - Execute as: **Me**
   - Who has access: **Anyone**
   - Click **Deploy**, copy the **Deployment ID** (not the Script ID)
   - On first deploy, Google will ask you to authorize the script.
     The script needs permission to call external URLs (your VPS).
5. Add the deployment ID to your `client_config.json`:

   ```json
   {
     "transport": {
       "type": "appsscript",
       "options": {
         "script_keys": "DEPLOYMENT_ID_FROM_STEP_4"
       }
     }
   }
   ```

   Multiple deployment IDs (across multiple Google accounts) extend
   the daily quota — see "Quota" below. Comma-separate them in
   `script_keys`.

## Updating the script

**Saving the script alone does not update what your clients hit.**
Every change requires a new deployment:

1. Edit `Code.gs`
2. Deploy → **New deployment** (or → Manage deployments → New version
   if updating an existing one — both create a new ID)
3. Update `script_keys` in `client_config.json` to the new ID

Apps Script's "version" feature is independent of the URL; the URL
embeds the deployment ID, so a new deployment = new URL.

## Quota

Each Google account has a ~20,000 UrlFetchApp invocations/day quota
that resets at midnight Pacific. Every `doPost` and `doGet` here
counts.

To extend ceiling:

- Run multiple deployments under multiple Google accounts.
- List all of them in `script_keys` (comma-separated).
- The BeaconGate `appsscript` transport round-robins across them and
  fails over on per-deployment errors.

The `doGet` handler reports the deployment's own day counter
(`{ok, date, count, version, protocol}` JSON). The transport polls it
every 30 minutes per deployment and surfaces the count in its
`Diagnose()` output.

## Security note

The `Anyone` access level on the deployment means anyone who knows
the deployment ID can POST to it. That's fine because:

1. Your BeaconGate AEAD key is what actually authenticates the
   payload; this forwarder only relays bytes, never decrypts.
2. The deployment ID is not secret — but a stranger POSTing junk to
   it just wastes your quota. Deploy under a Google account where
   that's an acceptable failure mode (i.e. not your main account).

If you find quota being burnt by unknown clients, rotate the
deployment ID (create a new deployment, update `script_keys`, the old
deployment can be deleted from Apps Script's "Manage deployments").
