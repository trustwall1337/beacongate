package com.beacongate

import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow

/**
 * Process-wide singleton sharing the live tunnel state between
 * [BeaconVpnService] (which owns the VPN side effects) and
 * [MainViewModel] (which renders the UI).
 *
 * Why a singleton instead of service-binding: service-binding adds
 * a binder.aidl, ServiceConnection lifecycle, and an extra layer of
 * IPC that's wasted overhead when the Service and the ViewModel
 * live in the same process. A static `object` exposing
 * [StateFlow]s gives both sides type-safe observation without the
 * boilerplate. (LocalBroadcastManager would be the third option but
 * is deprecated in Android 14+.)
 *
 * **Lifecycle ownership.** This repository tracks state, not
 * resources. The actual `Bindings.startVpn` / `stopVpn` calls happen
 * in [BeaconVpnService]; this object only mirrors what that service
 * has done so the UI can reflect it. If the process is killed and
 * recreated, both sides start fresh and the repository's defaults
 * (Idle / null) are correct again.
 *
 * **Thread safety.** All exposed mutators are atomic via
 * [MutableStateFlow]; observers via [StateFlow] are coroutine-safe
 * by construction.
 */
object TunnelStateRepository {

    /**
     * Sub-state for the tunnel layer specifically. The full UI
     * [ConnectionState] is a richer type that also covers import
     * states (Empty / Importing / ImportFailed) which the service
     * doesn't know about. The ViewModel reconciles the two.
     */
    sealed class TunnelState {
        /** No VPN running. The default at process start. */
        data object Disconnected : TunnelState()

        /**
         * Service has started but Bindings.startVpn hasn't returned
         * yet. Brief — usually <1s for the SOCKS bind + tun2socks
         * netstack handshake.
         */
        data object Starting : TunnelState()

        /** VPN is up and forwarding packets. */
        data object Running : TunnelState()

        /**
         * VPN is up but transport probes are currently failing.
         * Traffic may be partially degraded.
         */
        data class Degraded(val reason: String) : TunnelState()

        /**
         * Service tried to start but a layer failed. Carries the
         * platform-displayable reason. The service has already
         * cleaned up (closed tun fd, called Bindings.stopVpn);
         * the UI just shows the message and lets the user retry.
         */
        data class Error(val reason: String) : TunnelState()
    }

    private val _state: MutableStateFlow<TunnelState> = MutableStateFlow(TunnelState.Disconnected)

    /**
     * Read-only state flow. UI observes this; the service writes
     * via the package-private setters below.
     */
    val state: StateFlow<TunnelState> = _state.asStateFlow()

    /** Service signals: the tunnel start sequence has begun. */
    internal fun markStarting() {
        _state.value = TunnelState.Starting
    }

    /** Service signals: Bindings.startVpn returned successfully. */
    internal fun markRunning() {
        _state.value = TunnelState.Running
    }

    /** Service signals: VPN is up but health checks are failing. */
    internal fun markDegraded(reason: String) {
        _state.value = TunnelState.Degraded(reason)
    }

    /** Service signals: a layer failed; everything is torn down. */
    internal fun markError(reason: String) {
        _state.value = TunnelState.Error(reason)
    }

    /** Service signals: clean shutdown completed. */
    internal fun markDisconnected() {
        _state.value = TunnelState.Disconnected
    }

    /**
     * Test hook to reset the singleton between unit tests. Should
     * NOT be called from production code (the service's normal
     * lifecycle handles all transitions).
     */
    @Suppress("unused")
    internal fun resetForTesting() {
        _state.value = TunnelState.Disconnected
    }
}
