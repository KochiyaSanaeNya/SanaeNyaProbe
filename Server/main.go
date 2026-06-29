package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	defaultListen      = ":8080"
	offlineAfter       = 15 * time.Second
	maxReceiveBodySize = 1 << 20
)

type serverState struct {
	Key              string              `json:"key"`
	UUID             string              `json:"uuid"`
	Name             string              `json:"name"`
	Status           string              `json:"status"`
	Online           bool                `json:"online"`
	LastSeen         time.Time           `json:"last_seen"`
	LastSeenUnix     int64               `json:"last_seen_unix"`
	OfflineSince     *time.Time          `json:"offline_since,omitempty"`
	OfflineSinceUnix int64               `json:"offline_since_unix,omitempty"`
	Metrics          map[string][]string `json:"metrics"`
}

type monitorResponse struct {
	Now                 time.Time     `json:"now"`
	NowUnix             int64         `json:"now_unix"`
	OfflineAfterSeconds int           `json:"offline_after_seconds"`
	Servers             []serverState `json:"servers"`
}

type store struct {
	mu       sync.RWMutex
	servers  map[string]serverState
	timers   map[string]*time.Timer
	versions map[string]uint64
}

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)

	listen := flag.String("listen", defaultListen, "HTTP listen address")
	flag.Parse()

	state := newStore()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/recv", state.recv)
	mux.HandleFunc("/api/monitor", state.monitor)

	server := &http.Server{
		Addr:              *listen,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		log.Printf("SanaeNyaProbe server listening on %s", *listen)
		errCh <- server.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			log.Printf("shutdown server: %v", err)
		}
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("serve: %v", err)
		}
	}

	state.stopTimers()
	log.Print("SanaeNyaProbe server stopped")
}

func newStore() *store {
	return &store{
		servers:  make(map[string]serverState),
		timers:   make(map[string]*time.Timer),
		versions: make(map[string]uint64),
	}
}

func (s *store) recv(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxReceiveBodySize)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form data", http.StatusBadRequest)
		return
	}

	uuid := strings.TrimSpace(r.PostForm.Get("uuid"))
	name := strings.TrimSpace(r.PostForm.Get("name"))
	if uuid == "" {
		http.Error(w, "uuid is required", http.StatusBadRequest)
		return
	}
	if name == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}

	now := time.Now().UTC()
	s.update(uuid, name, copyValues(r.PostForm), now)
	w.WriteHeader(http.StatusNoContent)
}

func (s *store) monitor(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}

	now := time.Now().UTC()
	response := monitorResponse{
		Now:                 now,
		NowUnix:             now.Unix(),
		OfflineAfterSeconds: int(offlineAfter.Seconds()),
		Servers:             s.snapshot(now),
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := json.NewEncoder(w).Encode(response); err != nil {
		log.Printf("encode monitor response: %v", err)
	}
}

func (s *store) update(uuid, name string, metrics map[string][]string, now time.Time) {
	key := stateKey(uuid)

	s.mu.Lock()
	defer s.mu.Unlock()

	version := s.versions[key] + 1
	s.versions[key] = version
	if timer := s.timers[key]; timer != nil {
		timer.Stop()
	}

	s.servers[key] = serverState{
		Key:          key,
		UUID:         uuid,
		Name:         name,
		Status:       "online",
		Online:       true,
		LastSeen:     now,
		LastSeenUnix: now.Unix(),
		Metrics:      metrics,
	}
	s.timers[key] = time.AfterFunc(offlineAfter, func() {
		s.markOffline(key, version)
	})
}

func (s *store) markOffline(key string, version uint64) {
	now := time.Now().UTC()

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.versions[key] != version {
		return
	}
	state, ok := s.servers[key]
	if !ok {
		return
	}
	if now.Sub(state.LastSeen) < offlineAfter {
		return
	}

	state.Online = false
	state.Status = "offline"
	state.OfflineSince = &now
	state.OfflineSinceUnix = now.Unix()
	s.servers[key] = state
	delete(s.timers, key)
}

func (s *store) snapshot(now time.Time) []serverState {
	s.mu.RLock()
	states := make([]serverState, 0, len(s.servers))
	for _, state := range s.servers {
		state.Metrics = copyValues(state.Metrics)
		if now.Sub(state.LastSeen) >= offlineAfter {
			state.Online = false
			state.Status = "offline"
			if state.OfflineSince == nil {
				offlineSince := state.LastSeen.Add(offlineAfter)
				state.OfflineSince = &offlineSince
				state.OfflineSinceUnix = offlineSince.Unix()
			}
		}
		states = append(states, state)
	}
	s.mu.RUnlock()

	sort.Slice(states, func(i, j int) bool {
		if states[i].Name != states[j].Name {
			return states[i].Name < states[j].Name
		}
		return states[i].UUID < states[j].UUID
	})
	return states
}

func (s *store) stopTimers() {
	s.mu.Lock()
	defer s.mu.Unlock()

	for key, timer := range s.timers {
		timer.Stop()
		delete(s.timers, key)
	}
}

func stateKey(uuid string) string {
	return "uuid:" + uuid
}

func copyValues(values url.Values) map[string][]string {
	copy := make(map[string][]string, len(values))
	for key, list := range values {
		copy[key] = append([]string(nil), list...)
	}
	return copy
}

func methodNotAllowed(w http.ResponseWriter, method string) {
	w.Header().Set("Allow", method)
	http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
}
