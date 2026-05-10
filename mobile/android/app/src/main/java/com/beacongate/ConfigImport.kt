package com.beacongate

import android.content.ContentResolver
import android.net.Uri
import bindings.Bindings
import bindings.ConfigSnapshot
import java.io.IOException
import java.net.HttpURLConnection
import java.net.URL

/**
 * Two-path config-import helper.
 *
 * Both paths converge on the same validation step
 * ([Bindings.importConfig]) so the security and parsing logic stays
 * single-source-of-truth in the Go layer.
 *
 *   Path A (primary): a Google Drive share link, fetched directly
 *                     over HTTPS using the platform's built-in
 *                     [HttpURLConnection]. No third-party HTTP
 *                     client (keeps APK size tight).
 *   Path B (fallback): a system file picker URI, read via
 *                     [ContentResolver] (zero-permission file
 *                     access since Android 4.4).
 *
 * **Why a 64 KB cap.** A valid `.bg` config is well under 4 KB
 * (a single client_id, AES key, transport block, a few SNI hosts).
 * 64 KB is a generous overhead while still bounding the memory
 * cost of a malicious attacker pointing the app at a multi-GB
 * Drive file. Anything larger is fail-fast.
 */
object ConfigImport {

    /**
     * The result of a successful import. [snapshot] is gomobile's
     * validated view of the config (used for UI display);
     * [rawJson] is the canonical JSON the credential store
     * persists. Holding both keeps the persistence + display
     * paths from re-fetching or re-decoding the source.
     */
    data class Result(val snapshot: ConfigSnapshot, val rawJson: String)

    /** Hard cap on the import payload size, in bytes. See class doc. */
    private const val MAX_BYTES = 64 * 1024

    /**
     * Network connect + read timeout for the Drive fetch. 15s is
     * generous on Iranian mobile networks (~RTT 200ms + Drive
     * server response). Tighter than HttpURLConnection's default
     * (infinity) so the UI doesn't appear hung.
     */
    private const val DRIVE_TIMEOUT_MS = 15_000

    /**
     * Fetches a `.bg` config from a Google Drive share link, then
     * hands it to the Go bindings for validation.
     *
     * Accepts share links of the form:
     *   `https://drive.google.com/file/d/<FILE_ID>/view?usp=sharing`
     * or:
     *   `https://drive.google.com/uc?export=download&id=<FILE_ID>`
     *
     * Anything else fails with [IllegalArgumentException]. The
     * file the operator shares MUST be set to "Anyone with the
     * link can view" — otherwise Drive returns a sign-in HTML
     * page instead of the JSON, and validation will fail.
     *
     * @return the validated [ConfigSnapshot]; never null on success.
     * @throws IllegalArgumentException for malformed URLs.
     * @throws IOException for transport failures (DNS, TLS, HTTP
     *                     non-2xx, body too large).
     */
    @Throws(IOException::class)
    fun fromDriveLink(shareUrl: String): Result {
        val fileId = extractDriveFileId(shareUrl.trim())
            ?: throw IllegalArgumentException(
                "Not a recognised Google Drive share URL: $shareUrl"
            )
        val downloadUrl = "https://drive.google.com/uc?export=download&id=$fileId"

        val bytes = fetchBytes(downloadUrl)
        val text = String(bytes, Charsets.UTF_8)

        // Drive returns an HTML page (not the file) if the file
        // isn't shared publicly OR if it's larger than the
        // Drive-virus-scan threshold. Detect that early so the
        // user sees a useful error instead of "JSON parse failed".
        val sniff = text.trimStart()
        if (sniff.startsWith("<")) {
            throw IOException(
                "Drive returned an HTML page instead of the config. " +
                    "Make sure the file is shared as 'Anyone with the link can view'."
            )
        }
        return Result(snapshot = Bindings.importConfig(text), rawJson = text)
    }

    /**
     * Reads a `.bg` config from a [Uri] obtained via
     * `ACTION_OPEN_DOCUMENT` and validates it via the Go bindings.
     *
     * The [ContentResolver] argument is supplied by the caller
     * (typically `context.contentResolver`) so this object stays
     * Context-free and easier to unit-test.
     *
     * @throws IOException on read failure or oversized payload.
     */
    @Throws(IOException::class)
    fun fromFilePicker(resolver: ContentResolver, uri: Uri): Result {
        val bytes = resolver.openInputStream(uri)?.use { stream ->
            val buf = ByteArray(MAX_BYTES + 1)
            var off = 0
            while (off < buf.size) {
                val n = stream.read(buf, off, buf.size - off)
                if (n <= 0) break
                off += n
            }
            if (off > MAX_BYTES) {
                throw IOException(
                    "Selected file is larger than $MAX_BYTES bytes. " +
                        "Are you sure this is a BeaconGate config?"
                )
            }
            buf.copyOf(off)
        } ?: throw IOException("Could not open the selected file for reading.")

        val text = String(bytes, Charsets.UTF_8)
        return Result(snapshot = Bindings.importConfig(text), rawJson = text)
    }

    /**
     * Pulls bytes from [url] over plain HTTPS with a hard size cap.
     * Internal — used by the Drive path. Public visibility is
     * `internal` so unit tests in the same module can drive it
     * against a fake server, without it leaking to consumers.
     *
     * Hardening:
     *   - Connect + read timeout to prevent hangs.
     *   - Manual byte counter rejects any response over [MAX_BYTES].
     *     We do this with explicit counting rather than trusting
     *     `Content-Length` because Drive's CDN sometimes serves
     *     chunked encoding without an upfront length.
     *   - Non-2xx status codes raise [IOException] with the code.
     */
    @Throws(IOException::class)
    internal fun fetchBytes(url: String): ByteArray {
        val conn = (URL(url).openConnection() as HttpURLConnection).apply {
            connectTimeout = DRIVE_TIMEOUT_MS
            readTimeout = DRIVE_TIMEOUT_MS
            instanceFollowRedirects = true
            requestMethod = "GET"
            // No User-Agent override: HttpURLConnection's default
            // (`Java/<jdk-version>`) is fine for Drive. Some
            // hardening guides set Chrome-style UAs, but that
            // doesn't help here — Drive serves anonymous fetches
            // identically across UAs.
        }
        try {
            val code = conn.responseCode
            if (code !in 200..299) {
                throw IOException("Drive returned HTTP $code")
            }
            val out = ByteArray(MAX_BYTES + 1)
            var off = 0
            conn.inputStream.use { stream ->
                while (off < out.size) {
                    val n = stream.read(out, off, out.size - off)
                    if (n <= 0) break
                    off += n
                }
            }
            if (off > MAX_BYTES) {
                throw IOException(
                    "Drive response is larger than $MAX_BYTES bytes; " +
                        "refusing to load."
                )
            }
            return out.copyOf(off)
        } finally {
            conn.disconnect()
        }
    }

    /**
     * Parse a Google Drive share URL and extract the file ID. Returns
     * null if the URL doesn't match a known Drive share pattern.
     *
     * Recognised forms:
     *   - https://drive.google.com/file/d/<FILE_ID>/view[?usp=sharing]
     *   - https://drive.google.com/file/d/<FILE_ID>
     *   - https://drive.google.com/uc?export=download&id=<FILE_ID>
     *   - https://drive.google.com/open?id=<FILE_ID>
     */
    internal fun extractDriveFileId(url: String): String? {
        // Reject obvious non-Drive URLs early so we don't fetch
        // arbitrary user-supplied origins.
        if (!url.startsWith("https://drive.google.com/", ignoreCase = true)) {
            return null
        }
        // Pattern 1: /file/d/<id>/...
        Regex("""/file/d/([a-zA-Z0-9_-]+)""").find(url)?.let {
            return it.groupValues[1]
        }
        // Pattern 2: ?id=<id> (uc / open)
        Regex("""[?&]id=([a-zA-Z0-9_-]+)""").find(url)?.let {
            return it.groupValues[1]
        }
        return null
    }
}
