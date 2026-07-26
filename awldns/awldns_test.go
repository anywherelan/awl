package awldns

import (
	"context"
	"fmt"
	"net"
	"runtime"
	"testing"
	"time"

	"github.com/miekg/dns"
	"github.com/stretchr/testify/require"
)

func TestDNS(t *testing.T) {
	ctx := context.Background()
	a := require.New(t)
	port := FindFreePort()
	addr := fmt.Sprintf("127.0.0.1:%d", port)

	resolver := NewResolver(addr)
	defer resolver.Close()
	// TODO: remove sleep. We need it because NewResolver starts servers in goroutines
	time.Sleep(50 * time.Millisecond)

	name1 := "peer_id"
	name1Capitalized := "pEEr_Id"
	addr1 := "123.4.5.6"
	name2 := "laptop.office"
	name2Capitalized := "LAPTOP.office"
	addr2 := "10.66.0.2"

	namesMapping := map[string]string{
		name1: addr1,
		name2: addr2,
	}
	resolver.ReceiveConfiguration("", namesMapping, nil)

	client := NewResolverClient(addr)

	assertAddr := func(host, addr string) {
		addrs, err := client.LookupHost(ctx, host)
		a.NoError(err)
		a.Len(addrs, 1)
		a.Equal(addr, addrs[0])

		hosts, err := client.LookupAddr(ctx, addr)
		a.NoError(err)
		a.Len(hosts, 1)
		a.Equal(dns.CanonicalName(host), hosts[0])
	}

	assertAddr(name1+".awl", addr1)
	assertAddr(name1+".AWL", addr1)
	assertAddr(name1Capitalized+".awl", addr1)

	assertAddr(name2+".awl", addr2)
	assertAddr(name2Capitalized+".awl", addr2)

	addrs, err := client.LookupHost(ctx, "unknown.awl")
	a.Error(err)
	a.Empty(addrs)
	dnsErr := err.(*net.DNSError)
	// TODO: investigate why macos and linux in CI return `lookup unknown.awl on 127.0.0.53:53: server misbehaving`
	//  it should use only our resolver, but somehow it tries to use system resolver afterwards
	if runtime.GOOS == "windows" {
		a.Equalf(true, dnsErr.IsNotFound, "actual error: %v", err)
	}
}

func TestDNSAAAAQueryDoesNotReturnARecord(t *testing.T) {
	a := require.New(t)
	port := FindFreePort()
	addr := fmt.Sprintf("127.0.0.1:%d", port)

	resolver := NewResolver(addr)
	defer resolver.Close()
	// TODO: remove sleep. We need it because NewResolver starts servers in goroutines
	time.Sleep(50 * time.Millisecond)

	resolver.ReceiveConfiguration("", map[string]string{
		"admin": "127.0.0.66",
	}, nil)

	req := new(dns.Msg)
	req.SetQuestion("admin.awl.", dns.TypeAAAA)
	resp, _, err := new(dns.Client).Exchange(req, addr)
	a.NoError(err)
	a.Equal(dns.RcodeSuccess, resp.Rcode)
	a.Empty(resp.Answer)
}

func TestDNSAQueryReturnsARecord(t *testing.T) {
	a := require.New(t)
	port := FindFreePort()
	addr := fmt.Sprintf("127.0.0.1:%d", port)

	resolver := NewResolver(addr)
	defer resolver.Close()
	// TODO: remove sleep. We need it because NewResolver starts servers in goroutines
	time.Sleep(50 * time.Millisecond)

	resolver.ReceiveConfiguration("", map[string]string{
		"admin": "127.0.0.66",
	}, nil)

	req := new(dns.Msg)
	req.SetQuestion("admin.awl.", dns.TypeA)
	resp, _, err := new(dns.Client).Exchange(req, addr)
	a.NoError(err)
	a.Equal(dns.RcodeSuccess, resp.Rcode)
	a.Len(resp.Answer, 1)

	aRecord, ok := resp.Answer[0].(*dns.A)
	a.True(ok)
	a.Equal("admin.awl.", aRecord.Hdr.Name)
	a.Equal("127.0.0.66", aRecord.A.String())
}

func TestDNSUnknownAddressReturnsNameError(t *testing.T) {
	a := require.New(t)
	port := FindFreePort()
	addr := fmt.Sprintf("127.0.0.1:%d", port)

	resolver := NewResolver(addr)
	defer resolver.Close()
	// TODO: remove sleep. We need it because NewResolver starts servers in goroutines
	time.Sleep(50 * time.Millisecond)

	resolver.ReceiveConfiguration("", map[string]string{
		"admin": "127.0.0.66",
	}, nil)

	req := new(dns.Msg)
	req.SetQuestion("unknown.awl.", dns.TypeA)
	resp, _, err := new(dns.Client).Exchange(req, addr)
	a.NoError(err)
	a.Equal(dns.RcodeNameError, resp.Rcode)
	a.Empty(resp.Answer)
}

func TestResolverFromListeners(t *testing.T) {
	a := require.New(t)

	udpConn, err := net.ListenPacket("udp", "127.0.0.1:0")
	a.NoError(err)
	tcpListener, err := net.Listen("tcp", "127.0.0.1:0")
	a.NoError(err)

	// The reported address is decoupled from the listeners — on Android it is
	// the in-tunnel DNS IP, while the listeners live inside netstack.
	const dnsAddress = "10.66.255.254:53"
	resolver := NewResolverFromListeners(udpConn, tcpListener, dnsAddress)
	defer resolver.Close()

	resolver.ReceiveConfiguration("", map[string]string{
		"admin": "127.0.0.66",
	}, nil)

	// DNSAddress reports the passed address once both servers are serving.
	a.Eventually(func() bool { return resolver.DNSAddress() == dnsAddress },
		5*time.Second, 10*time.Millisecond)

	req := new(dns.Msg)
	req.SetQuestion("admin.awl.", dns.TypeA)

	resp, _, err := new(dns.Client).Exchange(req, udpConn.LocalAddr().String())
	a.NoError(err)
	a.Len(resp.Answer, 1)
	a.Equal("127.0.0.66", resp.Answer[0].(*dns.A).A.String())

	tcpClient := &dns.Client{Net: "tcp"}
	resp, _, err = tcpClient.Exchange(req, tcpListener.Addr().String())
	a.NoError(err)
	a.Len(resp.Answer, 1)
	a.Equal("127.0.0.66", resp.Answer[0].(*dns.A).A.String())

	resolver.Close()
	a.Eventually(func() bool { return resolver.DNSAddress() == "" },
		5*time.Second, 10*time.Millisecond)
}

func NewResolverClient(address string) *net.Resolver {
	dialer := &net.Dialer{Timeout: time.Second}
	return &net.Resolver{
		StrictErrors: true,
		PreferGo:     true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, network, address)
		},
	}
}

func FindFreePort() int {
	const maxAttempts = 50
	var lastErr error
	for i := 0; i < maxAttempts; i++ {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			lastErr = err
			continue
		}
		port := l.Addr().(*net.TCPAddr).Port

		// The DNS resolver listens on both TCP and UDP on this port, so it must be
		// free for both. A TCP-free port is not guaranteed to be UDP-free, and on
		// Windows the chosen port may fall inside an OS-excluded range (Hyper-V/WSL
		// reservations), which fails the UDP bind with WSAEACCES. Release the port
		// and try another instead of giving up.
		u, err := net.ListenPacket("udp", l.Addr().String())
		if err != nil {
			_ = l.Close()
			lastErr = err
			continue
		}
		_ = u.Close()
		_ = l.Close()
		return port
	}
	panic(fmt.Sprintf("failed to find a free tcp+udp port after %d attempts: %v", maxAttempts, lastErr))
}
