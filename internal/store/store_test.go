package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type state struct {
	Count int      `json:"count"`
	Names []string `json:"names"`
}

func TestSaveAndLoad(t *testing.T) {
	dir := t.TempDir()
	s := Open(dir, "svc")
	var got state
	if ok, err := s.Load(&got); ok || err != nil {
		t.Fatalf("empty dir: %v, %v", ok, err)
	}

	st := state{Count: 1, Names: []string{"a"}}
	s.Run(func() any { return st }, time.Hour) // The ticker never fires here
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if ok, err := Open(dir, "svc").Load(&got); !ok || err != nil || got.Count != 1 || got.Names[0] != "a" {
		t.Fatalf("load: %v %v %+v", ok, err, got)
	}
	// No temp files left behind.
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("files: %v", entries)
	}
}

func TestUnchangedStateIsNotRewritten(t *testing.T) {
	dir := t.TempDir()
	s := Open(dir, "svc")
	st := state{Count: 1}
	s.Run(func() any { return st }, time.Hour)
	s.Save()
	info1, _ := os.Stat(s.Path())
	time.Sleep(20 * time.Millisecond)
	s.Save()
	info2, _ := os.Stat(s.Path())
	if !info1.ModTime().Equal(info2.ModTime()) {
		t.Error("unchanged state was rewritten")
	}
	st.Count = 2
	s.Save()
	var got state
	Open(dir, "svc").Load(&got)
	if got.Count != 2 {
		t.Errorf("changed state not saved: %+v", got)
	}
	s.Close()
}

func TestPeriodicSave(t *testing.T) {
	dir := t.TempDir()
	s := Open(dir, "svc")
	n := 0
	s.Run(func() any { n++; return state{Count: n} }, 10*time.Millisecond)
	time.Sleep(60 * time.Millisecond)
	s.Close()
	var got state
	if ok, _ := Open(dir, "svc").Load(&got); !ok || got.Count == 0 {
		t.Errorf("periodic save: %v %+v", ok, got)
	}
}

func TestUnreadableFileIsSetAside(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "svc.json")
	for _, content := range []string{"not json", `{"version":99,"service":"svc","state":{}}`, `{"version":1,"service":"other","state":{}}`} {
		os.WriteFile(path, []byte(content), 0o644)
		var got state
		if ok, err := Open(dir, "svc").Load(&got); ok || err != nil {
			t.Errorf("%q: loaded=%v err=%v", content, ok, err)
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("%q: file not moved aside", content)
		}
	}
	entries, _ := os.ReadDir(dir)
	aside := 0
	for _, e := range entries {
		if strings.Contains(e.Name(), ".unreadable-") {
			aside++
		}
	}
	if aside == 0 {
		t.Error("no set-aside files kept")
	}
}
