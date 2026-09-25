// Package store keeps the send ledger (JSONL, dedupe + resume) and CSV I/O.
package store

import (
	"bufio"
	"encoding/csv"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/codeyevsky/liout/internal/model"
)

const (
	StatusSent    = "sent"
	StatusFailed  = "failed"
	StatusSkipped = "skipped"
	StatusDry     = "dry_run"
	StatusReplied = "replied"
	StatusInvited = "invited"
)

type Record struct {
	ProfileID string    `json:"profile_id"`
	URN       string    `json:"urn,omitempty"`
	Name      string    `json:"name"`
	Company   string    `json:"company,omitempty"`
	Status    string    `json:"status"`
	Message   string    `json:"message,omitempty"`
	Error     string    `json:"error,omitempty"`
	Variant   string    `json:"variant,omitempty"`  // A/B template name
	Campaign  string    `json:"campaign,omitempty"` // campaign label
	Step      int       `json:"step,omitempty"`     // 1 = first message, 2+ = follow-up
	SentAt    time.Time `json:"sent_at"`
}

type Store struct {
	mu      sync.Mutex
	f       *os.File
	recs    []Record
	idx     map[string]Record
	replied map[string]time.Time
}

func Open(path string) (*Store, error) {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}
	s := &Store{idx: map[string]Record{}, replied: map[string]time.Time{}}
	if b, err := os.Open(path); err == nil {
		sc := bufio.NewScanner(b)
		sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line == "" {
				continue
			}
			var r Record
			if json.Unmarshal([]byte(line), &r) != nil {
				continue
			}
			s.recs = append(s.recs, r)
			s.index(r)
		}
		b.Close()
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	s.f = f
	return s, nil
}

func (s *Store) Close() error { return s.f.Close() }

func (s *Store) index(r Record) {
	switch r.Status {
	case StatusSent, StatusInvited:
		s.idx[r.ProfileID] = r
	case StatusReplied:
		if s.replied == nil {
			s.replied = map[string]time.Time{}
		}
		s.replied[r.ProfileID] = r.SentAt
	}
}

// Records returns a copy of every record, for reporting.
func (s *Store) Records() []Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Record, len(s.recs))
	copy(out, s.recs)
	return out
}

// LastSent returns the most recent message sent to someone.
func (s *Store) LastSent(profileID string) (Record, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.idx[profileID]
	return r, ok
}

// Replied reports whether this person wrote back.
func (s *Store) Replied(profileID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.replied[profileID]
	return ok
}

// Steps counts how many messages this person received (first + follow-ups).
func (s *Store) Steps(profileID string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, r := range s.recs {
		if r.ProfileID == profileID && (r.Status == StatusSent || r.Status == StatusInvited) {
			n++
		}
	}
	return n
}

// FirstSentAt is when the campaign actually started, used for the warm-up ramp.
func (s *Store) FirstSentAt() (time.Time, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var first time.Time
	for _, r := range s.recs {
		if r.Status != StatusSent && r.Status != StatusInvited {
			continue
		}
		if first.IsZero() || r.SentAt.Before(first) {
			first = r.SentAt
		}
	}
	return first, !first.IsZero()
}

// Seen reports whether this person was already messaged successfully.
func (s *Store) Seen(profileID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.idx[profileID]
	return ok
}

func (s *Store) Append(r Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.SentAt.IsZero() {
		r.SentAt = time.Now()
	}
	s.recs = append(s.recs, r)
	s.index(r)
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if _, err := s.f.Write(append(b, '\n')); err != nil {
		return err
	}
	return s.f.Sync()
}

// CountSince counts messages actually sent since t, for rate limiting.
func (s *Store) CountSince(t time.Time) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, r := range s.recs {
		if r.Status == StatusSent && r.SentAt.After(t) {
			n++
		}
	}
	return n
}

// ---------- CSV ----------

func ReadCSV(path string) ([]model.Person, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	r := csv.NewReader(f)
	r.FieldsPerRecord = -1
	rows, err := r.ReadAll()
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	header := rows[0]
	out := make([]model.Person, 0, len(rows)-1)
	for _, row := range rows[1:] {
		if len(row) == 0 || strings.TrimSpace(strings.Join(row, "")) == "" {
			continue
		}
		var p model.Person
		for i, h := range header {
			if i < len(row) {
				p.Set(h, strings.TrimSpace(row[i]))
			}
		}
		out = append(out, p)
	}
	return out, nil
}

func WriteCSV(path string, people []model.Person) error {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	extraSet := map[string]bool{}
	for _, p := range people {
		for _, c := range p.ExtraCols() {
			extraSet[c] = true
		}
	}
	extras := make([]string, 0, len(extraSet))
	for c := range extraSet {
		extras = append(extras, c)
	}
	sortStrings(extras)

	w := csv.NewWriter(f)
	defer w.Flush()
	if err := w.Write(append(append([]string{}, model.CSVHeader...), extras...)); err != nil {
		return err
	}
	for _, p := range people {
		if err := w.Write(p.Row(extras)); err != nil {
			return err
		}
	}
	w.Flush()
	return w.Error()
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
