package com.beacongate

import android.content.ContentValues
import android.content.Context
import android.os.Build
import android.os.Environment
import android.provider.MediaStore
import android.util.Log
import java.io.File
import java.text.SimpleDateFormat
import java.util.Date
import java.util.Locale

/**
 * Diagnostic-only: writes a tiny file to the user's Downloads folder
 * at each startup milestone. The presence/absence of each file tells
 * us how far the app got before crashing — useful when the crash is
 * a native abort (SIGSEGV inside libgojni.so or similar) that Java's
 * UncaughtExceptionHandler in [CrashCapture] can't trap.
 *
 * The user finds the files via Samsung "My Files" → Internal storage
 * → Download → look for files named `beacongate_boot_<N>_<label>.txt`.
 *
 * **One file per milestone** (rather than appending) because
 * MediaStore.Downloads doesn't support open-for-append; each insert
 * creates a separate entry. Side-effect benefit: a missing file
 * cleanly indicates "we did not reach this milestone."
 *
 * Removed once the bring-up is debugged.
 */
object BootProbe {
    private const val TAG = "BG_BootProbe"

    /** Write a milestone file. Best-effort; failures are silent. */
    fun mark(context: Context, step: Int, label: String) {
        val ts = SimpleDateFormat("HH:mm:ss.SSS", Locale.US).format(Date())
        val body = "step=$step label=$label time=$ts\n" +
            "device=${Build.MANUFACTURER} ${Build.MODEL}\n" +
            "android=${Build.VERSION.RELEASE} (SDK ${Build.VERSION.SDK_INT})\n"
        val name = "beacongate_boot_${step}_$label.txt"
        try {
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q) {
                writeToDownloads(context, name, body)
                Log.i(TAG, "wrote Downloads/$name")
                return
            }
        } catch (t: Throwable) {
            Log.w(TAG, "Downloads write failed: ${t.message}")
        }
        // Fallback to app-private external dir (always works, no perm).
        try {
            val dir = context.getExternalFilesDir(null) ?: context.filesDir
            File(dir, name).writeText(body)
            Log.i(TAG, "wrote ${dir.absolutePath}/$name")
        } catch (t: Throwable) {
            Log.e(TAG, "private-dir write failed too", t)
        }
    }

    private fun writeToDownloads(ctx: Context, name: String, body: String) {
        val values = ContentValues().apply {
            put(MediaStore.MediaColumns.DISPLAY_NAME, name)
            put(MediaStore.MediaColumns.MIME_TYPE, "text/plain")
            put(MediaStore.MediaColumns.RELATIVE_PATH, Environment.DIRECTORY_DOWNLOADS)
        }
        val uri = ctx.contentResolver.insert(
            MediaStore.Downloads.EXTERNAL_CONTENT_URI,
            values,
        ) ?: throw IllegalStateException("MediaStore insert returned null")
        ctx.contentResolver.openOutputStream(uri)?.use { it.write(body.toByteArray()) }
            ?: throw IllegalStateException("openOutputStream returned null")
    }
}
