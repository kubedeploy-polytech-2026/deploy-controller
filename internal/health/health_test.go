package health

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestReadinessFollowsPollResults(t *testing.T) {
	status := &Status{}
	handler := status.Handler()
	check := func(path string, want int) {
		t.Helper()
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != want {
			t.Fatalf("%s: status %d, want %d", path, response.Code, want)
		}
		if strings.Contains(response.Body.String(), "private-error") {
			t.Fatal("probe exposed poll error")
		}
	}
	check("/readyz", http.StatusServiceUnavailable)
	check("/healthz", http.StatusOK)
	status.Record(nil)
	check("/readyz", http.StatusOK)
	status.Record(errors.New("private-error"))
	check("/readyz", http.StatusServiceUnavailable)
	check("/healthz", http.StatusOK)
	status.Record(nil)
	check("/readyz", http.StatusOK)
}

func TestProbeRoutes(t *testing.T) {
	handler := (&Status{}).Handler()
	for _, tc := range []struct {
		method string
		path   string
		code   int
	}{
		{http.MethodHead, "/healthz", http.StatusOK},
		{http.MethodPost, "/healthz", http.StatusMethodNotAllowed},
		{http.MethodPost, "/readyz", http.StatusMethodNotAllowed},
		{http.MethodGet, "/unknown", http.StatusNotFound},
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(tc.method, tc.path, nil))
		if response.Code != tc.code {
			t.Errorf("%s %s: status %d, want %d", tc.method, tc.path, response.Code, tc.code)
		}
	}
}

func TestConcurrentPollAndProbe(t *testing.T) {
	status := &Status{}
	handler := status.Handler()
	var workers sync.WaitGroup
	for range 4 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for i := range 100 {
				if i%2 == 0 {
					status.Record(nil)
				} else {
					status.Record(errors.New("poll unavailable"))
				}
				handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/readyz", nil))
			}
		}()
	}
	workers.Wait()
}
