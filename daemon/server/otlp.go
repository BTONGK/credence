package server

import (
	"bytes"
	"compress/gzip"
	"crypto/subtle"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/BTONGK/credence/adapters"
	"github.com/BTONGK/credence/adapters/generic"
	"github.com/BTONGK/credence/importers"
	"github.com/BTONGK/credence/internal/home"
)

// OTLPToken returns the stable bearer token OTLP exporters send to the
// daemon, creating it on first use. Unlike the UI token it survives
// restarts, so it can live in a shell profile:
//
//	OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:7171
//	OTEL_EXPORTER_OTLP_PROTOCOL=http/json
//	OTEL_EXPORTER_OTLP_HEADERS="Authorization=Bearer $(cat ~/.credence/otlp.token)"
func OTLPToken() (string, error) {
	p := home.Path("otlp.token")
	if b, err := os.ReadFile(p); err == nil {
		if t := string(bytes.TrimSpace(b)); len(t) >= 32 {
			return t, nil
		}
	}
	if err := os.MkdirAll(home.Dir(), 0o700); err != nil {
		return "", err
	}
	t := newToken()
	return t, os.WriteFile(p, []byte(t+"\n"), 0o600)
}

// otlp accepts OTLP/HTTP JSON logs and traces and captures them as
// generic transcripts.
func (d *Daemon) otlp(w http.ResponseWriter, r *http.Request) {
	want, err := OTLPToken()
	tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if err != nil || subtle.ConstantTimeCompare([]byte(tok), []byte(want)) != 1 {
		writeErr(w, http.StatusUnauthorized, errors.New("missing or wrong token; send Authorization: Bearer <~/.credence/otlp.token>"))
		return
	}
	if r.Header.Get("Origin") != "" {
		writeErr(w, http.StatusForbidden, errors.New("browser requests are not accepted"))
		return
	}
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		writeErr(w, http.StatusUnsupportedMediaType, errors.New("Credence accepts OTLP/HTTP JSON; set OTEL_EXPORTER_OTLP_PROTOCOL=http/json"))
		return
	}
	var body io.Reader = http.MaxBytesReader(w, r.Body, 32<<20)
	if r.Header.Get("Content-Encoding") == "gzip" {
		zr, err := gzip.NewReader(body)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		defer zr.Close()
		body = io.LimitReader(zr, 64<<20)
	}
	b, err := io.ReadAll(body)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	files, err := importers.WriteOTLP(d.Store, b, "")
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if a, ok := adapters.Get(generic.Name); ok {
		for _, f := range files {
			if err := d.Ingest.ProcessFile(r.Context(), f, a, true); err != nil {
				d.Log.Printf("otlp ingest %s: %v", f, err)
			}
		}
	}
	writeJSON(w, map[string]any{"partialSuccess": map[string]any{}})
}
