# xterm.js P2 性能基线

- **记录日期**：2026-09-02
- **机器**：Windows amd64, AMD Ryzen 7 8845H w/ Radeon 780M Graphics
- **Go**：go1.26.5 windows/amd64
- **命令**：`go test -bench=BenchmarkBatcher -benchmem -count=3 -timeout 180s -run ^$ nodeshell/internal/sessions`

固定策略使用 `newOutputBatcher(12ms, 48KiB)`；自适应策略使用生产构造器 `newSessionBatcher`（队列深度 ≥ 8 时将单批上限最多提到 96KiB，定时器仍为 12ms）。

三次运行取中位数：

| Benchmark | 策略 | ns/op | MB/s | B/op | allocs/op |
|---|---|---:|---:|---:|---:|
| SmallSlow（64×1B） | 固定 | 3021 | 21.18 | 1088 | 16 |
| ThresholdBurst（48KiB） | 固定 | 19938 | 2465 | 148168 | 11 |
| SustainedHighRate（64×32KiB，快 sink） | 固定 | 1547899 | 1355 | 7866502 | 143 |
| SustainedHighRate（64×32KiB，快 sink） | 自适应 | 2720902 | 771 | 8013048 | 140 |
| SlowSinkBackpressure（16×48KiB，1ms sink） | 固定 | 24348810 | 32.30 | 2360862 | 61 |
| SlowSinkBackpressure（16×48KiB，1ms sink） | 自适应 | 18489273 | 42.53 | 2648850 | 55 |
| MixedCorpus（ASCII/中文/emoji/ANSI/分片 UTF-8） | 固定 | 57106 | 630 | 234480 | 27 |

## 解读

- 低速小块路径仍走 12ms 定时器，本表不声称交互延迟有变化；正确性测试覆盖该路径。
- 快 sink 的微基准对自适应不友好：生产瓶颈是 Wails IPC（慢 sink），不是内存 memcpy。
- 慢 sink 背压是接近生产的模型。自适应把吞吐从约 **32.3 MB/s 提到 42.5 MB/s**（约 +32%），sink 调用更少（55 vs 61 allocs 也更低）。
- 因此生产路径启用自适应；低速 12ms / 48KiB 在队列未积压时保持不变。
