package com.beacongate

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.Context
import android.content.Intent
import android.content.pm.ServiceInfo
import android.net.VpnService
import android.os.Build
import android.os.ParcelFileDescriptor
import android.util.Log
import androidx.core.app.NotificationCompat
import bindings.Bindings
import bindings.ConfigSnapshot
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.launch

/**
 * Android VPN-tunnel service. Bridges the OS's TUN device to
 * BeaconGate's local SOCKS5 listener via tun2socks, all running
 * inside a single process so the gomobile-bound Go runtime sees
 * everything natively.
 *
 * Lifecycle:
 *
 *   onStartCommand(action=ACTION_START)
 *     → Bindings.importConfig(stored .bg JSON)
 *     → VpnService.Builder().establish() → ParcelFileDescriptor
 *     → Bindings.startVpn(snapshot, fd) (which itself starts
 *       SOCKS5 listener + tun2socks)
 *     → startForeground(NOTIFICATION_ID, ongoing notification)
 *     → TunnelStateRepository.markRunning()
 *
 *   onStartCommand(action=ACTION_STOP)
 *     → Bindings.stopVpn()
 *     → close TUN ParcelFileDescriptor
 *     → stopSelf()
 *
 *   onDestroy
 *     → idempotent cleanup of the above
 *
 * **Why startForeground.** Android requires VPN services to be
 * foreground (Android 8+ background restrictions; Android 14+
 * adds explicit FOREGROUND_SERVICE_TYPE_VPN typing). Without it,
 * the OS kills the service within seconds of the user backgrounding
 * the app — friends don't expect their tunnel to die just because
 * they switched to Telegram.
 *
 * **No service binding.** The Activity/ViewModel observe state via
 * [TunnelStateRepository] (a process-wide singleton). Cheaper than
 * AIDL/Binder for the small surface we need.
 */
class BeaconVpnService : VpnService() {

    private var tunPfd: ParcelFileDescriptor? = null

    /**
     * Coroutine scope for tunnel startup/shutdown work. SupervisorJob
     * so a startup failure on one branch doesn't cancel the other.
     */
    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.IO)
    private var startJob: Job? = null

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        when (intent?.action) {
            ACTION_STOP -> {
                shutdown(reason = null)
                return START_NOT_STICKY
            }
            // ACTION_START or null (system restart) both fall through
            // to the start path. START_NOT_STICKY below means the OS
            // does NOT auto-restart us if killed; the friend has to
            // tap Connect again. This is the correct semantic — a
            // restarted tunnel without the app's coordination would
            // race against the credential store load.
            else -> {
                if (startJob?.isActive == true) {
                    Log.d(TAG, "Start ignored: a start is already in flight.")
                    return START_NOT_STICKY
                }
                startJob = scope.launch { runStart() }
            }
        }
        return START_NOT_STICKY
    }

    /**
     * Asynchronous startup. Runs entirely on Dispatchers.IO so the
     * service's main thread stays responsive (the OS still
     * dispatches lifecycle callbacks on it).
     */
    private suspend fun runStart() {
        TunnelStateRepository.markStarting()
        startForegroundOngoing()

        // 1. Re-import the stored config. We re-validate every time
        //    instead of caching a Snapshot, so if the operator
        //    rotates a friend's key (revoke + re-add) and the user
        //    re-imports via the UI, the next Connect picks up the
        //    new credentials cleanly.
        val store = CredentialStore(applicationContext)
        val json = store.loadJson()
        if (json.isNullOrBlank()) {
            failAndStop("No config imported. Open BeaconGate and import a config first.")
            return
        }
        val snapshot: ConfigSnapshot = try {
            Bindings.importConfig(json)
        } catch (t: Throwable) {
            failAndStop("Stored config failed validation: ${t.message}")
            return
        }

        // 2. Build the TUN device. Address 10.0.0.2/32 + default
        //    route 0.0.0.0/0 captures all phone traffic. DNS
        //    server 1.1.1.1 is what the captured DNS queries hit
        //    (resolved through the tunnel like any other UDP/TCP
        //    flow). MTU matches the default tun2socks expects.
        val pfd: ParcelFileDescriptor? = Builder()
            .setSession(getString(R.string.app_name))
            .addAddress("10.0.0.2", 32)
            .addRoute("0.0.0.0", 0)
            .addDnsServer("1.1.1.1")
            .setMtu(TUN_MTU)
            .establish()
        if (pfd == null) {
            failAndStop("Could not establish the TUN device. Check VPN permission.")
            return
        }
        tunPfd = pfd

        // 3. Start the BeaconGate runtime + tun2socks. detachFd
        //    transfers ownership to the Go side; the Go runtime
        //    closes it on Bindings.stopVpn.
        val tunFd = pfd.detachFd()
        try {
            Bindings.startVpn(snapshot, tunFd.toLong())
        } catch (t: Throwable) {
            // Bindings.startVpn cleaned up its own state on
            // failure, but we still have the unforked TUN fd to
            // close if detachFd transferred ownership and Go
            // didn't accept it.
            try {
                ParcelFileDescriptor.adoptFd(tunFd).close()
            } catch (_: Throwable) {
                // best-effort; if Go closed it, this races and is
                // harmless.
            }
            failAndStop("Could not start tunnel: ${t.message}")
            return
        }

        TunnelStateRepository.markRunning()
        Log.i(TAG, "VPN running for ${snapshot.clientID}")
    }

    /**
     * Common path for any startup failure: log, mark the repository
     * with the reason, drop the foreground notification, stopSelf.
     */
    private fun failAndStop(reason: String) {
        Log.w(TAG, "VPN start failed: $reason")
        TunnelStateRepository.markError(reason)
        // Don't bother closing tunPfd here — runStart's own catch
        // blocks owned it. Just stop the service.
        stopForeground(STOP_FOREGROUND_REMOVE)
        stopSelf()
    }

    /**
     * Symmetric shutdown. Safe to call from any thread; relies on
     * Bindings.stopVpn being idempotent.
     */
    private fun shutdown(reason: String?) {
        startJob?.cancel()
        try {
            Bindings.stopVpn()
        } catch (t: Throwable) {
            Log.w(TAG, "stopVpn failed: ${t.message}")
        }
        try {
            tunPfd?.close()
        } catch (t: Throwable) {
            Log.w(TAG, "tun fd close failed: ${t.message}")
        }
        tunPfd = null
        if (reason == null) {
            TunnelStateRepository.markDisconnected()
        } else {
            TunnelStateRepository.markError(reason)
        }
        stopForeground(STOP_FOREGROUND_REMOVE)
        stopSelf()
    }

    override fun onDestroy() {
        shutdown(reason = null)
        super.onDestroy()
    }

    /**
     * Show the persistent notification while the VPN is up. Tapping
     * it returns the user to MainActivity. Required by Android's
     * background-service rules — without it the OS kills the
     * service within seconds.
     */
    private fun startForegroundOngoing() {
        ensureNotificationChannel()
        val openAppIntent = PendingIntent.getActivity(
            this,
            0,
            Intent(this, MainActivity::class.java).apply {
                addFlags(Intent.FLAG_ACTIVITY_NEW_TASK or Intent.FLAG_ACTIVITY_CLEAR_TOP)
            },
            PendingIntent.FLAG_IMMUTABLE,
        )
        val notification: Notification = NotificationCompat.Builder(this, NOTIFICATION_CHANNEL_ID)
            .setContentTitle(getString(R.string.app_name))
            .setContentText(getString(R.string.notification_running))
            .setSmallIcon(android.R.drawable.ic_lock_lock)
            .setContentIntent(openAppIntent)
            .setOngoing(true)
            .setPriority(NotificationCompat.PRIORITY_LOW)
            .build()

        // FOREGROUND_SERVICE_TYPE_SPECIAL_USE is required by
        // Android 14 (API 34) for foreground services that aren't
        // covered by a more specific type. VpnService doesn't have
        // its own foreground type prior to API 35; using
        // SPECIAL_USE keeps us compatible across the API range.
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.UPSIDE_DOWN_CAKE) {
            startForeground(
                NOTIFICATION_ID,
                notification,
                ServiceInfo.FOREGROUND_SERVICE_TYPE_SPECIAL_USE,
            )
        } else {
            startForeground(NOTIFICATION_ID, notification)
        }
    }

    private fun ensureNotificationChannel() {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.O) return
        val mgr = getSystemService(Context.NOTIFICATION_SERVICE) as NotificationManager
        if (mgr.getNotificationChannel(NOTIFICATION_CHANNEL_ID) != null) return
        val channel = NotificationChannel(
            NOTIFICATION_CHANNEL_ID,
            getString(R.string.notification_channel_name),
            NotificationManager.IMPORTANCE_LOW, // no sound, no heads-up
        ).apply {
            description = "BeaconGate VPN status"
            setShowBadge(false)
        }
        mgr.createNotificationChannel(channel)
    }

    companion object {
        private const val TAG = "BeaconVpnService"

        const val ACTION_START = "com.beacongate.action.START_VPN"
        const val ACTION_STOP = "com.beacongate.action.STOP_VPN"

        private const val NOTIFICATION_CHANNEL_ID = "beacongate_vpn_v1"
        private const val NOTIFICATION_ID = 1001
        private const val TUN_MTU = 1500

        /** Convenience for the Activity / ViewModel side. */
        fun startIntent(context: Context): Intent =
            Intent(context, BeaconVpnService::class.java).setAction(ACTION_START)

        /** Convenience for the disconnect path. */
        fun stopIntent(context: Context): Intent =
            Intent(context, BeaconVpnService::class.java).setAction(ACTION_STOP)
    }
}
