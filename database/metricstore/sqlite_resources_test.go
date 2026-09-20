package metricstore

import "testing"

func TestSQLiteResourceProfilesBoundMemoryAndConcurrency(t *testing.T) {
	tests := []struct {
		name       string
		memory     int64
		cpus       int
		writerKB   int
		readerKB   int
		readers    int
		concurrent int
		mmap       int64
	}{
		{"small-many-cpu", 512 * 1024 * 1024, 8, 2 * 1024, 2 * 1024, 1, 1, 0},
		{"one-gib-many-cpu", gib, 8, 4 * 1024, 2 * 1024, 2, 2, 0},
		{"two-gib-one-cpu", 2 * gib, 1, 6 * 1024, 3 * 1024, 1, 1, 0},
		{"large-many-cpu", 8 * gib, 16, 8 * 1024, 4 * 1024, 2, 2, 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := sqliteResourceProfileFor(test.memory, test.cpus)
			if got.WriterCacheKB != test.writerKB || got.ReaderCacheKB != test.readerKB ||
				got.ReadPoolSize != test.readers || got.HeavyReadConcurrent != test.concurrent ||
				got.MMapBytes != test.mmap {
				t.Fatalf("profile = %#v", got)
			}
			totalCacheKB := got.WriterCacheKB + got.ReaderCacheKB*got.ReadPoolSize
			if totalCacheKB > 16*1024 {
				t.Fatalf("sqlite cache budget = %d KiB, want <= 16 MiB", totalCacheKB)
			}
		})
	}
}
