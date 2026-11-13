package cfprobe

import "testing"

func TestParseUninstallArgsAllowsNoArgs(t *testing.T) {
	if err := parseUninstallArgs(nil); err != nil {
		t.Fatalf("parseUninstallArgs(nil) error = %v", err)
	}
}

func TestParseInstallOptionsRejectsCustomPathAndService(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{name: "install dir underscore", args: []string{"-install_dir=/tmp/cf-probe"}},
		{name: "install dir hyphen", args: []string{"-install-dir=/tmp/cf-probe"}},
		{name: "service name underscore", args: []string{"-service_name=probe-a"}},
		{name: "service name hyphen", args: []string{"-service-name=probe-a"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := parseInstallOptions(tt.args); err == nil {
				t.Fatalf("parseInstallOptions(%v) expected error", tt.args)
			}
		})
	}
}

func TestParseUninstallArgsRejectsExtraArgs(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{name: "service name underscore", args: []string{"-service_name=probe-a"}},
		{name: "service name hyphen", args: []string{"-service-name=probe-a"}},
		{name: "positional arg", args: []string{"probe-a"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := parseUninstallArgs(tt.args); err == nil {
				t.Fatalf("parseUninstallArgs(%v) expected error", tt.args)
			}
		})
	}
}

func TestParseInstallOptionsAutoUpdateDefaultOff(t *testing.T) {
	got, err := parseInstallOptions(nil)
	if err != nil {
		t.Fatalf("parseInstallOptions(nil) error = %v", err)
	}
	if got.AutoUpdate {
		t.Fatal("AutoUpdate = true, want false")
	}
}

func TestParseInstallOptionsAutoUpdateEnabled(t *testing.T) {
	got, err := parseInstallOptions([]string{"-auto_update=1"})
	if err != nil {
		t.Fatalf("parseInstallOptions(-auto_update=1) error = %v", err)
	}
	if !got.AutoUpdate {
		t.Fatal("AutoUpdate = false, want true")
	}
}

func TestParseInstallOptionsInstallProxy(t *testing.T) {
	got, err := parseInstallOptions([]string{"--install-ghproxy=https://gh-proxy.example.com"})
	if err != nil {
		t.Fatalf("parseInstallOptions(--install-ghproxy) error = %v", err)
	}
	if got.UpdateProxy != "https://gh-proxy.example.com" {
		t.Fatalf("UpdateProxy = %q", got.UpdateProxy)
	}
}

func TestParseInstallOptionsConnectionMode(t *testing.T) {
	got, err := parseInstallOptions([]string{"-connection_mode=http"})
	if err != nil {
		t.Fatalf("parseInstallOptions(-connection_mode=http) error = %v", err)
	}
	if got.ConnectionMode != connectionModeHTTP {
		t.Fatalf("ConnectionMode = %q, want %q", got.ConnectionMode, connectionModeHTTP)
	}

	got, err = parseInstallOptions([]string{"--connection-mode=websocket"})
	if err != nil {
		t.Fatalf("parseInstallOptions(--connection-mode=websocket) error = %v", err)
	}
	if got.ConnectionMode != connectionModeAuto {
		t.Fatalf("ConnectionMode = %q, want %q", got.ConnectionMode, connectionModeAuto)
	}

	if _, err := parseInstallOptions([]string{"-connection_mode=bad"}); err == nil {
		t.Fatal("parseInstallOptions(-connection_mode=bad) expected error")
	}
}

func TestParseInstallOptionsPingMode(t *testing.T) {
	got, err := parseInstallOptions([]string{"-ping_mode=icmp"})
	if err != nil {
		t.Fatalf("parseInstallOptions(-ping_mode=icmp) error = %v", err)
	}
	if got.PingMode != pingModeICMP {
		t.Fatalf("PingMode = %q, want %q", got.PingMode, pingModeICMP)
	}

	got, err = parseInstallOptions([]string{"--ping-mode=tcp"})
	if err != nil {
		t.Fatalf("parseInstallOptions(--ping-mode=tcp) error = %v", err)
	}
	if got.PingMode != pingModeTCP {
		t.Fatalf("PingMode = %q, want %q", got.PingMode, pingModeTCP)
	}

	if _, err := parseInstallOptions([]string{"-ping_mode=bad"}); err == nil {
		t.Fatal("parseInstallOptions(-ping_mode=bad) expected error")
	}
}

func TestParseInstallOptionsTracksExplicitFlags(t *testing.T) {
	got, err := parseInstallOptions([]string{"-auto_update=1", "--auto-update=1", "-collect=5", "-connection_mode=http", "-ping_mode=icmp", "--install-ghproxy=https://gh-proxy.example.com"})
	if err != nil {
		t.Fatalf("parseInstallOptions() error = %v", err)
	}
	for _, name := range []string{"auto_update", "collect_interval", "connection_mode", "ping_mode", "install_ghproxy"} {
		if !got.Explicit[name] {
			t.Fatalf("Explicit[%q] = false, want true (all: %v)", name, got.Explicit)
		}
	}
	if got.Explicit["reset_day"] || got.Explicit["interface"] {
		t.Fatalf("unexpected explicit flags: %v", got.Explicit)
	}
}

func TestParseInstallOptionsResetTimeTimezone(t *testing.T) {
	got, err := parseInstallOptions(nil)
	if err != nil {
		t.Fatalf("parseInstallOptions(nil) error = %v", err)
	}
	if got.ResetTime != "00:00:00" || got.Timezone != "" {
		t.Fatalf("defaults ResetTime/Timezone = %q/%q, want 00:00:00/empty", got.ResetTime, got.Timezone)
	}

	got, err = parseInstallOptions([]string{"-reset_time=15:30:00", "-timezone=America/Los_Angeles"})
	if err != nil {
		t.Fatalf("parseInstallOptions(-reset_time/-timezone) error = %v", err)
	}
	if got.ResetTime != "15:30:00" || got.Timezone != "America/Los_Angeles" {
		t.Fatalf("ResetTime/Timezone = %q/%q, want 15:30:00/America/Los_Angeles", got.ResetTime, got.Timezone)
	}
	if !got.Explicit["reset_time"] || !got.Explicit["timezone"] {
		t.Fatalf("Explicit = %v, want reset_time and timezone tracked", got.Explicit)
	}

	// 非法值回落默认值，不报错退出。
	got, err = parseInstallOptions([]string{"-reset_time=25:00:00", "-timezone=Bad!!Zone"})
	if err != nil {
		t.Fatalf("parseInstallOptions(invalid reset_time/timezone) error = %v", err)
	}
	if got.ResetTime != "00:00:00" || got.Timezone != "" {
		t.Fatalf("invalid ResetTime/Timezone = %q/%q, want 00:00:00/empty", got.ResetTime, got.Timezone)
	}
}
