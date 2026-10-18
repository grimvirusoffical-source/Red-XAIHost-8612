package host_test

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"redx/host"
)

func TestOpLogAppendReadReopenTruncateReset(t *testing.T) {
	p := filepath.Join(t.TempDir(), "oplog.jsonl")
	l, err := host.OpenOpLog(p, 5)
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 8; i++ {
		seq, err := l.Append(host.LogEntry{Op: "set", DB: "d", Path: fmt.Sprint("p", i)})
		if err != nil || seq != uint64(i) {
			t.Fatalf("append %d: %d %v", i, seq, err)
		}
	}
	es, err := l.Read(3, 100)
	if err != nil || len(es) != 6 || es[0].Seq != 3 || es[5].Seq != 8 {
		t.Fatalf("read: %v %+v", err, es)
	}
	es, _ = l.Read(4, 2)
	if len(es) != 2 || es[1].Seq != 5 {
		t.Fatalf("limit: %+v", es)
	}
	if es, err := l.Read(9, 10); err != nil || len(es) != 0 {
		t.Fatal("from=last+1 is an empty tail")
	}
	if _, err := l.Read(10, 10); err != host.ErrLogGap {
		t.Fatal("ahead of log must be a gap")
	}
	// compaction at 2*max (10 entries) keeps the last 5
	for i := 9; i <= 10; i++ {
		l.Append(host.LogEntry{Op: "set"})
	}
	l.MaybeCompact()
	if l.First() != 6 || l.Last() != 10 {
		t.Fatalf("after compaction first=%d last=%d", l.First(), l.Last())
	}
	if _, err := l.Read(2, 1); err != host.ErrLogGap {
		t.Fatal("truncated range must be a gap")
	}
	l.Close()

	// reopen keeps offsets; torn tail is discarded
	f, _ := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(`{"seq":11,"op":"set"`) // no newline: torn write
	f.Close()
	l, err = host.OpenOpLog(p, 5)
	if err != nil {
		t.Fatal(err)
	}
	if l.Last() != 10 || l.First() != 6 {
		t.Fatalf("reopen first=%d last=%d", l.First(), l.Last())
	}
	if seq, _ := l.Append(host.LogEntry{Op: "del"}); seq != 11 {
		t.Fatalf("seq after torn tail: %d", seq)
	}
	// follower style append must be sequential
	if err := l.AppendAt(host.LogEntry{Seq: 99}); err == nil {
		t.Fatal("non-sequential AppendAt must fail")
	}
	if err := l.AppendAt(host.LogEntry{Seq: 12, Op: "set"}); err != nil {
		t.Fatal(err)
	}
	// waiters are woken
	ch := l.Wait()
	go func() { time.Sleep(20 * time.Millisecond); l.Append(host.LogEntry{Op: "set"}) }()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal("waiter not woken")
	}
	if err := l.Reset(100); err != nil {
		t.Fatal(err)
	}
	if l.First() != 101 || l.Last() != 100 {
		t.Fatalf("reset first=%d last=%d", l.First(), l.Last())
	}
	if seq, _ := l.Append(host.LogEntry{Op: "set"}); seq != 101 {
		t.Fatal("append after reset")
	}
}

func TestTTLTracker(t *testing.T) {
	tr := host.NewTTLTracker("")
	now := time.Now()
	tr.Apply("d", "a", host.SetOpts{TTLSeconds: 1}, now)
	tr.Apply("d", "b", host.SetOpts{TTLSeconds: 100}, now)
	tr.Apply("d", "s", host.SetOpts{TTLSeconds: 10, Sliding: true}, now)
	tr.Apply("d", "gone", host.SetOpts{TTLSeconds: 1}, now)
	tr.Remove("d", "gone")
	if due := tr.Due(now); len(due) != 0 {
		t.Fatal("nothing due yet")
	}
	due := tr.Due(now.Add(2 * time.Second))
	if len(due) != 1 || due[0].Path != "a" {
		t.Fatalf("due: %+v", due)
	}
	// overwrite without TTL clears the deadline
	tr.Apply("d", "b", host.SetOpts{}, now)
	// touching a sliding key pushes its deadline
	tr.Touch("d", "s", now.Add(8*time.Second))
	if due := tr.Due(now.Add(11 * time.Second)); len(due) != 0 {
		t.Fatalf("sliding key should have been extended: %+v", due)
	}
	if due := tr.Due(now.Add(19 * time.Second)); len(due) != 1 || due[0].Path != "s" {
		t.Fatalf("sliding key due: %+v", due)
	}
	if tr.Len() != 0 {
		t.Fatalf("len %d", tr.Len())
	}
}
