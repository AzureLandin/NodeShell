package monitor

import (
	"encoding/base64"
	"errors"
	"regexp"
	"strings"
	"testing"
)

// Fixtures mirror tests/monitor-parse.test.ts so the Go parser is proven
// equivalent to the Electron reference on the same inputs.

const sampleOutput = `---STAT---
cpu  10 0 5 85 0 0 0 0
---MEM---
MemTotal:        1000000 kB
MemAvailable:     400000 kB
SwapTotal:             0 kB
SwapFree:              0 kB
---LOAD---
1.00 2.00 3.00 1/1 1
---NET---
Inter-|   Receive                                                |  Transmit
 face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed
  eth0: 100 0 0 0 0 0 0 0 200 0 0 0 0 0 0 0
---PS---
91234  1.7 YDService
    0  0.7 ksoftirqd/0
 7200  0.3 sshd
`

func TestScriptBase64WrappedAndCached(t *testing.T) {
	cmd := Script()
	wrapped := regexp.MustCompile(`^echo [A-Za-z0-9+/=]+ \| base64 -d \| /bin/sh$`)
	if !wrapped.MatchString(cmd) {
		t.Fatalf("script %q must be base64-wrapped for SSH shell -c", cmd)
	}
	if strings.Contains(cmd, "/bin/sh -c ") {
		t.Fatal("script must not rely on shell -c quoting")
	}
	b64 := cmd[len("echo ") : len(cmd)-len(" | base64 -d | /bin/sh")]
	script, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		t.Fatalf("base64 decode: %v", err)
	}
	for _, want := range []string{"---STAT---", "---MEM---", "---LOAD---", "---NET---", "---PS---", "$psbin", "$out"} {
		if !strings.Contains(string(script), want) {
			t.Fatalf("decoded script missing %q", want)
		}
	}
	if !strings.Contains(string(script), `if [ -z "$out" ]`) {
		t.Fatal("decoded script missing the ps fallback chain")
	}
	if Script() != Script() {
		t.Fatal("Script() must be cached (same value on every call)")
	}
}

func TestParseCpuStatLineAndPercentDelta(t *testing.T) {
	a, err := parseCpuStatLine("cpu  100 0 50 850 0 0 0 0")
	if err != nil {
		t.Fatalf("parseCpuStatLine: %v", err)
	}
	b, err := parseCpuStatLine("cpu  150 0 70 880 0 0 0 0")
	if err != nil {
		t.Fatalf("parseCpuStatLine: %v", err)
	}
	if a.Total != 1000 || a.Idle != 850 {
		t.Fatalf("a = %+v, want total 1000 idle 850", a)
	}
	got := cpuPercentFromDelta(a, b)
	if got < 69.99 || got > 70.01 {
		t.Fatalf("cpuPercentFromDelta = %v, want ~70", got)
	}
}

func TestParseCpuStatLineErrors(t *testing.T) {
	for _, line := range []string{"", "cpu 1 2 3", "mem 1 2 3 4 5", "cpu a 0 50 850 0"} {
		if _, err := parseCpuStatLine(line); err == nil {
			t.Fatalf("parseCpuStatLine(%q) must error", line)
		}
	}
}

func TestParseCpuPercentZeroWhenNoDelta(t *testing.T) {
	a, _ := parseCpuStatLine("cpu  100 0 50 850 0 0 0 0")
	if got := cpuPercentFromDelta(a, a); got != 0 {
		t.Fatalf("zero delta must give 0, got %v", got)
	}
}

func TestParseMeminfo(t *testing.T) {
	info := parseMeminfo(`MemTotal:       2048000 kB
MemFree:          100000 kB
MemAvailable:    1024000 kB
Buffers:           10000 kB
Cached:            20000 kB
SwapTotal:        512000 kB
SwapFree:         256000 kB
`)
	if info.MemTotalKb != 2048000 || info.MemAvailableKb != 1024000 ||
		info.SwapTotalKb != 512000 || info.SwapFreeKb != 256000 {
		t.Fatalf("meminfo = %+v", info)
	}
}

func TestParseMeminfoFallbackWithoutMemAvailable(t *testing.T) {
	info := parseMeminfo(`MemTotal:       100 kB
MemFree:          40 kB
Buffers:           10 kB
Cached:           30 kB
SwapTotal:         20 kB
SwapFree:          10 kB
`)
	if info.MemAvailableKb != 80 {
		t.Fatalf("fallback MemAvailable = %d, want 80", info.MemAvailableKb)
	}
}

func TestParseLoadavg(t *testing.T) {
	l1, l5, l15, err := parseLoadavg("0.15 0.20 0.25 1/100 1")
	if err != nil {
		t.Fatalf("parseLoadavg: %v", err)
	}
	if l1 != 0.15 || l5 != 0.20 || l15 != 0.25 {
		t.Fatalf("loadavg = %v %v %v", l1, l5, l15)
	}
	if _, _, _, err := parseLoadavg(""); err == nil {
		t.Fatal("parseLoadavg of empty line must error")
	}
}

func TestParseLoadavgRejectsNonFinite(t *testing.T) {
	// TS parity: the Electron parseLoadavg rejects any non-finite value via
	// Number.isFinite. strconv.ParseFloat accepts "nan"/"inf"/"Infinity"
	// without error, so the Go parser must reject them explicitly — a NaN/Inf
	// load would otherwise break the JSON marshalling of the snapshot.
	for _, line := range []string{
		"nan 0.10 0.20",
		"0.10 NaN 0.20",
		"0.10 0.20 Infinity",
		"inf 0.10 0.20",
		"-Infinity 0.10 0.20",
		"0.10 0.20 -inf",
	} {
		if _, _, _, err := parseLoadavg(line); err == nil {
			t.Fatalf("parseLoadavg(%q) must reject non-finite load values", line)
		}
	}
}

func TestParseNetDevExcludesLoopback(t *testing.T) {
	net := parseNetDev(`Inter-|   Receive                                                |  Transmit
 face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed
    lo: 1000 0 0 0 0 0 0 0 2000 0 0 0 0 0 0 0
  eth0: 5000 0 0 0 0 0 0 0 8000 0 0 0 0 0 0 0
`)
	if net.Rx != 5000 || net.Tx != 8000 {
		t.Fatalf("net = %+v, want rx 5000 tx 8000", net)
	}
}

func TestParseFullMonitorOutputMatchesTSSemantics(t *testing.T) {
	raw, err := parseMonitorOutput(sampleOutput)
	if err != nil {
		t.Fatalf("parseMonitorOutput: %v", err)
	}
	if raw.CPU.Total != 100 {
		t.Fatalf("cpu total = %d, want 100", raw.CPU.Total)
	}
	if raw.MemTotalKb != 1000000 || raw.MemAvailableKb != 400000 {
		t.Fatalf("mem = %+v", raw)
	}
	if raw.Load1 != 1.0 || raw.Load5 != 2.0 || raw.Load15 != 3.0 {
		t.Fatalf("load = %v %v %v", raw.Load1, raw.Load5, raw.Load15)
	}
	if raw.Net.Rx != 100 || raw.Net.Tx != 200 {
		t.Fatalf("net = %+v", raw.Net)
	}
	if len(raw.Processes) != 3 {
		t.Fatalf("processes = %+v, want 3", raw.Processes)
	}
	want := MonitorProcess{MemBytes: 91234 * 1024, CPUPercent: 1.7, Command: "YDService"}
	if raw.Processes[0] != want {
		t.Fatalf("processes[0] = %+v, want %+v", raw.Processes[0], want)
	}
	if raw.Processes[1].Command != "ksoftirqd/0" || raw.Processes[1].CPUPercent != 0.7 {
		t.Fatalf("processes[1] = %+v", raw.Processes[1])
	}
}

func TestParseProcessListAuxFormat(t *testing.T) {
	list := parseProcessList(`USER       PID %CPU %MEM    VSZ   RSS TTY      STAT START   TIME COMMAND
root         1  0.0  0.1 123456  4500 ?        Ss   Jan01   0:01 /sbin/init
root      1234  2.5  1.2 999999 89000 ?        S    10:00   0:10 /usr/sbin/sshd -D
`)
	var found bool
	for _, p := range list {
		if strings.Contains(p.Command, "sshd") {
			found = true
			if p.CPUPercent != 2.5 {
				t.Fatalf("sshd cpuPercent = %v, want 2.5", p.CPUPercent)
			}
			if p.MemBytes != 89000*1024 {
				t.Fatalf("sshd memBytes = %d", p.MemBytes)
			}
		}
	}
	if !found {
		t.Fatalf("aux list missing sshd: %+v", list)
	}
}

func TestParseProcessListSkipsSelfProcesses(t *testing.T) {
	list := parseProcessList(`123 1.0 bash
456 2.0 ps
789 3.0 head
12  4.0 sh
34  5.0 dash
56  6.0 awk
78  7.0 /usr/bin/bash
`)
	for _, p := range list {
		switch p.Command {
		case "bash", "ps", "head", "sh", "dash", "awk":
			t.Fatalf("self process %q must be filtered out", p.Command)
		}
	}
	if len(list) != 1 || list[0].Command != "/usr/bin/bash" {
		t.Fatalf("list = %+v, want only /usr/bin/bash", list)
	}
}

func TestParseProcessListSortsByCPUThenMemTop5(t *testing.T) {
	list := parseProcessList(`10 1.0 aaa
20 9.0 bbb
30 5.0 ccc
40 3.0 ddd
50 5.0 eee
60 7.0 fff
70 2.0 ggg
`)
	if len(list) != 5 {
		t.Fatalf("list length = %d, want 5", len(list))
	}
	want := []string{"bbb", "fff", "ccc", "eee", "ddd"} // cpu desc; ccc(5.0,30) before eee(5.0,50)? no: mem desc → eee(50) before ccc(30)
	// cpu desc: 9.0 bbb, 7.0 fff, 5.0 ccc(30) & 5.0 eee(50) → eee before ccc by mem desc, 3.0 ddd, 2.0 ggg, 1.0 aaa
	want = []string{"bbb", "fff", "eee", "ccc", "ddd"}
	for i, w := range want {
		if list[i].Command != w {
			t.Fatalf("list[%d] = %q, want %q (all: %+v)", i, list[i].Command, w, list)
		}
	}
}

func TestParseMonitorOutputMissingSectionIsGenericAndPathFree(t *testing.T) {
	broken := strings.Replace(sampleOutput, "---NET---", "", 1)
	_, err := parseMonitorOutput(broken)
	if err == nil {
		t.Fatal("output missing a section must error")
	}
	msg := err.Error()
	if strings.ContainsAny(msg, `/\`) || strings.Contains(msg, "tmp") || strings.Contains(msg, "C:") {
		t.Fatalf("parse error must be generic/path-free, got %q", msg)
	}
	if msg != "Incomplete monitor output" {
		t.Fatalf("error = %q, want 'Incomplete monitor output'", msg)
	}
}

func TestParseMonitorOutputMissingCpuLine(t *testing.T) {
	broken := strings.Replace(sampleOutput, "cpu  10 0 5 85 0 0 0 0", "intr 1 2 3 4 5 6 7", 1)
	if _, err := parseMonitorOutput(broken); err == nil {
		t.Fatal("output without a cpu line must error")
	}
}

func TestKBToBytes(t *testing.T) {
	if kbToBytes(1) != 1024 {
		t.Fatalf("kbToBytes(1) = %d", kbToBytes(1))
	}
}

// Ensure the error type produced by the parser is comparable to errors.New.
var _ = errors.New
