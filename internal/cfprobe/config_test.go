package cfprobe

import (
	"net/http"
	"os"
	"strings"
	"testing"
)

func TestNormalizeInterfaceList(t *testing.T) {
	got, err := normalizeInterfaceList(" eth0,ens3,eth0, pppoe-wan ")
	if err != nil {
		t.Fatalf("normalizeInterfaceList returned error: %v", err)
	}
	if got != "eth0,ens3,pppoe-wan" {
		t.Fatalf("unexpected normalized interfaces: %q", got)
	}
}

func TestNormalizeInterfaceListRejectsBadName(t *testing.T) {
	if _, err := normalizeInterfaceList("eth0, bad/name"); err == nil {
		t.Fatal("expected invalid interface name to be rejected")
	}
}

func TestNormalizeInterfaceListAllowsGlob(t *testing.T) {
	got, err := normalizeInterfaceList(" eth*,en[ops]* ")
	if err != nil {
		t.Fatalf("normalizeInterfaceList returned error: %v", err)
	}
	if got != "eth*,en[ops]*" {
		t.Fatalf("unexpected normalized interfaces: %q", got)
	}
}

func TestConfigPersistsUpdateProxy(t *testing.T) {
	path := t.TempDir() + "/config.conf"
	cfg := defaultConfig()
	cfg.ServerID = "sid"
	cfg.Secret = "secret"
	cfg.WorkerURL = "https://worker.example.com/report"
	cfg.AutoUpdate = true
	cfg.UpdateProxy = "https://gh-proxy.example.com"
	cfg.ConnectionMode = connectionModeHTTP
	cfg.PingMode = pingModeICMP

	if err := writeConfig(path, cfg); err != nil {
		t.Fatalf("writeConfig returned error: %v", err)
	}
	got, err := readConfig(path)
	if err != nil {
		t.Fatalf("readConfig returned error: %v", err)
	}
	if got.UpdateProxy != cfg.UpdateProxy {
		t.Fatalf("UpdateProxy = %q, want %q", got.UpdateProxy, cfg.UpdateProxy)
	}
	if !got.AutoUpdate {
		t.Fatal("AutoUpdate = false, want true")
	}
	if got.ConnectionMode != connectionModeHTTP {
		t.Fatalf("ConnectionMode = %q, want %q", got.ConnectionMode, connectionModeHTTP)
	}
	if got.PingMode != pingModeICMP {
		t.Fatalf("PingMode = %q, want %q", got.PingMode, pingModeICMP)
	}
}

func TestReadConfigDefaultsConnectionModeAuto(t *testing.T) {
	path := t.TempDir() + "/config.conf"
	data := []byte("SERVER_ID=\"sid\"\nSECRET=\"secret\"\nWORKER_URL=\"https://worker.example.com/update\"\n")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write config fixture: %v", err)
	}
	got, err := readConfig(path)
	if err != nil {
		t.Fatalf("readConfig returned error: %v", err)
	}
	if got.ConnectionMode != connectionModeAuto {
		t.Fatalf("ConnectionMode = %q, want %q", got.ConnectionMode, connectionModeAuto)
	}
	if got.PingMode != pingModeTCP {
		t.Fatalf("PingMode = %q, want %q", got.PingMode, pingModeTCP)
	}
	if got.UA != defaultUserAgent {
		t.Fatalf("UA = %q, want default %q", got.UA, defaultUserAgent)
	}
}

func TestNormalizeUserAgentValue(t *testing.T) {
	accepted := []string{
		"cfsm",
		"MyAgent/1.0",
		"cfsm (edge)",
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64)",
	}
	for _, value := range accepted {
		if got := normalizeUserAgentValue(value); got != value {
			t.Fatalf("normalizeUserAgentValue(%q) = %q", value, got)
		}
	}

	rejected := []string{
		"",
		"ua\"quote",
		"ua\\backslash",
		"ua'single",
		"ua$var",
		"ua&other",
		"ua|pipe",
		"ua%20",
		"中文UA",
		"ua\r\nInjected: 1",
	}
	for _, value := range rejected {
		if got := normalizeUserAgentValue(value); got != defaultUserAgent {
			t.Fatalf("normalizeUserAgentValue(%q) = %q, want %q", value, got, defaultUserAgent)
		}
	}

	longest := ""
	for len(longest) < maxUserAgentLength {
		longest += "a"
	}
	if got := normalizeUserAgentValue(longest); got != longest {
		t.Fatalf("max length UA should be accepted")
	}
	if got := normalizeUserAgentValue(longest + "a"); got != defaultUserAgent {
		t.Fatalf("over-long UA should fall back to default")
	}
}

func TestConfigPersistsUserAgent(t *testing.T) {
	path := t.TempDir() + "/config.conf"
	cfg := defaultConfig()
	cfg.ServerID = "sid"
	cfg.Secret = "secret"
	cfg.WorkerURL = "https://worker.example.com/update"
	cfg.UA = "MyAgent/1.0"

	if err := writeConfig(path, cfg); err != nil {
		t.Fatalf("writeConfig returned error: %v", err)
	}
	got, err := readConfig(path)
	if err != nil {
		t.Fatalf("readConfig returned error: %v", err)
	}
	if got.UA != cfg.UA {
		t.Fatalf("UA = %q, want %q", got.UA, cfg.UA)
	}
}

func TestApplyRemoteUserAgentUpdatesConfigOnce(t *testing.T) {
	tmp := t.TempDir()
	configFile := tmp + "/config.conf"
	agent := Agent{
		cfg: Config{
			ServerID:       "sid",
			Secret:         "secret",
			WorkerURL:      "https://worker.example.com/update",
			ReportInterval: 60,
			ResetDay:       1,
			ConnectionMode: connectionModeAuto,
			PingMode:       pingModeTCP,
			UA:             defaultUserAgent,
			ConfigMD5:      "none",
		},
		paths: Paths{ConfigFile: configFile, TrafficFile: tmp + "/traffic.dat"},
		log:   newLogger(false),
	}
	if err := writeConfig(configFile, agent.cfg); err != nil {
		t.Fatalf("writeConfig returned error: %v", err)
	}

	agent.applyRemoteUserAgent("MyAgent/1.0")
	if got := agent.configSnapshot().UA; got != "MyAgent/1.0" {
		t.Fatalf("in-memory UA = %q", got)
	}
	persisted, err := readConfig(configFile)
	if err != nil {
		t.Fatalf("readConfig returned error: %v", err)
	}
	if persisted.UA != "MyAgent/1.0" {
		t.Fatalf("persisted UA = %q", persisted.UA)
	}

	// 相同值不应重复写盘（避免每个上报周期写 flash）。
	before, err := os.Stat(configFile)
	if err != nil {
		t.Fatalf("stat config: %v", err)
	}
	agent.applyRemoteUserAgent("MyAgent/1.0")
	after, err := os.Stat(configFile)
	if err != nil {
		t.Fatalf("stat config: %v", err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Fatal("config file rewritten for identical UA")
	}

	// 非法值直接忽略，不能把已有 UA 打回默认值。
	agent.applyRemoteUserAgent("bad\"ua")
	if got := agent.configSnapshot().UA; got != "MyAgent/1.0" {
		t.Fatalf("UA after invalid input = %q", got)
	}
}

func TestNormalizeResetTime(t *testing.T) {
	valid := map[string]string{
		"00:00:00":   "00:00:00",
		"23:59:59":   "23:59:59",
		"15:30:00":   "15:30:00",
		"1:2:3":      "01:02:03",
		" 06:00:00 ": "06:00:00",
	}
	for in, want := range valid {
		if got := normalizeResetTime(in); got != want {
			t.Fatalf("normalizeResetTime(%q) = %q, want %q", in, got, want)
		}
	}
	invalid := []string{
		"",
		"24:00:00",
		"12:60:00",
		"12:00:60",
		"-1:00:00",
		"12:00",
		"12:00:00:00",
		"ab:cd:ef",
		"12:00:0x",
		"123:00:00",
	}
	for _, in := range invalid {
		if got := normalizeResetTime(in); got != "00:00:00" {
			t.Fatalf("normalizeResetTime(%q) = %q, want 00:00:00", in, got)
		}
	}
}

func TestNormalizeTimezone(t *testing.T) {
	valid := []string{
		"",
		"UTC",
		"America/Los_Angeles",
		"Asia/Shanghai",
		"Etc/GMT+5",
	}
	for _, in := range valid {
		if got := normalizeTimezone(in); got != in {
			t.Fatalf("normalizeTimezone(%q) = %q, want %q", in, got, in)
		}
	}
	if got := normalizeTimezone("  America/Los_Angeles  "); got != "America/Los_Angeles" {
		t.Fatalf("normalizeTimezone with spaces = %q", got)
	}
	invalid := []string{
		"Bad!!Zone",
		"No/Such_Zone",
		"America/Los Angeles",
		"../etc/passwd",
		strings.Repeat("a", 65),
	}
	for _, in := range invalid {
		if got := normalizeTimezone(in); got != "" {
			t.Fatalf("normalizeTimezone(%q) = %q, want empty", in, got)
		}
	}
}

func TestConfigPersistsResetTimeAndTimezone(t *testing.T) {
	path := t.TempDir() + "/config.conf"
	cfg := defaultConfig()
	cfg.ServerID = "sid"
	cfg.Secret = "secret"
	cfg.WorkerURL = "https://worker.example.com/report"
	cfg.ResetDay = 8
	cfg.ResetTime = "15:30:00"
	cfg.Timezone = "America/Los_Angeles"

	if err := writeConfig(path, cfg); err != nil {
		t.Fatalf("writeConfig returned error: %v", err)
	}
	got, err := readConfig(path)
	if err != nil {
		t.Fatalf("readConfig returned error: %v", err)
	}
	if got.ResetTime != "15:30:00" || got.Timezone != "America/Los_Angeles" || got.ResetDay != 8 {
		t.Fatalf("round-trip reset config = %d/%q/%q, want 8/15:30:00/America/Los_Angeles", got.ResetDay, got.ResetTime, got.Timezone)
	}

	// 配置文件里的非法值回落默认值，不报错退出。
	raw := "SERVER_ID=\"sid\"\nSECRET=\"secret\"\nWORKER_URL=\"https://worker.example.com/report\"\nRESET_TIME=\"99:99:99\"\nTIMEZONE=\"Bad!!Zone\"\n"
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatalf("write config fixture: %v", err)
	}
	got, err = readConfig(path)
	if err != nil {
		t.Fatalf("readConfig returned error: %v", err)
	}
	if got.ResetTime != "00:00:00" || got.Timezone != "" {
		t.Fatalf("invalid values fell back to %q/%q, want 00:00:00/empty", got.ResetTime, got.Timezone)
	}

	// 老配置文件没有这两个键时取默认值。
	raw = "SERVER_ID=\"sid\"\nSECRET=\"secret\"\nWORKER_URL=\"https://worker.example.com/report\"\n"
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatalf("write config fixture: %v", err)
	}
	got, err = readConfig(path)
	if err != nil {
		t.Fatalf("readConfig returned error: %v", err)
	}
	if got.ResetTime != "00:00:00" || got.Timezone != "" {
		t.Fatalf("missing keys defaulted to %q/%q, want 00:00:00/empty", got.ResetTime, got.Timezone)
	}
}

// schema 7（老面板）：不下发 reset_time/timezone，必须保持当前值。
func TestRemoteConfigSchema7KeepsResetTimeTimezone(t *testing.T) {
	tmp := t.TempDir()
	configFile := tmp + "/config.conf"
	a := Agent{
		cfg: Config{
			ServerID:       "sid",
			Secret:         "secret",
			WorkerURL:      "https://worker.example.com/report",
			ReportInterval: 60,
			ResetDay:       1,
			ResetTime:      "15:30:00",
			Timezone:       "America/Los_Angeles",
			ConnectionMode: connectionModeAuto,
			PingMode:       pingModeTCP,
			UA:             defaultUserAgent,
			ConfigMD5:      "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		},
		paths: Paths{ConfigFile: configFile, TrafficFile: tmp + "/traffic.dat"},
		log:   newLogger(false),
	}
	if err := writeConfig(configFile, a.cfg); err != nil {
		t.Fatalf("writeConfig returned error: %v", err)
	}
	headers := http.Header{"X-Agent-Config-Md5": []string{"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}}
	body := []byte("collect_interval=0&report_interval=60&reset_day=1&schema_version=7&interface=&node_1=&node_2=&node_3=&node_4=&connection_mode=auto&ping_mode=tcp")
	if err := a.applyRemoteConfig(body, headers); err != nil {
		t.Fatalf("applyRemoteConfig() error = %v", err)
	}
	got := a.configSnapshot()
	if got.ResetTime != "15:30:00" || got.Timezone != "America/Los_Angeles" {
		t.Fatalf("schema 7 reset config = %q/%q, want 15:30:00/America/Los_Angeles", got.ResetTime, got.Timezone)
	}
	persisted, err := readConfig(configFile)
	if err != nil {
		t.Fatalf("readConfig() error = %v", err)
	}
	if persisted.ResetTime != "15:30:00" || persisted.Timezone != "America/Los_Angeles" {
		t.Fatalf("persisted reset config = %q/%q, want 15:30:00/America/Los_Angeles", persisted.ResetTime, persisted.Timezone)
	}
}

// schema 8：按 payload 解析 reset_time/timezone；timezone 的 / 由面板
// percent-encode（%2F）后传输，ParseQuery 自动解码。
func TestRemoteConfigSchema8AppliesResetTimeTimezone(t *testing.T) {
	tmp := t.TempDir()
	configFile := tmp + "/config.conf"
	a := Agent{
		cfg: Config{
			ServerID:       "sid",
			Secret:         "secret",
			WorkerURL:      "https://worker.example.com/report",
			ReportInterval: 60,
			ResetDay:       1,
			ResetTime:      "00:00:00",
			Timezone:       "",
			ConnectionMode: connectionModeAuto,
			PingMode:       pingModeTCP,
			UA:             defaultUserAgent,
			ConfigMD5:      "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		},
		paths: Paths{ConfigFile: configFile, TrafficFile: tmp + "/traffic.dat"},
		log:   newLogger(false),
	}
	if err := writeConfig(configFile, a.cfg); err != nil {
		t.Fatalf("writeConfig returned error: %v", err)
	}
	headers := http.Header{"X-Agent-Config-Md5": []string{"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}}
	body := []byte("collect_interval=0&report_interval=60&reset_day=8&reset_time=15:30:00&timezone=America%2FLos_Angeles&schema_version=8&interface=&node_1=&node_2=&node_3=&node_4=&connection_mode=auto&ping_mode=tcp")
	if err := a.applyRemoteConfig(body, headers); err != nil {
		t.Fatalf("applyRemoteConfig() error = %v", err)
	}
	got := a.configSnapshot()
	if got.ResetDay != 8 || got.ResetTime != "15:30:00" || got.Timezone != "America/Los_Angeles" {
		t.Fatalf("schema 8 reset config = %d/%q/%q, want 8/15:30:00/America/Los_Angeles", got.ResetDay, got.ResetTime, got.Timezone)
	}
	persisted, err := readConfig(configFile)
	if err != nil {
		t.Fatalf("readConfig() error = %v", err)
	}
	if persisted.ResetTime != "15:30:00" || persisted.Timezone != "America/Los_Angeles" {
		t.Fatalf("persisted reset config = %q/%q, want 15:30:00/America/Los_Angeles", persisted.ResetTime, persisted.Timezone)
	}

	// 非法值回落默认值，不拒绝整个配置。
	headers = http.Header{"X-Agent-Config-Md5": []string{"cccccccccccccccccccccccccccccccc"}}
	body = []byte("collect_interval=0&report_interval=60&reset_day=1&reset_time=25:00:00&timezone=No%2FSuch_Zone&schema_version=8&interface=&node_1=&node_2=&node_3=&node_4=&connection_mode=auto&ping_mode=tcp")
	if err := a.applyRemoteConfig(body, headers); err != nil {
		t.Fatalf("applyRemoteConfig(invalid values) error = %v", err)
	}
	got = a.configSnapshot()
	if got.ResetTime != "00:00:00" || got.Timezone != "" {
		t.Fatalf("invalid values fell back to %q/%q, want 00:00:00/empty", got.ResetTime, got.Timezone)
	}

	// schema 9 仍拒绝。
	headers = http.Header{"X-Agent-Config-Md5": []string{"dddddddddddddddddddddddddddddddd"}}
	body = []byte("collect_interval=0&report_interval=60&reset_day=1&schema_version=9&interface=&node_1=&node_2=&node_3=&node_4=&connection_mode=auto&ping_mode=tcp")
	if err := a.applyRemoteConfig(body, headers); err == nil {
		t.Fatal("schema_version=9 was accepted")
	}
}

// 无 MD5 时（WSS 通道）靠 remoteConfigDiffers 判定：只改 reset_time/timezone
// 也必须触发应用。
func TestRemoteConfigDiffersOnResetTimeTimezone(t *testing.T) {
	tmp := t.TempDir()
	configFile := tmp + "/config.conf"
	a := Agent{
		cfg: Config{
			ServerID:        "sid",
			Secret:          "secret",
			WorkerURL:       "https://worker.example.com/report",
			CollectInterval: defaultWSSReportIntervalSec,
			ReportInterval:  60,
			ResetDay:        1,
			ResetTime:       "00:00:00",
			Timezone:        "",
			ConnectionMode:  connectionModeAuto,
			PingMode:        pingModeTCP,
			UA:              defaultUserAgent,
			ConfigMD5:       "none",
		},
		paths: Paths{ConfigFile: configFile, TrafficFile: tmp + "/traffic.dat"},
		log:   newLogger(false),
	}
	if err := writeConfig(configFile, a.cfg); err != nil {
		t.Fatalf("writeConfig returned error: %v", err)
	}
	body := []byte("collect_interval=0&report_interval=60&reset_day=1&reset_time=15:30:00&timezone=America%2FLos_Angeles&schema_version=8&interface=&node_1=&node_2=&node_3=&node_4=&connection_mode=auto&ping_mode=tcp")
	if err := a.applyWSSRemoteConfig(body, http.Header{}); err != nil {
		t.Fatalf("applyWSSRemoteConfig() error = %v", err)
	}
	got := a.configSnapshot()
	if got.ResetTime != "15:30:00" || got.Timezone != "America/Los_Angeles" {
		t.Fatalf("reset config = %q/%q, want 15:30:00/America/Los_Angeles", got.ResetTime, got.Timezone)
	}
}
