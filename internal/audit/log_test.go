package audit

import (
	"bufio"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestNewLoggerAtCreatesParentDir(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "audit.log")

	logger, err := NewLoggerAt(path)
	if err != nil {
		t.Fatalf("NewLoggerAt failed: %v", err)
	}
	defer logger.Close()

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected audit log file to exist: %v", err)
	}
}

func TestLoggerLogIsConcurrentSafe(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.log")
	logger, err := NewLoggerAt(path)
	if err != nil {
		t.Fatalf("NewLoggerAt failed: %v", err)
	}
	defer logger.Close()

	const entries = 20
	var wg sync.WaitGroup
	for i := 0; i < entries; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := logger.Log(Entry{Action: "exec", Result: "success", Detail: "entry"}); err != nil {
				t.Errorf("Log failed: %v", err)
			}
		}(i)
	}
	wg.Wait()

	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open audit log failed: %v", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	count := 0
	for scanner.Scan() {
		count++
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan audit log failed: %v", err)
	}
	if count != entries {
		t.Fatalf("line count = %d, want %d", count, entries)
	}
}
