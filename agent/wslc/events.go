package wslc

import (
	"bufio"
	"bytes"
	"encoding/json"
	"net/http"
)

// MapEvent converts one wslc events JSON line into a Docker event.
// The captured sample in testdata/event.json is already a Docker event
// (Type, Action, Actor.ID, time, timeNano, scope), so a line that has those
// fields is forwarded unchanged. Lines that do not are dropped.
func MapEvent(line []byte) ([]byte, bool) {
	line = bytes.TrimSpace(line)
	if len(line) == 0 || line[0] != '{' {
		return nil, false
	}
	var probe struct {
		Type   string `json:"Type"`
		Action string `json:"Action"`
		Actor  struct {
			ID string `json:"ID"`
		} `json:"Actor"`
	}
	if err := json.Unmarshal(line, &probe); err != nil {
		return nil, false
	}
	if probe.Type == "" || probe.Action == "" || probe.Actor.ID == "" {
		return nil, false
	}
	return append([]byte(nil), line...), true
}

func (h *Handler) events(w http.ResponseWriter, r *http.Request) {
	if err := h.acquire(r.Context()); err != nil {
		writeMessage(w, http.StatusTooManyRequests, "wslc agent: too many streaming commands")
		return
	}
	defer h.release()

	args := []string{"events", "--format", "json"}
	q := r.URL.Query()
	for _, key := range []string{"since", "until"} {
		value := q.Get(key)
		if value == "" {
			continue
		}
		norm, err := NormalizeSince(value)
		if err != nil {
			writeMessage(w, http.StatusBadRequest, err.Error())
			return
		}
		args = append(args, "--"+key, norm)
	}

	stdout, wait, err := h.runner.RunStreaming(r.Context(), args...)
	if err != nil {
		writeMessage(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer stdout.Close()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	flush(w)

	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		select {
		case <-r.Context().Done():
			_, _, _ = wait()
			return
		default:
		}
		mapped, ok := MapEvent(sc.Bytes())
		if !ok {
			continue
		}
		if _, err := w.Write(mapped); err != nil {
			_, _, _ = wait()
			return
		}
		if mapped[len(mapped)-1] != '\n' {
			if _, err := w.Write([]byte("\n")); err != nil {
				_, _, _ = wait()
				return
			}
		}
		flush(w)
	}
	_, _, _ = wait()
}
