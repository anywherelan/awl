package awldns

import (
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/ipfs/go-log/v2"
	"github.com/miekg/dns"

	"github.com/anywherelan/awl/metrics"
)

const (
	defaultTTL        = 60 * time.Second
	defaultTTLSeconds = uint32(defaultTTL / time.Second)
	ptrV4Suffix       = ".in-addr.arpa."
	ptrV6Suffix       = ".ip6.arpa."
)

const (
	netUDP = "udp"
	netTCP = "tcp"

	LocalDomain               = "awl"
	DefaultDNSAddress         = "127.0.0.66:53"
	DefaultDNSPort            = "53"
	DefaultUpstreamDNSAddress = "1.1.1.1:53"
)

type Resolver struct {
	udpServer *dns.Server
	tcpServer *dns.Server
	udpClient *dns.Client
	tcpClient *dns.Client
	cfg       atomic.Pointer[config]
	logger    *log.ZapEventLogger

	udpServerWorking atomic.Bool
	tcpServerWorking atomic.Bool

	dnsAddress string
}

type config struct {
	upstreamDNS     string
	directMapping   map[string]netip.Addr
	directMappingV6 map[string]netip.Addr
	reverseMapping  map[netip.Addr]string
}

// NewResolver creates a resolver that binds its own UDP and TCP sockets on
// dnsAddress (host:port).
func NewResolver(dnsAddress string) *Resolver {
	r := newResolver(dnsAddress)
	r.udpServer.Addr = dnsAddress
	r.udpServer.Net = netUDP
	r.tcpServer.Addr = dnsAddress
	r.tcpServer.Net = netTCP
	r.startServers()

	return r
}

// NewResolverFromListeners creates a resolver serving on already-open
// listeners instead of binding sockets itself — used on Android, where :53
// cannot be bound and the listeners live inside a netstack bridge
// (awldns/dnsbridge). dnsAddress is what DNSAddress reports once both
// servers are up; nothing is bound to it here.
func NewResolverFromListeners(udpConn net.PacketConn, tcpListener net.Listener, dnsAddress string) *Resolver {
	r := newResolver(dnsAddress)
	r.udpServer.PacketConn = udpConn
	r.tcpServer.Listener = tcpListener
	r.startServers()

	return r
}

func newResolver(dnsAddress string) *Resolver {
	r := &Resolver{
		logger: log.Logger("awl/dns"),
		udpClient: &dns.Client{
			Net: netUDP,
		},
		tcpClient: &dns.Client{
			Net: netTCP,
		},
		dnsAddress: dnsAddress,
	}
	r.cfg.Store(&config{})

	mux := dns.NewServeMux()
	mux.HandleFunc(LocalDomain, r.dnsLocalDomainHandler)
	mux.HandleFunc(strings.TrimPrefix(ptrV4Suffix, "."), r.ptrHandler)
	mux.HandleFunc(strings.TrimPrefix(ptrV6Suffix, "."), r.ptrHandler)
	mux.HandleFunc(".", r.dnsProxyHandler)

	r.udpServer = &dns.Server{
		Handler: mux,
		NotifyStartedFunc: func() {
			r.logger.Infof("udp server has started on %s", dnsAddress)
			r.udpServerWorking.Store(true)
		},
	}
	r.tcpServer = &dns.Server{
		Handler: mux,
		NotifyStartedFunc: func() {
			r.logger.Infof("tcp server has started on %s", dnsAddress)
			r.tcpServerWorking.Store(true)
		},
	}

	return r
}

func (r *Resolver) startServers() {
	go func() {
		err := serveDNSServer(r.udpServer)
		if err != nil {
			r.logger.Errorf("serve udp server: %v", err)
		}
		r.udpServerWorking.Store(false)
	}()
	go func() {
		err := serveDNSServer(r.tcpServer)
		if err != nil {
			r.logger.Errorf("serve tcp server: %v", err)
		}
		r.tcpServerWorking.Store(false)
	}()
}

// serveDNSServer serves srv either on a provided listener (PacketConn or
// Listener set) or on a socket it binds itself (Addr and Net set).
func serveDNSServer(srv *dns.Server) error {
	if srv.PacketConn != nil || srv.Listener != nil {
		return srv.ActivateAndServe()
	}
	return srv.ListenAndServe()
}

// ReceiveConfiguration replaces the names the resolver serves: namesMapping
// holds IPv4 and namesMappingV6 IPv6 addresses, by names without the .awl
// suffix.
func (r *Resolver) ReceiveConfiguration(upstreamDNS string, namesMapping, namesMappingV6 map[string]netip.Addr) {
	cfg := config{
		upstreamDNS:     upstreamDNS,
		directMapping:   make(map[string]netip.Addr, len(namesMapping)),
		directMappingV6: make(map[string]netip.Addr, len(namesMappingV6)),
		reverseMapping:  make(map[netip.Addr]string, len(namesMapping)+len(namesMappingV6)),
	}
	cfg.addNames(cfg.directMapping, namesMapping)
	cfg.addNames(cfg.directMappingV6, namesMappingV6)
	r.cfg.Store(&cfg)
}

// addNames adds names to direct and to the reverse mapping.
func (cfg *config) addNames(direct, names map[string]netip.Addr) {
	for name, addr := range names {
		canonicalName := dns.CanonicalName(name + "." + LocalDomain)
		direct[canonicalName] = addr
		// we always have at least two names for one ip: peerName and peerID
		// for consistency we will take the shortest one (usually peerName, which is more human-readable)
		if existing, ok := cfg.reverseMapping[addr]; !ok || len(canonicalName) < len(existing) {
			cfg.reverseMapping[addr] = canonicalName
		}
	}
}

func (r *Resolver) DNSAddress() string {
	if !r.tcpServerWorking.Load() || !r.udpServerWorking.Load() {
		return ""
	}

	return r.dnsAddress
}

func (r *Resolver) Close() {
	err := r.udpServer.Shutdown()
	if err != nil {
		r.logger.Warnf("shutdown udp server: %v", err)
	}
	err = r.tcpServer.Shutdown()
	if err != nil {
		r.logger.Warnf("shutdown tcp server: %v", err)
	}
}

func (r *Resolver) dnsLocalDomainHandler(resp dns.ResponseWriter, req *dns.Msg) {
	metrics.DNSQueriesTotal.WithLabelValues("awl").Inc()
	start := time.Now()
	defer func() {
		metrics.DNSQueryDurationSeconds.Observe(time.Since(start).Seconds())
	}()

	if len(req.Question) == 0 {
		return
	}
	cfg := r.loadConfig()

	m := new(dns.Msg)
	m.SetReply(req)

	for _, question := range req.Question {
		wantA := question.Qtype == dns.TypeA || question.Qtype == dns.TypeANY
		wantAAAA := question.Qtype == dns.TypeAAAA || question.Qtype == dns.TypeANY
		if !wantA && !wantAAAA {
			continue
		}

		hostname := question.Name
		hostnameLower := strings.ToLower(hostname)
		addr, found := cfg.directMapping[hostnameLower]
		addrV6, foundV6 := cfg.directMappingV6[hostnameLower]
		if !found && !foundV6 {
			m.SetRcode(req, dns.RcodeNameError)
			continue
		}

		// A name without an address of the asked family gets NOERROR with no answers (NODATA).
		if wantA && found {
			m.Answer = append(m.Answer, addrRR(hostname, addr))
		}
		if wantAAAA && foundV6 {
			m.Answer = append(m.Answer, addrRR(hostname, addrV6))
		}
	}

	processOwnResponse(req, resp, m)

	_ = resp.WriteMsg(m)
}

func (r *Resolver) ptrHandler(resp dns.ResponseWriter, req *dns.Msg) {
	metrics.DNSQueriesTotal.WithLabelValues("awl_ptr").Inc()
	start := time.Now()
	defer func() {
		metrics.DNSQueryDurationSeconds.Observe(time.Since(start).Seconds())
	}()

	if len(req.Question) == 0 || req.Question[0].Qtype != dns.TypePTR {
		r.dnsProxyHandler(resp, req)
		return
	}

	name := req.Question[0].Name
	cfg := r.loadConfig()

	addr := ptrNameToAddr(name)
	mappedName, found := cfg.reverseMapping[addr]
	if !addr.IsValid() || !found {
		r.dnsProxyHandler(resp, req)
		return
	}

	m := new(dns.Msg)
	m.SetReply(req)

	ptr := &dns.PTR{
		Hdr: dns.RR_Header{
			Name:   name,
			Rrtype: dns.TypePTR,
			Class:  dns.ClassINET,
			Ttl:    defaultTTLSeconds,
		},
		Ptr: mappedName,
	}
	m.Answer = append(m.Answer, ptr)

	processOwnResponse(req, resp, m)

	_ = resp.WriteMsg(m)
}

func (r *Resolver) dnsProxyHandler(resp dns.ResponseWriter, req *dns.Msg) {
	metrics.DNSQueriesTotal.WithLabelValues("proxy").Inc()
	start := time.Now()
	defer func() {
		metrics.DNSQueryDurationSeconds.Observe(time.Since(start).Seconds())
	}()

	cfg := r.loadConfig()

	dnsClient := r.udpClient
	if _, ok := resp.RemoteAddr().(*net.TCPAddr); ok {
		dnsClient = r.tcpClient
	}

	upstreamResp, _, err := dnsClient.Exchange(req, cfg.upstreamDNS)
	if err != nil {
		metrics.DNSQueryErrorsTotal.Inc()
		r.logger.Warnf("send request to upstream dns: %v", err)
		m := new(dns.Msg)
		m.SetRcode(req, dns.RcodeServerFailure)
		_ = resp.WriteMsg(m)
		return
	}

	_ = resp.WriteMsg(upstreamResp)
}

func (r *Resolver) loadConfig() config {
	cfg := r.cfg.Load()
	if cfg == nil {
		return config{}
	}
	return *cfg
}

func processOwnResponse(req *dns.Msg, respWriter dns.ResponseWriter, resp *dns.Msg) {
	maxSize := dns.MinMsgSize
	if respWriter.LocalAddr().Network() == netTCP {
		maxSize = dns.MaxMsgSize
	} else {
		if optRR := req.IsEdns0(); optRR != nil {
			udpsize := int(optRR.UDPSize())
			if udpsize > maxSize {
				maxSize = udpsize
			}
		}
	}
	resp.Truncate(maxSize)

	resp.Authoritative = true
	resp.RecursionAvailable = true
}

func TrimDomainName(domain string) string {
	domain = strings.TrimSpace(domain)
	domain = strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return '_'
		}
		return r
	}, domain)

	return strings.ToLower(domain)
}

func IsValidDomainName(domain string) bool {
	_, ok := dns.IsDomainName(domain + "." + LocalDomain)
	return ok && domain == TrimDomainName(domain)
}

// addrRR returns the record, A or AAAA by the family of addr, mapping name to addr.
func addrRR(name string, addr netip.Addr) dns.RR {
	hdr := dns.RR_Header{
		// we should return original name from the request as some clients expect that
		Name:  name,
		Class: dns.ClassINET,
		Ttl:   defaultTTLSeconds,
	}
	if addr.Is4() {
		hdr.Rrtype = dns.TypeA
		return &dns.A{Hdr: hdr, A: addr.AsSlice()}
	}
	hdr.Rrtype = dns.TypeAAAA
	return &dns.AAAA{Hdr: hdr, AAAA: addr.AsSlice()}
}

// ptrNameToAddr returns the address a PTR query name (in-addr.arpa or
// ip6.arpa) is about, or an invalid address if the name is malformed.
func ptrNameToAddr(name string) netip.Addr {
	name = strings.ToLower(name)
	if labels, ok := strings.CutSuffix(name, ptrV6Suffix); ok {
		return ptrV6LabelsToAddr(labels)
	}
	return ptrV4LabelsToAddr(strings.TrimSuffix(name, ptrV4Suffix))
}

// ptrV4LabelsToAddr parses the octets of an in-addr.arpa name, in reverse
// order: "4.3.2.1" is 1.2.3.4.
func ptrV4LabelsToAddr(labels string) netip.Addr {
	reversed, err := netip.ParseAddr(labels)
	if err != nil || !reversed.Is4() {
		return netip.Addr{}
	}
	b := reversed.As4()
	return netip.AddrFrom4([4]byte{b[3], b[2], b[1], b[0]})
}

// ptrV6LabelsToAddr parses the 32 nibbles of an ip6.arpa name, one hex digit
// per label, least significant first.
func ptrV6LabelsToAddr(labels string) netip.Addr {
	nibbles := strings.Split(labels, ".")
	if len(nibbles) != 32 {
		return netip.Addr{}
	}
	var b [16]byte
	for i, nibble := range nibbles {
		if len(nibble) != 1 {
			return netip.Addr{}
		}
		v, err := strconv.ParseUint(nibble, 16, 8)
		if err != nil {
			return netip.Addr{}
		}
		pos := 31 - i // nibble position in the address, most significant first
		if pos%2 == 0 {
			v <<= 4
		}
		b[pos/2] |= byte(v)
	}
	return netip.AddrFrom16(b)
}
