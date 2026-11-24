package wslc

import (
	"bufio"
	"encoding/binary"
	"io"
	"net/http"
	"strings"
	"time"
)

const followHeaderGrace = 200 * time.Millisecond

const stdoutStream byte = 1

func WriteLogFrame(w io.Writer, stream byte, payload []byte) error {
	var hdr [8]byte
	hdr[0] = stream
	binary.BigEndian.PutUint32(hdr[4:], uint32(len(payload)))
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	_, err := w.Write(payload)
	return err
}

func FrameLines(w io.Writer, text string) error {
	if text == "" {
		return nil
	}
	parts := strings.SplitAfter(text, "\n")
	for _, line := range parts {
		if line == "" {
			continue
		}
		if err := WriteLogFrame(w, stdoutStream, []byte(line)); err != nil {
			return err
		}
	}
	return nil
}

func (h *Handler) containerLogs(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	raw, ok := h.containerTTY(w, r, id)
	if !ok {
		return
	}
	q := r.URL.Query()
	if q.Get("stdout") == "0" && q.Get("stderr") == "0" {
		w.Header().Set("Content-Type", "application/vnd.docker.raw-stream")
		w.WriteHeader(http.StatusOK)
		return
	}
	args := []string{"logs"}
	follow := truthy(q.Get("follow"))
	if follow {
		args = append(args, "-f")
	}
	if truthy(q.Get("timestamps")) {
		args = append(args, "-t")
	}
	if tail := q.Get("tail"); tail != "" && tail != "all" {
		args = append(args, "--tail", tail)
	}
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
	args = append(args, id)

	if follow {
		if err := h.acquire(r.Context()); err != nil {
			writeMessage(w, http.StatusTooManyRequests, "wslc agent: too many streaming commands")
			return
		}
		defer h.release()
		h.streamLogs(w, r, args, raw)
		return
	}

	stdout, stderr, code, err := h.runner.Run(r.Context(), args...)
	if !h.success(w, stdout, stderr, code, err) {
		return
	}
	writeLogHeaders(w)
	writeLogBody(w, raw, []byte(stdout))
}

// containerTTY reports Config.Tty. ok is false when an error response was written.
func (h *Handler) containerTTY(w http.ResponseWriter, r *http.Request, id string) (bool, bool) {
	stdout, stderr, code, err := h.runner.Run(r.Context(), "inspect", "--type", "container", id)
	if !h.success(w, stdout, stderr, code, err) {
		return false, false
	}
	objs, err := parseInspectArray(stdout)
	if err != nil {
		writeMessage(w, http.StatusInternalServerError, err.Error())
		return false, false
	}
	if len(objs) == 0 {
		writeMessage(w, http.StatusNotFound, "wslc agent: no such container: "+id)
		return false, false
	}
	cfg := asMap(objs[0]["Config"])
	tty, _ := cfg["Tty"].(bool)
	return tty, true
}

func writeLogHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/vnd.docker.raw-stream")
	w.WriteHeader(http.StatusOK)
}

func writeLogBody(w http.ResponseWriter, raw bool, data []byte) {
	if len(data) == 0 {
		return
	}
	if raw {
		_, _ = w.Write(data)
		return
	}
	_ = FrameLines(w, string(data))
}

func (h *Handler) streamLogs(w http.ResponseWriter, r *http.Request, args []string, raw bool) {
	stdout, wait, err := h.runner.RunStreaming(r.Context(), args...)
	if err != nil {
		writeMessage(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer stdout.Close()

	type waitResult struct {
		stderr string
		code   int
		err    error
	}
	done := make(chan waitResult, 1)
	go func() {
		stderr, code, waitErr := wait()
		done <- waitResult{stderr, code, waitErr}
	}()

	timer := time.NewTimer(followHeaderGrace)
	defer timer.Stop()
	select {
	case res := <-done:
		data, _ := io.ReadAll(stdout)
		if len(data) == 0 {
			if !h.success(w, "", res.stderr, res.code, res.err) {
				return
			}
			writeLogHeaders(w)
			return
		}
		writeLogHeaders(w)
		writeLogBody(w, raw, data)
		return
	case <-timer.C:
	}

	writeLogHeaders(w)
	flush(w)

	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		select {
		case <-r.Context().Done():
			<-done
			return
		default:
		}
		line := sc.Text() + "\n"
		if raw {
			_, _ = io.WriteString(w, line)
		} else {
			_ = WriteLogFrame(w, stdoutStream, []byte(line))
		}
		flush(w)
	}
	<-done
}
