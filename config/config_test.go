package config

import (
	"testing"
)

func TestConfig_GetBootstrapPeers(t *testing.T) {
	cfg := &Config{}
	bootstrapPeers := cfg.GetBootstrapPeers()
	if len(bootstrapPeers) != 6 {
		t.Fatal()
	}
}
