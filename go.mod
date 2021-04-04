module github.com/anywherelan/awl

go 1.14

require (
	github.com/go-playground/validator/v10 v10.3.0
	github.com/ipfs/go-datastore v0.4.4
	github.com/ipfs/go-log/v2 v2.0.5
	github.com/labstack/echo/v4 v4.1.16
	github.com/libp2p/go-eventbus v0.2.1
	github.com/libp2p/go-libp2p v0.10.0
	github.com/libp2p/go-libp2p-connmgr v0.2.4
	github.com/libp2p/go-libp2p-core v0.6.0
	github.com/libp2p/go-libp2p-kad-dht v0.8.1
	github.com/libp2p/go-libp2p-noise v0.1.1
	github.com/libp2p/go-libp2p-peerstore v0.2.6
	github.com/libp2p/go-libp2p-quic-transport v0.6.0
	github.com/libp2p/go-libp2p-swarm v0.2.7
	github.com/libp2p/go-libp2p-tls v0.1.3
	github.com/libp2p/go-tcp-transport v0.2.0
	github.com/libp2p/go-ws-transport v0.3.1
	github.com/milosgajdos/tenus v0.0.3
	github.com/mr-tron/base58 v1.2.0
	github.com/multiformats/go-multiaddr v0.2.2
	github.com/stretchr/testify v1.6.1
	go.uber.org/multierr v1.5.0
	go.uber.org/zap v1.15.0
	golang.org/x/net v0.0.0-20210119194325-5f4716e94777
	golang.org/x/sys v0.0.0-20210124154548-22da62e12c0c
	golang.zx2c4.com/wireguard v0.0.0-20210203165646-9c7bd73be2cc
	golang.zx2c4.com/wireguard/windows v0.3.5
)

replace (
	github.com/ipfs/go-log/v2 => github.com/anywherelan/go-log/v2 v2.0.3-0.20210308150645-ad120b957e42
	github.com/libp2p/go-libp2p-swarm => github.com/anywherelan/go-libp2p-swarm v0.2.8-0.20210308145331-4dade4a1a222
)
