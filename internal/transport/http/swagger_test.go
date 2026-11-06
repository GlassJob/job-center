package httpapi

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/GlassJob/job-center/internal/config"
	"gopkg.in/yaml.v3"
)

func TestSwaggerAndOpenAPIDefaultEnabled(t *testing.T) {
	cfg := config.Default()
	srv := New(nil, nil, nil, cfg, nil, "Asia/Shanghai")
	mux := http.NewServeMux()
	srv.MountAdmin(mux)

	// 1. /openapi.yaml
	req := httptest.NewRequest(http.MethodGet, "/openapi.yaml", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /openapi.yaml status=%d", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("CORS header=%q, want *", got)
	}
	var spec struct {
		OpenAPI string                 `yaml:"openapi"`
		Paths   map[string]interface{} `yaml:"paths"`
	}
	if err := yaml.Unmarshal(rec.Body.Bytes(), &spec); err != nil {
		t.Fatalf("unmarshal openapi.yaml: %v", err)
	}
	if spec.OpenAPI != "3.1.0" || len(spec.Paths) < 30 {
		t.Fatalf("unexpected spec: openapi=%s paths=%d", spec.OpenAPI, len(spec.Paths))
	}
	for _, must := range []string{"/v1/admin/audits", "/metrics", "/openapi.yaml", "/swagger/", "/docs", "/v1/trigger/{service_code}/{task_code}", "/v1/trigger/executions/{execution_id}", "/v1/trigger/executions/{execution_id}/logs", "/v1/trigger/executions/{execution_id}/sse-ticket", "/v1/trigger/executions/{execution_id}/events", "/v1/trigger/executions/{execution_id}/cancel"} {
		if _, ok := spec.Paths[must]; !ok {
			t.Errorf("missing path %s in embedded openapi.yaml", must)
		}
	}

	// 2. /docs -> 302 /swagger/
	req = httptest.NewRequest(http.MethodGet, "/docs?spec=/custom.yaml", nil)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/swagger/?spec=/custom.yaml" {
		t.Fatalf("GET /docs status=%d loc=%q", rec.Code, rec.Header().Get("Location"))
	}

	// 3. /swagger/ offline HTML & no external CDN or hardcoded secrets
	req = httptest.NewRequest(http.MethodGet, "/swagger/", nil)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /swagger/ status=%d", rec.Code)
	}
	html := rec.Body.String()
	if !strings.Contains(html, "requestInterceptor") || !strings.Contains(html, "SwaggerUIBundle") {
		t.Fatal("swagger index missing SwaggerUIBundle or requestInterceptor")
	}
	extRef := regexp.MustCompile(`(?i)(src|href)\s*=\s*["']https?://`)
	if m := extRef.FindString(html); m != "" {
		t.Fatalf("swagger index contains external CDN URL: %s", m)
	}
	if strings.Contains(html, "dev-admin-secret") {
		t.Fatal("swagger index must not hardcode secrets")
	}

	// 4. Static JS bundle (plain + gzip)
	req = httptest.NewRequest(http.MethodGet, "/swagger/swagger-ui-bundle.js", nil)
	req.Header.Set("Accept-Encoding", "")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "SwaggerUIBundle") {
		t.Fatalf("plain bundle failed: status=%d len=%d", rec.Code, rec.Body.Len())
	}

	req = httptest.NewRequest(http.MethodGet, "/swagger/swagger-ui-bundle.js", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("gzip bundle failed: status=%d enc=%q", rec.Code, rec.Header().Get("Content-Encoding"))
	}
	gr, err := gzip.NewReader(rec.Body)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(gr)
	_ = gr.Close()
	if err != nil || !strings.Contains(string(raw), "SwaggerUIBundle") {
		t.Fatalf("gunzip bundle invalid: err=%v len=%d", err, len(raw))
	}

	// 5. CSS
	req = httptest.NewRequest(http.MethodGet, "/swagger/swagger-ui.css", nil)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), ".swagger-ui") {
		t.Fatalf("css failed: status=%d", rec.Code)
	}

	// 6. Executor port must not expose docs
	execMux := http.NewServeMux()
	srv.MountExecutor(execMux)
	for _, p := range []string{"/openapi.yaml", "/swagger/", "/docs"} {
		r2 := httptest.NewRecorder()
		execMux.ServeHTTP(r2, httptest.NewRequest(http.MethodGet, p, nil))
		if r2.Code != http.StatusNotFound {
			t.Errorf("Executor mux exposed %s with status %d", p, r2.Code)
		}
	}
}

func TestSwaggerDisabledByConfig(t *testing.T) {
	cfg := config.Default()
	off := false
	cfg.HTTP.DocsEnabled = &off
	srv := New(nil, nil, nil, cfg, nil, "Asia/Shanghai")
	mux := http.NewServeMux()
	srv.MountAdmin(mux)

	for _, p := range []string{"/openapi.yaml", "/swagger/", "/swagger/swagger-ui-bundle.js", "/docs"} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("docs_enabled=false: GET %s status=%d, want 404", p, rec.Code)
		}
	}
}
