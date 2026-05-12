package com.beacongate

import android.content.Intent
import android.content.pm.ApplicationInfo
import android.content.pm.PackageManager
import android.net.VpnService
import android.os.Bundle
import android.view.View
import androidx.activity.result.contract.ActivityResultContracts
import androidx.activity.viewModels
import androidx.appcompat.app.AlertDialog
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
    private lateinit var trafficScopeStore: TrafficScopeStore

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
        BootProbe.mark(this, 4, "activity_oncreate_entered")
        super.onCreate(savedInstanceState)
        BootProbe.mark(this, 5, "after_super_oncreate")
        binding = ActivityMainBinding.inflate(layoutInflater)
        BootProbe.mark(this, 6, "after_binding_inflate")
        setContentView(binding.root)
        BootProbe.mark(this, 7, "after_setContentView")
        trafficScopeStore = TrafficScopeStore(applicationContext)

        // Wrap the gomobile call in try/catch so a JNI loader
        // failure surfaces in the UI instead of crashing the whole
        // process before our breadcrumb at step 8 can be written.
        val version: String = try {
            Bindings.version().also { BootProbe.mark(this, 8, "after_bindings_version") }
        } catch (t: Throwable) {
            BootProbe.mark(this, 8, "bindings_version_THREW")
            "load-failed: ${t.javaClass.simpleName}"
        }
        binding.versionLine.text = getString(R.string.version_line, version)
        wireButtons()
        observeState()
        BootProbe.mark(this, 9, "activity_oncreate_complete")
    }

    override fun onStart() {
        BootProbe.mark(this, 10, "activity_onStart_entered")
        super.onStart()
        BootProbe.mark(this, 11, "activity_onStart_exit")
    }

    override fun onResume() {
        BootProbe.mark(this, 12, "activity_onResume_entered")
        super.onResume()
        BootProbe.mark(this, 13, "activity_onResume_exit")
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
        binding.routeAllSwitch.setOnCheckedChangeListener { _, isChecked ->
            trafficScopeStore.setRouteAllTraffic(isChecked)
            renderTrafficScopeControls()
        }
        binding.selectAppsButton.setOnClickListener {
            openAppPickerDialog()
        }
        renderTrafficScopeControls()
        // primaryButton's behaviour depends on state — see
        // applyState() for the click handler swap.
    }

    private data class AppOption(val label: String, val packageName: String)

    private fun listLaunchableApps(): List<AppOption> {
        val pm = packageManager
        val intent = Intent(Intent.ACTION_MAIN).addCategory(Intent.CATEGORY_LAUNCHER)
        return pm.queryIntentActivities(intent, PackageManager.MATCH_ALL)
            .mapNotNull { ri ->
                val pkg = ri.activityInfo?.packageName ?: return@mapNotNull null
                if (pkg == packageName) return@mapNotNull null
                val appInfo: ApplicationInfo = ri.activityInfo.applicationInfo ?: return@mapNotNull null
                val label = pm.getApplicationLabel(appInfo).toString().ifBlank { pkg }
                AppOption(label = label, packageName = pkg)
            }
            .distinctBy { it.packageName }
            .sortedBy { it.label.lowercase() }
    }

    private fun openAppPickerDialog() {
        val apps = listLaunchableApps()
        if (apps.isEmpty()) {
            AlertDialog.Builder(this)
                .setMessage(getString(R.string.pick_apps_empty))
                .setPositiveButton(android.R.string.ok, null)
                .show()
            return
        }
        val installed = apps.map { it.packageName }.toSet()
        // Prune any "ghost" packages from the stored set up front so the
        // picker's pre-checked state matches what's actually installable —
        // otherwise a user who selects nothing visible still ends up with
        // the original default ghosts persisting in storage.
        val selected = (trafficScopeStore.selectedPackages() intersect installed).toMutableSet()
        val labels = apps.map { it.label }.toTypedArray()
        val checked = apps.map { selected.contains(it.packageName) }.toBooleanArray()
        AlertDialog.Builder(this)
            .setTitle(getString(R.string.pick_apps_title))
            .setMultiChoiceItems(labels, checked) { _, which, isChecked ->
                val pkg = apps[which].packageName
                if (isChecked) selected.add(pkg) else selected.remove(pkg)
            }
            .setPositiveButton(android.R.string.ok) { _, _ ->
                // Persist only currently-installed selections; ghosts
                // (defaults / uninstalled apps) get pruned on save so the
                // saved set, the displayed count, and what the VPN actually
                // allows all agree.
                val pruned = selected intersect installed
                trafficScopeStore.setSelectedPackages(pruned)
                renderTrafficScopeControls()
            }
            .setNegativeButton(android.R.string.cancel, null)
            .show()
    }

    private fun renderTrafficScopeControls() {
        val routeAll = trafficScopeStore.routeAllTraffic()
        if (binding.routeAllSwitch.isChecked != routeAll) {
            binding.routeAllSwitch.isChecked = routeAll
        }
        binding.selectAppsButton.isEnabled = !routeAll
        // The displayed count must equal the number of apps the VPN will
        // actually route — i.e. selected ∩ installed. Without this filter
        // the count includes "ghost" packages (defaults that aren't on the
        // device, or apps the user uninstalled), which the VPN silently
        // drops at startup and confuses the operator.
        val installed = listLaunchableApps().map { it.packageName }.toSet()
        val effective = trafficScopeStore.selectedPackages() intersect installed
        binding.appsSelectedLine.text = getString(R.string.apps_selected_line, effective.size)
        binding.appsSelectedLine.alpha = if (routeAll) 0.5f else 1.0f
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
            is ConnectionState.Degraded -> renderDegraded(state.friendName, state.reason)
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

    private fun renderDegraded(friendName: String, reason: String) {
        binding.importGroup.visibility = View.GONE
        binding.statusGroup.visibility = View.VISIBLE
        binding.errorBanner.visibility = View.VISIBLE
        binding.errorMessage.text = reason
        binding.statusValue.text = getString(R.string.status_degraded)
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
