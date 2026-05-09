# Operator Handoff Checklist

A pre-flight list for the BeaconGate operator before sending a bundle
to a friend who will run it on Android.

This is the single doc that says **"the bundle is ready."** Walk it
top-to-bottom every time. If any check fails, fix it before sending.

If you are the friend the operator is sending the bundle to, this
isn't your doc — see [android-termux.md](android-termux.md) instead.

---

## Before you build the bundle

- [ ] **Server is running and healthy.**
      Run from a *different* network than the server (your phone on
      mobile data, or a VPS in another region):
      ```sh
      curl -fsS https://relay.your-domain.com/healthz   # https mode
      # or for appsscript mode, hit your script's URL:
      curl -fsS https://script.google.com/macros/s/$DEPLOYMENT_ID/exec
      ```
      Both should return `ok` or a JSON body. If a curl from your
      laptop on the same network as the server works but a curl from
      a different network does not, you have a firewall/DNS problem
      to fix before handing the bundle over.

- [ ] **The friend's expected destinations are not blocked by policy.**
      ```sh
      curl -x socks5h://127.0.0.1:1080 -fsS https://news.example.com
      curl -x socks5h://127.0.0.1:1080 -fsS https://api.example.org
      ```
      Run with the operator's own client pointed at the production
      server. Hits a `RESET POLICY_DENIED`? Add an allow rule before
      shipping. See [policy.md](policy.md) §"Adding a rule safely."

- [ ] **The shared key is the one in the bundle config.**
      Re-read `client_config.json` immediately before bundling. Easy
      mistake: edit a config, send the *previous* config because you
      copied the wrong file.

---

## Build the bundle

```sh
make build-android
ops/prepare-bundle.sh \
  --binary bin/beacongate-client-android-arm64 \
  --config client_config.json \
  --out /tmp/bundle-$(date +%Y%m%d).zip \
  --vps-ip 203.0.113.5            # your VPS public IP, for verify.sh's leak check
```

`prepare-bundle.sh` runs `beacongate-client -validate-only` on the
config before zipping. **A bundle is never produced from a
config that fails validation** — fail-closed by design.

---

## Before you send the bundle

- [ ] **The bundle's `verify.sh` passes on your own Linux box.**
      Unzip it on a Linux laptop, run the binary against your live
      server, point `curl -x socks5h://127.0.0.1:1080` and
      `curl http://127.0.0.1:9091/api/status` at it, and confirm all
      three checks pass. Don't ship a bundle you haven't yourself
      booted from cold.

- [ ] **SHA-256 logged out of band.**
      `prepare-bundle.sh` printed a SHA-256 — write it down somewhere
      you can read after the bundle has left your machine (a notes
      file, a 1Password note, a separate channel). When the friend
      receives the file, you can compare hashes and know they got the
      bundle you sent, not a corrupted or substituted one.

- [ ] **Friend has a way to reach you for support.**
      The first time anyone runs this on a new phone, *something*
      will go wrong. The friend will need to send you a screenshot
      of `verify.sh`'s output and a few log lines. Make sure they
      know how to do that.

- [ ] **You've sent them the link to [android-termux.md](android-termux.md).**
      The bundle's `README.txt` is the short version; the doc is the
      full walkthrough. Don't make them guess.

---

## After they have it running

- [ ] **Watch the server logs for the first connection.**
      ```sh
      journalctl -u beacongate-server -f
      ```
      You're looking for a `session.open` event tagged with the
      `client_id` from the friend's config. If you see
      `tunnel.auth_failed`, the keys diverged — re-issue the bundle.

- [ ] **Confirm the friend's `verify.sh` passes** before walking away.
      A successful `bash verify.sh` on the phone is the only thing
      that proves the disguise is working end-to-end.

- [ ] **Tell them about `termux-wake-lock`.**
      The single most common Phase-1 support call is "it worked for
      five minutes then stopped." That's Android killing Termux. The
      friend needs to run `termux-wake-lock` every session — it's in
      the bundle README, but reminding them face-to-face cuts the
      first-week support load roughly in half.

---

## Routine maintenance (after the friend is running)

- [ ] **Rotate the shared key every few months.**
      `beacongate-admin gen-key`, update `server_config.json`, restart
      the server, build a new bundle, hand it over. The old key is
      hard-cut.

- [ ] **For appsscript mode: watch quota.**
      `script_keys` rotation is handled per-bundle; if the friend
      reports occasional outages around the same time of day, that's
      the daily ~20K UrlFetchApp quota. Add another deployment under
      another Google account and reissue the config.

- [ ] **Review `RESET POLICY_DENIED` events monthly.**
      Real users hit edges that synthetic tests don't. Skim the
      server logs for `tunnel.policy_denied` and decide whether each
      one is a legitimate destination that should be allowed.

---

## Related runbooks

- [docs/android-termux.md](android-termux.md) — the friend's setup walkthrough
- [docs/troubleshooting.md](troubleshooting.md) — when something goes wrong
- [docs/policy.md](policy.md) — adding/removing/auditing policy rules
- [docs/deployment.md](deployment.md) — server deployment and config templates
