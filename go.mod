module github.com/anywherelan/awl

go 1.16

require (
	github.com/anywherelan/ts-dns v0.0.0-20211016195049-babc83989aee
	github.com/go-playground/validator/v10 v10.9.0
	github.com/ipfs/go-datastore v0.4.6
	github.com/ipfs/go-log/v2 v2.3.0
	github.com/labstack/echo/v4 v4.6.1
	github.com/libp2p/go-eventbus v0.2.1
	github.com/libp2p/go-libp2p v0.15.1
	github.com/libp2p/go-libp2p-connmgr v0.2.4
	github.com/libp2p/go-libp2p-core v0.9.0
	github.com/libp2p/go-libp2p-kad-dht v0.13.1
	github.com/libp2p/go-libp2p-noise v0.2.2
	github.com/libp2p/go-libp2p-peerstore v0.2.8
	github.com/libp2p/go-libp2p-quic-transport v0.11.2
	github.com/libp2p/go-libp2p-swarm v0.5.3
	github.com/libp2p/go-libp2p-tls v0.2.0
	github.com/libp2p/go-tcp-transport v0.2.8
	github.com/miekg/dns v1.1.43
	github.com/milosgajdos/tenus v0.0.3
	github.com/mr-tron/base58 v1.2.0
	github.com/multiformats/go-multiaddr v0.4.1
	github.com/olekukonko/tablewriter v0.0.5
	github.com/stretchr/testify v1.7.0
	github.com/urfave/cli/v2 v2.3.0
	go.uber.org/multierr v1.7.0
	go.uber.org/zap v1.19.1
	golang.org/x/net v0.0.0-20210913180222-943fd674d43e
	golang.org/x/sys v0.0.0-20210927094055-39ccf1dd6fa6
	golang.zx2c4.com/wireguard v0.0.0-20210905140043-2ef39d47540c
	golang.zx2c4.com/wireguard/windows v0.4.10
	inet.af/netaddr v0.0.0-20210721214506-ce7a8ad02cc1
)

replace github.com/ipfs/go-log/v2 => github.com/anywherelan/go-log/v2 v2.0.3-0.20210308150645-ad120b957e42
