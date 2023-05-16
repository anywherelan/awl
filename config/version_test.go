package config

import (
	"testing"
)

func TestVersionFromUserAgent(t *testing.T) {
	type args struct {
		userAgent string
	}
	tests := []struct {
		name string
		args args
		want string
	}{
		{
			name: "old_useragent",
			args: args{userAgent: "awl/v0.5.0"},
			want: "v0.5.0",
		},
		{
			name: "old_useragent_dev",
			args: args{userAgent: "awl/dev"},
			want: "dev",
		},
		{
			name: "new_useragent",
			args: args{userAgent: "awl/linux-amd64/v0.5.0"},
			want: "v0.5.0",
		},
		{
			name: "new_useragent_dev",
			args: args{userAgent: "awl/linux-amd64/dev"},
			want: "dev",
		},
		{
			name: "empty",
			args: args{userAgent: ""},
			want: "",
		},
		{
			name: "invalid_slash",
			args: args{userAgent: "/"},
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := VersionFromUserAgent(tt.args.userAgent); got != tt.want {
				t.Errorf("VersionFromUserAgent() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSystemInfoFromUserAgent(t *testing.T) {
	type args struct {
		userAgent string
	}
	tests := []struct {
		name       string
		args       args
		wantGoos   string
		wantGoarch string
	}{
		{
			name:       "old_useragent",
			args:       args{userAgent: "awl/v0.5.0"},
			wantGoos:   "",
			wantGoarch: "",
		},
		{
			name:       "old_useragent_dev",
			args:       args{userAgent: "awl/dev"},
			wantGoos:   "",
			wantGoarch: "",
		},
		{
			name:       "new_useragent",
			args:       args{userAgent: "awl/linux-amd64/v0.5.0"},
			wantGoos:   "linux",
			wantGoarch: "amd64",
		},
		{
			name:       "new_useragent_dev",
			args:       args{userAgent: "awl/linux-amd64/dev"},
			wantGoos:   "linux",
			wantGoarch: "amd64",
		},
		{
			name:       "empty",
			args:       args{userAgent: ""},
			wantGoos:   "",
			wantGoarch: "",
		},
		{
			name:       "invalid_slash",
			args:       args{userAgent: "/"},
			wantGoos:   "",
			wantGoarch: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotGoos, gotGoarch := SystemInfoFromUserAgent(tt.args.userAgent)
			if gotGoos != tt.wantGoos {
				t.Errorf("SystemInfoFromUserAgent() gotGoos = %v, want %v", gotGoos, tt.wantGoos)
			}
			if gotGoarch != tt.wantGoarch {
				t.Errorf("SystemInfoFromUserAgent() gotGoarch = %v, want %v", gotGoarch, tt.wantGoarch)
			}
		})
	}
}
