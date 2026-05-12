package com.beacongate

/**
 * The full lifecycle for the BeaconGate Android app.
 *
 * Modeled as a sealed class so the UI can `when`-exhaustive-match
 * over every state without an `else` branch — a refactor that adds a
 * new state forces every handler to acknowledge it.
 *
 * Naming convention: every state is a verb-as-noun describing what
 * the user can see RIGHT NOW. `Connecting` means "the connect-attempt
 * is in flight"; `Importing` means "the file/Drive fetch is in
 * flight". Failures carry a human-readable reason the UI displays
 * verbatim (no localization in v1).
 *
 * Transitions (state → state, trigger):
 *
 *   Empty           → Importing       (user taps Import)
 *   Importing       → Idle            (import succeeded)
 *   Importing       → ImportFailed    (import error)
 *   ImportFailed    → Empty           (user taps Dismiss)
 *   Idle            → VpnConsent      (user taps Connect; first run only)
 *   Idle            → Connecting      (user taps Connect; consent already granted)
 *   VpnConsent      → Connecting      (user accepts system dialog)
 *   VpnConsent      → Idle            (user dismisses system dialog)
 *   Connecting      → Connected       (transport probe succeeds)
 *   Connected       → Degraded        (probe timeout/failure)
 *   Degraded        → Connected       (probe recovers)
 *   Connecting      → Failed          (start failed: SOCKS bind, transport, etc.)
 *   Connected       → Idle            (user taps Disconnect)
 *   Connected       → Failed          (transport went unhealthy)
 *   Failed          → Idle            (user taps Dismiss)
 *
 * **Empty vs Idle**: distinct on purpose. `Empty` means no config in
 * the credential store (fresh install). `Idle` means a config is
 * stored and the tunnel is not running. The UI shows a different
 * primary action: `Empty` shows "Import config"; `Idle` shows
 * "Connect" (with a small "Re-import" link at the bottom).
 */
sealed class ConnectionState {

    /** No config has been imported yet. UI shows the import options. */
    data object Empty : ConnectionState()

    /** Import in progress (Drive fetch or file read). */
    data object Importing : ConnectionState()

    /** Import failed. Carries a UI-displayable reason. */
    data class ImportFailed(val reason: String) : ConnectionState()

    /**
     * Config is stored, no tunnel running. [friendName] is the
     * client_id from the imported config (e.g. "mahdi"); shown in
     * the UI as "Imported: mahdi" so the friend can confirm
     * they're using the right config.
     */
    data class Idle(val friendName: String) : ConnectionState()

    /**
     * The Android system needs to display the VPN-consent dialog
     * before we can call `VpnService.Builder().establish()`. The
     * Activity owns the dialog flow; this state just signals that
     * the ViewModel is paused awaiting the result.
     */
    data class VpnConsent(val friendName: String) : ConnectionState()

    /**
     * Connect in flight. `Bindings.startTunnel()` has been called;
     * waiting for the first probe to flip transport_healthy.
     */
    data class Connecting(val friendName: String) : ConnectionState()

    /** Tunnel up. UI shows the disconnect button. */
    data class Connected(val friendName: String) : ConnectionState()

    /** Tunnel up, but health probes are currently failing. */
    data class Degraded(val friendName: String, val reason: String) : ConnectionState()

    /** Tunnel failed. Carries a UI-displayable reason. */
    data class Failed(val reason: String, val friendName: String?) : ConnectionState()

    /**
     * Convenience: the friend's display name when one is available
     * for this state. Idle/Connecting/Connected/VpnConsent expose
     * the imported name; Empty/Importing/ImportFailed/Failed-with-no-config
     * return null.
     */
    fun friendNameOrNull(): String? = when (this) {
        is Idle -> friendName
        is VpnConsent -> friendName
        is Connecting -> friendName
        is Connected -> friendName
        is Degraded -> friendName
        is Failed -> friendName
        Empty, Importing -> null
        is ImportFailed -> null
    }
}
