package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/p2p/host/eventbus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConfig_GetBootstrapPeers(t *testing.T) {
	cfg := &Config{}
	bootstrapPeers := cfg.GetBootstrapPeers()
	if len(bootstrapPeers) != 5 {
		t.Fatal()
	}
}

func TestConfigSave(t *testing.T) {
	conf, path := newTestConfig(t)

	conf.Lock()
	conf.P2pNode.Name = "first"
	conf.Unlock()
	conf.Save()

	requireSaved(t, path, func(c *Config) bool {
		return c.P2pNode.Name == "first"
	})

	conf.Lock()
	conf.P2pNode.Name = "b"
	conf.Unlock()
	conf.Save()

	requireSaved(t, path, func(c *Config) bool {
		return c.P2pNode.Name == "b"
	})

	// make sure the staging file the atomic write goes through does not survive into the data dir.
	entries, err := os.ReadDir(conf.dataDir)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, AppConfigFilename, entries[0].Name())

	// check permission. Windows has no unix modes: os.Stat reports 0666 for any
	// writable file and 0444 for a read-only one, and Chmod only toggles the
	// read-only attribute, so there is nothing to assert there.
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(filesPerm), info.Mode().Perm())
	}
}

func TestConfigSaveThenLoad(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(AppDataDirEnvKey, dir)

	conf := &Config{dataDir: dir}
	setDefaults(conf, eventbus.NewBus())
	conf.startWriter()
	conf.P2pNode.Name = "saved-node"
	conf.KnownPeers["peer-1"] = KnownPeer{PeerID: "peer-1", Alias: "alias-1"}
	conf.Save()
	conf.Close()

	loaded, err := LoadConfigReadOnly(AppTypeAwl)
	require.NoError(t, err)
	assert.Equal(t, "saved-node", loaded.P2pNode.Name)
	assert.Equal(t, "alias-1", loaded.KnownPeers["peer-1"].Alias)
}

// TestConfigSaveCoalesces checks the property the capacity-one trigger buys:
// a burst of saves neither blocks the caller nor turns into a write each, and
// what lands on disk is the newest state rather than whichever snapshot won.
func TestConfigSaveCoalesces(t *testing.T) {
	conf, path := newTestConfig(t)

	started := time.Now()
	for i := range 200 {
		conf.Lock()
		conf.P2pNode.Name = fmt.Sprintf("node-%d", i)
		conf.Unlock()
		conf.Save()
	}
	elapsed := time.Since(started)

	// A synchronous write is ~7ms, so 200 of them could not fit in this budget.
	assert.Less(t, elapsed, 500*time.Millisecond, "Save appears to block on the write")

	conf.RLock()
	finalName := conf.P2pNode.Name
	conf.RUnlock()
	requireSaved(t, path, func(c *Config) bool { return c.P2pNode.Name == finalName })
}

func TestConfigCloseFlushesPendingSave(t *testing.T) {
	dir := t.TempDir()
	conf := &Config{dataDir: dir}
	setDefaults(conf, eventbus.NewBus())
	conf.startWriter()

	conf.Lock()
	conf.P2pNode.Name = "written-on-close"
	conf.Unlock()

	conf.Close()

	// No Eventually: Close must not return before the flush is on disk.
	data, err := os.ReadFile(filepath.Join(dir, AppConfigFilename))
	require.NoError(t, err)
	parsed := &Config{}
	require.NoError(t, json.Unmarshal(data, parsed))
	assert.Equal(t, "written-on-close", parsed.P2pNode.Name)

	// check idempotent
	assert.NotPanics(t, conf.Close, "Close must tolerate being called twice")
}

// TestConfigReadOnlyDoesNotWrite pins the point of the read-only constructors:
// no writer goroutine, so nothing reaches disk and there is nothing to close.
func TestConfigReadOnlyDoesNotWrite(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(AppDataDirEnvKey, dir)

	conf := NewConfigReadOnly(AppTypeAwl)
	require.Nil(t, conf.saveTrigger)
	require.Nil(t, conf.emitter, "a read-only config must not hold an emitter")

	// Save is a no-op rather than a panic: it DPanics, which aborts in dev mode
	// to surface the misuse but only logs otherwise.
	assert.NotPanics(t, conf.Save)
	assert.NotPanics(t, conf.Close, "Close on a read-only config must be a no-op")

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Empty(t, entries)
}

// TestLoadConfigCorrupted covers the fallback path: callers replace an
// unloadable config with a fresh one and overwrite the file, so the original
// bytes must be preserved first or the node's identity is lost for good.
func TestLoadConfigCorrupted(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(AppDataDirEnvKey, dir)
	configPath := filepath.Join(dir, AppConfigFilename)

	// Truncated JSON, the shape an interrupted non-atomic write produces.
	corrupted := []byte(`{"p2pNode": {"name": "my-node", "identi`)
	require.NoError(t, os.WriteFile(configPath, corrupted, filesPerm))

	_, err := LoadConfigReadOnly(AppTypeAwl)
	require.Error(t, err)

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)

	var backups []string
	for _, entry := range entries {
		if entry.Name() != AppConfigFilename {
			backups = append(backups, entry.Name())
		}
	}
	require.Len(t, backups, 1, "expected exactly one backup, got %v", entries)

	data, err := os.ReadFile(filepath.Join(dir, backups[0]))
	require.NoError(t, err)
	assert.Equal(t, corrupted, data)
}

func TestLoadConfigMissing(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(AppDataDirEnvKey, dir)

	_, err := LoadConfigReadOnly(AppTypeAwl)
	require.ErrorIs(t, err, os.ErrNotExist)

	// A first run is not corruption: nothing should be backed up.
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Empty(t, entries)
}

func newTestConfig(t *testing.T) (*Config, string) {
	t.Helper()

	dir := t.TempDir()
	conf := &Config{dataDir: dir}
	setDefaults(conf, eventbus.NewBus())
	conf.startWriter()
	t.Cleanup(conf.Close)

	return conf, filepath.Join(dir, AppConfigFilename)
}

// requireSaved waits for the writer goroutine to put a config on disk that
// satisfies check. Saving is asynchronous, so tests cannot read the file
// straight after Save.
func requireSaved(t *testing.T, path string, check func(*Config) bool) {
	t.Helper()

	var last []byte
	require.Eventually(t, func() bool {
		data, err := os.ReadFile(path)
		if err != nil {
			return false
		}
		last = data
		parsed := &Config{}
		if err := json.Unmarshal(data, parsed); err != nil {
			return false
		}
		return check(parsed)
	}, 3*time.Second, 10*time.Millisecond, "config on disk never matched; last read: %s", last)
}
