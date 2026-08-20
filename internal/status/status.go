package status

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"sync"
	"time"
)

//go:embed status.html
var page embed.FS

type State struct {
	mu        sync.RWMutex
	Name      string
	Protocol  string
	Relay     string
	Code      string
	Local     string
	Connected bool
	Started   time.Time
	Requests  uint64
}

type View struct {
	Name      string
	Protocol  string
	Relay     string
	Code      string
	Local     string
	Connected bool
	Started   time.Time
	Requests  uint64
}

func (s *State) Snapshot() View {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return View{
		Name: s.Name, Protocol: s.Protocol, Relay: s.Relay, Code: s.Code,
		Local: s.Local, Connected: s.Connected, Started: s.Started, Requests: s.Requests,
	}
}

func (s *State) SetConnected(connected bool) {
	s.mu.Lock()
	s.Connected = connected
	s.mu.Unlock()
}

func (s *State) AddRequest() {
	s.mu.Lock()
	s.Requests++
	s.mu.Unlock()
}

// Serve starts the built-in status server on a loopback address. The returned
// address is intended to be used as piktakd's local target.
func Serve(ctx context.Context, state *State) (string, error) {
	return ServeAddr(ctx, state, "127.0.0.1:0")
}

// ServeAddr starts the built-in status server on addr. Use a loopback address
// to keep the status page local to the machine.
func ServeAddr(ctx context.Context, state *State, addr string) (string, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return "", fmt.Errorf("status listen: %w", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/status", func(w http.ResponseWriter, r *http.Request) {
		state.AddRequest()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(state.Snapshot())
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		state.AddRequest()
		data := state.Snapshot()
		if err := template.Must(template.ParseFS(page, "status.html")).Execute(w, data); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	})

	server := &http.Server{Handler: mux}
	go func() {
		<-ctx.Done()
		_ = server.Shutdown(context.Background())
	}()
	go func() { _ = server.Serve(ln) }()
	return ln.Addr().String(), nil
}
