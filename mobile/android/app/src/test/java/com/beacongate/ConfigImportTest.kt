package com.beacongate

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test

/**
 * Pure-Kotlin tests for [ConfigImport.extractDriveFileId]. The HTTP
 * fetch + ContentResolver paths require an Android runtime and are
 * verified via on-device manual testing during the first-friend
 * step — this layer's only meaningful logic is URL parsing, and
 * it's worth pinning in a JUnit test so a typo in the regex
 * doesn't ship.
 */
class ConfigImportTest {

    @Test
    fun extractsIdFromStandardShareLink() {
        val url = "https://drive.google.com/file/d/1AbCdEf-GhIjKl_mnoPQr/view?usp=sharing"
        assertEquals("1AbCdEf-GhIjKl_mnoPQr", ConfigImport.extractDriveFileId(url))
    }

    @Test
    fun extractsIdWithoutTrailingPath() {
        val url = "https://drive.google.com/file/d/1AbCdEf-GhIjKl_mnoPQr"
        assertEquals("1AbCdEf-GhIjKl_mnoPQr", ConfigImport.extractDriveFileId(url))
    }

    @Test
    fun extractsIdFromDirectDownloadUrl() {
        val url = "https://drive.google.com/uc?export=download&id=1AbCdEf-GhIjKl_mnoPQr"
        assertEquals("1AbCdEf-GhIjKl_mnoPQr", ConfigImport.extractDriveFileId(url))
    }

    @Test
    fun extractsIdFromOpenUrl() {
        val url = "https://drive.google.com/open?id=1AbCdEf-GhIjKl_mnoPQr"
        assertEquals("1AbCdEf-GhIjKl_mnoPQr", ConfigImport.extractDriveFileId(url))
    }

    @Test
    fun rejectsNonDriveOrigin() {
        val attacker = "https://drive.google.com.attacker.example/file/d/abc/view"
        assertNull(ConfigImport.extractDriveFileId(attacker))

        val httpScheme = "http://drive.google.com/file/d/abc/view"
        assertNull(
            "non-https Drive URL must be rejected (no plaintext bg downloads)",
            ConfigImport.extractDriveFileId(httpScheme),
        )
    }

    @Test
    fun rejectsUnrelatedHosts() {
        assertNull(ConfigImport.extractDriveFileId("https://example.com/file/d/abc"))
        assertNull(ConfigImport.extractDriveFileId("https://docs.google.com/d/abc"))
    }

    @Test
    fun rejectsMalformedDriveUrl() {
        // Looks Drive-shaped but no file ID extractable.
        assertNull(ConfigImport.extractDriveFileId("https://drive.google.com/file/"))
        assertNull(ConfigImport.extractDriveFileId("https://drive.google.com/uc"))
        assertNull(ConfigImport.extractDriveFileId("https://drive.google.com/"))
    }

    @Test
    fun handlesCaseInsensitiveScheme() {
        val url = "HTTPS://drive.google.com/file/d/abc-123/view"
        assertEquals("abc-123", ConfigImport.extractDriveFileId(url))
    }
}
