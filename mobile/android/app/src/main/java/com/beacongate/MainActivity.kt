package com.beacongate

import android.content.Intent
import android.net.VpnService
import android.os.Bundle
import android.view.View
import androidx.activity.result.contract.ActivityResultContracts
import androidx.activity.viewModels
import androidx.appcompat.app.AppCompatActivity
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.lifecycleScope
import androidx.lifecycle.repeatOnLifecycle
import bindings.Bindings
import com.beacongate.databinding.ActivityMainBinding
import kotlinx.coroutines.flow.combine
import kotlinx.coroutines.launch

/**
 * Single-screen UI. Drives the [ConnectionState] machine in
 * [MainViewModel] and dispatches the platform-specific side effects:
 * the system file picker, the Drive link input, and the VPN-consent
 * dialog launcher.
 *
 * **Why an Activity instead of Fragments.** This is a one-screen app.
 * Fragments add lifecycle complexity for zero benefit at this size;
 * Activity-only keeps the navigation surface and APK overhead lower.
 *
 * **Concurrency.** All mutations happen via the ViewModel; the
 * Activity merely observes [MainViewModel.state] and renders. UI
 * updates re-run on `Lifecycle.State.STARTED`-or-greater so the
 * Activity doesn't try to draw while paused.
 */
class MainActivity : AppCompatActivity() {

    private lateinit var binding: ActivityMainBinding

    /**
     * Default [androidx.lifecycle.AndroidViewModelFactory] is fine —
     * MainViewModel is an [AndroidViewModel] and its CredentialStore
     * defaults to one constructed from the Application context.
     */
    private val viewModel: MainViewModel by viewModels()

    /**
     * VPN-consent launcher. Android's [VpnService.prepare] returns
     * an [Intent] when the OS hasn't yet granted this app the right
     * to set up a VPN; we dispatch that intent and observe the
     * result code (RESULT_OK / RESULT_CANCELED) here.
     */
    private val vpnConsent = registerForActivityResult(
        ActivityResultContracts.StartActivityForResult()
    ) { result ->
        if (result.resultCode == RESULT_OK) {
            viewModel.onVpnConsentGranted()
        } else {
            viewModel.onVpnConsentDenied()
        }
    }

    /**
     * System file picker. Returns a content:// URI we read via the
     * ContentResolver. We use OPEN_DOCUMENT (not OPEN_DOCUMENT_TREE)
     * so the user picks a single file — their `.bg`.
     */
    private val pickConfigFile = registerForActivityResult(
        ActivityResultContracts.OpenDocument()
    ) { uri ->
        if (uri != null) {
            viewModel.importFromFilePicker(contentResolver, uri)
        }
        // Null = user backed out; leave state unchanged (still
        // Empty / ImportFailed depending on prior state).
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        binding = ActivityMainBinding.inflate(layoutInflater)
        setContentView(binding.root)

        binding.versionLine.text = getString(R.string.version_line, Bindings.version())
        wireButtons()
        observeState()
    }

    private fun wireButtons() {
        binding.fetchDriveButton.setOnClickListener {
            val url = binding.driveLinkInput.text?.toString().orEmpty().trim()
            if (url.isEmpty()) return@setOnClickListener
            viewModel.importFromDriveLink(url)
        }
        binding.pickFileButton.setOnClickListener {
            // Filter to JSON-shaped MIME types; "*/*" works around
            // the fact that some file managers tag .bg as
            // application/octet-stream.
            pickConfigFile.launch(arrayOf("application/json", "*/*"))
        }
        binding.dismissErrorButton.setOnClickListener {
            viewModel.dismissError()
        }
        binding.reimportButton.setOnClickListener {
            viewModel.clearStoredConfig()
        }
        // primaryButton's behaviour depends on state — see
        // applyState() for the click handler swap.
    }

    private fun observeState() {
        lifecycleScope.launch {
            repeatOnLifecycle(Lifecycle.State.STARTED) {
                // Combine the two flows so a state change AND a
                // VPN-consent-request both trigger the same render
                // path. (vpnConsentRequest is a one-shot trigger;
                // we dispatch the intent and tell the ViewModel to
                // clear the flag.)
                combine(
                    viewModel.state,
                    viewModel.vpnConsentRequest,
                ) { state, consentRequested -> state to consentRequested }
                    .collect { (state, consentRequested) ->
                        applyState(state)
                        if (consentRequested) {
                            launchVpnConsent()
                            viewModel.onVpnConsentDispatched()
                        }
                    }
            }
        }
    }

    private fun launchVpnConsent() {
        val intent = VpnService.prepare(this)
        if (intent == null) {
            // Already granted — fast-path through.
            viewModel.onVpnConsentGranted()
        } else {
            vpnConsent.launch(intent)
        }
    }

    /**
     * Render the current [ConnectionState]. Each branch sets:
     *   - statusGroup / importGroup / errorBanner visibility
     *   - statusValue / friendLine / errorMessage text
     *   - primaryButton text + click handler
     */
    private fun applyState(state: ConnectionState) {
        when (state) {
            ConnectionState.Empty -> renderEmpty()
            ConnectionState.Importing -> renderImporting()
            is ConnectionState.ImportFailed -> renderImportFailed(state.reason)
            is ConnectionState.Idle -> renderIdle(state.friendName)
            is ConnectionState.VpnConsent -> renderVpnConsent(state.friendName)
            is ConnectionState.Connecting -> renderConnecting(state.friendName)
            is ConnectionState.Connected -> renderConnected(state.friendName)
            is ConnectionState.Failed -> renderFailed(state.reason, state.friendName)
        }
    }

    private fun renderEmpty() {
        binding.importGroup.visibility = View.VISIBLE
        binding.statusGroup.visibility = View.GONE
        binding.errorBanner.visibility = View.GONE
    }

    private fun renderImporting() {
        binding.importGroup.visibility = View.VISIBLE
        binding.statusGroup.visibility = View.GONE
        binding.errorBanner.visibility = View.GONE
        binding.fetchDriveButton.isEnabled = false
        binding.pickFileButton.isEnabled = false
        binding.fetchDriveButton.text = getString(R.string.status_importing)
    }

    private fun renderImportFailed(reason: String) {
        binding.importGroup.visibility = View.VISIBLE
        binding.statusGroup.visibility = View.GONE
        binding.errorBanner.visibility = View.VISIBLE
        binding.errorMessage.text = reason
        binding.fetchDriveButton.isEnabled = true
        binding.pickFileButton.isEnabled = true
        binding.fetchDriveButton.text = getString(R.string.fetch_drive_button)
    }

    private fun renderIdle(friendName: String) {
        binding.importGroup.visibility = View.GONE
        binding.statusGroup.visibility = View.VISIBLE
        binding.errorBanner.visibility = View.GONE
        binding.statusValue.text = getString(R.string.status_idle)
        binding.friendLine.text = getString(R.string.friend_line, friendName)
        binding.primaryButton.isEnabled = true
        binding.primaryButton.text = getString(R.string.connect_button)
        binding.primaryButton.setOnClickListener { viewModel.onConnectClicked() }
    }

    private fun renderVpnConsent(friendName: String) {
        binding.importGroup.visibility = View.GONE
        binding.statusGroup.visibility = View.VISIBLE
        binding.errorBanner.visibility = View.GONE
        binding.statusValue.text = getString(R.string.status_vpn_consent)
        binding.friendLine.text = getString(R.string.friend_line, friendName)
        binding.primaryButton.isEnabled = false
        binding.primaryButton.text = getString(R.string.connecting_button)
    }

    private fun renderConnecting(friendName: String) {
        binding.importGroup.visibility = View.GONE
        binding.statusGroup.visibility = View.VISIBLE
        binding.errorBanner.visibility = View.GONE
        binding.statusValue.text = getString(R.string.status_connecting)
        binding.friendLine.text = getString(R.string.friend_line, friendName)
        binding.primaryButton.isEnabled = false
        binding.primaryButton.text = getString(R.string.connecting_button)
    }

    private fun renderConnected(friendName: String) {
        binding.importGroup.visibility = View.GONE
        binding.statusGroup.visibility = View.VISIBLE
        binding.errorBanner.visibility = View.GONE
        binding.statusValue.text = getString(R.string.status_connected)
        binding.friendLine.text = getString(R.string.friend_line, friendName)
        binding.primaryButton.isEnabled = true
        binding.primaryButton.text = getString(R.string.disconnect_button)
        binding.primaryButton.setOnClickListener { viewModel.onDisconnectClicked() }
    }

    private fun renderFailed(reason: String, friendName: String?) {
        binding.importGroup.visibility = if (friendName == null) View.VISIBLE else View.GONE
        binding.statusGroup.visibility = if (friendName != null) View.VISIBLE else View.GONE
        binding.errorBanner.visibility = View.VISIBLE
        binding.errorMessage.text = reason
        binding.statusValue.text = getString(R.string.status_failed)
        if (friendName != null) {
            binding.friendLine.text = getString(R.string.friend_line, friendName)
            binding.primaryButton.isEnabled = true
            binding.primaryButton.text = getString(R.string.connect_button)
            binding.primaryButton.setOnClickListener { viewModel.onConnectClicked() }
        }
    }
}
