package quickweb

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

func openDatabase(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}

	return db, nil
}

func migrateDatabase(db *sql.DB) error {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS page_state (
  namespace TEXT PRIMARY KEY,
  document_json TEXT NOT NULL,
  created_utc TEXT NOT NULL,
  updated_utc TEXT NOT NULL
);`)
	if err != nil {
		return fmt.Errorf("migrate database: %w", err)
	}

	return nil
}

func (s *Server) handleData(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/data" {
		http.NotFound(w, r)
		return
	}

	switch r.Method {
	case http.MethodGet:
		s.handleDataGet(w, r)
	case http.MethodPut, http.MethodPost:
		s.handleDataWrite(w, r)
	default:
		methodNotAllowed(w, http.MethodGet, http.MethodPut, http.MethodPost)
	}
}

func (s *Server) handleDataGet(w http.ResponseWriter, r *http.Request) {
	namespace, err := s.dataNamespace(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	document, err := loadDocument(s.db, namespace)
	if err != nil {
		http.Error(w, "load document", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, document)
}

func (s *Server) handleDataWrite(w http.ResponseWriter, r *http.Request) {
	if !isJSONContentType(r.Header.Get("Content-Type")) {
		http.Error(w, "Content-Type must be application/json or another JSON media type", http.StatusUnsupportedMediaType)
		return
	}

	namespace, err := s.dataNamespace(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	body, err := readLimitedBody(r.Body, hardMaxJSONBytes)
	if err != nil {
		if errors.Is(err, errBodyTooLarge) {
			http.Error(w, "JSON document exceeds 10 MiB limit", http.StatusRequestEntityTooLarge)
			return
		}

		http.Error(w, "read request body", http.StatusBadRequest)
		return
	}

	if !json.Valid(body) {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	document := string(body)
	if err := storeDocument(s.db, namespace, document); err != nil {
		http.Error(w, "store document", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, document)
}

func (s *Server) dataNamespace(r *http.Request) (string, error) {
	value, ok := r.URL.Query()["path"]
	if !ok || len(value) == 0 {
		return "", errors.New("missing path query parameter")
	}

	return s.namespaceForPath(value[0])
}

func loadDocument(db *sql.DB, namespace string) (string, error) {
	var document string
	err := db.QueryRow(`SELECT document_json FROM page_state WHERE namespace = ?`, namespace).Scan(&document)
	if errors.Is(err, sql.ErrNoRows) {
		return "{}", nil
	}
	if err != nil {
		return "", err
	}

	return document, nil
}

func storeDocument(db *sql.DB, namespace, document string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := db.Exec(`INSERT INTO page_state (namespace, document_json, created_utc, updated_utc)
VALUES (?, ?, ?, ?)
ON CONFLICT(namespace) DO UPDATE SET
  document_json = excluded.document_json,
  updated_utc = excluded.updated_utc`, namespace, document, now, now)

	return err
}

func isJSONContentType(contentType string) bool {
	if strings.TrimSpace(contentType) == "" {
		return false
	}

	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return false
	}

	return mediaType == "application/json" || strings.HasSuffix(mediaType, "+json")
}

var errBodyTooLarge = errors.New("body too large")

func readLimitedBody(body io.Reader, limit int64) ([]byte, error) {
	limited := io.LimitReader(body, limit+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}

	if int64(len(data)) > limit {
		return nil, errBodyTooLarge
	}

	return data, nil
}
