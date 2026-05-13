# Operator Handoff Checklist

Pre-flight list for the BeaconGate operator before handing a config
(and, optionally, a built APK) to an end user. Walk it top to bottom
every time; if any check fails, fix it before sending.

The end-user-facing walkthrough is in
[mobile/android/README.md](../mobile/android/README.md).

---

## Before you generate the config

- [ ] **Server is running and healthy** — verified from a different
      network than the server (e.g. your phone on mobile data, or a
      VPS in another region):
      ```sh
      curl -fsS https://relay.your-domain.com/healthz   # https mode
      # or for appsscript mode, hit the script URL:
      curl -fsS https://script.google.com/macros/s/$DEPLOYMENT_ID/exec
      ```
      Both should return `ok` or a JSON body. If `curl` from the same
      network as the server works but a different network does not,
      fix the firewall / DNS issue before handing anything over.

- [ ] **The end user's expected destinations are not blocked by policy** —
      from your own operator client pointed at the production server:
      ```sh
      curl -x socks5h://127.0.0.1:1080 -fsS https://news.example.com
      curl -x socks5h://127.0.0.1:1080 -fsS https://api.example.org
      ```
      A `RESET POLICY_DENIED` means you need to add an allow rule
      before shipping. See [policy.md](policy.md) §"Adding a rule
      safely."

- [ ] **Server `client_template` is set.** `beacongate-admin
      add-client` requires a `client_template` block in
      `server_config.json`. If absent, the CLI's error tells you
      exactly what to add.

---

## Generate the per-user config

```sh
ssh beacongate-vps "cd /etc/beacongate && \
  beacongate-admin add-client \
    --server-config server_config.json \
    --name alice \
    --output alice.json && \
  systemctl restart beacongate-server"
ssh beacongate-vps "cat /etc/beacongate/alice.json" > /tmp/alice.json
```

`add-client` appends the new client to the server's allowlist and
writes a ready-to-import JSON config containing the per-client key,
the server URL, and the transport options.

- [ ] **The config validates.** Run
      `beacongate-client -validate-only -config /tmp/alice.json`. A
      non-zero exit means do not ship.

- [ ] **The key in the config is the one the server will accept.**
      Easy mistake: edit `server_config.json`, restart, then ship a
      config that was generated before the restart. If you've rotated
      keys, re-run `add-client` after the restart.

---

## Before you send the config

- [ ] **Test the config end-to-end from your own laptop.**
      ```sh
      beacongate-client -config /tmp/alice.json -control-addr 127.0.0.1:9091 &
      curl -x socks5h://127.0.0.1:1080 https://api.ipify.org
      ```
      The returned IP should be your VPS's public IP. Don't ship a
      config you haven't yourself booted from cold.

- [ ] **SHA-256 logged out of band.**
      ```sh
      sha256sum /tmp/alice.json
      ```
      Save the hash somewhere you can read after the file has left
      your machine (notes file, password manager, separate channel).
      The user compares it on receipt and knows they got what you
      sent, not a corrupted or substituted file.

- [ ] **The user has a way to reach you for support.** First-run
      installs hit edge cases. The user will need to send you a
      screenshot of the app's status screen and a few log lines.
      Confirm they know how to do that.

- [ ] **The user has a link to the install walkthrough** —
      [mobile/android/README.md](../mobile/android/README.md), or the
      legacy [android-termux.md](android-termux.md) if you're handing
      over a Termux bundle instead.

---

## After they have it running

- [ ] **Watch the server logs for the first connection.**
      ```sh
      journalctl -u beacongate-server -f
      ```
      Look for `session.open` tagged with the user's `client_id`. A
      `tunnel.auth_failed` means the key in the shipped config does
      not match the server — re-issue.

- [ ] **Confirm the user reports a green status screen** before
      walking away. On the native Android app, that means status
      reads *Connected* and a destination they normally cannot reach
      loads.

---

## Routine maintenance

- [ ] **Rotate the master key every few months.**
      `beacongate-admin gen-key`, update `server_config.json`, restart
      the server, re-issue per-user configs with `add-client`. The
      old keys are hard-cut.

- [ ] **For appsscript mode: watch quota.** If the user reports
      occasional outages around the same time of day, that's the
      ~20,000-invocations-per-Google-account daily ceiling. Add
      another deployment under another Google account and re-issue
      the config.

- [ ] **Review `RESET POLICY_DENIED` events monthly.** Real users hit
      edges that synthetic tests don't. Skim the server logs for
      `tunnel.policy_denied` and decide whether each destination
      should be allowed.

---

## Related

- [mobile/android/README.md](../mobile/android/README.md) — native Android end-user walkthrough
- [docs/android-termux.md](android-termux.md) — legacy Termux walkthrough (Phase 1 path)
- [docs/troubleshooting.md](troubleshooting.md) — failure-mode runbook
- [docs/policy.md](policy.md) — adding, removing, auditing policy rules
- [docs/deployment.md](deployment.md) — server deployment and config templates
