package dashboard

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func makeTwoRootServer(t *testing.T) (*Server, string, string) {
	t.Helper()
	movies, tv := makeRootDirectories(t)
	registry, err := NewRootRegistry([]string{"Movies=" + movies, "TV=" + tv})
	if err != nil {
		t.Fatal(err)
	}
	server, err := NewServer(registry, "unused-shrinkray", t.TempDir(), "test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(server.Close)
	return server, movies, tv
}

func requestServer(t *testing.T, server *Server, method, target string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, target, bytes.NewReader(body))
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	return response
}

func TestFilesAreListedOnlyUnderSelectedRoot(t *testing.T) {
	server, movies, tv := makeTwoRootServer(t)
	if err := os.Mkdir(filepath.Join(movies, "Action"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(movies, "Action", "film.mkv"))
	writeTestFile(t, filepath.Join(tv, "show.mkv"))

	moviesResponse := requestServer(t, server, http.MethodGet, "/api/files?root=movies&path=Action", nil)
	if moviesResponse.Code != http.StatusOK || !strings.Contains(moviesResponse.Body.String(), "film.mkv") {
		t.Fatalf("Movies listing status/body = %d, %s", moviesResponse.Code, moviesResponse.Body.String())
	}

	wrongRootResponse := requestServer(t, server, http.MethodGet, "/api/files?root=tv&path=Action", nil)
	if wrongRootResponse.Code != http.StatusBadRequest || strings.Contains(wrongRootResponse.Body.String(), "film.mkv") {
		t.Fatalf("TV root accepted Movies path: %d, %s", wrongRootResponse.Code, wrongRootResponse.Body.String())
	}
}

func TestFilesRejectUnknownRootID(t *testing.T) {
	server, _, _ := makeTwoRootServer(t)
	response := requestServer(t, server, http.MethodGet, "/api/files?root=unknown&path=", nil)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("unknown root status = %d; want %d", response.Code, http.StatusBadRequest)
	}
}

func TestHealthAndJobResponsesHideAbsoluteRoots(t *testing.T) {
	server, movies, tv := makeTwoRootServer(t)
	writeTestFile(t, filepath.Join(movies, "movie.mkv"))

	health := requestServer(t, server, http.MethodGet, "/api/health", nil)
	if health.Code != http.StatusOK || !strings.Contains(health.Body.String(), `"roots":[{"id":"movies","label":"Movies"},{"id":"tv","label":"TV"}]`) {
		t.Fatalf("health response = %d, %s", health.Code, health.Body.String())
	}
	for _, absolute := range []string{movies, tv} {
		if strings.Contains(health.Body.String(), absolute) {
			t.Fatalf("health response exposed absolute root %q: %s", absolute, health.Body.String())
		}
	}

	requestBody, err := json.Marshal(createJobRequest{RootID: "movies", Path: "movie.mkv", Preset: "balanced", Container: "mkv"})
	if err != nil {
		t.Fatal(err)
	}
	job := requestServer(t, server, http.MethodPost, "/api/jobs", requestBody)
	if job.Code != http.StatusAccepted {
		t.Fatalf("job response = %d, %s", job.Code, job.Body.String())
	}
	for _, absolute := range []string{movies, tv} {
		if strings.Contains(job.Body.String(), absolute) {
			t.Fatalf("job response exposed absolute root %q: %s", absolute, job.Body.String())
		}
	}
}

func TestJobSubmissionRequiresRootID(t *testing.T) {
	server, movies, _ := makeTwoRootServer(t)
	writeTestFile(t, filepath.Join(movies, "movie.mkv"))
	body := []byte(`{"path":"movie.mkv","preset":"balanced","container":"mkv"}`)
	response := requestServer(t, server, http.MethodPost, "/api/jobs", body)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("missing root ID status = %d; want %d", response.Code, http.StatusBadRequest)
	}
}
