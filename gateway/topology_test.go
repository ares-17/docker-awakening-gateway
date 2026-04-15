package gateway

import (
	"html/template"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHandleTopology_ReturnsHTML(t *testing.T) {
	tmpl, err := template.ParseFS(templatesFS, "templates/*.html")
	if err != nil {
		t.Fatalf("template parse: %v", err)
	}
	s := &Server{
		cfg:         &GatewayConfig{Containers: []ContainerConfig{}, Groups: []GroupConfig{}},
		tmpl:        tmpl,
		rateLimiter: newRateLimiter(time.Second),
		manager:     NewContainerManager(&DockerClient{}),
	}

	req := httptest.NewRequest(http.MethodGet, "/_topology", nil)
	req.RemoteAddr = "127.0.0.1:9999"
	rr := httptest.NewRecorder()

	s.handleTopology(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", ct)
	}
	if !strings.Contains(rr.Body.String(), "sv-node") {
		t.Error("body does not contain SVG node marker 'sv-node'")
	}
}
