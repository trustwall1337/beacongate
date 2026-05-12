package com.beacongate

import android.content.ContentValues
import android.content.Context
import android.os.Build
import android.os.Environment
import android.provider.MediaStore
import android.util.Log
import java.io.File
import java.io.PrintWriter
import java.io.StringWriter
import java.text.SimpleDateFormat
import java.util.Date
import java.util.Locale

/**
 * One-shot diagnostic: catch every uncaught exception process-wide,
 * write a human-readable report to a place the user can copy out of
 * the phone without `adb`, then re-throw so the OS still tears the
 * process down.
 *
 * Two write targets, in order of preference:
 *
 *   1. **MediaStore Downloads** (Android 10+ / API 29+). Writes
 *      `Download/beacongate_crash_<timestamp>.txt`. Universally
 *      visible in any Files manager — no permission, no
 *      Android-data folder navigation, no scoped-storage friction.
 *
 *   2. **App's private external dir** (every API). Writes
 *      `/sdcard/Android/data/com.beacongate/files/last_crash.txt`.
 *      Samsung's My Files exposes this; some third-party file
 *      managers don't on Android 11+. Used as fallback if MediaStore
 *      fails.
 *
 * **Why install at attachBaseContext.** This handler must be in
 * place BEFORE the first Activity's class loader runs, otherwise
 * crashes during ViewModel construction / gomobile JNI init are
 * lost. attachBaseContext is the earliest hook in an Android
 * Application's lifecycle.
 */
object CrashCapture {
    private const val TAG = "BG_CrashCapture"

    /** Install once, at the earliest possible point in the process. */
    fun install(context: Context) {
        val appCtx = context.applicationContext
        val previous = Thread.getDefaultUncaughtExceptionHandler()
        Thread.setDefaultUncaughtExceptionHandler { thread, exception ->
            try {
                writeCrashReport(appCtx, thread, exception)
            } catch (t: Throwable) {
                // Best effort. If we can't write anywhere, at least
                // the line below ensures the OS still dialogs the
                // crash so the user sees something happened.
                Log.e(TAG, "crash capture failed: ${t.message}", t)
            }
            // Chain to the previous handler so the OS's normal
            // crash dialog still fires (and the process exits with
            // the right code for ANR/restart logic).
            previous?.uncaughtException(thread, exception)
        }
    }

    private fun writeCrashReport(ctx: Context, thread: Thread, t: Throwable) {
        val text = buildReport(thread, t)
        Log.e(TAG, "writing crash report (${text.length} bytes)")
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q) {
            try {
                writeToDownloads(ctx, text)
                return
            } catch (e: Throwable) {
                Log.w(TAG, "MediaStore Downloads write failed, falling back: ${e.message}")
            }
        }
        writeToPrivateExt(ctx, text)
    }

    /**
     * Insert a fresh entry under MediaStore.Downloads. Each crash
     * gets a unique timestamp-suffixed filename so we never lose a
     * prior report; MediaStore won't overwrite by name and assigning
     * a stable name would silently produce a `(1).txt` suffix on the
     * second run.
     */
    private fun writeToDownloads(ctx: Context, text: String) {
        val ts = SimpleDateFormat("yyyyMMdd-HHmmss", Locale.US).format(Date())
        val name = "beacongate_crash_$ts.txt"
        val values = ContentValues().apply {
            put(MediaStore.MediaColumns.DISPLAY_NAME, name)
            put(MediaStore.MediaColumns.MIME_TYPE, "text/plain")
            put(MediaStore.MediaColumns.RELATIVE_PATH, Environment.DIRECTORY_DOWNLOADS)
        }
        val uri = ctx.contentResolver.insert(
            MediaStore.Downloads.EXTERNAL_CONTENT_URI,
            values,
        ) ?: throw IllegalStateException("MediaStore.Downloads insert returned null URI")
        ctx.contentResolver.openOutputStream(uri)?.use { os ->
            os.write(text.toByteArray(Charsets.UTF_8))
        } ?: throw IllegalStateException("could not open MediaStore output stream")
        Log.i(TAG, "wrote crash to Downloads/$name")
    }

    /** Fallback: app-private external dir. */
    private fun writeToPrivateExt(ctx: Context, text: String) {
        val dir = ctx.getExternalFilesDir(null) ?: ctx.filesDir
        val file = File(dir, "last_crash.txt")
        file.writeText(text, Charsets.UTF_8)
        Log.i(TAG, "wrote crash to ${file.absolutePath}")
    }

    /**
     * Format the report. Includes everything an offsite debugger
     * might need without having to ask follow-up questions:
     * device + OS + app + thread + full stack chain.
     */
    private fun buildReport(thread: Thread, t: Throwable): String {
        val sw = StringWriter()
        val pw = PrintWriter(sw)
        pw.println("==== BeaconGate crash report ====")
        pw.println("Time:         ${Date()}")
        pw.println("Thread:       ${thread.name} (id=${thread.id})")
        pw.println("Manufacturer: ${Build.MANUFACTURER}")
        pw.println("Model:        ${Build.MODEL}")
        pw.println("Device:       ${Build.DEVICE}")
        pw.println("Product:      ${Build.PRODUCT}")
        pw.println("Android:      ${Build.VERSION.RELEASE} (SDK ${Build.VERSION.SDK_INT})")
        pw.println("App version:  0.1.0")
        pw.println()
        pw.println("==== Stack trace ====")
        t.printStackTrace(pw)
        // Walk the cause chain explicitly — printStackTrace already
        // shows it, but we duplicate the headers so a partial copy
        // (small phone screen, user scrolls) still has the relevant
        // info up top.
        var cause = t.cause
        var depth = 0
        while (cause != null && depth < 5) {
            pw.println()
            pw.println("==== Caused by [${depth + 1}] ====")
            cause.printStackTrace(pw)
            cause = cause.cause
            depth++
        }
        return sw.toString()
    }
}
