package wslc

import (
	"bufio"
	"encoding/binary"
	"io"
	"net/http"
	"strings"
)

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
	args = append(args, r.PathValue("id"))

	if follow {
		if err := h.acquire(r.Context()); err != nil {
			writeMessage(w, http.StatusTooManyRequests, "wslc agent: too many streaming commands")
			return
		}
		defer h.release()
		h.streamLogs(w, r, args)
		return
	}

	stdout, stderr, code, err := h.runner.Run(r.Context(), args...)
	if !h.success(w, stdout, stderr, code, err) {
		return
	}
	w.Header().Set("Content-Type", "application/vnd.docker.raw-stream")
	w.WriteHeader(http.StatusOK)
	_ = FrameLines(w, stdout)
}

func (h *Handler) streamLogs(w http.ResponseWriter, r *http.Request, args []string) {
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
		w.Header().Set("Content-Type", "application/vnd.docker.raw-stream")
		w.WriteHeader(http.StatusOK)
		headerSent = true
	}

	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		select {
		case <-r.Context().Done():
			_, _, _ = wait()
			return
		default:
		}
		sendHeader()
		_ = WriteLogFrame(w, stdoutStream, []byte(sc.Text()+"\n"))
		flush(w)
	}
	stderr, code, waitErr := wait()
	if !headerSent {
		if !h.success(w, "", stderr, code, waitErr) {
			return
		}
		sendHeader()
	}
}
