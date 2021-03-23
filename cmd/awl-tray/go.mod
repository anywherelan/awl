module awl-tray

go 1.14

require (
	github.com/Kodeworks/golang-image-ico v0.0.0-20141118225523-73f0f4cfade9
	github.com/anywherelan/awl v0.0.0-00010101000000-000000000000
	github.com/getlantern/systray v1.0.2
	github.com/ipfs/go-log/v2 v2.0.5
	github.com/rakyll/statik v0.1.7
	github.com/skratchdot/open-golang v0.0.0-20200116055534-eef842397966
)

replace (
	github.com/anywherelan/awl => ../../
	github.com/ipfs/go-log/v2 => github.com/anywherelan/go-log/v2 v2.0.3-0.20210308150645-ad120b957e42
	github.com/libp2p/go-libp2p-swarm => github.com/anywherelan/go-libp2p-swarm v0.2.8-0.20210308145331-4dade4a1a222
)
