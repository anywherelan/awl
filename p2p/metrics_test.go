package p2p

import (
	"reflect"
	"testing"

	ma "github.com/multiformats/go-multiaddr"
)

func Test_parseMultiaddrToInfo(t *testing.T) {
	type args struct {
		addr ma.Multiaddr
	}
	tests := []struct {
		name  string
		args  args
		want  ConnectionInfo
		want1 bool
	}{
		{
			name: "tcp",
			args: args{addr: mustNewMultiaddr("/ip4/192.168.1.21/tcp/6300")},
			want: ConnectionInfo{
				Multiaddr:    "/ip4/192.168.1.21/tcp/6300",
				ThroughRelay: false,
				RelayPeerID:  "",
				Address:      "192.168.1.21:6300",
				Protocol:     "tcp",
			},
			want1: true,
		},
		{
			name: "quic",
			args: args{addr: mustNewMultiaddr("/ip4/192.168.1.21/udp/6400/quic")},
			want: ConnectionInfo{
				Multiaddr:    "/ip4/192.168.1.21/udp/6400/quic",
				ThroughRelay: false,
				RelayPeerID:  "",
				Address:      "192.168.1.21:6400",
				Protocol:     "quic",
			},
			want1: true,
		},
		{
			name: "quic-v1",
			args: args{addr: mustNewMultiaddr("/ip4/192.168.1.11/udp/6100/quic-v1")},
			want: ConnectionInfo{
				Multiaddr:    "/ip4/192.168.1.11/udp/6100/quic-v1",
				ThroughRelay: false,
				RelayPeerID:  "",
				Address:      "192.168.1.11:6100",
				Protocol:     "quic",
			},
			want1: true,
		},
		{
			name: "relay",
			args: args{addr: mustNewMultiaddr("/ip4/192.168.1.21/udp/6150/quic-v1/p2p/12D3KooWNWa2r6dJVogbjNf1CKrKNttVAhKZr1PpWRPJYX7o4t4M/p2p-circuit")},
			want: ConnectionInfo{
				Multiaddr:    "/ip4/192.168.1.21/udp/6150/quic-v1/p2p/12D3KooWNWa2r6dJVogbjNf1CKrKNttVAhKZr1PpWRPJYX7o4t4M/p2p-circuit",
				ThroughRelay: true,
				RelayPeerID:  "12D3KooWNWa2r6dJVogbjNf1CKrKNttVAhKZr1PpWRPJYX7o4t4M",
				Address:      "",
				Protocol:     "",
			},
			want1: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, got1 := parseMultiaddrToInfo(tt.args.addr)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("parseMultiaddrToInfo() got = %v, want %v", got, tt.want)
			}
			if got1 != tt.want1 {
				t.Errorf("parseMultiaddrToInfo() got1 = %v, want %v", got1, tt.want1)
			}
		})
	}
}

func mustNewMultiaddr(addrStr string) ma.Multiaddr {
	multiaddr, err := ma.NewMultiaddr(addrStr)
	if err != nil {
		panic(err)
	}
	return multiaddr
}
