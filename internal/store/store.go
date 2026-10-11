// Package store keeps a service's state across restarts. Each service saves
// one JSON document under the data directory: written atomically (temp file
// + rename), at most once per interval and only when it changed, and once
// more on shutdown. On start the service loads it back.
//
// This is a snapshot, not the append-only event log of D4: it answers "keep
// what I was looking at when make serve redeploys" without rewriting every
// service around events. A crash can lose up to one interval of changes.
package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

// Version is the document format. A file with another version is set aside
// and the service starts empty, so a format change never crashes a service.
const Version = 1

// DefaultInterval is how often changed state is saved.
const DefaultInterval = time.Second

// Dir returns the data directory: SN_DATA_DIR, or ./data.
func Dir() string {
	if d := os.Getenv("SN_DATA_DIR"); d != "" {
		return d
	}
	return "data"
}

type document struct {
	Version int             `json:"version"`
	Service string          `json:"service"`
	SavedAt time.Time       `json:"saved_at"`
	State   json.RawMessage `json:"state"`
}

// Store saves one service's state to <dir>/<service>.json.
type Store struct {
	service string
	path    string

	mu       sync.Mutex
	snapshot func() any
	last     []byte // The state as last written, to skip unchanged saves
	stop     chan struct{}
	done     chan struct{}
}

// Open returns the store for a service in dir.
func Open(dir, service string) *Store {
	return &Store{service: service, path: filepath.Join(dir, service+".json")}
}

// Path is the file the store reads and writes.
func (s *Store) Path() string { return s.path }

// Load reads the saved state into v. It reports false when there is nothing
// to load. A file that can't be read as this version is renamed aside (so it
// is kept for inspection) and Load reports false.
func (s *Store) Load(v any) (bool, error) {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var doc document
	if err := json.Unmarshal(data, &doc); err != nil || doc.Version != Version || doc.Service != s.service {
		return false, s.setAside(fmt.Sprintf("unreadable or wrong version (%v)", err))
	}
	if err := json.Unmarshal(doc.State, v); err != nil {
		return false, s.setAside(err.Error())
	}
	s.mu.Lock()
	s.last = doc.State
	s.mu.Unlock()
	return true, nil
}

func (s *Store) setAside(reason string) error {
	aside := fmt.Sprintf("%s.unreadable-%s", s.path, time.Now().UTC().Format("20060102T150405"))
	log.Printf("store: %s: %s; moved to %s and starting empty", s.path, reason, aside)
	return os.Rename(s.path, aside)
}

// Save writes the state now if it changed since the last write.
func (s *Store) Save() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.snapshot == nil {
		return nil
	}
	state, err := json.Marshal(s.snapshot())
	if err != nil {
		return err
	}
	if bytes.Equal(state, s.last) {
		return nil
	}
	doc, err := json.Marshal(document{Version: Version, Service: s.service, SavedAt: time.Now().UTC(), State: state})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), filepath.Base(s.path)+".tmp-*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(doc); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), s.path); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	s.last = state
	return nil
}

// Run saves the state returned by snapshot every interval while it changes,
// until Close. snapshot must return a copy taken under the owner's locks.
func (s *Store) Run(snapshot func() any, interval time.Duration) {
	stop, done := make(chan struct{}), make(chan struct{})
	s.mu.Lock()
	s.snapshot = snapshot
	s.stop, s.done = stop, done
	s.mu.Unlock()
	go func() {
		defer close(done)
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				if err := s.Save(); err != nil {
					log.Printf("store: saving %s: %v", s.path, err)
				}
			}
		}
	}()
}

// Close stops the periodic save and writes a final one.
func (s *Store) Close() error {
	s.mu.Lock()
	stop, done := s.stop, s.done
	s.stop = nil
	s.mu.Unlock()
	if stop != nil {
		close(stop)
		<-done
	}
	return s.Save()
}

// SaveOnExit closes the stores when the process gets SIGINT or SIGTERM
// (make serve stops services with SIGTERM), runs before, then exits.
func SaveOnExit(before func(), stores ...*Store) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-ch
		if before != nil {
			before()
		}
		for _, s := range stores {
			if err := s.Close(); err != nil {
				log.Printf("store: final save of %s: %v", s.path, err)
			}
		}
		log.Printf("saved state on %s", sig)
		os.Exit(0)
	}()
}
