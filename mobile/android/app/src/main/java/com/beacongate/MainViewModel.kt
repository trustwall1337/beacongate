package com.beacongate

import android.content.ContentResolver
import android.net.Uri
import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewModelScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext

/**
 * Owns the [ConnectionState] for the single-screen UI.
 *
 * **Why a ViewModel.** Activity recreations (rotation, dark-mode
 * switches, low-memory recycle) destroy the Activity. Holding the
 * state in a ViewModel preserves it across recreations without
 * persisting to disk — exactly the right scope.
 *
 * **What this class does NOT own.** The actual VPN service lifetime
 * (BeaconVpnService) and the JNI bridge to the Go runtime
 * (Bindings.startTunnel etc.) live in the foreground service.
 * MainViewModel only:
 *   - Reads / writes the credential store.
 *   - Runs the import flow off the main thread.
 *   - Tracks UI state (`StateFlow<ConnectionState>`).
 *   - Asks the Activity to dispatch the VPN-consent intent at the
 *     right moment (via [vpnConsentRequest]).
 *
 * Step 6 (this commit) does NOT yet wire the service-bound state
 * polling — that's part of Step 7. Today's surface is enough for
 * the import flow + the UI state-machine plumbing; the Connect
 * button transitions through Connecting → Failed with a
 * "Step 7 pending" reason so the failure-state branch is exercised
 * end-to-end before VpnService lands.
 */
class MainViewModel(
    private val store: CredentialStore,
) : ViewModel() {

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

    /** Initial state at ViewModel construction time. */
    private fun initialState(): ConnectionState =
        if (store.isImported()) {
            ConnectionState.Idle(friendName = store.friendName().orEmpty())
        } else {
            ConnectionState.Empty
        }

    /** User tapped "Import from Drive link". */
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

    /** User picked a file via the system file picker. */
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

    /** User dismissed an import-failure or connect-failure banner. */
    fun dismissError() {
        _state.value = initialState()
        _vpnConsentRequest.value = false
    }

    /** User tapped Connect. */
    fun onConnectClicked() {
        val current = _state.value
        if (current !is ConnectionState.Idle) return
        // Step 6 placeholder: ask the Activity to fire the system
        // VPN-consent intent. Once it returns OK, the Activity
        // calls onVpnConsentGranted; on cancel, onVpnConsentDenied.
        _state.value = ConnectionState.VpnConsent(current.friendName)
        _vpnConsentRequest.value = true
    }

    /** Activity has dispatched the consent intent; clear the trigger. */
    fun onVpnConsentDispatched() {
        _vpnConsentRequest.value = false
    }

    /** System dialog returned OK. Step 7 will start BeaconVpnService here. */
    fun onVpnConsentGranted() {
        val current = _state.value
        val name = current.friendNameOrNull() ?: return
        _state.value = ConnectionState.Connecting(name)
        // STEP 7: start BeaconVpnService here. Until then we
        // synthetically transition to Failed so the UI can
        // exercise that branch end-to-end.
        viewModelScope.launch {
            _state.value = ConnectionState.Failed(
                reason = "VpnService integration is pending (Step 7).",
                friendName = name,
            )
        }
    }

    /** System dialog returned CANCELED. Drop back to Idle. */
    fun onVpnConsentDenied() {
        _state.value = initialState()
        _vpnConsentRequest.value = false
    }

    /** User tapped Disconnect. Step 7 will stop the VpnService. */
    fun onDisconnectClicked() {
        val name = _state.value.friendNameOrNull() ?: return
        _state.value = ConnectionState.Idle(name)
        // STEP 7: stop BeaconVpnService here.
    }

    /** User tapped "Re-import" / "Use a different config". */
    fun clearStoredConfig() {
        store.clear()
        _state.value = ConnectionState.Empty
    }
}
