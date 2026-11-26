package socket

import (
	"os"
	"path/filepath"
	"testing"
)

// TestSpool checks numbering, the window's resend after a rewind, the acks kept on disk and a
// restart that resends only what was not acked.
func TestSpool(t *testing.T) {
	dir := t.TempDir()
	s := OpenSpool(dir, 5)
	for i := 0; i < 3; i++ {
		s.Append(Item{"kind": "output"})
	}
	if s.Seq != 3 || s.Get(2).Kind() != "output" {
		t.Fatalf("seq %d, item 2 %v", s.Seq, s.Get(2))
	}
	if s.Ack(2) {
		t.Error("done before its end")
	}
	s.Append(Item{"kind": "end"})
	// a restart : the acks read back, the next item the first not acked
	again := OpenSpool(dir, 5)
	if again.Seq != 4 || again.Acked != 2 || again.Next != 3 || !again.Ended {
		t.Errorf("read back %+v", again)
	}
	if again.Get(3) == nil || again.Get(3).Kind() != "output" {
		t.Errorf("item 3 from the file: %v", again.Get(3))
	}
	if !again.Ack(4) {
		t.Error("not done once its end is acked")
	}
	again.Remove()
	if _, err := os.Stat(SpoolFile(dir, 5)); !os.IsNotExist(err) {
		t.Error("the file is still there")
	}
	if SpoolJobID("job-12.jsonl") != 12 || SpoolJobID("job-x.jsonl") != 0 || filepath.Base(SpoolFile(dir, 3)) != "job-3.jsonl" {
		t.Error("spool names")
	}
}

// TestSpoolEdges checks a cut and a null line read back, an item without a number, the memory
// kept to its last items, a repeated ack, and a rewind.
func TestSpoolEdges(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(SpoolFile(dir, 6), []byte("{\"kind\":\"output\",\"seq\":1}\nnull\n{\"kind\":\"out\n"), 0o600)
	s := OpenSpool(dir, 6)
	if s.Seq != 1 {
		t.Errorf("read back seq %d", s.Seq)
	}
	if (Item{"seq": "x"}).seq() != 0 || (Item{"seq": int64(4)}).seq() != 4 {
		t.Error("an item's number")
	}
	for i := 0; i < memoryItems+10; i++ {
		s.Append(Item{"kind": "output"})
	}
	if len(s.memory) != memoryItems || s.Get(2) == nil {
		t.Errorf("memory %d, item 2 from the file %v", len(s.memory), s.Get(2))
	}
	s.Ack(5)
	if s.Ack(3) || s.Acked != 5 {
		t.Error("an older ack moved it back")
	}
	s.Next = 20
	s.Rewind()
	if s.Next != 6 {
		t.Errorf("rewound to %d", s.Next)
	}
}

// TestSpoolWriteFails checks an item that is no JSON, a folder that is not there and a full
// disk: the spool logs it and goes on.
func TestSpoolWriteFails(t *testing.T) {
	s := OpenSpool(t.TempDir(), 7)
	s.Append(Item{"kind": "output", "bad": func() {}})
	s.file = filepath.Join(t.TempDir(), "none", "job-7.jsonl")
	s.Append(Item{"kind": "output"})
	s.file = "/dev/full"
	s.Append(Item{"kind": "output"})
	if s.Seq != 3 {
		t.Errorf("seq %d", s.Seq)
	}
}
