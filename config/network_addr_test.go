package config

import (
	"net"
	"testing"

	"github.com/libp2p/go-libp2p/p2p/host/eventbus"
)

func TestIncrementIPAddr(t *testing.T) {
	type args struct {
		ip string
	}
	tests := []struct {
		name string
		args args
		want string
	}{
		{name: "", args: args{ip: "127.16.0.1"}, want: "127.16.0.2"},
		{name: "", args: args{ip: "127.16.0.236"}, want: "127.16.0.237"},
		{name: "", args: args{ip: "127.16.1.1"}, want: "127.16.1.2"},
		{name: "", args: args{ip: "127.16.3.254"}, want: "127.16.3.255"},
		{name: "", args: args{ip: "127.16.0.254"}, want: "127.16.0.255"},
		{name: "", args: args{ip: "127.16.0.255"}, want: "127.16.1.0"},
		{name: "", args: args{ip: "10.66.0.1"}, want: "10.66.0.2"},
	}
	for _, tt := range tests {
		ip := net.ParseIP(tt.args.ip)
		ip = ip.To4()
		t.Run(tt.name, func(t *testing.T) {
			if got := incrementIPAddr(ip); got.String() != tt.want {
				t.Errorf("IncrementIPAddr() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestConfig_GenerateNextIpAddr(t *testing.T) {
	cfg := new(Config)
	setDefaults(cfg, eventbus.NewBus())

	addr := cfg.GenerateNextIpAddr()
	if addr != "10.66.0.2" {
		t.Fail()
	}
}
