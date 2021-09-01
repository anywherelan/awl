package awldns

import (
	"net"
	"strings"
	"sync/atomic"
	"time"

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
	cfg       atomic.Value
	logger    *log.ZapEventLogger
}

type config struct {
	upstreamDNS    string
	directMapping  map[string]string
	reverseMapping map[string]string
}

func NewResolver() *Resolver {
	r := &Resolver{
		logger: log.Logger("awl/dns"),
		udpClient: &dns.Client{
			Net:            "udp",
			SingleInflight: true,
		},
		tcpClient: &dns.Client{
			Net:            "tcp",
			SingleInflight: true,
		},
	}
	r.cfg.Store(config{upstreamDNS: "127.0.0.1:53"})

	mux := dns.NewServeMux()
	mux.HandleFunc(LocalDomain, r.dnsLocalDomainHandler)
	mux.HandleFunc(strings.TrimPrefix(ptrV4Suffix, "."), r.ptrv4Handler)
	mux.HandleFunc(".", r.dnsProxyHandler)

	r.udpServer = &dns.Server{
		Addr:    DNSAddress,
		Net:     "udp",
		Handler: mux,
		NotifyStartedFunc: func() {
			r.logger.Infof("udp server has started on %s", DNSAddress)
		},
	}
	r.tcpServer = &dns.Server{
		Addr:    DNSAddress,
		Net:     "tcp",
		Handler: mux,
		NotifyStartedFunc: func() {
			r.logger.Infof("tcp server has started on %s", DNSAddress)
		},
	}
	go func() {
		err := r.udpServer.ListenAndServe()
		if err != nil {
			r.logger.Errorf("serve udp server: %v", err)
		}
	}()
	go func() {
		err := r.tcpServer.ListenAndServe()
		if err != nil {
			r.logger.Errorf("serve tcp server: %v", err)
		}
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
		} else if exists && canonicalName < existedName {
			reverseMapping[ip] = canonicalName
		}
	}

	cfg := config{
		upstreamDNS:    upstreamDNS,
		directMapping:  directMapping,
		reverseMapping: reverseMapping,
	}
	r.cfg.Store(cfg)
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
	cfg := r.cfg.Load().(config)

	m := new(dns.Msg)
	m.SetReply(req)
	m.Authoritative = true

	for _, question := range req.Question {
		hostname := question.Name
		qtype := question.Qtype
		mappedIP, found := cfg.directMapping[hostname]

		switch qtype {
		case dns.TypeA, dns.TypeAAAA, dns.TypeANY:
			aRec := &dns.A{
				Hdr: dns.RR_Header{
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

	_ = resp.WriteMsg(m)
}

func (r *Resolver) ptrv4Handler(resp dns.ResponseWriter, req *dns.Msg) {
	if len(req.Question) == 0 || req.Question[0].Qtype != dns.TypePTR {
		r.dnsProxyHandler(resp, req)
		return
	}

	name := req.Question[0].Name
	cfg := r.cfg.Load().(config)

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
	m.Authoritative = true

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
	_ = resp.WriteMsg(m)
}

func (r *Resolver) dnsProxyHandler(resp dns.ResponseWriter, req *dns.Msg) {
	cfg := r.cfg.Load().(config)

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

func ptrV4NameToIP(name string) net.IP {
	s := strings.TrimSuffix(name, ptrV4Suffix)
	revIp := net.ParseIP(s)
	revIp = revIp.To4()
	if revIp == nil {
		return nil
	}
	return net.IP{revIp[3], revIp[2], revIp[1], revIp[0]}
}
