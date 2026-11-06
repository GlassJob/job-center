package archive

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLocalSinkRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s := LocalSink{Dir: dir}
	day := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	uri, bytes, sum, n, err := s.Write("job_execution", day, func(w io.Writer) (int64, error) {
		enc := json.NewEncoder(w)
		_ = enc.Encode(map[string]any{"id": 1, "status": "SUCCEEDED"})
		_ = enc.Encode(map[string]any{"id": 2, "status": "FAILED"})
		return 2, nil
	})
	if err != nil || n != 2 || bytes <= 0 || sum == "" {
		t.Fatalf("write: uri=%s n=%d bytes=%d sum=%s err=%v", uri, n, bytes, sum, err)
	}
	if _, err := os.Stat(uri); err != nil {
		t.Fatal(err)
	}
	if filepath.Ext(uri) != ".gz" && filepath.Base(uri)[len(filepath.Base(uri))-8:] != "jsonl.gz" {
		// path ends with .jsonl.gz
	}
	vn, vsum, err := s.Verify(uri)
	if err != nil || vn != 2 || vsum != sum {
		t.Fatalf("verify vn=%d sum=%s/%s err=%v", vn, vsum, sum, err)
	}
}

func TestNextRun(t *testing.T) {
	a := New(nil, LocalSink{}, Options{DailyAt: "03:30", Location: time.UTC}, nil)
	from := time.Date(2026, 11, 6, 1, 0, 0, 0, time.UTC)
	next := a.NextRun(from)
	if next.Hour() != 3 || next.Minute() != 30 || next.Day() != 6 {
		t.Fatalf("got %v", next)
	}
	from2 := time.Date(2026, 11, 6, 4, 0, 0, 0, time.UTC)
	next2 := a.NextRun(from2)
	if next2.Day() != 7 {
		t.Fatalf("got %v", next2)
	}
}
