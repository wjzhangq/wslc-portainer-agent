package wslc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"
)

const (
	maxStreams = 16
)

// wslc uses exit code 1 for every failure, including bad flags. A missing
// object is identified by these stable engine codes. inspect is the exception:
// it prints an empty JSON array and does not emit an engine code.

var versionPrefix = regexp.MustCompile(`^/v\d+\.\d+`)

// Handler serves the subset of the Docker Engine API implemented on top of wslc.
type Handler struct {
	runner Runner
	slots  chan struct{}
	mux    *http.ServeMux
}

func NewHandler() (*Handler, error) {
	exec, err := NewExecutor()
	if err != nil {
		return nil, err
	}
	return NewHandlerWithRunner(exec), nil
}

func NewHandlerWithRunner(runner Runner) *Handler {
	h := &Handler{
		runner: runner,
		slots:  make(chan struct{}, maxStreams),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /_ping", h.ping)
	mux.HandleFunc("HEAD /_ping", h.ping)
	mux.HandleFunc("GET /version", h.version)
	mux.HandleFunc("GET /info", h.dockerInfo)
	mux.HandleFunc("GET /containers/json", h.listContainers)
	mux.HandleFunc("GET /containers/{id}/json", h.inspectContainer)
	mux.HandleFunc("POST /containers/{id}/start", h.startContainer)
	mux.HandleFunc("POST /containers/{id}/stop", h.stopContainer)
	mux.HandleFunc("POST /containers/{id}/restart", h.restartContainer)
	mux.HandleFunc("POST /containers/{id}/kill", h.killContainer)
	mux.HandleFunc("POST /containers/{id}/pause", h.notImplemented)
	mux.HandleFunc("POST /containers/{id}/unpause", h.notImplemented)
	mux.HandleFunc("DELETE /containers/{id}", h.removeContainer)
	mux.HandleFunc("GET /containers/{id}/logs", h.containerLogs)
	mux.HandleFunc("GET /containers/{id}/stats", h.containerStats)
	mux.HandleFunc("GET /images/json", h.listImages)
	mux.HandleFunc("GET /images/{name...}", h.imageByName)
	mux.HandleFunc("POST /images/create", h.pullImage)
	mux.HandleFunc("DELETE /images/{name...}", h.removeImage)
	mux.HandleFunc("GET /events", h.events)
	mux.HandleFunc("GET /networks", h.networks)
	mux.HandleFunc("GET /volumes", h.volumes)
	mux.HandleFunc("/", h.notImplemented)
	h.mux = mux
	return h
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := stripAPIVersion(r.URL.Path)
	if path != r.URL.Path {
		cloned := r.Clone(r.Context())
		cloned.URL.Path = path
		cloned.URL.RawPath = ""
		r = cloned
	}
	w.Header().Set("Api-Version", apiVersion)
	w.Header().Set("Ostype", "linux")
	capture := &statusCapture{ResponseWriter: w}
	h.mux.ServeHTTP(capture, r)
	if capture.code == http.StatusMethodNotAllowed {
		h.notImplemented(w, r)
	}
}

func stripAPIVersion(path string) string {
	for {
		next := versionPrefix.ReplaceAllString(path, "")
		if next == "" {
			next = "/"
		}
		if next == path {
			return path
		}
		path = next
	}
}

func (h *Handler) ping(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Api-Version", apiVersion)
	w.Header().Set("Ostype", "linux")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, "OK")
}

func (h *Handler) networks(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, []any{})
}

func (h *Handler) volumes(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"Volumes":  []any{},
		"Warnings": nil,
	})
}

func (h *Handler) notImplemented(w http.ResponseWriter, r *http.Request) {
	writeMessage(w, http.StatusNotImplemented, "wslc agent: not implemented: "+r.Method+" "+r.URL.Path)
}

func (h *Handler) acquire(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case h.slots <- struct{}{}:
		return nil
	default:
		return errTooManyStreams
	}
}

func (h *Handler) release() {
	<-h.slots
}

var errTooManyStreams = errors.New("too many streaming commands")

func (h *Handler) success(w http.ResponseWriter, stdout, stderr string, code int, err error) bool {
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return false
		}
		writeMessage(w, http.StatusInternalServerError, err.Error())
		return false
	}
	if code == 0 {
		return true
	}
	msg := strings.TrimSpace(stderr)
	if msg == "" {
		msg = strings.TrimSpace(stdout)
	}
	if msg == "" {
		msg = "wslc exited"
	}
	status := http.StatusInternalServerError
	if notFoundOutput(stdout, stderr) {
		status = http.StatusNotFound
	}
	writeMessage(w, status, msg)
	return false
}

func notFoundOutput(stdout, stderr string) bool {
	text := stderr + "\n" + stdout
	if strings.Contains(text, "WSLC_E_CONTAINER_NOT_FOUND") || strings.Contains(text, "WSLC_E_IMAGE_NOT_FOUND") {
		return true
	}
	return strings.TrimSpace(stdout) == "[]"
}

func (h *Handler) streamText(w http.ResponseWriter, r *http.Request, args []string, format func(string) []byte) {
	stdout, wait, err := h.runner.RunStreaming(r.Context(), args...)
	if err != nil {
		writeMessage(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer stdout.Close()

	headerSent := false
	sendHeader := func() {
		if headerSent {
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		headerSent = true
		flush(w)
	}

	sc := bufioScanner(stdout)
	for sc.Scan() {
		select {
		case <-r.Context().Done():
			_, _, _ = wait()
			return
		default:
		}
		payload := format(sc.Text())
		if len(payload) == 0 {
			continue
		}
		sendHeader()
		if _, err := w.Write(payload); err != nil {
			_, _, _ = wait()
			return
		}
		flush(w)
	}
	stderr, code, waitErr := wait()
	if r.Context().Err() != nil {
		return
	}
	if !headerSent {
		if errors.Is(waitErr, context.Canceled) {
			return
		}
		if waitErr != nil {
			writeMessage(w, http.StatusInternalServerError, waitErr.Error())
			return
		}
		if code != 0 {
			msg := strings.TrimSpace(stderr)
			if msg == "" {
				msg = "wslc exited"
			}
			writeMessage(w, http.StatusInternalServerError, msg)
			return
		}
		sendHeader()
		return
	}
	if waitErr == nil && code == 0 {
		return
	}
	msg := strings.TrimSpace(stderr)
	if msg == "" && waitErr != nil {
		msg = waitErr.Error()
	}
	if msg == "" {
		msg = "wslc exited"
	}
	encoded, err := json.Marshal(map[string]any{
		"error":       msg,
		"errorDetail": map[string]string{"message": msg},
	})
	if err != nil {
		return
	}
	_, _ = w.Write(append(encoded, '\n'))
	flush(w)
}

// statusCapture swallows the standard library's 405 so the handler can answer
// with the Docker not-implemented JSON instead.
type statusCapture struct {
	http.ResponseWriter
	code int
}

func (c *statusCapture) WriteHeader(code int) {
	c.code = code
	if code == http.StatusMethodNotAllowed {
		return
	}
	c.ResponseWriter.WriteHeader(code)
}

func (c *statusCapture) Write(p []byte) (int, error) {
	if c.code == 0 {
		c.code = http.StatusOK
	}
	if c.code == http.StatusMethodNotAllowed {
		return len(p), nil
	}
	return c.ResponseWriter.Write(p)
}

func (c *statusCapture) Flush() {
	if c.code == http.StatusMethodNotAllowed {
		return
	}
	flush(c.ResponseWriter)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func writeMessage(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"message": message})
}

func bufioScanner(r io.Reader) *bufio.Scanner {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	return sc
}

func flush(w http.ResponseWriter) {
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

func truthy(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes":
		return true
	default:
		return false
	}
}
