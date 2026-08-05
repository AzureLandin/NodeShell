// Package monitor owns the remote Linux /proc sampling: the single base64-
// wrapped shell script, the parsers (ported from src/main/monitor-parse.ts
// with identical semantics) and the polling Service. It never touches Wails —
// the App wires the execer and the event sink.
package monitor

import (
	"encoding/base64"
	"errors"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// MonitorScript is the single remote script. The process list must NOT use
// `ps|head || fallback` — when ps fails, head still exits 0 and the fallback
// never runs (mirrors the Electron build).
const MonitorScript = `echo ---STAT---
head -n 1 /proc/stat
echo ---MEM---
cat /proc/meminfo
echo ---LOAD---
cat /proc/loadavg
echo ---NET---
cat /proc/net/dev
echo ---PS---
psbin=$(command -v ps 2>/dev/null || true)
[ -n "$psbin" ] || { [ -x /usr/bin/ps ] && psbin=/usr/bin/ps; }
[ -n "$psbin" ] || { [ -x /bin/ps ] && psbin=/bin/ps; }
out=
if [ -n "$psbin" ]; then out=$($psbin -eo rss=,pcpu=,comm= --sort=-pcpu 2>/dev/null || true); fi
if [ -z "$out" ] && [ -n "$psbin" ]; then out=$($psbin -eo rss=,pcpu=,comm= 2>/dev/null || true); fi
if [ -z "$out" ] && [ -n "$psbin" ]; then out=$($psbin axo rss=,pcpu=,comm= 2>/dev/null || true); fi
if [ -z "$out" ] && [ -n "$psbin" ]; then out=$($psbin aux 2>/dev/null || true); fi
printf "%s\n" "$out" | awk "NF>0 {print; if (++n >= 8) exit}"`

// shellWrap base64-encodes the script so $vars and real newlines survive the
// SSH shell -c (the Electron wrap: JSON.stringify + double quotes would let
// $psbin/$out expand and break the if/then structure).
func shellWrap(script string) string {
	return "echo " + base64.StdEncoding.EncodeToString([]byte(script)) + " | base64 -d | /bin/sh"
}

var cachedScript = shellWrap(MonitorScript)

// Script returns the cached base64-wrapped monitor command.
func Script() string { return cachedScript }

// CpuCounters are the /proc/stat cpu jiffies used for delta percent.
type CpuCounters struct {
	Idle  int64
	Total int64
}

// NetCounters are the summed non-loopback interface byte counters.
type NetCounters struct {
	Rx int64
	Tx int64
}

// MonitorProcess is one process row; JSON tags match MonitorProcess in the
// frontend (and the Snapshot DTO).
type MonitorProcess struct {
	MemBytes   int64   `json:"memBytes"`
	CPUPercent float64 `json:"cpuPercent"`
	Command    string  `json:"command"`
}

// ProcSnapshotRaw is one parsed sample (the service turns it into Snapshot).
type ProcSnapshotRaw struct {
	CPU            CpuCounters
	MemTotalKb     int64
	MemAvailableKb int64
	SwapTotalKb    int64
	SwapFreeKb     int64
	Load1          float64
	Load5          float64
	Load15         float64
	Net            NetCounters
	Processes      []MonitorProcess
}

// MemInfo is the parsed subset of /proc/meminfo.
type MemInfo struct {
	MemTotalKb     int64
	MemAvailableKb int64
	SwapTotalKb    int64
	SwapFreeKb     int64
}

func parseCpuStatLine(line string) (CpuCounters, error) {
	parts := strings.Fields(line)
	if len(parts) < 5 || parts[0] != "cpu" {
		return CpuCounters{}, errors.New("Invalid /proc/stat cpu line")
	}
	nums := make([]int64, len(parts)-1)
	for i, p := range parts[1:] {
		n, err := strconv.ParseInt(p, 10, 64)
		if err != nil {
			return CpuCounters{}, errors.New("Invalid /proc/stat numbers")
		}
		nums[i] = n
	}
	var total int64
	for _, n := range nums {
		total += n
	}
	idle := nums[3]
	if len(nums) > 4 {
		idle += nums[4] // idle + iowait
	}
	return CpuCounters{Idle: idle, Total: total}, nil
}

func cpuPercentFromDelta(prev, next CpuCounters) float64 {
	idleDelta := next.Idle - prev.Idle
	totalDelta := next.Total - prev.Total
	if totalDelta <= 0 {
		return 0
	}
	used := 1 - float64(idleDelta)/float64(totalDelta)
	return math.Min(100, math.Max(0, used*100))
}

var meminfoRe = regexp.MustCompile(`^(\w+):\s+(\d+)`)

func parseMeminfo(text string) MemInfo {
	values := map[string]int64{}
	for _, line := range strings.Split(text, "\n") {
		if m := meminfoRe.FindStringSubmatch(line); m != nil {
			if n, err := strconv.ParseInt(m[2], 10, 64); err == nil {
				values[m[1]] = n
			}
		}
	}
	info := MemInfo{
		MemTotalKb:  values["MemTotal"],
		SwapTotalKb: values["SwapTotal"],
		SwapFreeKb:  values["SwapFree"],
	}
	if av, ok := values["MemAvailable"]; ok {
		info.MemAvailableKb = av
	} else {
		info.MemAvailableKb = values["MemFree"] + values["Buffers"] + values["Cached"]
	}
	return info
}

func parseLoadavg(line string) (load1, load5, load15 float64, err error) {
	parts := strings.Fields(line)
	if len(parts) < 3 {
		return 0, 0, 0, errors.New("Invalid /proc/loadavg")
	}
	values := make([]float64, 3)
	for i := 0; i < 3; i++ {
		n, e := strconv.ParseFloat(parts[i], 64)
		// TS parity: Number.isFinite rejects NaN/±Inf, which strconv accepts
		// without error; a non-finite load would break JSON marshalling.
		if e != nil || math.IsNaN(n) || math.IsInf(n, 0) {
			return 0, 0, 0, errors.New("Invalid /proc/loadavg")
		}
		values[i] = n
	}
	return values[0], values[1], values[2], nil
}

func parseNetDev(text string) NetCounters {
	var rx, tx int64
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		idx := strings.IndexByte(trimmed, ':')
		if idx < 0 {
			continue
		}
		if iface := strings.TrimSpace(trimmed[:idx]); iface == "lo" {
			continue
		}
		nums := strings.Fields(trimmed[idx+1:])
		if len(nums) < 9 {
			continue
		}
		valid := true
		for i := 0; i < 9; i++ {
			if _, err := strconv.ParseInt(nums[i], 10, 64); err != nil {
				valid = false
				break
			}
		}
		if !valid {
			continue
		}
		rx += atoi64(nums[0])
		tx += atoi64(nums[8])
	}
	return NetCounters{Rx: rx, Tx: tx}
}

func atoi64(s string) int64 {
	n, _ := strconv.ParseInt(s, 10, 64)
	return n
}

var (
	procHeaderRe = regexp.MustCompile(`(?i)^(RSS|%CPU|PID|COMMAND|USER|%MEM)\b`)
	procRSSRe    = regexp.MustCompile(`^(\d+)\s+([\d.]+)\s+(.+)$`)
	auxPidRe     = regexp.MustCompile(`^\d+$`)
	auxCpuRe     = regexp.MustCompile(`^[\d.]+$`)
	selfProcRe   = regexp.MustCompile(`^(ps|head|awk|sh|dash|bash)$`)
)

func parseProcessList(text string) []MonitorProcess {
	out := make([]MonitorProcess, 0)
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || procHeaderRe.MatchString(line) {
			continue
		}
		// rss= pcpu= comm= → "12345  1.7 sshd"
		if m := procRSSRe.FindStringSubmatch(line); m != nil {
			rssKb, e1 := strconv.ParseInt(m[1], 10, 64)
			cpu, e2 := strconv.ParseFloat(m[2], 64)
			command := strings.TrimSpace(m[3])
			if e1 == nil && e2 == nil && command != "" && !selfProcRe.MatchString(command) {
				out = append(out, MonitorProcess{MemBytes: rssKb * 1024, CPUPercent: cpu, Command: command})
			}
			continue
		}
		// ps aux → USER PID %CPU %MEM VSZ RSS TTY STAT START TIME COMMAND
		aux := strings.Fields(line)
		if len(aux) >= 11 && auxPidRe.MatchString(aux[1]) && auxCpuRe.MatchString(aux[2]) {
			cpu, e2 := strconv.ParseFloat(aux[2], 64)
			rssKb, e1 := strconv.ParseInt(aux[5], 10, 64)
			command := strings.Join(aux[10:], " ")
			if e1 == nil && e2 == nil && command != "" {
				out = append(out, MonitorProcess{MemBytes: rssKb * 1024, CPUPercent: cpu, Command: command})
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].CPUPercent != out[j].CPUPercent {
			return out[i].CPUPercent > out[j].CPUPercent
		}
		return out[i].MemBytes > out[j].MemBytes
	})
	if len(out) > 5 {
		out = out[:5]
	}
	return out
}

// sectionMarkerRe matches a section header; the optional newline belongs to
// the marker so the section body starts on the next line.
var sectionMarkerRe = regexp.MustCompile(`---(STAT|MEM|LOAD|NET|PS)---\n?`)

func splitSections(output string) map[string]string {
	sections := map[string]string{}
	idx := sectionMarkerRe.FindAllStringSubmatchIndex(output, -1)
	for i := range idx {
		key := output[idx[i][2]:idx[i][3]]
		start := idx[i][1]
		end := len(output)
		if i+1 < len(idx) {
			end = idx[i+1][0]
		}
		sections[key] = output[start:end]
	}
	return sections
}

func parseMonitorOutput(output string) (ProcSnapshotRaw, error) {
	sections := splitSections(output)
	stat, okStat := sections["STAT"]
	mem, okMem := sections["MEM"]
	load, okLoad := sections["LOAD"]
	net, okNet := sections["NET"]
	if !okStat || !okMem || !okLoad || !okNet {
		return ProcSnapshotRaw{}, errors.New("Incomplete monitor output")
	}
	var cpuLine string
	for _, l := range strings.Split(stat, "\n") {
		if trimmed := strings.TrimSpace(l); strings.HasPrefix(trimmed, "cpu ") {
			cpuLine = trimmed
			break
		}
	}
	if cpuLine == "" {
		return ProcSnapshotRaw{}, errors.New("Missing cpu line")
	}
	cpu, err := parseCpuStatLine(cpuLine)
	if err != nil {
		return ProcSnapshotRaw{}, err
	}
	info := parseMeminfo(mem)
	l1, l5, l15, err := parseLoadavg(strings.Split(strings.TrimSpace(load), "\n")[0])
	if err != nil {
		return ProcSnapshotRaw{}, err
	}
	return ProcSnapshotRaw{
		CPU:            cpu,
		MemTotalKb:     info.MemTotalKb,
		MemAvailableKb: info.MemAvailableKb,
		SwapTotalKb:    info.SwapTotalKb,
		SwapFreeKb:     info.SwapFreeKb,
		Load1:          l1,
		Load5:          l5,
		Load15:         l15,
		Net:            parseNetDev(net),
		Processes:      parseProcessList(sections["PS"]),
	}, nil
}

func kbToBytes(kb int64) int64 { return kb * 1024 }
