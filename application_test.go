package awl

import (
	"context"
	"encoding/hex"
	"fmt"
	"math/rand"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anywherelan/awl/api/apiclient"
	"github.com/anywherelan/awl/config"
	"github.com/anywherelan/awl/entity"
	"github.com/ipfs/go-log/v2"
	"github.com/libp2p/go-eventbus"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zapcore"
	"golang.zx2c4.com/wireguard/tun/tuntest"
)

func init() {
	useAwldns = false
}

func TestMakeFriends(t *testing.T) {
	a := require.New(t)

	peer1 := newTestPeer(t, false)
	defer peer1.Close()
	peer2 := newTestPeer(t, false)
	defer peer2.Close()

	makeFriends(a, peer2, peer1)
}

func BenchmarkTunnelPackets(b *testing.B) {
	a := require.New(b)

	peer1 := newTestPeer(b, true)
	defer peer1.Close()
	peer2 := newTestPeer(b, true)
	defer peer2.Close()

	makeFriends(a, peer2, peer1)
	b.ResetTimer()

	packetSizes := []int{40, 300, 800, 1300, 1800, 2300, 2800, 3500}
	for _, packetSize := range packetSizes {
		b.Run(fmt.Sprintf("%d bytes per package", packetSize), func(b *testing.B) {
			b.SetBytes(int64(packetSize))
			var packetsReceived int64
			var packetsSent int64
			closeCh := make(chan struct{})
			go func() {
				for {
					select {
					case <-peer2.tun.Inbound:
						atomic.AddInt64(&packetsReceived, 1)
					case <-closeCh:
						return
					}
				}
			}()
			packet := testPacket(packetSize)
			for i := 0; i < b.N; i++ {
				peer1.tun.Outbound <- packet
				atomic.AddInt64(&packetsSent, 1)
				// to have packet_loss at reasonable level (but more than 0)
				time.Sleep(4 * time.Microsecond)
			}
			close(closeCh)
			received := atomic.LoadInt64(&packetsReceived)
			sent := atomic.LoadInt64(&packetsSent)
			packetLoss := (float64(1) - float64(received)/float64(sent)) * 100
			bandwidth := float64(received) * float64(packetSize) / 1024 / 1024
			b.ReportMetric(bandwidth, "MB/s")
			b.ReportMetric(float64(received), "packets/s")
			b.ReportMetric(packetLoss, "packet_loss")
		})
	}
}

func testPacket(length int) []byte {
	data, err := hex.DecodeString("4500002828f540004011fd490a4200010a420002a9d0238200148bfd68656c6c6f20776f726c6421")
	if err != nil {
		panic(err)
	}

	if length > len(data) {
		packet := make([]byte, length)
		copy(packet[:], data)
		rand.Read(packet[len(data):])
		return packet
	}

	return data
}

type testPeer struct {
	app *Application
	api *apiclient.Client
	tun *tuntest.ChannelTUN
}

func (tp testPeer) Close() {
	tp.app.Close()
}

func (tp testPeer) PeerID() string {
	return tp.app.Conf.P2pNode.PeerID
}

func newTestPeer(t testing.TB, disableLogging bool) testPeer {
	a := require.New(t)

	tempDir := t.TempDir()
	a.NoError(os.Setenv(config.AppDataDirEnvKey, tempDir))
	if disableLogging {
		tempConf := config.NewConfig(eventbus.NewBus())
		tempConf.LoggerLevel = "fatal"
		tempConf.Save()
	}

	app := New()
	app.SetupLoggerAndConfig()
	if disableLogging {
		log.SetupLogging(zapcore.NewNopCore(), func(string) zapcore.Level {
			return zapcore.FatalLevel
		})
	}
	// TODO: do not use real bootstrap peers for test
	app.Conf.HttpListenAddress = "127.0.0.1:0"
	ctx := context.Background()

	testTUN := tuntest.NewChannelTUN()
	err := app.Init(ctx, testTUN.TUN())
	a.NoError(err)

	return testPeer{
		app: app,
		api: apiclient.New(app.Api.Address()),
		tun: testTUN,
	}
}

func makeFriends(a *require.Assertions, peer1, peer2 testPeer) {
	err := peer1.api.SendFriendRequest(peer2.PeerID(), "")
	a.NoError(err)

	var authRequests []entity.AuthRequest
	a.Eventually(func() bool {
		authRequests, err = peer2.api.AuthRequests()
		a.NoError(err)
		return len(authRequests) == 1
	}, 15*time.Second, 50*time.Millisecond)
	err = peer2.api.AcceptFriendRequest(authRequests[0].PeerID, "")
	a.NoError(err)

	time.Sleep(500 * time.Millisecond)
	knownPeer, exists := peer2.app.Conf.GetPeer(peer1.PeerID())
	a.True(exists)
	a.True(knownPeer.Confirmed)
}
