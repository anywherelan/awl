module awl-tray

go 1.20

require (
	fyne.io/systray v1.10.1-0.20230403195833-7dc3c09283d6
	github.com/GrigoryKrasnochub/updaterini v0.1.0
	github.com/Kodeworks/golang-image-ico v0.0.0-20141118225523-73f0f4cfade9
	github.com/anywherelan/awl v0.0.0-00010101000000-000000000000
	github.com/gen2brain/beeep v0.0.0-20230307103607-6e717729cb4f
	github.com/godbus/dbus/v5 v5.1.0
	github.com/ipfs/go-log/v2 v2.5.1
	github.com/libp2p/go-libp2p v0.27.2
	github.com/ncruces/zenity v0.10.8
	github.com/skratchdot/open-golang v0.0.0-20200116055534-eef842397966
	golang.org/x/exp v0.0.0-20230321023759-10a507213a29
)

replace (
	fyne.io/systray => github.com/anywherelan/systray v0.0.0-20230509110713-9d662d95edcb
	github.com/anywherelan/awl => ../../
	github.com/ipfs/go-log/v2 => github.com/anywherelan/go-log/v2 v2.0.3-0.20221101180049-46e3967f6fe5
	github.com/ncruces/zenity => github.com/pymq/zenity v0.0.0-20230509161854-c117c448544d
)

require (
	github.com/akavel/rsrc v0.10.2 // indirect
	github.com/anywherelan/ts-dns v0.0.0-20211016195049-babc83989aee // indirect
	github.com/benbjohnson/clock v1.3.0 // indirect
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/blang/semver/v4 v4.0.0 // indirect
	github.com/cespare/xxhash/v2 v2.2.0 // indirect
	github.com/containerd/cgroups v1.1.0 // indirect
	github.com/coreos/go-systemd/v22 v22.5.0 // indirect
	github.com/cpuguy83/go-md2man/v2 v2.0.2 // indirect
	github.com/davidlazar/go-crypto v0.0.0-20200604182044-b73af7476f6c // indirect
	github.com/dchest/jsmin v0.0.0-20220218165748-59f39799265f // indirect
	github.com/decred/dcrd/dcrec/secp256k1/v4 v4.1.0 // indirect
	github.com/docker/go-units v0.5.0 // indirect
	github.com/docker/libcontainer v2.2.1+incompatible // indirect
	github.com/elastic/gosigar v0.14.2 // indirect
	github.com/flynn/noise v1.0.0 // indirect
	github.com/francoispqt/gojay v1.2.13 // indirect
	github.com/go-logr/logr v1.2.3 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/go-playground/locales v0.14.1 // indirect
	github.com/go-playground/universal-translator v0.18.1 // indirect
	github.com/go-playground/validator/v10 v10.13.0 // indirect
	github.com/go-task/slim-sprig v0.0.0-20230315185526-52ccab3ef572 // indirect
	github.com/go-toast/toast v0.0.0-20190211030409-01e6764cf0a4 // indirect
	github.com/gogo/protobuf v1.3.2 // indirect
	github.com/golang-jwt/jwt v3.2.2+incompatible // indirect
	github.com/golang/mock v1.6.0 // indirect
	github.com/golang/protobuf v1.5.3 // indirect
	github.com/google/go-querystring v1.1.0 // indirect
	github.com/google/gopacket v1.1.19 // indirect
	github.com/google/pprof v0.0.0-20230405160723-4a4c7d95572b // indirect
	github.com/google/uuid v1.3.0 // indirect
	github.com/hashicorp/errwrap v1.1.0 // indirect
	github.com/hashicorp/go-multierror v1.1.1 // indirect
	github.com/hashicorp/golang-lru v0.5.4 // indirect
	github.com/huin/goupnp v1.1.0 // indirect
	github.com/ipfs/boxo v0.8.0 // indirect
	github.com/ipfs/go-cid v0.4.1 // indirect
	github.com/ipfs/go-datastore v0.6.0 // indirect
	github.com/ipfs/go-ipfs-util v0.0.2 // indirect
	github.com/ipfs/go-log v1.0.5 // indirect
	github.com/ipld/go-ipld-prime v0.20.0 // indirect
	github.com/jackpal/go-nat-pmp v1.0.2 // indirect
	github.com/jbenet/go-temp-err-catcher v0.1.0 // indirect
	github.com/jbenet/goprocess v0.1.4 // indirect
	github.com/josephspurrier/goversioninfo v1.4.0 // indirect
	github.com/klauspost/compress v1.16.4 // indirect
	github.com/klauspost/cpuid/v2 v2.2.4 // indirect
	github.com/koron/go-ssdp v0.0.4 // indirect
	github.com/labstack/echo/v4 v4.10.2 // indirect
	github.com/labstack/gommon v0.4.0 // indirect
	github.com/leodido/go-urn v1.2.3 // indirect
	github.com/libp2p/go-buffer-pool v0.1.0 // indirect
	github.com/libp2p/go-cidranger v1.1.0 // indirect
	github.com/libp2p/go-flow-metrics v0.1.0 // indirect
	github.com/libp2p/go-libp2p-asn-util v0.3.0 // indirect
	github.com/libp2p/go-libp2p-kad-dht v0.23.0 // indirect
	github.com/libp2p/go-libp2p-kbucket v0.5.0 // indirect
	github.com/libp2p/go-libp2p-record v0.2.0 // indirect
	github.com/libp2p/go-msgio v0.3.0 // indirect
	github.com/libp2p/go-nat v0.1.0 // indirect
	github.com/libp2p/go-netroute v0.2.1 // indirect
	github.com/libp2p/go-reuseport v0.2.0 // indirect
	github.com/libp2p/go-yamux/v4 v4.0.0 // indirect
	github.com/marten-seemann/tcp v0.0.0-20210406111302-dfbc87cc63fd // indirect
	github.com/mattn/go-colorable v0.1.13 // indirect
	github.com/mattn/go-isatty v0.0.18 // indirect
	github.com/mattn/go-runewidth v0.0.9 // indirect
	github.com/matttproud/golang_protobuf_extensions v1.0.4 // indirect
	github.com/miekg/dns v1.1.54 // indirect
	github.com/mikioh/tcpinfo v0.0.0-20190314235526-30a79bb1804b // indirect
	github.com/mikioh/tcpopt v0.0.0-20190314235656-172688c1accc // indirect
	github.com/milosgajdos/tenus v0.0.3 // indirect
	github.com/minio/sha256-simd v1.0.0 // indirect
	github.com/mr-tron/base58 v1.2.0 // indirect
	github.com/multiformats/go-base32 v0.1.0 // indirect
	github.com/multiformats/go-base36 v0.2.0 // indirect
	github.com/multiformats/go-multiaddr v0.9.0 // indirect
	github.com/multiformats/go-multiaddr-dns v0.3.1 // indirect
	github.com/multiformats/go-multiaddr-fmt v0.1.0 // indirect
	github.com/multiformats/go-multibase v0.2.0 // indirect
	github.com/multiformats/go-multicodec v0.8.1 // indirect
	github.com/multiformats/go-multihash v0.2.1 // indirect
	github.com/multiformats/go-multistream v0.4.1 // indirect
	github.com/multiformats/go-varint v0.0.7 // indirect
	github.com/nu7hatch/gouuid v0.0.0-20131221200532-179d4d0c4d8d // indirect
	github.com/olekukonko/tablewriter v0.0.5 // indirect
	github.com/onsi/ginkgo/v2 v2.9.2 // indirect
	github.com/opencontainers/runtime-spec v1.0.2 // indirect
	github.com/opentracing/opentracing-go v1.2.0 // indirect
	github.com/pbnjay/memory v0.0.0-20210728143218-7b4eea64cf58 // indirect
	github.com/pkg/errors v0.9.1 // indirect
	github.com/polydawn/refmt v0.89.0 // indirect
	github.com/prometheus/client_golang v1.14.0 // indirect
	github.com/prometheus/client_model v0.3.0 // indirect
	github.com/prometheus/common v0.42.0 // indirect
	github.com/prometheus/procfs v0.9.0 // indirect
	github.com/quic-go/qpack v0.4.0 // indirect
	github.com/quic-go/qtls-go1-19 v0.3.2 // indirect
	github.com/quic-go/qtls-go1-20 v0.2.2 // indirect
	github.com/quic-go/quic-go v0.33.0 // indirect
	github.com/quic-go/webtransport-go v0.5.2 // indirect
	github.com/randall77/makefat v0.0.0-20210315173500-7ddd0e42c844 // indirect
	github.com/raulk/go-watchdog v1.3.0 // indirect
	github.com/russross/blackfriday/v2 v2.1.0 // indirect
	github.com/spaolacci/murmur3 v1.1.0 // indirect
	github.com/tadvi/systray v0.0.0-20190226123456-11a2b8fa57af // indirect
	github.com/tevino/abool v1.2.0 // indirect
	github.com/urfave/cli/v2 v2.25.3 // indirect
	github.com/valyala/bytebufferpool v1.0.0 // indirect
	github.com/valyala/fasttemplate v1.2.2 // indirect
	github.com/whyrusleeping/go-keyspace v0.0.0-20160322163242-5b898ac5add1 // indirect
	github.com/xrash/smetrics v0.0.0-20201216005158-039620a65673 // indirect
	go.opencensus.io v0.24.0 // indirect
	go.opentelemetry.io/otel v1.14.0 // indirect
	go.opentelemetry.io/otel/trace v1.14.0 // indirect
	go.uber.org/atomic v1.10.0 // indirect
	go.uber.org/dig v1.16.1 // indirect
	go.uber.org/fx v1.19.2 // indirect
	go.uber.org/multierr v1.11.0 // indirect
	go.uber.org/zap v1.24.0 // indirect
	go4.org/intern v0.0.0-20211027215823-ae77deb06f29 // indirect
	go4.org/unsafe/assume-no-moving-gc v0.0.0-20230221090011-e4bae7ad2296 // indirect
	golang.org/x/crypto v0.7.0 // indirect
	golang.org/x/image v0.7.0 // indirect
	golang.org/x/mod v0.10.0 // indirect
	golang.org/x/net v0.9.0 // indirect
	golang.org/x/sync v0.1.0 // indirect
	golang.org/x/sys v0.8.0 // indirect
	golang.org/x/text v0.9.0 // indirect
	golang.org/x/time v0.3.0 // indirect
	golang.org/x/tools v0.7.0 // indirect
	golang.zx2c4.com/wireguard v0.0.0-20210905140043-2ef39d47540c // indirect
	golang.zx2c4.com/wireguard/windows v0.4.10 // indirect
	gonum.org/v1/gonum v0.11.0 // indirect
	google.golang.org/protobuf v1.30.0 // indirect
	inet.af/netaddr v0.0.0-20220811202034-502d2d690317 // indirect
	lukechampine.com/blake3 v1.1.7 // indirect
	nhooyr.io/websocket v1.8.7 // indirect
)
