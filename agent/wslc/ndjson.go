package wslc

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

// DecodeNDJSON reads one JSON object per line. Empty input is an empty slice.
func DecodeNDJSON(r io.Reader) ([]json.RawMessage, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	out := make([]json.RawMessage, 0)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		if bytes.HasPrefix(line, []byte{0xEF, 0xBB, 0xBF}) {
			line = bytes.TrimSpace(line[3:])
			if len(line) == 0 {
				continue
			}
		}
		if !json.Valid(line) {
			return nil, fmt.Errorf("wslc: invalid JSON on line %d", lineNo)
		}
		out = append(out, append([]byte(nil), line...))
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func DecodeNDJSONBytes(b []byte) ([]json.RawMessage, error) {
	return DecodeNDJSON(bytes.NewReader(b))
}
