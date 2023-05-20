package awldns

import (
	"net"
	"strings"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/ipfs/go-log/v2"
	"github.com/miekg/dns"
)

const (
	defaultTTL        = 60 * time.Second
	defaultTTLSeconds = uint32(defaultTTL / time.Second)
	ptrV4Suffix       = ".in-addr.arpa."
)

const (
	LocalDomain               = "awl"
	DNSIp                     = "127.0.0.66"
	DefaultDNSPort            = "53"
	DNSAddress                = "127.0.0.66:53"
	DefaultUpstreamDNSAddress = "1.1.1.1:53"
)

type Resolver struct {
	udpServer *dns.Server
	tcpServer *dns.Server
	udpClient *dns.Client
	tcpClient *dns.Client
	cfg       atomic.Pointer[config]
	logger    *log.ZapEventLogger

	udpServerWorking bool
	tcpServerWorking bool

	dnsAddress string
}

type config struct {
	upstreamDNS    string
	directMapping  map[string]string
	reverseMapping map[string]string
}

func NewResolver(dnsAddress string) *Resolver {
	r := &Resolver{
		logger: log.Logger("awl/dns"),
		udpClient: &dns.Client{
			Net: "udp",
		},
		tcpClient: &dns.Client{
			Net: "tcp",
		},
		dnsAddress: dnsAddress,
	}
	r.cfg.Store(&config{})

	mux := dns.NewServeMux()
	mux.HandleFunc(LocalDomain, r.dnsLocalDomainHandler)
	mux.HandleFunc(strings.TrimPrefix(ptrV4Suffix, "."), r.ptrv4Handler)
	mux.HandleFunc(".", r.dnsProxyHandler)

	r.udpServer = &dns.Server{
		Addr:    dnsAddress,
		Net:     "udp",
		Handler: mux,
		NotifyStartedFunc: func() {
			r.logger.Infof("udp server has started on %s", dnsAddress)
			r.udpServerWorking = true
		},
	}
	r.tcpServer = &dns.Server{
		Addr:    dnsAddress,
		Net:     "tcp",
		Handler: mux,
		NotifyStartedFunc: func() {
			r.logger.Infof("tcp server has started on %s", dnsAddress)
			r.tcpServerWorking = true
		},
	}
	go func() {
		err := r.udpServer.ListenAndServe()
		if err != nil {
			r.logger.Errorf("serve udp server: %v", err)
		}
		r.udpServerWorking = false
	}()
	go func() {
		err := r.tcpServer.ListenAndServe()
		if err != nil {
			r.logger.Errorf("serve tcp server: %v", err)
		}
		r.tcpServerWorking = false
	}()

	return r
}

func (r *Resolver) ReceiveConfiguration(upstreamDNS string, namesMapping map[string]string) {
	reverseMapping := make(map[string]string, len(namesMapping))
	directMapping := make(map[string]string, len(namesMapping))
	for key, ip := range namesMapping {
		canonicalName := dns.CanonicalName(key + "." + LocalDomain)
		directMapping[canonicalName] = ip
		existedName, exists := reverseMapping[ip]
		// we always have at least two names for one ip: peerName and peerID
		// for consistency we will take the shortest one (usually peerName, which is more human-readable)
		if !exists {
			reverseMapping[ip] = canonicalName
		} else if exists && len(canonicalName) < len(existedName) {
			reverseMapping[ip] = canonicalName
		}
	}

	cfg := config{
		upstreamDNS:    upstreamDNS,
		directMapping:  directMapping,
		reverseMapping: reverseMapping,
	}
	r.cfg.Store(&cfg)
}

func (r *Resolver) DNSAddress() string {
	if !r.tcpServerWorking || !r.udpServerWorking {
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
	if len(req.Question) == 0 {
		return
	}
	cfg := r.loadConfig()

	m := new(dns.Msg)
	m.SetReply(req)

	for _, question := range req.Question {
		hostname := question.Name
		qtype := question.Qtype
		hostnameLower := strings.ToLower(hostname)
		mappedIP, found := cfg.directMapping[hostnameLower]

		switch qtype {
		case dns.TypeA, dns.TypeAAAA, dns.TypeANY:
			aRec := &dns.A{
				Hdr: dns.RR_Header{
					// we should return original name from the request as some clients expect that
					Name:   hostname,
					Rrtype: dns.TypeA,
					Class:  dns.ClassINET,
					Ttl:    defaultTTLSeconds,
				},
			}
			if found {
				// TODO: support ipv6
				aRec.A = net.ParseIP(mappedIP).To4()
			} else {
				m.SetRcode(req, dns.RcodeNameError)
			}
			m.Answer = append(m.Answer, aRec)
		}
	}

	processOwnResponse(req, resp, m)

	_ = resp.WriteMsg(m)
}

func (r *Resolver) ptrv4Handler(resp dns.ResponseWriter, req *dns.Msg) {
	if len(req.Question) == 0 || req.Question[0].Qtype != dns.TypePTR {
		r.dnsProxyHandler(resp, req)
		return
	}

	name := req.Question[0].Name
	cfg := r.loadConfig()

	ip := ptrV4NameToIP(name)
	if ip == nil {
		r.dnsProxyHandler(resp, req)
		return
	}
	mappedName, found := cfg.reverseMapping[ip.String()]
	if !found {
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
	cfg := r.loadConfig()

	dnsClient := r.udpClient
	if _, ok := resp.RemoteAddr().(*net.TCPAddr); ok {
		dnsClient = r.tcpClient
	}

	upstreamResp, _, err := dnsClient.Exchange(req, cfg.upstreamDNS)
	if err != nil {
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
	if respWriter.LocalAddr().Network() == "tcp" {
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

func ptrV4NameToIP(name string) net.IP {
	s := strings.TrimSuffix(name, ptrV4Suffix)
	revIp := net.ParseIP(s)
	revIp = revIp.To4()
	if revIp == nil {
		return nil
	}
	return net.IP{revIp[3], revIp[2], revIp[1], revIp[0]}
}
