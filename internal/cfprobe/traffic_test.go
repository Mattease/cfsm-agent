package cfprobe

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestReadMonthlyTrafficTotals(t *testing.T) {
	now := time.Date(2026, time.October, 22, 12, 0, 0, 0, time.FixedZone("UTC+8", 8*60*60))
	// now 自带 +08 时区：显式把它当作重置时区传入，loc=nil 会取机器本地时区，
	// 与这个虚构的 now 不一致。
	loc := now.Location()
	currentPeriod := periodStartTS(now, 1, "00:00:00", loc)
	previousPeriod := periodStartTS(now.AddDate(0, -1, 0), 1, "00:00:00", loc)
	for _, tt := range []struct {
		name        string
		resetDay    int
		periodStart int64
		lastCheck   int64
		iface       string
		wantRX      uint64
		wantTX      uint64
	}{
		{"current period", 1, currentPeriod, now.Unix(), "eth0", 500, 600},
		{"previous period", 1, previousPeriod, now.AddDate(0, -1, 0).Unix(), "eth0", 0, 0},
		// 接口名只是计数器来源，换名不丢累计值。
		{"different interface", 1, currentPeriod, now.Unix(), "eth1", 500, 600},
		{"uninitialized state", 1, currentPeriod, 0, "eth0", 0, 0},
		{"no reset", 0, previousPeriod, now.AddDate(0, -1, 0).Unix(), "eth0", 500, 600},
		{"legacy current period", 1, 0, now.Unix(), "eth0", 500, 600},
		{"legacy previous period", 1, 0, now.AddDate(0, -1, 0).Unix(), "eth0", 0, 0},
		{"legacy local midnight", 1, 0, time.Date(2026, time.October, 1, 0, 30, 0, 0, now.Location()).Unix(), "eth0", 500, 600},
	} {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "traffic.dat")
			st := trafficState{RXPrev: 100, TXPrev: 200, RXPeriod: 500, TXPeriod: 600, LastCheck: tt.lastCheck, PeriodStart: tt.periodStart, Interface: tt.iface}
			if err := writeTrafficState(path, st); err != nil {
				t.Fatal(err)
			}
			rx, tx := readMonthlyTrafficTotals(path, tt.resetDay, "00:00:00", loc, "eth0", now)
			if rx != tt.wantRX || tx != tt.wantTX {
				t.Fatalf("totals=%d/%d, want %d/%d", rx, tx, tt.wantRX, tt.wantTX)
			}
			if got := readTrafficState(path); got != st {
				t.Fatalf("read changed persisted state: %+v", got)
			}
		})
	}
	path := filepath.Join(t.TempDir(), "missing", "traffic.dat")
	if rx, tx := readMonthlyTrafficTotals(path, 1, "00:00:00", loc, "eth0", now); rx != 0 || tx != 0 {
		t.Fatalf("missing state returned totals=%d/%d", rx, tx)
	}
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Fatalf("read created a directory or failed unexpectedly: %v", err)
	}
}

func TestMonthlyTrafficReportsCheckpointFailure(t *testing.T) {
	for _, initialized := range []bool{false, true} {
		path := filepath.Join(t.TempDir(), "traffic.dat")
		if initialized {
			if err := writeTrafficState(path, trafficState{RXPrev: 100, RXPeriod: 500, LastCheck: 1}); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.Mkdir(path+".tmp", 0o700); err != nil {
			t.Fatal(err)
		}
		if _, _, err := calcMonthlyTraffic(path, NetBytes{RX: 110}, 0, "00:00:00", nil, ""); err == nil {
			t.Fatal("checkpoint failure was discarded")
		}
		if initialized {
			st := readTrafficState(path)
			if st.RXPrev != 100 || st.RXPeriod != 500 {
				t.Fatalf("failed checkpoint overwrote persisted state: %+v", st)
			}
		}
	}
}

func TestPeriodStartTSClampsResetDay(t *testing.T) {
	now := time.Date(2026, time.February, 15, 12, 0, 0, 0, time.UTC)
	got := time.Unix(periodStartTS(now, 31, "00:00:00", time.UTC), 0).UTC()
	want := time.Date(2026, time.January, 31, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("got %s, want %s", got, want)
	}
}

func TestPeriodStartTSRollsMissingResetDayToNextMonth(t *testing.T) {
	now := time.Date(2026, time.April, 30, 12, 0, 0, 0, time.UTC)
	got := time.Unix(periodStartTS(now, 31, "00:00:00", time.UTC), 0).UTC()
	want := time.Date(2026, time.March, 31, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("got %s, want %s", got, want)
	}
}

func TestPeriodStartTSNoReset(t *testing.T) {
	if got := periodStartTS(time.Date(2026, time.August, 4, 0, 0, 0, 0, time.UTC), 0, "00:00:00", time.UTC); got != 0 {
		t.Fatalf("got %d, want 0", got)
	}
}

// --- 任务一：重启丢流量 bug ---

// 重启后网卡计数器从大变小，但仍在同一周期内：累计值必须保留，只重设计数基线。
func TestCalcMonthlyTrafficKeepsTotalsAfterReboot(t *testing.T) {
	loc := time.FixedZone("UTC+8", 8*60*60)
	lastCheck := time.Date(2026, time.October, 22, 11, 0, 0, 0, loc)
	now := time.Date(2026, time.October, 22, 12, 0, 0, 0, loc)
	path := filepath.Join(t.TempDir(), "traffic.dat")
	st := trafficState{
		RXPrev: 100000, TXPrev: 200000,
		RXPeriod: 500, TXPeriod: 600,
		LastCheck:   lastCheck.Unix(),
		PeriodStart: periodStartTS(lastCheck, 1, "00:00:00", loc),
		Interface:   "eth0",
	}
	if err := writeTrafficState(path, st); err != nil {
		t.Fatal(err)
	}

	// 重启后计数器从 0 开始：本次读数远小于上次基线。
	rx, tx, err := calcMonthlyTrafficAt(path, NetBytes{RX: 50, TX: 80}, 1, "00:00:00", loc, "eth0", now)
	if err != nil {
		t.Fatalf("calcMonthlyTrafficAt error = %v", err)
	}
	if rx != 500 || tx != 600 {
		t.Fatalf("reboot totals=%d/%d, want 500/600", rx, tx)
	}
	if got := readTrafficState(path); got.RXPrev != 50 || got.TXPrev != 80 || got.RXPeriod != 500 || got.TXPeriod != 600 || got.PeriodStart != st.PeriodStart {
		t.Fatalf("persisted state after reboot: %+v", got)
	}
	// 重启后的下一跳正常累计。
	rx, tx, err = calcMonthlyTrafficAt(path, NetBytes{RX: 150, TX: 180}, 1, "00:00:00", loc, "eth0", now.Add(time.Minute))
	if err != nil {
		t.Fatalf("calcMonthlyTrafficAt error = %v", err)
	}
	if rx != 600 || tx != 700 {
		t.Fatalf("post-reboot totals=%d/%d, want 600/700", rx, tx)
	}
}

// 只有真正跨过重置日才清零，且只计新周期的增量。
func TestCalcMonthlyTrafficResetsOnlyOnResetDay(t *testing.T) {
	loc := time.FixedZone("UTC+8", 8*60*60)
	path := filepath.Join(t.TempDir(), "traffic.dat")
	lastCheck := time.Date(2026, time.October, 31, 23, 0, 0, 0, loc)
	st := trafficState{
		RXPrev: 1000, TXPrev: 2000,
		RXPeriod: 500, TXPeriod: 600,
		LastCheck:   lastCheck.Unix(),
		PeriodStart: periodStartTS(lastCheck, 1, "00:00:00", loc),
		Interface:   "eth0",
	}
	if err := writeTrafficState(path, st); err != nil {
		t.Fatal(err)
	}

	// 同周期内（10 月 31 日 23:30）：只累计。
	rx, tx, err := calcMonthlyTrafficAt(path, NetBytes{RX: 1100, TX: 2100}, 1, "00:00:00", loc, "eth0", time.Date(2026, time.October, 31, 23, 30, 0, 0, loc))
	if err != nil {
		t.Fatalf("calcMonthlyTrafficAt error = %v", err)
	}
	if rx != 600 || tx != 700 {
		t.Fatalf("same-period totals=%d/%d, want 600/700", rx, tx)
	}
	// 跨重置日（11 月 1 日 00:30）：清零，只计新周期增量。
	rx, tx, err = calcMonthlyTrafficAt(path, NetBytes{RX: 1200, TX: 2200}, 1, "00:00:00", loc, "eth0", time.Date(2026, time.November, 1, 0, 30, 0, 0, loc))
	if err != nil {
		t.Fatalf("calcMonthlyTrafficAt error = %v", err)
	}
	if rx != 100 || tx != 100 {
		t.Fatalf("new-period totals=%d/%d, want 100/100", rx, tx)
	}
}

// 接口名变化只重设计数基线，不丢累计值；换名后的下一跳正常累计。
func TestCalcMonthlyTrafficKeepsTotalsOnInterfaceChange(t *testing.T) {
	loc := time.FixedZone("UTC+8", 8*60*60)
	lastCheck := time.Date(2026, time.October, 22, 11, 0, 0, 0, loc)
	now := time.Date(2026, time.October, 22, 12, 0, 0, 0, loc)
	path := filepath.Join(t.TempDir(), "traffic.dat")
	st := trafficState{
		RXPrev: 1000, TXPrev: 2000,
		RXPeriod: 500, TXPeriod: 600,
		LastCheck:   lastCheck.Unix(),
		PeriodStart: periodStartTS(lastCheck, 1, "00:00:00", loc),
		Interface:   "eth0",
	}
	if err := writeTrafficState(path, st); err != nil {
		t.Fatal(err)
	}

	rx, tx, err := calcMonthlyTrafficAt(path, NetBytes{RX: 99999, TX: 88888}, 1, "00:00:00", loc, "eth1", now)
	if err != nil {
		t.Fatalf("calcMonthlyTrafficAt error = %v", err)
	}
	if rx != 500 || tx != 600 {
		t.Fatalf("interface-change totals=%d/%d, want 500/600", rx, tx)
	}
	if got := readTrafficState(path); got.Interface != "eth1" || got.RXPrev != 99999 || got.TXPrev != 88888 || got.RXPeriod != 500 || got.TXPeriod != 600 {
		t.Fatalf("persisted state after interface change: %+v", got)
	}
	rx, tx, err = calcMonthlyTrafficAt(path, NetBytes{RX: 100099, TX: 88988}, 1, "00:00:00", loc, "eth1", now.Add(time.Minute))
	if err != nil {
		t.Fatalf("calcMonthlyTrafficAt error = %v", err)
	}
	if rx != 600 || tx != 700 {
		t.Fatalf("post-change totals=%d/%d, want 600/700", rx, tx)
	}
}

// PERIOD_START 缺失（老数据）时按 LastCheck 推算所属周期。
func TestCalcMonthlyTrafficDerivesPeriodFromLastCheck(t *testing.T) {
	loc := time.FixedZone("UTC+8", 8*60*60)
	t.Run("last check in current period", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "traffic.dat")
		st := trafficState{
			RXPrev: 1000, TXPrev: 2000,
			RXPeriod: 500, TXPeriod: 600,
			LastCheck:   time.Date(2026, time.October, 22, 11, 0, 0, 0, loc).Unix(),
			PeriodStart: 0,
			Interface:   "eth0",
		}
		if err := writeTrafficState(path, st); err != nil {
			t.Fatal(err)
		}
		rx, tx, err := calcMonthlyTrafficAt(path, NetBytes{RX: 1100, TX: 2100}, 1, "00:00:00", loc, "eth0", time.Date(2026, time.October, 22, 12, 0, 0, 0, loc))
		if err != nil {
			t.Fatalf("calcMonthlyTrafficAt error = %v", err)
		}
		if rx != 600 || tx != 700 {
			t.Fatalf("totals=%d/%d, want 600/700", rx, tx)
		}
	})
	t.Run("last check in previous period", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "traffic.dat")
		st := trafficState{
			RXPrev: 1000, TXPrev: 2000,
			RXPeriod: 500, TXPeriod: 600,
			LastCheck:   time.Date(2026, time.September, 30, 23, 0, 0, 0, loc).Unix(),
			PeriodStart: 0,
			Interface:   "eth0",
		}
		if err := writeTrafficState(path, st); err != nil {
			t.Fatal(err)
		}
		rx, tx, err := calcMonthlyTrafficAt(path, NetBytes{RX: 1100, TX: 2100}, 1, "00:00:00", loc, "eth0", time.Date(2026, time.October, 2, 12, 0, 0, 0, loc))
		if err != nil {
			t.Fatalf("calcMonthlyTrafficAt error = %v", err)
		}
		if rx != 100 || tx != 100 {
			t.Fatalf("totals=%d/%d, want 100/100", rx, tx)
		}
	})
}

// --- 任务二：reset_time + timezone ---

// resetDay=8、resetTime=15:30:00（洛杉矶时间）：
// 北京时间 10 月 8 日 16:00 = 洛杉矶 10 月 8 日 01:00，未到当日 15:30，仍在旧周期；
// 北京时间 10 月 9 日 07:30 = 洛杉矶 10 月 8 日 16:30，已过 15:30，进入新周期。
func TestPeriodStartTSWithResetTimeAndTimezone(t *testing.T) {
	la, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Fatalf("LoadLocation America/Los_Angeles: %v", err)
	}
	beijing := time.FixedZone("CST", 8*60*60)
	before := time.Date(2026, time.October, 8, 16, 0, 0, 0, beijing)
	got := time.Unix(periodStartTS(before, 8, "15:30:00", la), 0)
	want := time.Date(2026, time.September, 8, 15, 30, 0, 0, la)
	if !got.Equal(want) {
		t.Fatalf("before reset: got %s, want %s", got, want)
	}
	after := time.Date(2026, time.October, 9, 7, 30, 0, 0, beijing)
	got = time.Unix(periodStartTS(after, 8, "15:30:00", la), 0)
	want = time.Date(2026, time.October, 8, 15, 30, 0, 0, la)
	if !got.Equal(want) {
		t.Fatalf("after reset: got %s, want %s", got, want)
	}
}

// resetTime 本身就是周期边界：06:00 前一分钟属上月，06:00 整进入新周期。
func TestPeriodStartTSResetTimeBoundary(t *testing.T) {
	loc := time.UTC
	before := time.Date(2026, time.November, 1, 5, 59, 0, 0, loc)
	got := time.Unix(periodStartTS(before, 1, "06:00:00", loc), 0).UTC()
	want := time.Date(2026, time.October, 1, 6, 0, 0, 0, loc)
	if !got.Equal(want) {
		t.Fatalf("before reset time: got %s, want %s", got, want)
	}
	at := time.Date(2026, time.November, 1, 6, 0, 0, 0, loc)
	got = time.Unix(periodStartTS(at, 1, "06:00:00", loc), 0).UTC()
	want = time.Date(2026, time.November, 1, 6, 0, 0, 0, loc)
	if !got.Equal(want) {
		t.Fatalf("at reset time: got %s, want %s", got, want)
	}
}

// reset_time 默认 00:00:00 与老行为一致：空串、非法值、显式 00:00:00 三者
// 结果相同，且与手工算出的当月/上月 resetDay 日 00:00 逐项比对；
// loc=nil 与 loc=time.Local 结果相同。
func TestPeriodStartTSDefaultResetTimeMatchesMidnight(t *testing.T) {
	loc := time.FixedZone("UTC+8", 8*60*60)
	nows := []time.Time{
		time.Date(2026, time.February, 15, 12, 0, 0, 0, loc),
		time.Date(2026, time.April, 30, 23, 59, 0, 0, loc),
		time.Date(2026, time.January, 1, 0, 0, 0, 0, loc),
		time.Date(2026, time.December, 31, 23, 59, 59, 0, loc),
	}
	for _, now := range nows {
		for _, resetDay := range []int{1, 8, 31} {
			base := periodStartTS(now, resetDay, "00:00:00", loc)
			if got := periodStartTS(now, resetDay, "", loc); got != base {
				t.Fatalf("now=%s resetDay=%d: empty resetTime got %d, want %d", now, resetDay, got, base)
			}
			if got := periodStartTS(now, resetDay, "25:00:00", loc); got != base {
				t.Fatalf("now=%s resetDay=%d: invalid resetTime got %d, want %d", now, resetDay, got, base)
			}
		}
		for _, resetDay := range []int{1, 8} {
			year, month, _ := now.Date()
			want := time.Date(year, month, resetDay, 0, 0, 0, 0, loc)
			if now.Before(want) {
				want = time.Date(year, month-1, resetDay, 0, 0, 0, 0, loc)
			}
			if got := periodStartTS(now, resetDay, "00:00:00", loc); got != want.Unix() {
				t.Fatalf("now=%s resetDay=%d: got %d, want %d (%s)", now, resetDay, got, want.Unix(), want)
			}
		}
	}
	now := time.Now()
	if got, want := periodStartTS(now, 8, "00:00:00", nil), periodStartTS(now, 8, "00:00:00", time.Local); got != want {
		t.Fatalf("nil loc got %d, want time.Local %d", got, want)
	}
}

// resetTime + timezone 同样决定 calcMonthlyTraffic 的清零边界。
func TestCalcMonthlyTrafficRespectsResetTimeAndTimezone(t *testing.T) {
	la, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Fatalf("LoadLocation America/Los_Angeles: %v", err)
	}
	newState := func(lastCheck time.Time) trafficState {
		return trafficState{
			RXPrev: 1000, TXPrev: 2000,
			RXPeriod: 500, TXPeriod: 600,
			LastCheck:   lastCheck.Unix(),
			PeriodStart: periodStartTS(lastCheck, 8, "15:30:00", la),
			Interface:   "eth0",
		}
	}
	t.Run("crosses 15:30 reset", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "traffic.dat")
		if err := writeTrafficState(path, newState(time.Date(2026, time.October, 8, 10, 0, 0, 0, la))); err != nil {
			t.Fatal(err)
		}
		rx, tx, err := calcMonthlyTrafficAt(path, NetBytes{RX: 1100, TX: 2100}, 8, "15:30:00", la, "eth0", time.Date(2026, time.October, 8, 16, 0, 0, 0, la))
		if err != nil {
			t.Fatalf("calcMonthlyTrafficAt error = %v", err)
		}
		if rx != 100 || tx != 100 {
			t.Fatalf("totals=%d/%d, want 100/100", rx, tx)
		}
	})
	t.Run("same period after reset time", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "traffic.dat")
		if err := writeTrafficState(path, newState(time.Date(2026, time.October, 8, 16, 0, 0, 0, la))); err != nil {
			t.Fatal(err)
		}
		rx, tx, err := calcMonthlyTrafficAt(path, NetBytes{RX: 1100, TX: 2100}, 8, "15:30:00", la, "eth0", time.Date(2026, time.October, 8, 17, 0, 0, 0, la))
		if err != nil {
			t.Fatalf("calcMonthlyTrafficAt error = %v", err)
		}
		if rx != 600 || tx != 700 {
			t.Fatalf("totals=%d/%d, want 600/700", rx, tx)
		}
	})
}

func TestParseResetTime(t *testing.T) {
	for _, tt := range []struct {
		in   string
		hour int
		min  int
		sec  int
	}{
		{"15:30:00", 15, 30, 0},
		{"00:00:00", 0, 0, 0},
		{"23:59:59", 23, 59, 59},
		{"1:2:3", 1, 2, 3},
		{"", 0, 0, 0},
		{"25:00:00", 0, 0, 0},
		{"12:60:00", 0, 0, 0},
		{"12:00:60", 0, 0, 0},
		{"12:00", 0, 0, 0},
		{"ab:cd:ef", 0, 0, 0},
		{"12:00:00:00", 0, 0, 0},
	} {
		h, m, s := parseResetTime(tt.in)
		if h != tt.hour || m != tt.min || s != tt.sec {
			t.Fatalf("parseResetTime(%q) = %d:%d:%d, want %d:%d:%d", tt.in, h, m, s, tt.hour, tt.min, tt.sec)
		}
	}
}

func TestResolveResetLocation(t *testing.T) {
	if got := resolveResetLocation(""); got != time.Local {
		t.Fatalf("resolveResetLocation(\"\") = %v, want time.Local", got)
	}
	la, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Fatalf("LoadLocation America/Los_Angeles: %v", err)
	}
	// Location 指针同一性不在 time 包的保证范围内，按名字比对。
	if got := resolveResetLocation("America/Los_Angeles"); got.String() != la.String() {
		t.Fatalf("resolveResetLocation(\"America/Los_Angeles\") = %v, want %v", got, la)
	}
	if got := resolveResetLocation("No/Such_Zone"); got != time.Local {
		t.Fatalf("resolveResetLocation(\"No/Such_Zone\") = %v, want time.Local", got)
	}
	if got := resolveResetLocation("Bad!!Zone"); got != time.Local {
		t.Fatalf("resolveResetLocation(\"Bad!!Zone\") = %v, want time.Local", got)
	}
}
