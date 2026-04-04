package vpn

import (
	"encoding/binary"
	"errors"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.zx2c4.com/wireguard/tun"
)

// fakeTUN is a minimal in-memory tun.Device for exercising SwappableTUN without
// a real interface. Read serves packets queued via feed(); Close unblocks a
// pending Read with os.ErrClosed, exactly like NativeTun does when its fd is
// closed.
type fakeTUN struct {
	name   string
	batch  int
	readCh chan []byte
	events chan tun.Event
	closed chan struct{}
	once   sync.Once

	mu      sync.Mutex
	written [][]byte
}

func newFakeTUN() *fakeTUN {
	return &fakeTUN{
		name:   "awl0",
		batch:  1,
		readCh: make(chan []byte, 64),
		events: make(chan tun.Event, 5),
		closed: make(chan struct{}),
	}
}

func (f *fakeTUN) feed(p []byte) { f.readCh <- p }

func (f *fakeTUN) Read(bufs [][]byte, sizes []int, offset int) (int, error) {
	select {
	case <-f.closed:
		return 0, os.ErrClosed
	case p := <-f.readCh:
		n := copy(bufs[0][offset:], p)
		sizes[0] = n
		return 1, nil
	}
}

func (f *fakeTUN) Write(bufs [][]byte, offset int) (int, error) {
	select {
	case <-f.closed:
		return 0, os.ErrClosed
	default:
	}
	f.mu.Lock()
	for _, b := range bufs {
		f.written = append(f.written, append([]byte(nil), b[offset:]...))
	}
	f.mu.Unlock()
	return len(bufs), nil
}

func (f *fakeTUN) MTU() (int, error)        { return InterfaceMTU, nil }
func (f *fakeTUN) Name() (string, error)    { return f.name, nil }
func (f *fakeTUN) File() *os.File           { return nil }
func (f *fakeTUN) Events() <-chan tun.Event { return f.events }
func (f *fakeTUN) BatchSize() int           { return f.batch }

func (f *fakeTUN) Close() error {
	f.once.Do(func() {
		close(f.closed)
		close(f.events)
	})
	return nil
}

// makeTestPacket builds a minimal but Parse-able IPv4 packet carrying seq in its
// payload so tests can assert delivery order.
func makeTestPacket(seq uint32) []byte {
	p := make([]byte, 24)
	p[0] = 0x45 // IPv4, IHL=5
	binary.BigEndian.PutUint16(p[2:4], 24)
	p[9] = 17 // UDP
	p[12], p[13], p[14], p[15] = 10, 66, 0, 1
	p[16], p[17], p[18], p[19] = 10, 66, 0, 2
	binary.BigEndian.PutUint32(p[20:24], seq)
	return p
}

func packetSeq(pkt *Packet) uint32 {
	return binary.BigEndian.Uint32(pkt.Packet[20:24])
}

// TestSwappableTUN_ReadContinuesAcrossSwap is the core integration test: a real
// vpn.Device running ReadTUNPackets on top of SwappableTUN must keep reading,
// in order, when the underlying device is swapped out mid-flight.
func TestSwappableTUN_ReadContinuesAcrossSwap(t *testing.T) {
	a := require.New(t)

	fake1 := newFakeTUN()
	sw := NewSwappableTUN(fake1)
	dev, err := NewDevice(sw, "awl0", net.IPv4(10, 66, 0, 1), net.CIDRMask(24, 32))
	a.NoError(err)

	var mu sync.Mutex
	var got []uint32
	go dev.ReadTUNPackets(func(pkts []*Packet) {
		mu.Lock()
		for _, pk := range pkts {
			got = append(got, packetSeq(pk))
		}
		mu.Unlock()
	})

	lenGot := func() int { mu.Lock(); defer mu.Unlock(); return len(got) }

	// First half via fake1.
	for i := range 5 {
		fake1.feed(makeTestPacket(uint32(i)))
	}
	a.Eventually(func() bool { return lenGot() >= 5 }, time.Second, time.Millisecond)

	// Swap to fake2; the reader is now blocked on fake1 and must transparently
	// resume on fake2 without ReadTUNPackets exiting.
	fake2 := newFakeTUN()
	a.NoError(sw.Swap(fake2))

	for i := 5; i < 10; i++ {
		fake2.feed(makeTestPacket(uint32(i)))
	}
	a.Eventually(func() bool { return lenGot() >= 10 }, time.Second, time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	want := []uint32{0, 1, 2, 3, 4, 5, 6, 7, 8, 9}
	a.Equal(want, got, "packets must arrive in order across the swap")
}

// TestSwappableTUN_ReadStopsOnClose verifies a real shutdown (not a swap)
// propagates os.ErrClosed so the ReadTUNPackets loop exits.
func TestSwappableTUN_ReadStopsOnClose(t *testing.T) {
	a := require.New(t)

	fake := newFakeTUN()
	sw := NewSwappableTUN(fake)

	done := make(chan struct{})
	go func() {
		bufs := [][]byte{make([]byte, 64)}
		sizes := []int{0}
		for {
			_, err := sw.Read(bufs, sizes, 0)
			if err != nil {
				a.ErrorIs(err, os.ErrClosed)
				close(done)
				return
			}
		}
	}()

	a.NoError(sw.Close())
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Read did not return after Close")
	}
}

// TestSwappableTUN_EventsStableAcrossSwap verifies the wrapper's event channel
// survives a swap and only closes when the wrapper itself closes.
func TestSwappableTUN_EventsStableAcrossSwap(t *testing.T) {
	a := require.New(t)

	fake1 := newFakeTUN()
	sw := NewSwappableTUN(fake1)

	recv := func() (tun.Event, bool) {
		select {
		case ev, ok := <-sw.Events():
			return ev, ok
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for event")
			return 0, false
		}
	}

	fake1.events <- tun.EventUp
	ev, ok := recv()
	a.True(ok)
	a.Equal(tun.Event(tun.EventUp), ev)

	fake2 := newFakeTUN()
	a.NoError(sw.Swap(fake2))

	// Channel must still be open and forwarding from the new device.
	fake2.events <- tun.EventMTUUpdate
	ev, ok = recv()
	a.True(ok)
	a.Equal(tun.Event(tun.EventMTUUpdate), ev)

	// Closing the wrapper closes the event channel.
	a.NoError(sw.Close())
	a.Eventually(func() bool {
		select {
		case _, ok := <-sw.Events():
			return !ok
		default:
			return false
		}
	}, time.Second, time.Millisecond)
}

func TestSwappableTUN_BatchSizeConstant(t *testing.T) {
	a := require.New(t)

	fake1 := newFakeTUN()
	sw := NewSwappableTUN(fake1)
	want := sw.BatchSize()

	a.NoError(sw.Swap(newFakeTUN()))
	a.Equal(want, sw.BatchSize(), "BatchSize must not change across swaps")
}

func TestSwappableTUN_SwapRejectsDifferentBatchSize(t *testing.T) {
	a := require.New(t)

	sw := NewSwappableTUN(newFakeTUN()) // batch 1
	mismatched := newFakeTUN()
	mismatched.batch = 2

	a.Error(sw.Swap(mismatched))
	// The rejected device must have been closed.
	select {
	case <-mismatched.closed:
	default:
		t.Fatal("Swap must close a device it rejects for BatchSize mismatch")
	}
	// The original device must still be in place and usable.
	a.Equal(1, sw.BatchSize())
}

func TestSwappableTUN_SwapAfterCloseIsRejected(t *testing.T) {
	a := require.New(t)

	sw := NewSwappableTUN(newFakeTUN())
	a.NoError(sw.Close())

	late := newFakeTUN()
	a.ErrorIs(sw.Swap(late), os.ErrClosed)
	// The rejected device must have been closed by Swap.
	select {
	case <-late.closed:
	default:
		t.Fatal("Swap after Close must close the passed-in device")
	}
}

// TestSwappableTUN_RetryDoesNotBusyLoopOnSwap is a light stress check: rapid
// swaps interleaved with reads must not lose or reorder packets.
func TestSwappableTUN_RetryUnderRepeatedSwaps(t *testing.T) {
	a := require.New(t)

	const n = 50
	fake := newFakeTUN()
	sw := NewSwappableTUN(fake)

	got := make(chan uint32, n)
	go func() {
		bufs := [][]byte{make([]byte, 64)}
		sizes := []int{0}
		for {
			cnt, err := sw.Read(bufs, sizes, 0)
			if errors.Is(err, os.ErrClosed) {
				return
			}
			if err != nil {
				return
			}
			if cnt == 1 {
				got <- binary.BigEndian.Uint32(bufs[0][20:24])
			}
		}
	}()

	cur := fake
	for i := range n {
		cur.feed(makeTestPacket(uint32(i)))
		// Wait for delivery before swapping so no packet is left buffered on a
		// device we are about to close.
		select {
		case seq := <-got:
			a.Equal(uint32(i), seq)
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for packet %d", i)
		}
		next := newFakeTUN()
		a.NoError(sw.Swap(next))
		cur = next
	}
	a.NoError(sw.Close())
}
