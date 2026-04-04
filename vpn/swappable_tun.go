package vpn

import (
	"errors"
	"fmt"
	"os"
	"sync/atomic"

	"golang.zx2c4.com/wireguard/tun"
)

// SwappableTUN wraps a tun.Device and lets the underlying device be replaced at
// runtime ("hot swap") without disturbing any of its consumers. It exists so
// that platforms which cannot mutate routing on a live interface (Android,
// where routing is owned by VpnService.Builder and a route change requires a
// fresh VpnService.establish() and therefore a new tun fd) can switch the TUN
// in place while the rest of awl — the libp2p host, service.Tunnel, the
// ReadTUNPackets loop — keeps running untouched.
//
// The swap is fully hidden behind the tun.Device contract:
//
//   - Read never surfaces a swap as an error. When the current inner device is
//     closed by Swap, the in-flight Read unblocks with os.ErrClosed; Read
//     detects that the device pointer has moved and transparently retries on
//     the new inner. os.ErrClosed is propagated to the caller only when the
//     whole SwappableTUN is closed (real shutdown) — which is exactly when the
//     ReadTUNPackets loop should exit.
//
//   - Events() returns a single stable channel owned by the wrapper. A
//     forwarder goroutine copies events from whichever inner device is current
//     and only closes the wrapper's channel when the wrapper itself is closed,
//     so a swap does not terminate the consumer's range loop.
//
//   - BatchSize is captured once at construction and returned unchanged, as the
//     tun.Device contract requires it to be constant for the device's lifetime
//     (ReadTUNPackets sizes its buffers from it exactly once).
//
// Concurrency: Read/Write run lock-free and may be called concurrently with
// Swap and Close. Swap and Close are assumed NOT to run concurrently with each
// other — on Android both are driven serially by the host app over a single
// MethodChannel task queue. The lock-free Read/Write hot path is the only one
// that matters for performance, hence atomic.Pointer rather than a mutex.
type SwappableTUN struct {
	// cur is the current inner device, wrapped in tunHandle so the
	// atomic.Pointer is type-safe regardless of the concrete tun.Device type.
	cur atomic.Pointer[tunHandle]

	// closed is set by Close. It is what lets Read/Write distinguish a swap
	// (retry on the new inner) from a genuine shutdown (propagate os.ErrClosed).
	closed atomic.Bool

	// events is the wrapper-owned, swap-stable event channel handed to Events().
	events chan tun.Event

	// batchSize is captured from the initial inner device. The tun.Device
	// contract requires BatchSize to be constant for a device's lifetime, and
	// ReadTUNPackets sizes its read buffers from it exactly once. wireguard's
	// NativeTun derives this value by probing the fd for IFF_VNET_HDR
	// (batchSize=conn.IdealBatchSize with GRO/GSO offload, else 1). An Android
	// VpnService fd has no IFF_VNET_HDR, so every device built from a fresh
	// establish() reports 1 — i.e. the value is stable across swaps. Swap
	// enforces this rather than trusting it: a device with a different
	// BatchSize is rejected, since ReadTUNPackets' buffers would be mis-sized.
	batchSize int
}

type tunHandle struct {
	dev tun.Device
}

var _ tun.Device = (*SwappableTUN)(nil)

// NewSwappableTUN wraps initial as the starting inner device. initial must be
// non-nil. The wrapper takes ownership of initial: Close (or a subsequent Swap)
// will close it.
func NewSwappableTUN(initial tun.Device) *SwappableTUN {
	w := &SwappableTUN{
		events:    make(chan tun.Event, 8),
		batchSize: initial.BatchSize(),
	}
	w.cur.Store(&tunHandle{dev: initial})
	go w.forwardEvents()
	return w
}

// Swap replaces the current inner device with newDev and closes the old one.
// Closing the old device unblocks any in-flight Read/Write so it can retry on
// newDev. The pointer is swapped BEFORE the old device is closed: this ordering
// guarantees that when the unblocked Read observes os.ErrClosed it will already
// see the updated pointer and retry rather than mistake the swap for a real
// shutdown.
//
// If the wrapper is already closed, newDev is closed and os.ErrClosed returned.
func (w *SwappableTUN) Swap(newDev tun.Device) error {
	if w.closed.Load() {
		_ = newDev.Close()
		return os.ErrClosed
	}
	// Reject a device whose BatchSize differs from the one ReadTUNPackets sized
	// its buffers for. See the batchSize field comment.
	if bs := newDev.BatchSize(); bs != w.batchSize {
		_ = newDev.Close()
		return fmt.Errorf("swap rejected: new device BatchSize %d != %d", bs, w.batchSize)
	}
	old := w.cur.Swap(&tunHandle{dev: newDev})
	return old.dev.Close()
}

func (w *SwappableTUN) Read(bufs [][]byte, sizes []int, offset int) (int, error) {
	for {
		h := w.cur.Load()
		n, err := h.dev.Read(bufs, sizes, offset)
		if err == nil || !errors.Is(err, os.ErrClosed) {
			// Success, or any non-close error (e.g. tun.ErrTooManySegments,
			// genuine read failures) — pass straight through, preserving the
			// existing ReadTUNPackets error handling.
			return n, err
		}
		if w.closed.Load() {
			// Real shutdown: let the caller's loop exit.
			return n, err
		}
		if w.cur.Load() != h {
			// The device was swapped out from under us; retry on the new one.
			continue
		}
		// os.ErrClosed without a swap and without wrapper close: a genuine
		// close of the current device. Propagate.
		return n, err
	}
}

func (w *SwappableTUN) Write(bufs [][]byte, offset int) (int, error) {
	for {
		h := w.cur.Load()
		n, err := h.dev.Write(bufs, offset)
		if err == nil || !errors.Is(err, os.ErrClosed) {
			return n, err
		}
		if w.closed.Load() {
			return n, err
		}
		if w.cur.Load() != h {
			continue
		}
		return n, err
	}
}

func (w *SwappableTUN) MTU() (int, error) {
	return w.cur.Load().dev.MTU()
}

func (w *SwappableTUN) Name() (string, error) {
	return w.cur.Load().dev.Name()
}

func (w *SwappableTUN) File() *os.File {
	return w.cur.Load().dev.File()
}

// BatchSize returns the value captured at construction. It must not change over
// the device's lifetime; all inner devices on a given platform share the same
// preferred batch size, so this is safe across swaps.
func (w *SwappableTUN) BatchSize() int {
	return w.batchSize
}

// Events returns the wrapper-owned channel. It stays open across swaps and is
// closed only when the wrapper is closed.
func (w *SwappableTUN) Events() <-chan tun.Event {
	return w.events
}

// Close marks the wrapper closed and closes the current inner device. Closing
// the inner device closes its event channel, which makes the forwarder
// goroutine close the wrapper's event channel and exit. Idempotent.
func (w *SwappableTUN) Close() error {
	if w.closed.Swap(true) {
		return nil
	}
	return w.cur.Load().dev.Close()
}

// forwardEvents copies events from the current inner device to the wrapper's
// stable channel. When an inner device's channel closes (because Swap or Close
// closed that device) it reloads the current device: on a swap it continues
// with the new inner; on shutdown (closed set) it closes the wrapper channel
// and exits.
func (w *SwappableTUN) forwardEvents() {
	for {
		if w.closed.Load() {
			close(w.events)
			return
		}
		inner := w.cur.Load().dev
		for ev := range inner.Events() {
			select {
			case w.events <- ev:
			default:
			}
		}
		// inner.Events() closed: either this device was swapped out, or the
		// wrapper is shutting down. Loop to re-evaluate w.closed / reload cur.
	}
}
