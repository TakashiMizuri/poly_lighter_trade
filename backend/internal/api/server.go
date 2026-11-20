package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"poly-lighter.trade/bot/internal/engine"
	"poly-lighter.trade/bot/internal/store"
)

type Server struct {
	Addr       string
	Password   string
	Secure     bool
	CookieName string
	Engine     *engine.Engine
	Store      *store.Store

	mu       sync.Mutex
	sessions map[string]time.Time
	fails    map[string][]time.Time
}

func NewWithCookie(addr, password string, secure bool, cookieName string, eng *engine.Engine, st *store.Store) *Server {
	if cookieName == "" {
		cookieName = "poly_lighter_session"
	}
	return &Server{
		Addr: addr, Password: password, Secure: secure, CookieName: cookieName,
		Engine: eng, Store: st, sessions: map[string]time.Time{}, fails: map[string][]time.Time{},
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", s.health)
	mux.HandleFunc("POST /api/login", s.login)
	mux.HandleFunc("POST /api/logout", s.logout)
	mux.HandleFunc("GET /api/status", s.auth(s.status))
	mux.HandleFunc("GET /api/windows", s.auth(s.windows))
	mux.HandleFunc("GET /api/trades", s.auth(s.trades))
	mux.HandleFunc("GET /api/orders", s.auth(s.orders))
	mux.HandleFunc("GET /api/equity", s.auth(s.equity))
	mux.HandleFunc("GET /api/stats", s.auth(s.stats))
	mux.HandleFunc("GET /api/events", s.auth(s.events))
	mux.HandleFunc("POST /api/kill", s.auth(s.kill))
	mux.HandleFunc("POST /api/resume", s.auth(s.resume))
	return mux
}

func (s *Server) ListenAndServe() error {
	go s.gc()
	srv := &http.Server{Addr: s.Addr, Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second}
	return srv.ListenAndServe()
}

func (s *Server) gc() {
	t := time.NewTicker(30 * time.Minute)
	defer t.Stop()
	for range t.C {
		s.mu.Lock()
		now := time.Now()
		for k, exp := range s.sessions {
			if now.After(exp) {
				delete(s.sessions, k)
			}
		}
		s.mu.Unlock()
	}
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	ready, detail := s.Engine.Health()
	ws, wsErr := s.Engine.WSState()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "ready": ready, "detail": detail, "ws": ws, "ws_error": wsErr})
}

func clientIP(r *http.Request) string {
	if x := r.Header.Get("X-Forwarded-For"); x != "" {
		if i := strings.IndexByte(x, ','); i >= 0 {
			return strings.TrimSpace(x[:i])
		}
		return strings.TrimSpace(x)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (s *Server) loginLocked(ip string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	cut := time.Now().Add(-10 * time.Minute)
	var keep []time.Time
	for _, t := range s.fails[ip] {
		if t.After(cut) {
			keep = append(keep, t)
		}
	}
	s.fails[ip] = keep
	return len(keep) >= 8
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if s.loginLocked(ip) {
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "too many attempts"})
		return
	}
	var body struct {
		Password string `json:"password"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body.Password != s.Password {
		s.mu.Lock()
		s.fails[ip] = append(s.fails[ip], time.Now())
		s.mu.Unlock()
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "bad password"})
		return
	}
	tok := randomToken()
	s.mu.Lock()
	s.sessions[tok] = time.Now().Add(14 * 24 * time.Hour)
	s.mu.Unlock()
	http.SetCookie(w, &http.Cookie{
		Name: s.CookieName, Value: tok, Path: "/", HttpOnly: true,
		SameSite: http.SameSiteLaxMode, Secure: s.Secure, MaxAge: 14 * 24 * 3600,
	})
	writeJSON(w, http.StatusOK, map[string]string{"ok": "1"})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(s.CookieName); err == nil {
		s.mu.Lock()
		delete(s.sessions, c.Value)
		s.mu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: s.CookieName, Value: "", Path: "/", MaxAge: -1})
	writeJSON(w, http.StatusOK, map[string]string{"ok": "1"})
}

func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(s.CookieName)
		if err != nil {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "auth"})
			return
		}
		s.mu.Lock()
		exp, ok := s.sessions[c.Value]
		s.mu.Unlock()
		if !ok || time.Now().After(exp) {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "auth"})
			return
		}
		next(w, r)
	}
}

func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.Engine.Snapshot(r.Context()))
}

func (s *Server) windows(w http.ResponseWriter, r *http.Request) {
	rows, err := s.Store.ListWindows(r.Context(), 300)
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, rows)
}

func (s *Server) trades(w http.ResponseWriter, r *http.Request) {
	rows, err := s.Store.ListWindows(r.Context(), 400)
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	var out []store.Window
	for _, x := range rows {
		if x.Side == "long" || x.Side == "short" {
			out = append(out, x)
		}
	}
	writeJSON(w, 200, out)
}

func (s *Server) orders(w http.ResponseWriter, r *http.Request) {
	rows, err := s.Store.ListOrders(r.Context(), 200)
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, rows)
}

func (s *Server) equity(w http.ResponseWriter, r *http.Request) {
	rows, err := s.Store.ListCurve(r.Context(), 4000)
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, rows)
}

func (s *Server) stats(w http.ResponseWriter, r *http.Request) {
	st, err := s.Store.ComputeStats(r.Context())
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, st)
}

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	rows, err := s.Store.ListEvents(r.Context(), 80)
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, rows)
}

func (s *Server) kill(w http.ResponseWriter, r *http.Request) {
	if err := s.Engine.KillAll(r.Context()); err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]string{"ok": "1"})
}

func (s *Server) resume(w http.ResponseWriter, r *http.Request) {
	s.Engine.Resume(r.Context())
	writeJSON(w, 200, map[string]string{"ok": "1"})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func randomToken() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
