package com.beacongate

import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * Pure-Kotlin tests for [TunnelStateRepository]. The repository is a
 * `object` (process-wide singleton); these tests use [resetForTesting]
 * to bring it back to a known baseline between cases.
 *
 * The flow's collection semantics aren't exercised here (kotlinx-
 * coroutines-test would be needed); we cover the synchronous side
 * — a mutator changes `state.value`, observed via the same flow
 * the production code uses.
 */
class TunnelStateRepositoryTest {

    @After
    fun cleanup() {
        TunnelStateRepository.resetForTesting()
    }

    @Test
    fun startsDisconnected() {
        // resetForTesting in @After means we always start from
        // Disconnected at the beginning of the next test, so the
        // initial value here is whatever it was at process load —
        // a `data object Disconnected`.
        TunnelStateRepository.resetForTesting()
        assertEquals(TunnelStateRepository.TunnelState.Disconnected, TunnelStateRepository.state.value)
    }

    @Test
    fun markStartingTransitionsToStarting() {
        TunnelStateRepository.markStarting()
        assertEquals(TunnelStateRepository.TunnelState.Starting, TunnelStateRepository.state.value)
    }

    @Test
    fun markRunningTransitionsToRunning() {
        TunnelStateRepository.markStarting()
        TunnelStateRepository.markRunning()
        assertEquals(TunnelStateRepository.TunnelState.Running, TunnelStateRepository.state.value)
    }

    @Test
    fun markErrorCarriesReason() {
        TunnelStateRepository.markError("transport timed out")
        val s = TunnelStateRepository.state.value
        assertTrue("expected Error state, got $s", s is TunnelStateRepository.TunnelState.Error)
        assertEquals("transport timed out", (s as TunnelStateRepository.TunnelState.Error).reason)
    }

    @Test
    fun markDisconnectedFromAnyStateLands() {
        // Verify the transition from each non-terminal state lands
        // cleanly back to Disconnected — important because the
        // service triggers Disconnected from multiple paths
        // (clean stop, error recovery, etc.).
        listOf(
            { TunnelStateRepository.markStarting() },
            { TunnelStateRepository.markRunning() },
            { TunnelStateRepository.markError("x") },
        ).forEach { setup ->
            setup()
            TunnelStateRepository.markDisconnected()
            assertEquals(
                TunnelStateRepository.TunnelState.Disconnected,
                TunnelStateRepository.state.value,
            )
        }
    }
}
