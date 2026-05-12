package com.beacongate

import android.app.Application
import android.content.Context

/**
 * Application subclass — installs the crash-capture handler at the
 * earliest possible point in the process lifecycle.
 *
 * **Why attachBaseContext, not onCreate.** attachBaseContext runs
 * BEFORE the Application's onCreate, before any Activity is
 * resolved, before any class loader has touched our app's other
 * classes. If the crash is in an early static initializer (e.g.
 * gomobile's go.Seq class loading the .so library), this is our
 * only shot at trapping it.
 *
 * **No business logic here.** Initialization belongs in
 * Application.onCreate (which we don't override). attachBaseContext
 * stays minimal so it cannot itself become a source of crashes.
 */
class BeaconApp : Application() {
    override fun attachBaseContext(base: Context) {
        super.attachBaseContext(base)
        // Probe BEFORE installing the crash handler so even if the
        // handler itself fails to install we have at least one file
        // proving Java code ran.
        BootProbe.mark(base, 1, "attachBaseContext")
        CrashCapture.install(base)
        BootProbe.mark(base, 2, "handler_installed")
    }

    override fun onCreate() {
        super.onCreate()
        BootProbe.mark(this, 3, "app_onCreate")
    }
}
