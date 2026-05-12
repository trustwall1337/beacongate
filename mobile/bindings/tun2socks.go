package bindings

import (
	"errors"
	"fmt"
	"runtime"
	"sync"
	"syscall"

	"github.com/xjasonlyu/tun2socks/v2/engine"
)

// Default MTU for the Android TUN device. 1500 matches the Ethernet
// MTU and lets ICMP path-MTU work correctly upstream; lowering this
// risks fragmentation through the Apps Script transport.
const defaultTUNMTU = 1500

// vpnMu guards the whole "VPN running?" question. Independent of mu
// (which guards the BeaconGate runtime) because tun2socks lives in
// engine{}'s own globals — keeping the two locks distinct makes
// failure recovery (Stop one layer when the other won't shut down)
// less surprising.
var (
	vpnMu      sync.Mutex
	vpnRunning bool
)

// StartVpn brings up the full mobile VPN stack:
//
//  1. Starts the BeaconGate runtime + pump + SOCKS5 listener via
//     StartTunnel(cfg).
//  2. Hands tunFd to xjasonlyu/tun2socks/v2/engine, which wraps the
//     TUN device and forwards every captured TCP/UDP flow to the
//     local SOCKS5 listener (cfg.ListenAddr).
//
// On Android, tunFd comes from `VpnService.Builder().establish()
// .detachFd()`. The Go side takes ownership: tun2socks' netstack
// dups the fd internally and closes the original on shutdown, so
// the platform must NOT keep using it after this call returns.
//
// Idempotent failure: if either layer fails to start, both are torn
// down and the function returns the first error. The caller can
// retry without calling StopVpn() explicitly.
//
// Returns an error if the VPN is already running, or if either layer
// fails to start.
func StartVpn(cfg *ConfigSnapshot, tunFd int) error {
	if cfg == nil || cfg.raw == nil {
		return errors.New("StartVpn: cfg is nil or unimported")
	}
	if tunFd < 0 {
		return fmt.Errorf("StartVpn: invalid tun fd %d", tunFd)
	}

	vpnMu.Lock()
	defer vpnMu.Unlock()
	if vpnRunning {
		return errors.New("StartVpn: VPN already running")
	}

	// Step 1: bring up the BeaconGate side first. If this fails,
	// nothing else has been touched.
	bestEffortRaiseNoFile()
	if err := StartTunnel(cfg); err != nil {
		return fmt.Errorf("StartVpn: %w", err)
	}

	// Step 2: configure tun2socks to dial our local SOCKS5
	// listener and read packets from tunFd. Device "fd://N" tells
	// tun2socks to wrap the existing fd rather than open a TUN
	// device by name (which it can't on Android — VpnService is
	// the only legal way).
	socksAddr := cfg.raw.ListenAddr
	engine.Insert(&engine.Key{
		MTU:    defaultTUNMTU,
		Device: fmt.Sprintf("fd://%d", tunFd),
		Proxy:  "socks5://" + socksAddr,
		// "warn" avoids high-volume per-packet logs that can become a
		// bottleneck on emulator / lower-end devices.
		LogLevel: "warn",
	})
	// engine.Start launches its goroutines and returns
	// immediately; failures are surfaced via panics or zap-logged
	// errors. We can't observe a synchronous error here, so a
	// readiness check belongs to the platform side (it will see a
	// connection attempt fail when no packets flow). For now,
	// trust engine.Start.
	engine.Start()

	vpnRunning = true
	return nil
}

// StopVpn tears down both layers in reverse order: tun2socks first
// (so no more packets are forwarded), then the BeaconGate runtime
// (which closes the SOCKS5 listener, drains the pump, releases
// transport connections).
//
// Idempotent. Calling StopVpn when no VPN is running returns nil.
//
// Returns the first non-nil error from either layer; the other
// layer is still torn down regardless.
func StopVpn() error {
	vpnMu.Lock()
	defer vpnMu.Unlock()

	if !vpnRunning {
		return nil
	}

	// Defensive: clear the flag FIRST so a panic in either Stop
	// path doesn't leave the package in a wedged state. Worst
	// case, the platform side restarts the service and the next
	// StartVpn finds vpnRunning=false and proceeds.
	vpnRunning = false

	var firstErr error
	// tun2socks engine.Stop is documented to wait for the
	// netstack goroutines; on slow shutdowns this can block up
	// to ~5s while gVisor flushes pending TCP retransmits.
	// Acceptable on Android — the foreground service notification
	// stays up until this returns.
	engine.Stop()
	if err := StopTunnel(); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}

// IsVpnRunning reports whether the full VPN stack is currently up.
// Polled by the platform side in lieu of binding to the service —
// cheaper than service-binding for the simple "are we connected?"
// question.
//
// Note: returns true between StartVpn and StopVpn even if the
// underlying transport went unhealthy. For transport-health,
// poll Status().TransportHealthy.
func IsVpnRunning() bool {
	vpnMu.Lock()
	defer vpnMu.Unlock()
	return vpnRunning
}

func bestEffortRaiseNoFile() {
	if runtime.GOOS != "android" && runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		return
	}
	var lim syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &lim); err != nil {
		return
	}
	target := lim.Max
	if target > 8192 {
		target = 8192
	}
	if target <= lim.Cur {
		return
	}
	_ = syscall.Setrlimit(syscall.RLIMIT_NOFILE, &syscall.Rlimit{
		Cur: target,
		Max: lim.Max,
	})
}
