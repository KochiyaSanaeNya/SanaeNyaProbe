package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadServerConfig(t *testing.T) {
	path := writeConfig(t, `{
		"port": 8443,
		"public_key_path": "/etc/SanaeNyaProbeServer/fullchain.pem",
		"private_key_path": "/etc/SanaeNyaProbeServer/privkey.pem"
	}`)

	cfg, err := loadServerConfig(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.Port != 8443 {
		t.Fatalf("port = %d, want 8443", cfg.Port)
	}
	if cfg.PublicKeyPath != "/etc/SanaeNyaProbeServer/fullchain.pem" {
		t.Fatalf("public key path = %q", cfg.PublicKeyPath)
	}
	if cfg.PrivateKeyPath != "/etc/SanaeNyaProbeServer/privkey.pem" {
		t.Fatalf("private key path = %q", cfg.PrivateKeyPath)
	}
	if cfg.listenAddr() != ":8443" {
		t.Fatalf("listen addr = %q, want :8443", cfg.listenAddr())
	}
}

func TestLoadServerConfigRejectsInvalidValues(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{
			name: "unknown field",
			body: `{"port":8443,"public_key_path":"/cert.pem","private_key_path":"/key.pem","listen":"0.0.0.0:8443"}`,
		},
		{
			name: "missing port",
			body: `{"public_key_path":"/cert.pem","private_key_path":"/key.pem"}`,
		},
		{
			name: "port too high",
			body: `{"port":65536,"public_key_path":"/cert.pem","private_key_path":"/key.pem"}`,
		},
		{
			name: "missing public key path",
			body: `{"port":8443,"private_key_path":"/key.pem"}`,
		},
		{
			name: "missing private key path",
			body: `{"port":8443,"public_key_path":"/cert.pem"}`,
		},
		{
			name: "extra json value",
			body: `{"port":8443,"public_key_path":"/cert.pem","private_key_path":"/key.pem"} {}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := loadServerConfig(writeConfig(t, tt.body)); err == nil {
				t.Fatal("load config succeeded, want error")
			}
		})
	}
}

func TestRecvAndMonitor(t *testing.T) {
	state := newStore()

	form := url.Values{}
	form.Set("uuid", "machine-1")
	form.Set("name", "server-01")
	form.Set("uptime_sec", "123.45")
	form.Set("net_rx_bps", "1024.00")

	req := httptest.NewRequest(http.MethodPost, "/api/recv", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp := httptest.NewRecorder()
	state.recv(resp, req)

	if resp.Code != http.StatusNoContent {
		t.Fatalf("recv status = %d, want %d", resp.Code, http.StatusNoContent)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/monitor", nil)
	resp = httptest.NewRecorder()
	state.monitor(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("monitor status = %d, want %d", resp.Code, http.StatusOK)
	}

	var body monitorResponse
	if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode monitor response: %v", err)
	}
	if len(body.Servers) != 1 {
		t.Fatalf("server count = %d, want 1", len(body.Servers))
	}

	server := body.Servers[0]
	if server.Key != "uuid:machine-1" || server.UUID != "machine-1" || server.Name != "server-01" {
		t.Fatalf("unexpected server identity: %+v", server)
	}
	if !server.Online || server.Status != "online" {
		t.Fatalf("server should be online: %+v", server)
	}
	if got := server.Metrics["uptime_sec"]; len(got) != 1 || got[0] != "123.45" {
		t.Fatalf("uptime metric = %#v, want [123.45]", got)
	}
	if got := server.Metrics["net_rx_bps"]; len(got) != 1 || got[0] != "1024.00" {
		t.Fatalf("net_rx_bps metric = %#v, want [1024.00]", got)
	}
}

func TestRecvRejectsMissingIdentity(t *testing.T) {
	state := newStore()

	form := url.Values{}
	form.Set("uuid", "machine-1")
	req := httptest.NewRequest(http.MethodPost, "/api/recv", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp := httptest.NewRecorder()
	state.recv(resp, req)

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("recv status = %d, want %d", resp.Code, http.StatusBadRequest)
	}
}

func TestSnapshotMarksOfflineAfterTimeout(t *testing.T) {
	state := newStore()
	lastSeen := time.Now().UTC().Add(-offlineAfter - time.Second)

	state.update("machine-1", "server-01", map[string][]string{"uuid": {"machine-1"}, "name": {"server-01"}}, lastSeen)
	servers := state.snapshot(time.Now().UTC())

	if len(servers) != 1 {
		t.Fatalf("server count = %d, want 1", len(servers))
	}
	if servers[0].Online || servers[0].Status != "offline" {
		t.Fatalf("server should be offline after timeout: %+v", servers[0])
	}
	if servers[0].OfflineSince == nil || servers[0].OfflineSinceUnix == 0 {
		t.Fatalf("offline time should be set: %+v", servers[0])
	}
}

func writeConfig(t *testing.T, body string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "server.json")
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}
