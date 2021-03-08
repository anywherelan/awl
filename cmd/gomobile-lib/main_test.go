package peerlan

import (
	"testing"
)

func Test_getPortFromAddress(t *testing.T) {
	type args struct {
		addr string
	}
	tests := []struct {
		name string
		args args
		want int
	}{
		{name: "", args: args{"127.0.0.1:123"}, want: 123},
		{name: "", args: args{"localhost:9"}, want: 9},
		{name: "", args: args{":2"}, want: 2},
		{name: "", args: args{"0.0.0.0:3"}, want: 3},
		{name: "", args: args{"192.168.1.45:6"}, want: 6},
		{name: "", args: args{"172.17.0.1:9563"}, want: 9563},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := getPortFromAddress(tt.args.addr); got != tt.want {
				t.Errorf("getPortFromAddress() = %v, want %v", got, tt.want)
			}
		})
	}
}
