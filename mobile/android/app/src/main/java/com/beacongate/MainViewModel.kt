package com.beacongate

import android.app.Application
import android.content.ContentResolver
import android.content.Intent
import android.net.Uri
import android.os.Build
import androidx.core.content.ContextCompat
import androidx.lifecycle.AndroidViewModel
import androidx.lifecycle.viewModelScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext

/**
 * Owns the [ConnectionState] for the single-screen UI. Reconciles
 * the UI state with the live tunnel state from
 * [TunnelStateRepository], so the activity never has to bind to the
 * service.
 *
 * **Why an AndroidViewModel.** We need a [Context] to start/stop
 * [BeaconVpnService] and to construct the credential store; the
 * `Application` context is the longest-lived non-leaky context
 * available, exactly what AndroidViewModel provides.
 *
 * **What this class does NOT own.** The actual VpnService and the
 * gomobile JNI bridge to the Go runtime live in [BeaconVpnService]
 * (the foreground service). This view model only:
 *   - Reads / writes [CredentialStore].
 *   - Runs the import flow off the main thread.
 *   - Sends start/stop intents to [BeaconVpnService].
 *   - Mirrors [TunnelStateRepository.state] into the UI's
 *     [ConnectionState] so the single screen renders correctly.
 */
class MainViewModel(
    application: Application,
    private val store: CredentialStore = CredentialStore(application.applicationContext),
) : AndroidViewModel(application) {

    private val _state: MutableStateFlow<ConnectionState> = MutableStateFlow(initialState())
    val state: StateFlow<ConnectionState> = _state.asStateFlow()

    /**
     * One-shot trigger the Activity observes to launch the VPN-
     * consent dialog. Set to true when the user taps Connect on a
     * fresh install; reset to false after the Activity dispatches
     * the system intent.
     */
    private val _vpnConsentRequest = MutableStateFlow(false)
    val vpnConsentRequest: StateFlow<Boolean> = _vpnConsentRequest.asStateFlow()

    init {
        // Mirror the service's tunnel state onto the UI state. The
        // ViewModel scope outlives every Activity; this stays
        // active for the process lifetime.
        viewModelScope.launch {
            TunnelStateRepository.state.collect { tunnel ->
                applyTunnelState(tunnel)
            }
        }
    }

    /** Initial UI state at ViewModel construction time. */
    private fun initialState(): ConnectionState =
        if (store.isImported()) {
            ConnectionState.Idle(friendName = store.friendName().orEmpty())
        } else {
            ConnectionState.Empty
        }

    /**
     * Reconcile a [TunnelStateRepository.TunnelState] change into
     * the UI's [ConnectionState]. The two are not 1:1 — the UI has
     * extra states (Empty, Importing, ImportFailed, VpnConsent)
     * that the service doesn't see. Rule:
     *   - If the service is Disconnected, fall back to whichever
     *     UI state we'd be in based on the credential store.
     *   - Otherwise, project the service state onto the UI state
     *     using the friendName from whatever's currently held.
     */
    private fun applyTunnelState(tunnel: TunnelStateRepository.TunnelState) {
        val name = _state.value.friendNameOrNull() ?: store.friendName()
        _state.value = when (tunnel) {
            TunnelStateRepository.TunnelState.Disconnected -> {
                // Don't clobber Importing / ImportFailed states —
                // the service has nothing to say about those.
                when (val cur = _state.value) {
                    is ConnectionState.Importing,
                    is ConnectionState.ImportFailed -> cur
                    else -> initialState()
                }
            }
            TunnelStateRepository.TunnelState.Starting -> {
                if (name != null) ConnectionState.Connecting(name) else _state.value
            }
            TunnelStateRepository.TunnelState.Running -> {
                if (name != null) ConnectionState.Connected(name) else _state.value
            }
            is TunnelStateRepository.TunnelState.Error -> {
                ConnectionState.Failed(reason = tunnel.reason, friendName = name)
            }
        }
    }

    // --- Import flow --------------------------------------------------

    fun importFromDriveLink(url: String) {
        _state.value = ConnectionState.Importing
        viewModelScope.launch {
            val outcome = runCatching {
                withContext(Dispatchers.IO) { ConfigImport.fromDriveLink(url) }
            }
            outcome.onSuccess { result ->
                store.save(json = result.rawJson, friendName = result.snapshot.clientID)
                _state.value = ConnectionState.Idle(result.snapshot.clientID)
            }
            outcome.onFailure { err ->
                _state.value = ConnectionState.ImportFailed(
                    reason = err.message ?: "Unknown import error"
                )
            }
        }
    }

    fun importFromFilePicker(resolver: ContentResolver, uri: Uri) {
        _state.value = ConnectionState.Importing
        viewModelScope.launch {
            val outcome = runCatching {
                withContext(Dispatchers.IO) { ConfigImport.fromFilePicker(resolver, uri) }
            }
            outcome.onSuccess { result ->
                store.save(json = result.rawJson, friendName = result.snapshot.clientID)
                _state.value = ConnectionState.Idle(result.snapshot.clientID)
            }
            outcome.onFailure { err ->
                _state.value = ConnectionState.ImportFailed(
                    reason = err.message ?: "Unknown import error"
                )
            }
        }
    }

    fun dismissError() {
        _state.value = initialState()
        _vpnConsentRequest.value = false
        // If the repository is still in Error, transition it back
        // so the next Connect doesn't immediately re-error.
        if (TunnelStateRepository.state.value is TunnelStateRepository.TunnelState.Error) {
            TunnelStateRepository.markDisconnected()
        }
    }

    // --- Connect / disconnect -----------------------------------------

    fun onConnectClicked() {
        val current = _state.value
        if (current !is ConnectionState.Idle && current !is ConnectionState.Failed) return
        val name = current.friendNameOrNull() ?: return
        _state.value = ConnectionState.VpnConsent(name)
        _vpnConsentRequest.value = true
    }

    fun onVpnConsentDispatched() {
        _vpnConsentRequest.value = false
    }

    fun onVpnConsentGranted() {
        // Service start: from this point the service drives the
        // state via TunnelStateRepository. Don't manually set
        // Connecting here — the service's markStarting() does it.
        val ctx = getApplication<Application>().applicationContext
        val intent = BeaconVpnService.startIntent(ctx)
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            ContextCompat.startForegroundService(ctx, intent)
        } else {
            ctx.startService(intent)
        }
    }

    fun onVpnConsentDenied() {
        _state.value = initialState()
        _vpnConsentRequest.value = false
    }

    fun onDisconnectClicked() {
        val ctx = getApplication<Application>().applicationContext
        ctx.startService(BeaconVpnService.stopIntent(ctx))
    }

    fun clearStoredConfig() {
        store.clear()
        _state.value = ConnectionState.Empty
    }
}
