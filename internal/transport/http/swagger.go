package httpapi

import (
	"bytes"
	"compress/gzip"
	"embed"
	"io"
	"net/http"
	"os"
	"strings"
)

//go:embed swagger/*
var swaggerFS embed.FS

func (s *Server) docsEnabled() bool {
	if s.cfg == nil {
		return true
	}
	return s.cfg.IsDocsEnabled()
}

func (s *Server) mountDocs(mux *http.ServeMux) {
	if !s.docsEnabled() {
		return
	}
	mux.HandleFunc("GET /openapi.yaml", s.serveOpenAPI)
	mux.HandleFunc("OPTIONS /openapi.yaml", s.serveOpenAPICORS)

	mux.HandleFunc("GET /docs", redirectToSwagger)
	mux.HandleFunc("GET /docs/", redirectToSwagger)
	mux.HandleFunc("GET /swagger", redirectToSwagger)

	mux.HandleFunc("GET /swagger/", s.serveSwaggerIndex)
	mux.HandleFunc("GET /swagger/index.html", s.serveSwaggerIndex)
	mux.HandleFunc("GET /swagger/swagger-ui-bundle.js", func(w http.ResponseWriter, r *http.Request) {
		serveGzipAsset(w, r, "swagger/swagger-ui-bundle.js.gz", "application/javascript; charset=utf-8")
	})
	mux.HandleFunc("GET /swagger/swagger-ui.css", func(w http.ResponseWriter, r *http.Request) {
		serveGzipAsset(w, r, "swagger/swagger-ui.css.gz", "text/css; charset=utf-8")
	})
	mux.HandleFunc("GET /swagger/favicon-32x32.png", func(w http.ResponseWriter, _ *http.Request) {
		b, err := swaggerFS.ReadFile("swagger/favicon-32x32.png")
		if err != nil {
			http.NotFound(w, nil)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Cache-Control", "public, max-age=86400")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(b)
	})
}

func redirectToSwagger(w http.ResponseWriter, r *http.Request) {
	target := "/swagger/"
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	http.Redirect(w, r, target, http.StatusFound)
}

func (s *Server) serveOpenAPICORS(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) serveOpenAPI(w http.ResponseWriter, r *http.Request) {
	var b []byte
	var err error
	if s.cfg != nil && strings.TrimSpace(s.cfg.HTTP.OpenAPIFile) != "" {
		b, err = os.ReadFile(strings.TrimSpace(s.cfg.HTTP.OpenAPIFile))
	} else {
		b, err = swaggerFS.ReadFile("swagger/openapi.yaml")
	}
	if err != nil {
		writeAPIError(w, r, http.StatusInternalServerError, "UNAVAILABLE", "openapi spec unavailable", nil)
		return
	}
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(b)
}

func (s *Server) serveSwaggerIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/swagger/" && r.URL.Path != "/swagger/index.html" {
		http.NotFound(w, r)
		return
	}
	b, err := swaggerFS.ReadFile("swagger/index.html")
	if err != nil {
		http.Error(w, "swagger index unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(b)
}

func serveGzipAsset(w http.ResponseWriter, r *http.Request, embeddedPath, contentType string) {
	gzBytes, err := swaggerFS.ReadFile(embeddedPath)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.Header().Set("Vary", "Accept-Encoding")
	if strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
		w.Header().Set("Content-Encoding", "gzip")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(gzBytes)
		return
	}
	gr, err := gzip.NewReader(bytes.NewReader(gzBytes))
	if err != nil {
		http.Error(w, "decompress error", http.StatusInternalServerError)
		return
	}
	defer gr.Close()
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, gr)
}
