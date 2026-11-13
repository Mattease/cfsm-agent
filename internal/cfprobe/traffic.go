package cfprobe

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type trafficState struct {
	RXPrev      uint64
	TXPrev      uint64
	RXPeriod    uint64
	TXPeriod    uint64
	LastCheck   int64
	PeriodStart int64
	Interface   string
}

func readTrafficState(path string) trafficState {
	values, err := parseKVFile(path)
	if err != nil {
		return trafficState{}
	}
	return trafficState{
		RXPrev:      parseUintDefault(values["RX_PREV"], 0),
		TXPrev:      parseUintDefault(values["TX_PREV"], 0),
		RXPeriod:    parseUintDefault(values["RX_PERIOD"], 0),
		TXPeriod:    parseUintDefault(values["TX_PERIOD"], 0),
		LastCheck:   atoi64Default(values["LAST_CHECK"], 0),
		PeriodStart: atoi64Default(values["PERIOD_START"], 0),
		Interface:   values["INTERFACE"],
	}
}

// readMonthlyTrafficTotals restores matching totals without changing the
// persisted counter baseline when a fresh network sample is unavailable.
//
// The saved totals belong to the billing period containing LastCheck, not
// necessarily to the period named by the stored PERIOD_START marker: the
// marker may be stale (missing on old states, or recorded under a previous
// reset_time/timezone). Comparing the LastCheck period against the current
// period is what keeps a reboot — or a timezone change — from looking like
// a period rollover. The interface name is only the counter source, so a
// different interface never discards the accumulated totals either.
func readMonthlyTrafficTotals(path string, resetDay int, resetTime string, loc *time.Location, iface string, now time.Time) (uint64, uint64) {
	st := readTrafficState(path)
	if st.LastCheck == 0 {
		return 0, 0
	}
	if currentPeriod := periodStartTS(now, resetDay, resetTime, loc); currentPeriod != 0 {
		savedPeriod := periodStartTS(time.Unix(st.LastCheck, 0), resetDay, resetTime, loc)
		if savedPeriod == 0 {
			// Older states may not record a period start at all.
			savedPeriod = st.PeriodStart
		}
		if savedPeriod != currentPeriod {
			return 0, 0
		}
	}
	return st.RXPeriod, st.TXPeriod
}

func writeTrafficState(path string, st trafficState) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data := fmt.Sprintf("RX_PREV=%d\nTX_PREV=%d\nRX_PERIOD=%d\nTX_PERIOD=%d\nLAST_CHECK=%d\nPERIOD_START=%d\nINTERFACE=%s\n",
		st.RXPrev, st.TXPrev, st.RXPeriod, st.TXPeriod, st.LastCheck, st.PeriodStart, st.Interface)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(data), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func calcMonthlyTraffic(path string, current NetBytes, resetDay int, resetTime string, loc *time.Location, iface string) (uint64, uint64, error) {
	return calcMonthlyTrafficAt(path, current, resetDay, resetTime, loc, iface, time.Now())
}

// calcMonthlyTrafficAt is calcMonthlyTraffic with an injectable clock, so
// period-boundary behaviour can be tested without waiting for a real reset.
//
// Rollover rule: the accumulated RX_PERIOD/TX_PERIOD are cleared only when
// the billing period containing LastCheck differs from the current period.
// A counter fallback (current < prev, e.g. after a reboot the kernel
// counters restart from 0) contributes a zero delta and never clears the
// totals, and an interface rename only re-baselines the counters.
func calcMonthlyTrafficAt(path string, current NetBytes, resetDay int, resetTime string, loc *time.Location, iface string, nowTime time.Time) (uint64, uint64, error) {
	now := nowTime.Unix()
	st := readTrafficState(path)
	ifaceChanged := st.Interface != iface
	periodStart := periodStartTS(nowTime, resetDay, resetTime, loc)
	if st.LastCheck == 0 {
		st.RXPrev = current.RX
		st.TXPrev = current.TX
		st.LastCheck = now
		st.PeriodStart = periodStart
		st.Interface = iface
		return st.RXPeriod, st.TXPeriod, writeTrafficState(path, st)
	}

	var rxDelta, txDelta uint64
	if current.RX >= st.RXPrev {
		rxDelta = current.RX - st.RXPrev
	}
	if current.TX >= st.TXPrev {
		txDelta = current.TX - st.TXPrev
	}
	if ifaceChanged {
		// Interface name is only the counter source: keep the accumulated
		// totals, re-baseline on the new counters, this sample adds nothing.
		rxDelta, txDelta = 0, 0
	}
	if periodStart != 0 {
		lastPeriod := periodStartTS(time.Unix(st.LastCheck, 0), resetDay, resetTime, loc)
		if lastPeriod != 0 && lastPeriod != periodStart {
			st.RXPeriod = rxDelta
			st.TXPeriod = txDelta
		} else {
			st.RXPeriod += rxDelta
			st.TXPeriod += txDelta
		}
	} else {
		st.RXPeriod += rxDelta
		st.TXPeriod += txDelta
	}
	st.RXPrev = current.RX
	st.TXPrev = current.TX
	st.LastCheck = now
	st.PeriodStart = periodStart
	st.Interface = iface
	return st.RXPeriod, st.TXPeriod, writeTrafficState(path, st)
}

func applyTrafficCorrectionFromNetwork(path, iface, rxGB, txGB string) error {
	current, err := readNetBytes(iface)
	if err != nil {
		return fmt.Errorf("read network counters before traffic correction: %w", err)
	}
	return applyTrafficCorrection(path, current, iface, rxGB, txGB)
}

func applyTrafficCorrection(path string, current NetBytes, iface string, rxGB, txGB string) error {
	rx, err := parseTrafficCorrectionGB(rxGB)
	if err != nil {
		return err
	}
	tx, err := parseTrafficCorrectionGB(txGB)
	if err != nil {
		return err
	}
	st := readTrafficState(path)
	st.RXPrev = current.RX
	st.TXPrev = current.TX
	st.RXPeriod = rx
	st.TXPeriod = tx
	st.LastCheck = time.Now().Unix()
	st.Interface = iface
	return writeTrafficState(path, st)
}

// resolveResetLocation maps a configured IANA timezone name to a location.
// Empty or unresolvable names fall back to the machine local timezone.
func resolveResetLocation(tz string) *time.Location {
	if tz == "" {
		return time.Local
	}
	if loc, err := time.LoadLocation(tz); err == nil {
		return loc
	}
	return time.Local
}

// parseResetTime parses an "HH:MM:SS" reset time; anything invalid yields
// midnight (0, 0, 0), matching the default reset_time.
func parseResetTime(s string) (hour, min, sec int) {
	parts := strings.Split(strings.TrimSpace(s), ":")
	if len(parts) != 3 {
		return 0, 0, 0
	}
	h, err1 := strconv.Atoi(parts[0])
	m, err2 := strconv.Atoi(parts[1])
	sec, err3 := strconv.Atoi(parts[2])
	if err1 != nil || err2 != nil || err3 != nil {
		return 0, 0, 0
	}
	if h < 0 || h > 23 || m < 0 || m > 59 || sec < 0 || sec > 59 {
		return 0, 0, 0
	}
	return h, m, sec
}

// periodStartTS returns the Unix start of the billing period containing now:
// resetDay of the current (or previous) month at resetTime in loc.
// resetDay 0 means "never reset" and yields 0.
func periodStartTS(now time.Time, resetDay int, resetTime string, loc *time.Location) int64 {
	if resetDay == 0 {
		return 0
	}
	if resetDay < 1 || resetDay > 31 {
		return now.Unix()
	}
	return lastResetDate(now, resetDay, resetTime, loc).Unix()
}

func lastResetDate(now time.Time, resetDay int, resetTime string, loc *time.Location) time.Time {
	if loc == nil {
		loc = time.Local
	}
	// Convert to the reset timezone first: a UTC instant and the same instant
	// in e.g. America/Los_Angeles can sit on different calendar days.
	localNow := now.In(loc)
	year, month, _ := localNow.Date()
	thisMonth := actualResetDate(year, month, resetDay, resetTime, loc)
	if !localNow.Before(thisMonth) {
		return thisMonth
	}
	prevMonth := month - 1
	prevYear := year
	if prevMonth < time.January {
		prevMonth = time.December
		prevYear--
	}
	return actualResetDate(prevYear, prevMonth, resetDay, resetTime, loc)
}

func actualResetDate(year int, month time.Month, resetDay int, resetTime string, loc *time.Location) time.Time {
	if loc == nil {
		loc = time.Local
	}
	hour, min, sec := parseResetTime(resetTime)
	lastDayOfMonth := time.Date(year, month+1, 0, 0, 0, 0, 0, loc).Day()
	if resetDay <= lastDayOfMonth {
		return time.Date(year, month, resetDay, hour, min, sec, 0, loc)
	}
	nextMonth := month + 1
	nextYear := year
	if nextMonth > time.December {
		nextMonth = time.January
		nextYear++
	}
	return time.Date(nextYear, nextMonth, 1, hour, min, sec, 0, loc)
}

func parseUintDefault(raw string, def uint64) uint64 {
	n, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return def
	}
	return n
}
