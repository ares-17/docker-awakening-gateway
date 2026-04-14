package gateway

import (
	"html/template"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// ─── mermaidID ────────────────────────────────────────────────────────────────

func TestMermaidID(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{"hyphen", "my-app", "my_app"},
		{"dot", "app.v2", "app_v2"},
		{"combined", "my-app.v2", "my_app_v2"},
		{"clean", "myapp", "myapp"},
		{"underscore", "my_app", "my_app"},
		{"multi-hyphen", "a-b-c", "a_b_c"},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := mermaidID(tt.in); got != tt.want {
				t.Errorf("mermaidID(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// ─── dockerStatusToClass ─────────────────────────────────────────────────────

func TestDockerStatusToClass(t *testing.T) {
	tests := []struct{ in, want string }{
		{"running", "running"},
		{"exited", "stopped"},
		{"created", "stopped"},
		{"paused", "stopped"},
		{"unknown", "stopped"},
		{"", "stopped"},
		{"restarting", "starting"},
		{"dead", "failed"},
		{"removing", "failed"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := dockerStatusToClass(tt.in); got != tt.want {
				t.Errorf("dockerStatusToClass(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// ─── buildMermaidGraph ────────────────────────────────────────────────────────

func TestBuildMermaidGraph_Empty(t *testing.T) {
	out := buildMermaidGraph(nil, nil, nil)
	if !strings.HasPrefix(out, "graph LR") {
		t.Errorf("expected 'graph LR' prefix, got: %q", out)
	}
}

func TestBuildMermaidGraph_ClassDefinitions(t *testing.T) {
	out := buildMermaidGraph(nil, nil, nil)
	for _, cls := range []string{"classDef running", "classDef stopped", "classDef starting", "classDef failed"} {
		if !strings.Contains(out, cls) {
			t.Errorf("expected %q in output:\n%s", cls, out)
		}
	}
}

func TestBuildMermaidGraph_NodeLabel(t *testing.T) {
	out := buildMermaidGraph([]ContainerConfig{{Name: "my-app"}}, nil, nil)
	if !strings.Contains(out, `my_app["my-app"]`) {
		t.Errorf("expected node `my_app[\"my-app\"]`, got:\n%s", out)
	}
}

func TestBuildMermaidGraph_ClassApplied(t *testing.T) {
	containers := []ContainerConfig{{Name: "web"}, {Name: "db"}}
	statusMap := map[string]string{"web": "running", "db": "exited"}
	out := buildMermaidGraph(containers, nil, statusMap)
	if !strings.Contains(out, ":::running") {
		t.Errorf("expected :::running in output:\n%s", out)
	}
	if !strings.Contains(out, ":::stopped") {
		t.Errorf("expected :::stopped in output:\n%s", out)
	}
}

func TestBuildMermaidGraph_Edge(t *testing.T) {
	containers := []ContainerConfig{
		{Name: "app", DependsOn: []string{"db"}},
		{Name: "db"},
	}
	out := buildMermaidGraph(containers, nil, nil)
	if !strings.Contains(out, "app --> db") {
		t.Errorf("expected edge 'app --> db':\n%s", out)
	}
}

func TestBuildMermaidGraph_HyphenInEdge(t *testing.T) {
	containers := []ContainerConfig{
		{Name: "my-app", DependsOn: []string{"my-db"}},
		{Name: "my-db"},
	}
	out := buildMermaidGraph(containers, nil, nil)
	if !strings.Contains(out, "my_app --> my_db") {
		t.Errorf("expected edge 'my_app --> my_db':\n%s", out)
	}
}

func TestBuildMermaidGraph_Subgraph(t *testing.T) {
	containers := []ContainerConfig{{Name: "app1"}, {Name: "app2"}}
	groups := []GroupConfig{{Name: "backend", Containers: []string{"app1", "app2"}}}
	out := buildMermaidGraph(containers, groups, nil)
	if !strings.Contains(out, `subgraph grp_backend["backend"]`) {
		t.Errorf("expected subgraph declaration:\n%s", out)
	}
	start := strings.Index(out, "subgraph grp_backend")
	block := out[start : start+strings.Index(out[start:], "end")]
	if !strings.Contains(block, "app1") || !strings.Contains(block, "app2") {
		t.Errorf("expected members inside subgraph block:\n%s", block)
	}
}

func TestBuildMermaidGraph_ClickDirectives(t *testing.T) {
	containers := []ContainerConfig{{Name: "web-app"}, {Name: "redis"}}
	out := buildMermaidGraph(containers, nil, nil)
	if !strings.Contains(out, "click web_app onNodeClick") {
		t.Errorf("expected click directive for web_app:\n%s", out)
	}
	if !strings.Contains(out, "click redis onNodeClick") {
		t.Errorf("expected click directive for redis:\n%s", out)
	}
}

// ─── handleTopology smoke test ────────────────────────────────────────────────

func TestHandleTopology_ReturnsHTML(t *testing.T) {
	tmpl, err := template.ParseFS(templatesFS, "templates/*.html")
	if err != nil {
		t.Fatalf("template parse: %v", err)
	}
	s := &Server{
		cfg:         &GatewayConfig{Containers: []ContainerConfig{}, Groups: []GroupConfig{}},
		tmpl:        tmpl,
		rateLimiter: newRateLimiter(time.Second),
		manager:     NewContainerManager(&DockerClient{}), // 0 containers → InspectContainer never called
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
	if !strings.Contains(rr.Body.String(), "graph LR") {
		t.Error("body does not contain 'graph LR'")
	}
}
