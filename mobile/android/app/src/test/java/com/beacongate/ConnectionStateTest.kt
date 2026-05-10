package com.beacongate

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * Pure-Kotlin tests for the [ConnectionState] sealed class. The
 * UI's correctness depends on the friend-name helper returning the
 * right value for each branch; refactors that add a new state must
 * extend `friendNameOrNull` exhaustively, and these tests pin the
 * existing branches so the compiler catches the rest at edit time.
 */
class ConnectionStateTest {

    @Test
    fun emptyHasNoFriendName() {
        assertNull(ConnectionState.Empty.friendNameOrNull())
    }

    @Test
    fun importingHasNoFriendName() {
        assertNull(ConnectionState.Importing.friendNameOrNull())
    }

    @Test
    fun importFailedHasNoFriendName() {
        val s = ConnectionState.ImportFailed("bad input")
        assertNull(s.friendNameOrNull())
    }

    @Test
    fun idleExposesFriendName() {
        val s = ConnectionState.Idle(friendName = "mahdi")
        assertEquals("mahdi", s.friendNameOrNull())
    }

    @Test
    fun vpnConsentExposesFriendName() {
        val s = ConnectionState.VpnConsent(friendName = "sara")
        assertEquals("sara", s.friendNameOrNull())
    }

    @Test
    fun connectingExposesFriendName() {
        val s = ConnectionState.Connecting(friendName = "reza")
        assertEquals("reza", s.friendNameOrNull())
    }

    @Test
    fun connectedExposesFriendName() {
        val s = ConnectionState.Connected(friendName = "ali")
        assertEquals("ali", s.friendNameOrNull())
    }

    @Test
    fun failedExposesFriendNameWhenAvailable() {
        val withName = ConnectionState.Failed(reason = "timeout", friendName = "mahdi")
        assertEquals("mahdi", withName.friendNameOrNull())

        val withoutName = ConnectionState.Failed(reason = "bad config", friendName = null)
        assertNull(withoutName.friendNameOrNull())
    }

    @Test
    fun statesWithSameDataAreEqual() {
        // data class identity check — important because StateFlow
        // dedupes equal emissions, so equality must match the
        // operator's mental model.
        val a = ConnectionState.Idle("mahdi")
        val b = ConnectionState.Idle("mahdi")
        assertEquals(a, b)
        // Different friend = different state.
        val c = ConnectionState.Idle("sara")
        assertNotEquals(a, c)
    }

    @Test
    fun objectStatesAreSingletons() {
        // Empty / Importing are `data object`s; both references
        // resolve to the same instance, which is what makes
        // StateFlow dedupe correctly.
        assertTrue(ConnectionState.Empty === ConnectionState.Empty)
        assertTrue(ConnectionState.Importing === ConnectionState.Importing)
        assertFalse((ConnectionState.Empty as Any) === (ConnectionState.Importing as Any))
    }
}
