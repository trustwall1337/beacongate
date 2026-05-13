package com.beacongate

import android.content.Context

/**
 * Stores VPN traffic-scope preferences:
 * - routeAllTraffic=true  => tunnel all apps (default)
 * - routeAllTraffic=false => tunnel only selected packages
 */
class TrafficScopeStore(context: Context) {

    private val prefs = context.getSharedPreferences(PREFS_FILE_NAME, Context.MODE_PRIVATE)

    // Default to "route all apps" on a fresh install. Per-app
    // selective routing is a v2 feature; for v1 we want friends to
    // see "tap Connect → everything works." If a user explicitly
    // toggles the switch off later, the false value is stored and
    // honored on subsequent launches.
    fun routeAllTraffic(): Boolean = prefs.getBoolean(KEY_ROUTE_ALL, true)

    fun setRouteAllTraffic(enabled: Boolean) {
        prefs.edit().putBoolean(KEY_ROUTE_ALL, enabled).apply()
    }

    fun selectedPackages(): Set<String> =
        prefs.getStringSet(KEY_SELECTED_PACKAGES, DEFAULT_SELECTED_PACKAGES) ?: DEFAULT_SELECTED_PACKAGES

    fun setSelectedPackages(pkgs: Set<String>) {
        prefs.edit().putStringSet(KEY_SELECTED_PACKAGES, pkgs).apply()
    }

    companion object {
        private const val PREFS_FILE_NAME = "beacongate_traffic_scope_v1"
        private const val KEY_ROUTE_ALL = "route_all_traffic"
        private const val KEY_SELECTED_PACKAGES = "selected_packages"

        // Default app-scope set requested by product:
        // X, YouTube, Telegram, Instagram, WhatsApp.
        private val DEFAULT_SELECTED_PACKAGES = setOf(
            "com.twitter.android",
            "com.google.android.youtube",
            "org.telegram.messenger",
            "com.instagram.android",
            "com.whatsapp",
        )
    }
}
