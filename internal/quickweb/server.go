package quickweb

import (
	"database/sql"
	"encoding/json"
	"net/http"
)

// Server is a Quickweb HTTP server.
type Server struct {
	cfg           Config
	root          *osRoot
	db            *sql.DB
	candidateURLs []string
	migrationOK   bool
}

// NewServer constructs a Quickweb server.
func NewServer(cfg Config, root *osRoot, db *sql.DB, candidateURLs []string, migrationOK bool) *Server {
	return &Server{cfg: cfg, root: root, db: db, candidateURLs: candidateURLs, migrationOK: migrationOK}
}

// Handler returns the HTTP handler for this server.
func (s *Server) Handler() http.Handler { return http.HandlerFunc(s.serveHTTP) }

func (s *Server) serveHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/healthz":
		s.handleHealth(w, r)
	case "/skills":
		s.handleSkills(w, r)
	case "/data":
		s.handleData(w, r)
	default:
		s.handleStatic(w, r)
	}
}

type healthResponse struct {
	OK            bool     `json:"ok"`
	ServiceName   string   `json:"service_name,omitempty"`
	ContentRoot   string   `json:"content_root"`
	DBPath        string   `json:"db_path"`
	Addr          string   `json:"addr"`
	BaseURL       string   `json:"base_url,omitempty"`
	CandidateURLs []string `json:"candidate_urls"`
	MigrationOK   bool     `json:"migration_ok"`
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/healthz" {
		http.NotFound(w, r)
		return
	}

	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		methodNotAllowed(w, http.MethodGet, http.MethodHead)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if r.Method == http.MethodHead {
		return
	}

	_ = json.NewEncoder(w).Encode(healthResponse{
		OK:            true,
		ServiceName:   s.cfg.ServiceName,
		ContentRoot:   s.cfg.ContentRoot,
		DBPath:        s.cfg.DBPath,
		Addr:          s.cfg.Addr,
		BaseURL:       s.cfg.BaseURL,
		CandidateURLs: s.candidateURLs,
		MigrationOK:   s.migrationOK,
	})
}

func methodNotAllowed(w http.ResponseWriter, allowed ...string) {
	for i, method := range allowed {
		if i == 0 {
			w.Header().Set("Allow", method)
			continue
		}

		w.Header().Set("Allow", w.Header().Get("Allow")+", "+method)
	}

	http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
}
