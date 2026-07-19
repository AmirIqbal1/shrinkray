package dashboard

import (
	"embed"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

const maxJSONBody = 64 << 10

//go:embed web/*
var webAssets embed.FS

type Server struct {
	roots    *RootRegistry
	jobs     *JobManager
	stateDir string
	version  string
	handler  http.Handler
}

type createJobRequest struct {
	RootID       string `json:"root_id"`
	Path         string `json:"path"`
	Preset       string `json:"preset"`
	Container    string `json:"container"`
	KeepAllAudio bool   `json:"keep_all_audio"`
	ExactMB      int64  `json:"exact_mb"`
}

func NewServer(roots *RootRegistry, shrinkrayBin, stateDir, version string) (*Server, error) {
	if roots == nil || len(roots.Roots()) == 0 {
		return nil, errors.New("at least one media root is required")
	}
	s := &Server{roots: roots, stateDir: stateDir, version: version}
	s.jobs = NewJobManager(roots, NewCLIRunner(roots, shrinkrayBin))
	s.handler = s.routes()
	return s, nil
}

func (s *Server) Handler() http.Handler { return s.handler }
func (s *Server) Close()                { s.jobs.Close() }

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/health", s.health)
	mux.HandleFunc("/api/files", s.files)
	mux.HandleFunc("/api/probe", s.probe)
	mux.HandleFunc("/api/jobs", s.jobsEndpoint)
	mux.HandleFunc("/api/jobs/", s.jobAction)

	webRoot, err := fs.Sub(webAssets, "web")
	if err != nil {
		panic(err)
	}
	static := http.FileServer(http.FS(webRoot))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if r.URL.Path != "/" && r.URL.Path != "/app.js" && r.URL.Path != "/styles.css" {
			http.NotFound(w, r)
			return
		}
		static.ServeHTTP(w, r)
	})

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'")
		mux.ServeHTTP(w, r)
	})
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "ok", "version": s.version, "roots": s.roots.Summaries(),
	})
}

func (s *Server) requestedRoot(r *http.Request) (*MediaRoot, bool) {
	root, err := s.roots.Get(r.URL.Query().Get("root"))
	return root, err == nil
}

func (s *Server) files(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	root, ok := s.requestedRoot(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "unknown or missing media root")
		return
	}
	list, err := root.Root.List(r.URL.Query().Get("path"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid folder path")
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) probe(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	root, ok := s.requestedRoot(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "unknown or missing media root")
		return
	}
	details, err := root.Root.Probe(r.URL.Query().Get("path"))
	if err != nil {
		message := "could not inspect that movie"
		if errors.Is(err, ErrInvalidPath) {
			message = "invalid movie path"
		}
		writeError(w, http.StatusBadRequest, message)
		return
	}
	details.RootID, details.RootLabel = root.ID, root.Label
	writeJSON(w, http.StatusOK, details)
}

func (s *Server) jobsEndpoint(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{"jobs": s.jobs.List()})
	case http.MethodPost:
		if !strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
			writeError(w, http.StatusUnsupportedMediaType, "content type must be application/json")
			return
		}
		var request createJobRequest
		if err := decodeJSON(w, r, &request); err != nil {
			writeError(w, http.StatusBadRequest, "invalid job request")
			return
		}
		job, err := s.jobs.Submit(request.RootID, request.Path, request.Preset, request.Container, request.KeepAllAudio, request.ExactMB)
		if err != nil {
			status := http.StatusBadRequest
			if strings.Contains(err.Error(), "already exists") || strings.Contains(err.Error(), "already targets") {
				status = http.StatusConflict
			}
			writeError(w, status, err.Error())
			return
		}
		writeJSON(w, http.StatusAccepted, job)
	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *Server) jobAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	remainder := strings.TrimPrefix(r.URL.Path, "/api/jobs/")
	id, action := path.Split(remainder)
	id = strings.TrimSuffix(id, "/")
	if id == "" || action != "cancel" || strings.Contains(id, "/") {
		http.NotFound(w, r)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1)
	body, err := io.ReadAll(r.Body)
	if err != nil || len(body) != 0 {
		writeError(w, http.StatusBadRequest, "cancel request must not have a body")
		return
	}
	if err := s.jobs.Cancel(id); err != nil {
		status := http.StatusConflict
		if err.Error() == "job not found" {
			status = http.StatusNotFound
		}
		writeError(w, status, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "cancelled"})
}

func decodeJSON(w http.ResponseWriter, r *http.Request, target any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxJSONBody)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("request body must contain one JSON object")
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
