// Go port of Coda Hale's Metrics library
//
// <https://github.com/rcrowley/go-metrics>
//
// Coda Hale's original work: <https://github.com/codahale/metrics>

package metrics

import (
	"runtime/metrics"
	"runtime/pprof"
	"sync/atomic"
	"time"
)

var (
	metricsEnabled atomic.Bool
)

// Enabled is checked by functions that are deemed 'expensive', e.g. if a
// meter-type does locking and/or non-trivial math operations during update.
func Enabled() bool {
	return metricsEnabled.Load()
}

// Enable enables the metrics system.
// The Enabled-flag is expected to be set, once, during startup, but toggling off and on
// is not supported.
func Enable() {
	metricsEnabled.Store(true)
	startMeterTickerLoop()
}

var threadCreateProfile = pprof.Lookup("threadcreate")

type runtimeStats struct {
	// GC related metrics
	GCCyclesAutomatic uint64
	GCCyclesForced    uint64
	GCCycles          uint64
	GCHeapAllocBytes  uint64
	GCHeapFreedBytes  uint64
	GCPauses          *metrics.Float64Histogram
	GCPausesSTW       *metrics.Float64Histogram

	// Memory related metrics
	MemTotal     uint64
	HeapObjects  uint64
	HeapFree     uint64
	HeapReleased uint64
	HeapUnused   uint64

	Goroutines   uint64
	SchedLatency *metrics.Float64Histogram

	// CPU related metrics (cumulative cpu-seconds, compare only within /cpu/classes)
	GCCPUMarkAssist    float64
	GCCPUMarkDedicated float64
	GCCPUMarkIdle      float64
	GCCPUTotal         float64
	GCCPUPause         float64
	CPUUser            float64
}

var runtimeSamples = []metrics.Sample{
	// Refer to https://pkg.go.dev/runtime/metrics for more details.

	// GC related
	{Name: "/gc/cycles/automatic:gc-cycles"},
	{Name: "/gc/cycles/forced:gc-cycles"},
	{Name: "/gc/cycles/total:gc-cycles"},
	{Name: "/gc/heap/allocs:bytes"},
	{Name: "/gc/heap/frees:bytes"},
	{Name: "/sched/pauses/total/gc:seconds"},    // histogram
	{Name: "/sched/pauses/stopping/gc:seconds"}, // histogram

	// Memory related metrics
	{Name: "/memory/classes/total:bytes"},
	{Name: "/memory/classes/heap/objects:bytes"},
	{Name: "/memory/classes/heap/free:bytes"},
	{Name: "/memory/classes/heap/released:bytes"},
	{Name: "/memory/classes/heap/unused:bytes"},
	{Name: "/sched/goroutines:goroutines"},
	{Name: "/sched/latencies:seconds"}, // histogram

	// CPU related metrics
	{Name: "/cpu/classes/gc/mark/assist:cpu-seconds"},
	{Name: "/cpu/classes/gc/mark/dedicated:cpu-seconds"},
	{Name: "/cpu/classes/gc/mark/idle:cpu-seconds"},
	{Name: "/cpu/classes/gc/total:cpu-seconds"},
	{Name: "/cpu/classes/gc/pause:cpu-seconds"},
	{Name: "/cpu/classes/user:cpu-seconds"},
}

func ReadRuntimeStats() *runtimeStats {
	r := new(runtimeStats)
	readRuntimeStats(r)
	return r
}

func readRuntimeStats(v *runtimeStats) {
	metrics.Read(runtimeSamples)

	for _, s := range runtimeSamples {
		// Skip invalid/unknown metrics. This is needed because some metrics
		// are unavailable in older Go versions, and attempting to read a 'bad'
		// metric panics.
		if s.Value.Kind() == metrics.KindBad {
			continue
		}

		switch s.Name {
		case "/gc/cycles/automatic:gc-cycles":
			v.GCCyclesAutomatic = s.Value.Uint64()
		case "/gc/cycles/forced:gc-cycles":
			v.GCCyclesForced = s.Value.Uint64()
		case "/gc/cycles/total:gc-cycles":
			v.GCCycles = s.Value.Uint64()
		case "/gc/heap/allocs:bytes":
			v.GCHeapAllocBytes = s.Value.Uint64()
		case "/gc/heap/frees:bytes":
			v.GCHeapFreedBytes = s.Value.Uint64()
		case "/sched/pauses/total/gc:seconds":
			v.GCPauses = s.Value.Float64Histogram()
		case "/sched/pauses/stopping/gc:seconds":
			v.GCPausesSTW = s.Value.Float64Histogram()

		case "/memory/classes/total:bytes":
			v.MemTotal = s.Value.Uint64()
		case "/memory/classes/heap/objects:bytes":
			v.HeapObjects = s.Value.Uint64()
		case "/memory/classes/heap/free:bytes":
			v.HeapFree = s.Value.Uint64()
		case "/memory/classes/heap/released:bytes":
			v.HeapReleased = s.Value.Uint64()
		case "/memory/classes/heap/unused:bytes":
			v.HeapUnused = s.Value.Uint64()
		case "/sched/goroutines:goroutines":
			v.Goroutines = s.Value.Uint64()
		case "/sched/latencies:seconds":
			v.SchedLatency = s.Value.Float64Histogram()

		case "/cpu/classes/gc/mark/assist:cpu-seconds":
			v.GCCPUMarkAssist = s.Value.Float64()
		case "/cpu/classes/gc/mark/dedicated:cpu-seconds":
			v.GCCPUMarkDedicated = s.Value.Float64()
		case "/cpu/classes/gc/mark/idle:cpu-seconds":
			v.GCCPUMarkIdle = s.Value.Float64()
		case "/cpu/classes/gc/total:cpu-seconds":
			v.GCCPUTotal = s.Value.Float64()
		case "/cpu/classes/gc/pause:cpu-seconds":
			v.GCCPUPause = s.Value.Float64()
		case "/cpu/classes/user:cpu-seconds":
			v.CPUUser = s.Value.Float64()
		}
	}
}

// CollectProcessMetrics periodically collects various metrics about the running process.
func CollectProcessMetrics(refresh time.Duration) {
	// Short circuit if the metrics system is disabled
	if !metricsEnabled.Load() {
		return
	}

	// Create the various data collectors
	var (
		cpustats  = make([]CPUStats, 2)
		diskstats = make([]DiskStats, 2)
		rstats    = make([]runtimeStats, 2)
	)

	// This scale factor is used for the runtime's time metrics. It's useful to convert to
	// ns here because the runtime gives times in float seconds, but runtimeHistogram can
	// only provide integers for the minimum and maximum values.
	const secondsToNs = float64(time.Second)

	// Define the various metrics to collect
	var (
		cpuSysLoad            = GetOrRegisterGauge("system/cpu/sysload", DefaultRegistry)
		cpuSysWait            = GetOrRegisterGauge("system/cpu/syswait", DefaultRegistry)
		cpuProcLoad           = GetOrRegisterGauge("system/cpu/procload", DefaultRegistry)
		cpuSysLoadTotal       = GetOrRegisterCounterFloat64("system/cpu/sysload/total", DefaultRegistry)
		cpuSysWaitTotal       = GetOrRegisterCounterFloat64("system/cpu/syswait/total", DefaultRegistry)
		cpuProcLoadTotal      = GetOrRegisterCounterFloat64("system/cpu/procload/total", DefaultRegistry)
		cpuThreads            = GetOrRegisterGauge("system/cpu/threads", DefaultRegistry)
		cpuGoroutines         = GetOrRegisterGauge("system/cpu/goroutines", DefaultRegistry)
		cpuSchedLatency       = getOrRegisterRuntimeHistogram("system/cpu/schedlatency", secondsToNs, nil)
		memAllocs             = GetOrRegisterMeter("system/memory/allocs", DefaultRegistry)
		memFrees              = GetOrRegisterMeter("system/memory/frees", DefaultRegistry)
		memTotal              = GetOrRegisterGauge("system/memory/held", DefaultRegistry)
		heapUsed              = GetOrRegisterGauge("system/memory/used", DefaultRegistry)
		heapObjects           = GetOrRegisterGauge("system/memory/objects", DefaultRegistry)
		diskReads             = GetOrRegisterMeter("system/disk/readcount", DefaultRegistry)
		diskReadBytes         = GetOrRegisterMeter("system/disk/readdata", DefaultRegistry)
		diskReadBytesCounter  = GetOrRegisterCounter("system/disk/readbytes", DefaultRegistry)
		diskWrites            = GetOrRegisterMeter("system/disk/writecount", DefaultRegistry)
		diskWriteBytes        = GetOrRegisterMeter("system/disk/writedata", DefaultRegistry)
		diskWriteBytesCounter = GetOrRegisterCounter("system/disk/writebytes", DefaultRegistry)

		// GC CPU time breakdown (cumulative cpu-seconds, reported as deltas)
		// Use rate() on Datadog to get cpu-seconds/second (0 to GOMAXPROCS range).
		// Compute GC fraction: rate(gc/total) / (rate(gc/total) + rate(user))
		gcCPUMarkAssist    = GetOrRegisterCounterFloat64("system/cpu/gc/mark/assist", DefaultRegistry)
		gcCPUMarkDedicated = GetOrRegisterCounterFloat64("system/cpu/gc/mark/dedicated", DefaultRegistry)
		gcCPUMarkIdle      = GetOrRegisterCounterFloat64("system/cpu/gc/mark/idle", DefaultRegistry)
		gcCPUTotal         = GetOrRegisterCounterFloat64("system/cpu/gc/total", DefaultRegistry)
		gcCPUPause         = GetOrRegisterCounterFloat64("system/cpu/gc/pause", DefaultRegistry)
		cpuUser            = GetOrRegisterCounterFloat64("system/cpu/user", DefaultRegistry)

		// GC scheduling latency histograms (in nanoseconds)
		gcPauses    = getOrRegisterRuntimeHistogram("system/gc/pauses/total", secondsToNs, nil)
		gcPausesSTW = getOrRegisterRuntimeHistogram("system/gc/pauses/stopping", secondsToNs, nil)

		// GC cycle counts (monotonically increasing, use rate() for cycles/sec)
		gcCyclesAutomatic = GetOrRegisterCounter("system/gc/cycles/automatic", DefaultRegistry)
		gcCyclesForced    = GetOrRegisterCounter("system/gc/cycles/forced", DefaultRegistry)
		gcCycles          = GetOrRegisterCounter("system/gc/cycles/total", DefaultRegistry)
	)

	var lastCollectTime time.Time

	// Iterate loading the different stats and updating the meters.
	now, prev := 0, 1

	for ; ; now, prev = prev, now {
		// Gather CPU times.
		ReadCPUStats(&cpustats[now])

		collectTime := time.Now()
		secondsSinceLastCollect := collectTime.Sub(lastCollectTime).Seconds()
		lastCollectTime = collectTime

		if secondsSinceLastCollect > 0 {
			sysLoad := cpustats[now].GlobalTime - cpustats[prev].GlobalTime
			sysWait := cpustats[now].GlobalWait - cpustats[prev].GlobalWait
			procLoad := cpustats[now].LocalTime - cpustats[prev].LocalTime
			// Convert to integer percentage.
			cpuSysLoad.Update(int64(sysLoad / secondsSinceLastCollect * 100))
			cpuSysWait.Update(int64(sysWait / secondsSinceLastCollect * 100))
			cpuProcLoad.Update(int64(procLoad / secondsSinceLastCollect * 100))
			// increment counters (ms)
			cpuSysLoadTotal.Inc(sysLoad)
			cpuSysWaitTotal.Inc(sysWait)
			cpuProcLoadTotal.Inc(procLoad)
		}

		// Threads
		cpuThreads.Update(int64(threadCreateProfile.Count()))

		// Go runtime metrics
		readRuntimeStats(&rstats[now])

		cpuGoroutines.Update(int64(rstats[now].Goroutines))
		cpuSchedLatency.update(rstats[now].SchedLatency)

		memAllocs.Mark(int64(rstats[now].GCHeapAllocBytes - rstats[prev].GCHeapAllocBytes))
		memFrees.Mark(int64(rstats[now].GCHeapFreedBytes - rstats[prev].GCHeapFreedBytes))

		memTotal.Update(int64(rstats[now].MemTotal))
		heapUsed.Update(int64(rstats[now].MemTotal - rstats[now].HeapUnused - rstats[now].HeapFree - rstats[now].HeapReleased))
		heapObjects.Update(int64(rstats[now].HeapObjects))

		// GC CPU time breakdown (incremented by delta each collection)
		gcCPUMarkAssist.Inc(rstats[now].GCCPUMarkAssist - rstats[prev].GCCPUMarkAssist)
		gcCPUMarkDedicated.Inc(rstats[now].GCCPUMarkDedicated - rstats[prev].GCCPUMarkDedicated)
		gcCPUMarkIdle.Inc(rstats[now].GCCPUMarkIdle - rstats[prev].GCCPUMarkIdle)
		gcCPUTotal.Inc(rstats[now].GCCPUTotal - rstats[prev].GCCPUTotal)
		gcCPUPause.Inc(rstats[now].GCCPUPause - rstats[prev].GCCPUPause)
		cpuUser.Inc(rstats[now].CPUUser - rstats[prev].CPUUser)

		// GC scheduling latency histograms
		gcPauses.update(rstats[now].GCPauses)
		gcPausesSTW.update(rstats[now].GCPausesSTW)

		// GC cycle counts (incremented by delta each collection)
		gcCyclesAutomatic.Inc(int64(rstats[now].GCCyclesAutomatic - rstats[prev].GCCyclesAutomatic))
		gcCyclesForced.Inc(int64(rstats[now].GCCyclesForced - rstats[prev].GCCyclesForced))
		gcCycles.Inc(int64(rstats[now].GCCycles - rstats[prev].GCCycles))

		// Disk
		if ReadDiskStats(&diskstats[now]) == nil {
			diskReads.Mark(diskstats[now].ReadCount - diskstats[prev].ReadCount)
			diskReadBytes.Mark(diskstats[now].ReadBytes - diskstats[prev].ReadBytes)
			diskWrites.Mark(diskstats[now].WriteCount - diskstats[prev].WriteCount)
			diskWriteBytes.Mark(diskstats[now].WriteBytes - diskstats[prev].WriteBytes)
			diskReadBytesCounter.Inc(diskstats[now].ReadBytes - diskstats[prev].ReadBytes)
			diskWriteBytesCounter.Inc(diskstats[now].WriteBytes - diskstats[prev].WriteBytes)
		}

		time.Sleep(refresh)
	}
}
