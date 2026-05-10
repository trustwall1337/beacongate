package com.beacongate

import android.content.Context
import androidx.security.crypto.EncryptedSharedPreferences
import androidx.security.crypto.MasterKey

/**
 * On-device store for the imported `.bg` config JSON.
 *
 * Backed by [EncryptedSharedPreferences] — the JSON payload is
 * authenticated-encrypted at rest with an AES-256-GCM key whose
 * master key lives in the Android Keystore. The Keystore master key
 * is hardware-backed on devices with a TEE/StrongBox (most modern
 * Iranian-sold phones); on devices without one, it falls back to
 * software but is still scoped to this app's UID.
 *
 * The threat model this addresses: a friend's phone is seized at a
 * border / by an authority who can pull the userdata partition but
 * does not have root + Keystore extraction tooling. Without
 * encryption, the imported `.bg` (which contains the per-friend AES
 * key) would be plain JSON in
 * `/data/data/com.beacongate/shared_prefs/`. With encryption, an
 * attacker needs to defeat the Keystore — which raises the bar
 * substantially.
 *
 * **Single-profile assumption.** v1 holds at most one config (the
 * one the friend imported). Multiple-profile support is a v2
 * decision that requires UI changes anyway, so this class doesn't
 * expose a list.
 */
class CredentialStore(context: Context) {

    /**
     * EncryptedSharedPreferences instance. Created once per
     * CredentialStore instance — the Jetpack Security wrapper does
     * its own caching internally, so cheap repeated calls.
     *
     * The master key spec: AES256_GCM, default validity (the
     * Keystore-default; effectively never expires for this use
     * case). User authentication NOT required — we want the
     * tunnel to come up after a phone reboot without forcing the
     * friend to unlock-then-unlock-app.
     */
    private val prefs by lazy {
        val masterKey = MasterKey.Builder(context)
            .setKeyScheme(MasterKey.KeyScheme.AES256_GCM)
            .build()
        EncryptedSharedPreferences.create(
            context,
            PREFS_FILE_NAME,
            masterKey,
            EncryptedSharedPreferences.PrefKeyEncryptionScheme.AES256_SIV,
            EncryptedSharedPreferences.PrefValueEncryptionScheme.AES256_GCM,
        )
    }

    /**
     * Persist the imported `.bg` JSON. Overwrites any existing
     * stored config; v1 is single-profile.
     *
     * @param json raw config JSON (NOT a `bg://` URI — those are
     *             decoded upstream in [bindings.Bindings.importConfig]
     *             before reaching this layer; we always store the
     *             canonical JSON form for size and stability).
     * @param friendName operator-assigned client_id, used as the
     *                   display label (e.g. "mahdi"). Stored
     *                   alongside the JSON so the UI can show
     *                   "Imported: <name>" without re-parsing the
     *                   payload.
     */
    fun save(json: String, friendName: String) {
        prefs.edit()
            .putString(KEY_CONFIG_JSON, json)
            .putString(KEY_FRIEND_NAME, friendName)
            .apply()
    }

    /**
     * Returns the previously-stored JSON, or null if no config has
     * been imported yet (fresh install or after [clear]).
     */
    fun loadJson(): String? = prefs.getString(KEY_CONFIG_JSON, null)

    /**
     * Returns the friend's display name (the imported config's
     * client_id), or null when no config is stored.
     */
    fun friendName(): String? = prefs.getString(KEY_FRIEND_NAME, null)

    /** Whether a config has been imported and persisted. */
    fun isImported(): Boolean = loadJson() != null

    /**
     * Wipe the stored config. Used by an "import a different
     * config" flow (Step 5 polish) and by the upcoming v2
     * "factory reset" UI. Safe to call when no config is stored.
     */
    fun clear() {
        prefs.edit()
            .remove(KEY_CONFIG_JSON)
            .remove(KEY_FRIEND_NAME)
            .apply()
    }

    companion object {
        // Filename of the encrypted prefs file under
        // /data/data/com.beacongate/shared_prefs/. Versioned so a
        // future schema migration can be a no-op for old installs
        // (we just ignore the v1 file and read from a v2 one).
        private const val PREFS_FILE_NAME = "beacongate_creds_v1"

        private const val KEY_CONFIG_JSON = "config_json"
        private const val KEY_FRIEND_NAME = "friend_name"
    }
}
