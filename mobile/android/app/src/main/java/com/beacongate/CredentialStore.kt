package com.beacongate

import android.content.Context
import android.content.SharedPreferences

/**
 * On-device store for the imported `.bg` config JSON.
 *
 * v1 implementation: plain UID-scoped [SharedPreferences].
 *
 * **Original plan was [androidx.security.crypto.EncryptedSharedPreferences],**
 * but the 1.1.0-alpha06 release is unstable on certain Samsung
 * Android 12/13 builds — Tink's Keystore initialization crashes
 * native-side at app launch, in a way that bypasses Java's
 * UncaughtExceptionHandler. We hit that bring-up bug on Samsung
 * SM-G988B (Android 13 / SDK 33) during first-friend testing.
 *
 * **Threat model trade-off.** Plain SharedPreferences are stored at
 * `/data/data/com.beacongate/shared_prefs/<file>.xml`, owned by
 * this app's UID, mode 0600. The threat surface vs. encryption:
 *
 *   - Non-rooted attacker with physical access: unchanged.
 *     `/data/data` is unreadable without root or `adb backup -ab`
 *     (which is disabled — `android:allowBackup="false"`).
 *   - Rooted attacker with physical access: marginally weaker. With
 *     EncryptedSharedPreferences they'd also need to extract the
 *     Keystore master key (hardware-backed on most devices, raises
 *     the bar). With plain SharedPreferences they read the JSON
 *     directly. Per-friend revocation
 *     (server-side `beacongate-admin revoke-client`) mitigates this:
 *     a stolen `.bg` is one VPS command away from being dead.
 *   - Other apps on the device: unchanged. UID-scoped storage
 *     prevents cross-app reads in either case.
 *
 * Net: this is a small step down, mitigated by per-friend
 * revocation. We can revisit encrypted storage in v1.1 once the
 * Tink alpha stabilizes or we switch to a self-managed AES-GCM
 * wrapper around the same `SharedPreferences` keys.
 */
class CredentialStore(context: Context) {

    private val prefs: SharedPreferences =
        context.getSharedPreferences(PREFS_FILE_NAME, Context.MODE_PRIVATE)

    /**
     * Persist the imported `.bg` JSON. Overwrites any existing
     * stored config; v1 is single-profile.
     */
    fun save(json: String, friendName: String) {
        prefs.edit()
            .putString(KEY_CONFIG_JSON, json)
            .putString(KEY_FRIEND_NAME, friendName)
            .apply()
    }

    /** Returns the previously-stored JSON, or null if nothing stored. */
    fun loadJson(): String? = prefs.getString(KEY_CONFIG_JSON, null)

    /** Returns the friend's display name, or null when no config is stored. */
    fun friendName(): String? = prefs.getString(KEY_FRIEND_NAME, null)

    /** Whether a config has been imported and persisted. */
    fun isImported(): Boolean = loadJson() != null

    /** Wipe the stored config. Safe to call when nothing is stored. */
    fun clear() {
        prefs.edit()
            .remove(KEY_CONFIG_JSON)
            .remove(KEY_FRIEND_NAME)
            .apply()
    }

    companion object {
        // Filename of the prefs file under
        // /data/data/com.beacongate/shared_prefs/. Versioned so a
        // future schema migration can ignore the v1 file.
        private const val PREFS_FILE_NAME = "beacongate_creds_v1"

        private const val KEY_CONFIG_JSON = "config_json"
        private const val KEY_FRIEND_NAME = "friend_name"
    }
}
