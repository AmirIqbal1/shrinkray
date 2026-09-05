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
	"time"
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

func TestDashboardServesLogScrollHelpersBeforeApplication(t *testing.T) {
	server, _, _ := makeTwoRootServer(t)
	index := requestServer(t, server, http.MethodGet, "/", nil)
	if index.Code != http.StatusOK {
		t.Fatalf("dashboard index status = %d; want %d", index.Code, http.StatusOK)
	}
	logScrollIndex := strings.Index(index.Body.String(), `src="/log-scroll.js"`)
	encoderOptionsIndex := strings.Index(index.Body.String(), `src="/encoder-options.js"`)
	appIndex := strings.Index(index.Body.String(), `src="/app.js"`)
	if logScrollIndex < 0 || encoderOptionsIndex < 0 || appIndex < 0 || logScrollIndex > encoderOptionsIndex || encoderOptionsIndex > appIndex {
		t.Fatalf("dashboard scripts are missing or out of order: %s", index.Body.String())
	}

	helpers := requestServer(t, server, http.MethodGet, "/log-scroll.js", nil)
	if helpers.Code != http.StatusOK || !strings.Contains(helpers.Body.String(), "captureLogScroll") {
		t.Fatalf("log scroll helper response = %d, %s", helpers.Code, helpers.Body.String())
	}
	encoderOptions := requestServer(t, server, http.MethodGet, "/encoder-options.js", nil)
	if encoderOptions.Code != http.StatusOK || !strings.Contains(encoderOptions.Body.String(), "buildEncoderOptions") {
		t.Fatalf("encoder options helper response = %d, %s", encoderOptions.Code, encoderOptions.Body.String())
	}
}

func TestDashboardReplacementOptionDefaultsUnchecked(t *testing.T) {
	server, _, _ := makeTwoRootServer(t)
	index := requestServer(t, server, http.MethodGet, "/", nil)
	if index.Code != http.StatusOK {
		t.Fatalf("dashboard index status = %d; want %d", index.Code, http.StatusOK)
	}
	html := index.Body.String()
	if !strings.Contains(html, `id="replace-original" type="checkbox"`) || strings.Contains(html, `id="replace-original" type="checkbox" checked`) {
		t.Fatalf("replacement option is missing or pre-checked: %s", html)
	}
	if !strings.Contains(html, "The source is kept until the new file has completed and passed validation.") {
		t.Fatal("replacement safety helper text is missing")
	}
}

func TestReplacementFieldIsAdditiveToJobAPI(t *testing.T) {
	server, movies, _ := makeTwoRootServer(t)
	writeTestFile(t, filepath.Join(movies, "movie.mkv"))
	body := []byte(`{"root_id":"movies","path":"movie.mkv","preset":"balanced","container":"mkv","replace_original":true}`)
	response := requestServer(t, server, http.MethodPost, "/api/jobs", body)
	if response.Code != http.StatusAccepted || !strings.Contains(response.Body.String(), `"replace_original":true`) || !strings.Contains(response.Body.String(), `"output_path":"movie.mkv"`) {
		t.Fatalf("replacement API response = %d, %s", response.Code, response.Body.String())
	}
}

func TestClearHistoryEndpointAndDashboardControls(t *testing.T) {
	movies, _ := makeRootDirectories(t)
	stateDir := t.TempDir()
	finished := time.Now().UTC()
	store := newJobStateStore(stateDir)
	if err := store.Save(jobStateSnapshot{revision: 1, state: persistedJobState{
		Version: jobStateVersion,
		NextID:  8,
		Jobs: []*Job{{
			ID: "7", RootID: "movies", RootLabel: "Movies", Path: "old.mkv", Filename: "old.mkv",
			State: StateFailed, Stage: "Failed", QueuedAt: finished, FinishedAt: &finished, Logs: []string{},
		}},
	}}); err != nil {
		t.Fatal(err)
	}
	registry, err := NewRootRegistry([]string{"Movies=" + movies})
	if err != nil {
		t.Fatal(err)
	}
	server, err := NewServer(registry, "unused-shrinkray", stateDir, "test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(server.Close)

	response := requestServer(t, server, http.MethodDelete, "/api/jobs/history", nil)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"removed":1`) {
		t.Fatalf("clear history response = %d, %s", response.Code, response.Body.String())
	}
	jobs := requestServer(t, server, http.MethodGet, "/api/jobs", nil)
	if jobs.Code != http.StatusOK || strings.Contains(jobs.Body.String(), `"id":"7"`) {
		t.Fatalf("cleared history remained in API: %d, %s", jobs.Code, jobs.Body.String())
	}
	wrongMethod := requestServer(t, server, http.MethodPost, "/api/jobs/history", nil)
	if wrongMethod.Code != http.StatusMethodNotAllowed {
		t.Fatalf("clear history POST status = %d; want %d", wrongMethod.Code, http.StatusMethodNotAllowed)
	}

	index := requestServer(t, server, http.MethodGet, "/", nil)
	if !strings.Contains(index.Body.String(), "Active / queued jobs") || !strings.Contains(index.Body.String(), "Clear history") {
		t.Fatalf("dashboard does not distinguish active jobs and history: %s", index.Body.String())
	}
	application := requestServer(t, server, http.MethodGet, "/app.js", nil)
	if !strings.Contains(application.Body.String(), "Clear completed, failed and cancelled job history?") {
		t.Fatal("dashboard clear-history confirmation is missing")
	}
}

func TestCapabilitiesEndpointDoesNotExposeDeviceDetails(t *testing.T) {
	server, _, _ := makeTwoRootServer(t)
	server.capabilities = EncoderCapabilities{Encoders: EncoderAvailability{Software: true, QSV: true}, AutoSelected: "qsv"}
	response := requestServer(t, server, http.MethodGet, "/api/capabilities", nil)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"qsv":true`) || !strings.Contains(response.Body.String(), `"auto_selected":"qsv"`) {
		t.Fatalf("capabilities response = %d, %s", response.Code, response.Body.String())
	}
	for _, forbidden := range []string{"renderD", "hostname", "serial"} {
		if strings.Contains(response.Body.String(), forbidden) {
			t.Fatalf("capabilities response exposed %q: %s", forbidden, response.Body.String())
		}
	}
}
