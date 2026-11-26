package socket

// A job's items on the builder's disk until the controller stored them: every output line,
// start and end is numbered and written to the job's spool file before it is sent. The
// controller acks what it stored ; after a reconnect, to the same controller node or another,
// the builder sends again what was not acked, and the controller skips what it had stored
// already: each item exactly once. Once the end is acked the file goes.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"

	"github.com/ansibleforms/builder/internal/logger"
)

// memoryItems is how many items it keeps in memory too, per job: older ones are read back from
// the file.
const memoryItems = 256

// spoolName matches a spool file's name, job-<id>.jsonl.
var spoolName = regexp.MustCompile(`^job-(\d+)\.jsonl$`)

// Item is one item of a job: { kind, seq, ... }.
type Item map[string]any

// Spool is the spool of one job. It is not safe for concurrent use: its owner locks it.
type Spool struct {
	// JobID is the job.
	JobID int64
	// file is the spool's file.
	file string
	// Seq is the last item numbered, Acked the last one the controller acked, Next the next one
	// to send.
	Seq, Acked, Next int64
	// Ended tells that its end (or abandon) is written.
	Ended bool
	// memory keeps the latest items, by number, and order their numbers oldest first.
	memory map[int64]Item
	order  []int64
}

// SpoolFile is the file of a job's spool.
//
// Args:
//
//	dir: the spool folder.
//	jobID: the job.
//
// Returns:
//
//	The file's path.
func SpoolFile(dir string, jobID int64) string {
	return filepath.Join(dir, fmt.Sprintf("job-%d.jsonl", jobID))
}

// SpoolJobID reads the job of a spool file's name.
//
// Args:
//
//	name: the file's name.
//
// Returns:
//
//	The job, 0 when the name is no spool's.
func SpoolJobID(name string) int64 {
	m := spoolName.FindStringSubmatch(name)
	if m == nil {
		return 0
	}
	id, _ := strconv.ParseInt(m[1], 10, 64)
	return id
}

// OpenSpool opens (or makes) a job's spool, its file read back when an earlier start left one.
//
// Args:
//
//	dir: the spool folder.
//	jobID: the job.
//
// Returns:
//
//	The spool.
func OpenSpool(dir string, jobID int64) *Spool {
	s := &Spool{JobID: jobID, file: SpoolFile(dir, jobID), Next: 1, memory: map[int64]Item{}}
	s.load()
	return s
}

// readLines reads the spool's file, line by line, the lines cut by a crash skipped.
//
// Args:
//
//	each: called with every item read ; false stops.
func (s *Spool) readLines(each func(Item) bool) {
	f, err := os.Open(s.file)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), MaxPayload)
	for sc.Scan() {
		var item Item
		if json.Unmarshal(sc.Bytes(), &item) != nil || item == nil {
			// a line cut by a crash : the items before it count
			continue
		}
		if !each(item) {
			return
		}
	}
}

// load reads back an existing file (a builder restarted with items not acked).
func (s *Spool) load() {
	s.readLines(func(item Item) bool {
		seq := item.seq()
		if item.Kind() == "ack" {
			s.Acked = max(s.Acked, seq)
			return true
		}
		s.Seq = max(s.Seq, seq)
		if k := item.Kind(); k == "end" || k == "abandon" {
			s.Ended = true
		}
		return true
	})
	s.Next = s.Acked + 1
}

// Kind is the item's kind.
//
// Returns:
//
//	The kind.
func (i Item) Kind() string {
	k, _ := i["kind"].(string)
	return k
}

// seq is the item's number.
//
// Returns:
//
//	The number, 0 when it has none.
func (i Item) seq() int64 {
	switch v := i["seq"].(type) {
	case float64:
		return int64(v)
	case int64:
		return v
	}
	return 0
}

// Append numbers an item and writes it to the file.
//
// Args:
//
//	item: the item ({ kind, ... }).
//
// Returns:
//
//	The item with its seq.
func (s *Spool) Append(item Item) Item {
	s.Seq++
	numbered := Item{}
	for k, v := range item {
		numbered[k] = v
	}
	numbered["seq"] = s.Seq
	s.writeLine(numbered)
	if k := numbered.Kind(); k == "end" || k == "abandon" {
		s.Ended = true
	}
	s.memory[s.Seq] = numbered
	s.order = append(s.order, s.Seq)
	for len(s.memory) > memoryItems && len(s.order) > 0 {
		delete(s.memory, s.order[0])
		s.order = s.order[1:]
	}
	return numbered
}

// writeLine appends one line to the file, only its owner reading it.
//
// Args:
//
//	v: what to write, as JSON.
func (s *Spool) writeLine(v any) {
	data, err := json.Marshal(v)
	if err != nil {
		logger.Error("Spool of job %d: an item that is no JSON: %v", s.JobID, err)
		return
	}
	f, err := os.OpenFile(s.file, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		logger.Error("Spool of job %d: cannot write: %v", s.JobID, err)
		return
	}
	defer f.Close()
	if _, err := f.Write(append(data, '\n')); err != nil {
		logger.Error("Spool of job %d: cannot write: %v", s.JobID, err)
	}
}

// Get finds an item by its number, from memory or the file.
//
// Args:
//
//	seq: its number.
//
// Returns:
//
//	The item, nil when it is not there.
func (s *Spool) Get(seq int64) Item {
	if item, ok := s.memory[seq]; ok {
		return item
	}
	var found Item
	s.readLines(func(item Item) bool {
		if item.seq() == seq && item.Kind() != "ack" {
			found = item
			return false
		}
		return true
	})
	return found
}

// Ack takes the controller's word that it stored the items up to a number.
//
// Args:
//
//	seq: the last item stored.
//
// Returns:
//
//	True when every item is acked and the job ended (the spool can go).
func (s *Spool) Ack(seq int64) bool {
	if seq <= s.Acked {
		return s.Done()
	}
	s.Acked = min(seq, s.Seq)
	kept := s.order[:0]
	for _, k := range s.order {
		if k <= s.Acked {
			delete(s.memory, k)
			continue
		}
		kept = append(kept, k)
	}
	s.order = kept
	// the ack on disk too : a restart then resends only what came after
	s.writeLine(Item{"kind": "ack", "seq": s.Acked})
	if s.Next <= s.Acked {
		s.Next = s.Acked + 1
	}
	return s.Done()
}

// Done tells that every item is acked and the job ended.
//
// Returns:
//
//	True when the spool can go.
func (s *Spool) Done() bool { return s.Ended && s.Acked >= s.Seq }

// Rewind makes everything not acked go again (after a reconnect).
func (s *Spool) Rewind() { s.Next = s.Acked + 1 }

// Remove removes the file (the end acked).
func (s *Spool) Remove() { _ = os.Remove(s.file) }
