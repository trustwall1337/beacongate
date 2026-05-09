# BeaconGate benchmarks

This directory holds reference performance numbers and the procedure
for re-measuring them. Plan A7 #8–13 defines the budgets these
measurements feed into.

## Two scopes

1. **In-process micro-benchmarks** (`make bench`). Measures
   BeaconGate-internal cost only — base64 encode/decode, Roundtrip
   dispatch, AEAD key cache. Source:
   [engine/transport/appsscript/bench_test.go](../engine/transport/appsscript/bench_test.go).
   Run on every CI build; numbers below are the reference baseline.
2. **End-to-end measurements**. Captured manually against a real
   Apps Script deployment. Procedure in `apps_script/README.md` and
   in `docs/deployment.md` Playbook B "B7. Verify".

The in-process numbers are the floor. Real-world latency is
dominated by the network leg (Google edge + Apps Script invocation
+ VPS RTT) and will be much higher; what the bench tells you is
whether the BeaconGate-internal overhead has regressed.

## Running

```
make bench
```

This runs `go test -bench=. -benchmem ./engine/transport/appsscript/`
with default `-benchtime=1s`. CI uses the same.

## Reference baseline (Apple M1 Pro, in-process)

Captured 2026-05-09 against a fake Apps Script + fake VPS in-process
harness. Update this section when the M1 Pro reference moves more
than ~20% in either direction.

| Benchmark | Time/op | Throughput | Allocs/op |
| --- | --- | --- | --- |
| `RoundtripSmallBatch` (256 B) | ~89 µs | — | 185 |
| `RoundtripLargeBatch` (64 KB) | ~563 µs | ~116 MB/s | 329 |
| `RoundtripParallel` (4 KB, 10 workers) | ~161 µs | — | 270 |
| `Base64EncodeDecode` (4 KB, isolated) | ~6 µs | ~665 MB/s | 3 |

Allocs and throughput are stable between runs; time-per-op varies
~10% with system load.

## What this does NOT measure

- Real Apps Script latency (the Google leg adds 100–300 ms typical).
- TLS handshake cost (prewarm makes it irrelevant on warm sessions,
  but is real on cold start).
- AEAD seal/open cost in client/server runtimes (the harness
  currently isolates the transport layer; the AEAD cost is
  measured separately in `engine/crypto`'s tests when run with
  `-bench`).

## Compared to Goose

GooseRelayVPN's `bench/{harness,sink,diff}/` is a richer ~1K-LOC
standalone harness that measures end-to-end through real frame
encoding + crypto + zstd compression. BeaconGate intentionally
chose a lighter `testing.B`-based approach: idiomatic Go,
reproducible in CI, but less fidelity to real-world numbers.

When the operator needs end-to-end numbers (e.g. A7 #13 Goose-
parity check), the procedure is to:

1. Stand up a real Apps Script deployment per
   [apps_script/README.md](../apps_script/README.md).
2. Run `beacongate-client` with structured logging.
3. Drive traffic through the SOCKS bridge.
4. Compute p50/p95 from the timing logs.

Goose's harness can be run side-by-side against the same Apps Script
endpoint for direct parity comparison.
