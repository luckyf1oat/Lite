package metricstore

import "runtime"

const gib = int64(1024 * 1024 * 1024)

type sqliteResourceProfile struct {
	MemoryBytes         int64
	CPUCount            int
	WriterCacheKB       int
	ReaderCacheKB       int
	ReadPoolSize        int
	HeavyReadConcurrent int
	MMapBytes           int64
}

func detectSQLiteResourceProfile() sqliteResourceProfile {
	memoryBytes, cpuCount := detectEffectiveResources()
	return sqliteResourceProfileFor(memoryBytes, cpuCount)
}

func sqliteResourceProfileFor(memoryBytes int64, cpuCount int) sqliteResourceProfile {
	if memoryBytes <= 0 {
		memoryBytes = gib
	}
	if cpuCount <= 0 {
		cpuCount = runtime.NumCPU()
	}
	if cpuCount <= 0 {
		cpuCount = 1
	}

	// Size caches for a typical Lite fleet, not the host's unused RAM.
	// mmap is off: SQLite would otherwise keep hot pages in both the page
	// cache and the mapping, so RSS grows with metrics.db for no extra hit rate.
	profile := sqliteResourceProfile{
		MemoryBytes:         memoryBytes,
		CPUCount:            cpuCount,
		WriterCacheKB:       4 * 1024,
		ReaderCacheKB:       2 * 1024,
		ReadPoolSize:        1,
		HeavyReadConcurrent: 1,
		MMapBytes:           0,
	}
	switch {
	case memoryBytes <= 512*1024*1024:
		profile.WriterCacheKB = 2 * 1024
		profile.ReaderCacheKB = 2 * 1024
	case memoryBytes <= gib:
		// Defaults above: about 6 MiB of SQLite cache on 1 GiB hosts.
	case memoryBytes <= 2*gib:
		profile.WriterCacheKB = 6 * 1024
		profile.ReaderCacheKB = 3 * 1024
	default:
		profile.WriterCacheKB = 8 * 1024
		profile.ReaderCacheKB = 4 * 1024
	}

	if cpuCount >= 2 && memoryBytes > 512*1024*1024 {
		profile.ReadPoolSize = 2
		profile.HeavyReadConcurrent = 2
	}
	return profile
}
