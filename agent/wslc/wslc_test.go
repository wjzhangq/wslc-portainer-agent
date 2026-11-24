package wslc

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestDecodeNDJSON(t *testing.T) {
	empty, err := DecodeNDJSONBytes(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(empty) != 0 {
		t.Fatalf("empty input produced %d rows", len(empty))
	}
	if _, err := DecodeNDJSONBytes([]byte("")); err != nil {
		t.Fatal(err)
	}

	one, err := DecodeNDJSONBytes([]byte("{\"a\":1}\n"))
	if err != nil || len(one) != 1 {
		t.Fatalf("one line: %d %v", len(one), err)
	}
	two, err := DecodeNDJSONBytes([]byte("{\"a\":1}\n{\"b\":2}\n"))
	if err != nil || len(two) != 2 {
		t.Fatalf("two lines: %d %v", len(two), err)
	}
}

func TestParseCreatedAt(t *testing.T) {
	got, err := ParseTime("2026-10-22 18:37:10 +0800 GMT+8")
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 10, 22, 10, 37, 10, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("got %s want %s", got, want)
	}
	if got.Unix() != want.Unix() {
		t.Fatalf("unix %d want %d", got.Unix(), want.Unix())
	}

	frac, err := ParseTime("2026-11-24 05:46:33.255274614 +0000 UTC")
	if err != nil {
		t.Fatal(err)
	}
	if frac.UTC().Format(time.RFC3339Nano) != "2026-11-24T05:46:33.255274614Z" {
		t.Fatal(frac)
	}

	rfc, err := ParseTime("2026-10-22T10:37:10.144254856Z")
	if err != nil {
		t.Fatal(err)
	}
	if rfc.Unix() != want.Unix() {
		t.Fatalf("rfc unix %d", rfc.Unix())
	}
}

func TestNormalizeSince(t *testing.T) {
	got, err := NormalizeSince("1730000000.123")
	if err != nil {
		t.Fatal(err)
	}
	if got != "1730000000" {
		t.Fatalf("got %q", got)
	}
	got, err = NormalizeSince("2026-10-22T10:37:10Z")
	if err != nil || got != "2026-10-22T10:37:10Z" {
		t.Fatalf("rfc3339: %q %v", got, err)
	}
	got, err = NormalizeSince("1730000000")
	if err != nil || got != "1730000000" {
		t.Fatalf("int: %q %v", got, err)
	}
}

func TestContainerListFixture(t *testing.T) {
	ps := readFixture(t, "testdata/ps.json")
	inspect := readFixture(t, "testdata/container-inspect.json")
	h := NewHandlerWithRunner(&scripted{run: func(args []string) (string, string, int, error) {
		switch args[0] {
		case "ps":
			if !contains(args, "-a") {
				t.Errorf("list with all=1 did not pass -a: %v", args)
			}
			return ps, "", 0, nil
		case "inspect":
			return inspect, "", 0, nil
		default:
			t.Errorf("unexpected command %v", args)
			return "", "unexpected", 1, nil
		}
	}})

	body := getJSON(t, h, "/containers/json?all=1&limit=1&size=1&filters=%7B%7D")
	prefixed := getJSON(t, h, "/v1.44/containers/json?all=1")
	again := getJSON(t, h, "/v1.44/v1.41/containers/json?all=1")
	if body != prefixed || body != again {
		t.Fatalf("version prefix changed the list\n%s\n%s", body, prefixed)
	}

	var list []map[string]any
	if err := json.Unmarshal([]byte(body), &list); err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("len %d", len(list))
	}
	item := list[0]
	if item["State"] != "exited" {
		t.Fatalf("state %v", item["State"])
	}
	if item["Status"] != "Exited (255) About an hour ago" {
		t.Fatalf("status %v", item["Status"])
	}
	names, _ := item["Names"].([]any)
	if len(names) != 1 || names[0] != "/wild_flinders" {
		t.Fatalf("names %v", item["Names"])
	}
	command := item["Command"].(string)
	if strings.Contains(command, `"`) {
		t.Fatalf("ps quotes leaked into command %q", command)
	}
	wantCmd := "/app/llama-server -m /models/Qwen3-0.6B-GGUF/Qwen3-0.6B-Q8_0.gguf --host 0.0.0.0 --port 8080 -ngl 99 -c 8192"
	if command != wantCmd {
		t.Fatalf("command %q", command)
	}
	created := int64(item["Created"].(float64))
	wantCreated, _ := time.Parse(time.RFC3339Nano, "2026-10-22T10:37:10.144254856Z")
	if created != wantCreated.Unix() {
		t.Fatalf("created %d want %d", created, wantCreated.Unix())
	}
	ports := item["Ports"].([]any)
	if len(ports) != 1 {
		t.Fatalf("ports %#v", item["Ports"])
	}
	port := ports[0].(map[string]any)
	if port["PublicPort"].(float64) != 9080 || port["PrivatePort"].(float64) != 8080 || port["IP"] != "127.0.0.1" {
		t.Fatalf("port %#v", port)
	}
	mounts := item["Mounts"].([]any)
	mount := mounts[0].(map[string]any)
	if mount["Source"] != `D:\models` || mount["Destination"] != "/models" || mount["RW"] != true {
		t.Fatalf("mount %#v", mount)
	}
}

func TestContainerInspectUnwrapsPorts(t *testing.T) {
	inspect := readFixture(t, "testdata/container-inspect.json")
	h := NewHandlerWithRunner(&scripted{run: func(args []string) (string, string, int, error) {
		return inspect, "", 0, nil
	}})
	raw := getJSON(t, h, "/containers/ac3d3bff1501/json")
	var obj map[string]any
	if err := json.Unmarshal([]byte(raw), &obj); err != nil {
		t.Fatal(err)
	}
	if _, ok := obj["Ports"]; ok {
		t.Fatal("top-level Ports was kept")
	}
	ns := obj["NetworkSettings"].(map[string]any)
	ports := ns["Ports"].(map[string]any)
	if _, ok := ports["8080/tcp"]; !ok {
		t.Fatalf("NetworkSettings.Ports %#v", ports)
	}
	hc := obj["HostConfig"].(map[string]any)
	bindings := hc["PortBindings"].(map[string]any)
	if _, ok := bindings["8080/tcp"]; !ok {
		t.Fatalf("PortBindings %#v", bindings)
	}
	if _, ok := hc["Binds"].([]any); !ok {
		t.Fatalf("Binds %#v", hc["Binds"])
	}
	state := obj["State"].(map[string]any)
	if state["Pid"].(float64) != 0 || state["OOMKilled"] != false || state["Error"] != "" {
		t.Fatalf("state defaults %#v", state)
	}
	if obj["Created"] != "2026-10-22T10:37:10.144254856Z" {
		t.Fatalf("created was rewritten: %v", obj["Created"])
	}
}

func TestImageListMergesTagsAndUsesInspectSize(t *testing.T) {
	images := readFixture(t, "testdata/images.json")
	inspect := readFixture(t, "testdata/image-inspect.json")
	list, err := BuildImageList([]byte(images), []byte(inspect))
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("len %d", len(list))
	}
	var llama map[string]any
	for _, item := range list {
		if item["Id"] == "sha256:9806c8d385d34d4806d19be5edd046aeeb5e1a923cb55a07ea043fc19c97592a" {
			llama = item
		}
	}
	if llama == nil {
		t.Fatal("missing llama image")
	}
	if llama["Size"].(int64) != 4356415882 || llama["VirtualSize"].(int64) != 4356415882 {
		t.Fatalf("size %#v virtual %#v", llama["Size"], llama["VirtualSize"])
	}
	if llama["Size"] == int64(4360000000) {
		t.Fatal("used the rounded 4.36GB text")
	}
	tags := llama["RepoTags"].([]string)
	if len(tags) != 1 || tags[0] != "ghcr.nju.edu.cn/ggml-org/llama.cpp:server-cuda" {
		t.Fatalf("tags %#v", tags)
	}
	wantCreated, err := ParseTime("2026-10-22 13:45:22 +0800 GMT+8")
	if err != nil {
		t.Fatal(err)
	}
	if llama["Created"].(int64) != wantCreated.Unix() {
		t.Fatalf("created %v", llama["Created"])
	}
	if llama["Containers"].(int) != 1 {
		t.Fatalf("containers %v", llama["Containers"])
	}

	merged, err := BuildImageList([]byte(strings.Join([]string{
		`{"ID":"sha256:abc","Repository":"repo","Tag":"one","CreatedAt":"2026-10-22 13:45:22 +0800 GMT+8","Size":"4.36GB","Containers":"2"}`,
		`{"ID":"sha256:abc","Repository":"repo","Tag":"two","CreatedAt":"2026-10-22 13:45:22 +0800 GMT+8","Size":"9GB","Containers":"1"}`,
		`{"ID":"sha256:abc","Repository":"<none>","Tag":"<none>","CreatedAt":"2026-10-22 13:45:22 +0800 GMT+8","Size":"4.36GB","Containers":"0"}`,
	}, "\n")), []byte(`[{"Id":"sha256:abc","Size":1234}]`))
	if err != nil {
		t.Fatal(err)
	}
	if len(merged) != 1 {
		t.Fatalf("merged len %d", len(merged))
	}
	if merged[0]["Size"].(int64) != 1234 {
		t.Fatalf("merged size %v", merged[0]["Size"])
	}
	mtags := merged[0]["RepoTags"].([]string)
	if len(mtags) != 2 || mtags[0] != "repo:one" || mtags[1] != "repo:two" {
		t.Fatalf("merged tags %#v", mtags)
	}
}

func TestLogFrame(t *testing.T) {
	var buf bytes.Buffer
	if err := FrameLines(&buf, "hello\n"); err != nil {
		t.Fatal(err)
	}
	b := buf.Bytes()
	if len(b) != 14 || b[0] != 1 || b[1] != 0 || b[2] != 0 || b[3] != 0 {
		t.Fatalf("header %v", b)
	}
	if binary.BigEndian.Uint32(b[4:8]) != 6 || string(b[8:]) != "hello\n" {
		t.Fatalf("payload %q len %d", b[8:], binary.BigEndian.Uint32(b[4:8]))
	}
}

func TestStatsCPUAndZero(t *testing.T) {
	row := statsRow{
		ID: "abc", Name: "box", CPUPerc: "12.50%",
		MemUsage: "10MB / 1GB", NetIO: "1kB / 2kB", BlockIO: "3B / 4B", PIDs: 3,
	}
	at := time.Date(2026, 11, 24, 0, 0, 0, 0, time.UTC)
	first, counters := synthesize(row, 8, at, time.Time{}, nil)
	if math.Abs(portainerCPUPercent(first)-12.5) > 0.01 {
		t.Fatalf("first cpu %v", portainerCPUPercent(first))
	}
	second, _ := synthesize(row, 8, at.Add(time.Second), at, &counters)
	if math.Abs(portainerCPUPercent(second)-12.5) > 0.01 {
		t.Fatalf("second cpu %v", portainerCPUPercent(second))
	}
	if second.PreCPU.CPUUsage.TotalUsage != first.CPUStats.CPUUsage.TotalUsage {
		t.Fatal("precpu is not the previous sample")
	}
	if first.Memory.Usage != 10_000_000 || first.Memory.Limit != 1_000_000_000 {
		t.Fatalf("memory %#v", first.Memory)
	}
	if first.Networks["eth0"].RxBytes != 1000 || first.Networks["eth0"].TxBytes != 2000 {
		t.Fatalf("net %#v", first.Networks)
	}
	zero := zeroStats("id", "stopped", at)
	if portainerCPUPercent(zero) != 0 || zero.Memory.Usage != 0 || zero.CPUStats.CPUUsage.TotalUsage != 0 || zero.PidsStats.Current != 0 {
		t.Fatalf("zero sample %#v", zero)
	}
}

func TestMapEventFixture(t *testing.T) {
	raw := readFixture(t, "testdata/event.json")
	mapped, ok := MapEvent([]byte(raw))
	if !ok {
		t.Fatal("event was not mapped")
	}
	var obj map[string]any
	if err := json.Unmarshal(mapped, &obj); err != nil {
		t.Fatal(err)
	}
	if obj["Type"] != "container" || obj["Action"] != "start" {
		t.Fatalf("%v %v", obj["Type"], obj["Action"])
	}
	actor := obj["Actor"].(map[string]any)
	if actor["ID"] == "" {
		t.Fatal("missing actor id")
	}
	if _, ok := MapEvent([]byte("not json")); ok {
		t.Fatal("non-json was forwarded")
	}
	if _, ok := MapEvent([]byte(`{"hello":"world"}`)); ok {
		t.Fatal("unrelated json was forwarded")
	}
}

func TestParseInfoAndStoragePath(t *testing.T) {
	text := readFixture(t, "testdata/info.txt")
	info := ParseInfo(text)
	if info.ManagerVersion != "3.0.3" || info.KernelVersion != "6.18.54.1-1" || info.WSLVersion != "3.0.3.0" || info.WindowsVersion != "10.0.26200.8457" || info.Session != "1" {
		t.Fatalf("%#v", info)
	}
	ps := readFixture(t, "testdata/ps.json")
	images := readFixture(t, "testdata/images.json")
	running, paused, stopped, total := countContainerStates(ps)
	if running != 0 || paused != 0 || stopped != 1 || total != 1 {
		t.Fatalf("counts %d %d %d %d", running, paused, stopped, total)
	}
	if countImageIDs(images) != 2 {
		t.Fatal(countImageIDs(images))
	}
	got := parseStoragePath("# storagePath: default\nstoragePath: d:\\wslc-data\n", `C:\fallback`)
	if got != `d:\wslc-data` {
		t.Fatal(got)
	}
	if parseStoragePath("storagePath: default\n", `C:\fallback`) != `C:\fallback` {
		t.Fatal("default")
	}
}

func TestRoutes(t *testing.T) {
	h := NewHandlerWithRunner(&scripted{run: func(args []string) (string, string, int, error) {
		switch args[0] {
		case "ps", "images", "info":
			return "", "", 0, nil
		case "inspect":
			return `[{"Id":"abc","Config":{}}]`, "", 0, nil
		case "rm":
			return "", "container is gone\n错误代码： WSLC_E_CONTAINER_NOT_FOUND", 1, nil
		default:
			return "", "无效的 signal 值", 1, nil
		}
	}})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/containers/create", nil))
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("create %d", rec.Code)
	}
	var msg map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &msg); err != nil {
		t.Fatal(err)
	}
	if msg["message"] != "wslc agent: not implemented: POST /containers/create" {
		t.Fatalf("message %q", msg["message"])
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1.44/containers/abc/pause", nil))
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("pause %d %s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/_ping", nil))
	if rec.Code != 200 || rec.Body.String() != "OK" || rec.Header().Get("Api-Version") != "1.44" {
		t.Fatalf("ping %d %q %q", rec.Code, rec.Body.String(), rec.Header().Get("Api-Version"))
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/networks", nil))
	if strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Fatalf("networks %s", rec.Body.String())
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/volumes", nil))
	var volumes map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &volumes); err != nil {
		t.Fatal(err)
	}
	if volumes["Warnings"] != nil {
		t.Fatalf("warnings %#v", volumes["Warnings"])
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/containers/missing", nil))
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "WSLC_E_CONTAINER_NOT_FOUND") {
		t.Fatalf("delete %d %s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/networks", nil))
	if rec.Code != http.StatusNotImplemented || !strings.Contains(rec.Body.String(), "not implemented: POST /networks") {
		t.Fatalf("post networks %d %s", rec.Code, rec.Body.String())
	}

	for i := 0; i < maxStreams; i++ {
		h.slots <- struct{}{}
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/containers/abc/logs?follow=1", nil))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("follow limit %d %s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/containers/abc/stats?stream=1", nil))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("stats limit %d %s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/containers/abc/stats?stream=0", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("stats snapshot %d %s", rec.Code, rec.Body.String())
	}
}

func TestSessionFlagOrder(t *testing.T) {
	e := &Executor{Session: "2"}
	got := e.commandArgs([]string{"ps", "-a"})
	if strings.Join(got, " ") != "--session 2 ps -a" {
		t.Fatal(got)
	}
	e.Session = ""
	if strings.Join(e.commandArgs([]string{"ps"}), " ") != "ps" {
		t.Fatal(e.commandArgs([]string{"ps"}))
	}
}

func TestEmptyContainerList(t *testing.T) {
	h := NewHandlerWithRunner(&scripted{run: func(args []string) (string, string, int, error) {
		return "", "", 0, nil
	}})
	if strings.TrimSpace(getJSON(t, h, "/containers/json")) != "[]" {
		t.Fatal("empty list was not an array")
	}
}

func TestNotFoundIgnoresProse(t *testing.T) {
	prose := NewHandlerWithRunner(&scripted{run: func(args []string) (string, string, int, error) {
		return "", "找不到对象: missing", 1, nil
	}})
	rec := httptest.NewRecorder()
	prose.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/containers/missing", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("prose %d %s", rec.Code, rec.Body.String())
	}

	coded := NewHandlerWithRunner(&scripted{run: func(args []string) (string, string, int, error) {
		return "", "object missing\n错误代码： WSLC_E_CONTAINER_NOT_FOUND", 1, nil
	}})
	rec = httptest.NewRecorder()
	coded.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/containers/missing", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("engine code %d %s", rec.Code, rec.Body.String())
	}

	inspected := NewHandlerWithRunner(&scripted{run: func(args []string) (string, string, int, error) {
		return "[]", "phrase changed", 1, nil
	}})
	rec = httptest.NewRecorder()
	inspected.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/containers/missing/json", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("empty inspect %d %s", rec.Code, rec.Body.String())
	}
}

func TestContainerListInspectFailure(t *testing.T) {
	ps := readFixture(t, "testdata/ps.json")
	h := NewHandlerWithRunner(&scripted{run: func(args []string) (string, string, int, error) {
		if args[0] == "ps" {
			return ps, "", 0, nil
		}
		return "", "inspect exploded", 1, nil
	}})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/containers/json?all=1", nil))
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "inspect exploded") {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}

func TestContainerLabelFilter(t *testing.T) {
	ps := readFixture(t, "testdata/ps.json")
	inspect := readFixture(t, "testdata/container-inspect.json")
	h := NewHandlerWithRunner(&scripted{run: func(args []string) (string, string, int, error) {
		switch args[0] {
		case "ps":
			return ps, "", 0, nil
		case "inspect":
			return inspect, "", 0, nil
		default:
			t.Errorf("unexpected %v", args)
			return "", "", 1, nil
		}
	}})
	keep := "/containers/json?all=1&filters=" + url.QueryEscape(`{"label":["org.opencontainers.image.title=llama.cpp"]}`)
	var list []map[string]any
	if err := json.Unmarshal([]byte(getJSON(t, h, keep)), &list); err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("kept %d", len(list))
	}
	drop := "/containers/json?all=1&filters=" + url.QueryEscape(`{"label":["com.docker.compose.project=demo"]}`)
	if strings.TrimSpace(getJSON(t, h, drop)) != "[]" {
		t.Fatal("unmatched label was returned")
	}
	status := "/containers/json?all=1&filters=" + url.QueryEscape(`{"status":["exited"],"name":["wild_flinders"]}`)
	if strings.TrimSpace(getJSON(t, h, status)) == "[]" {
		t.Fatal("status and name filter dropped the container")
	}
}

func TestImageReferenceFilter(t *testing.T) {
	images := readFixture(t, "testdata/images.json")
	inspect := readFixture(t, "testdata/image-inspect.json")
	list, err := BuildImageList([]byte(images), []byte(inspect))
	if err != nil {
		t.Fatal(err)
	}
	if len(filterImages(list, `{"reference":["ghcr.nju.edu.cn/ggml-org/llama.cpp"]}`)) != 1 {
		t.Fatal("reference filter")
	}
	for _, item := range filterImages(list, `{"dangling":["true"]}`) {
		if len(stringList(item["RepoTags"])) != 0 {
			t.Fatalf("dangling kept %#v", item["RepoTags"])
		}
	}
}

func TestPullReportsFailure(t *testing.T) {
	failed := NewHandlerWithRunner(&scripted{stream: func(args []string) (io.ReadCloser, func() (string, int, error), error) {
		return io.NopCloser(strings.NewReader("")), func() (string, int, error) {
			return "no such image\n错误代码： WSLC_E_IMAGE_NOT_FOUND", 1, nil
		}, nil
	}})
	rec := httptest.NewRecorder()
	failed.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/images/create?fromImage=missing", nil))
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "WSLC_E_IMAGE_NOT_FOUND") {
		t.Fatalf("early %d %s", rec.Code, rec.Body.String())
	}

	partial := NewHandlerWithRunner(&scripted{stream: func(args []string) (io.ReadCloser, func() (string, int, error), error) {
		return io.NopCloser(strings.NewReader("{\"status\":\"Pulling\"}\n")), func() (string, int, error) {
			return "denied", 1, nil
		}, nil
	}})
	rec = httptest.NewRecorder()
	partial.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/images/create?fromImage=nginx&tag=latest", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"status":"Pulling"`) || !strings.Contains(rec.Body.String(), `"error":"denied"`) {
		t.Fatalf("partial %d %s", rec.Code, rec.Body.String())
	}
}

func TestFollowLogsHeadersBeforeOutput(t *testing.T) {
	pr, pw := io.Pipe()
	release := make(chan struct{})
	h := NewHandlerWithRunner(&scripted{
		run: func(args []string) (string, string, int, error) {
			if args[0] == "inspect" {
				return `[{"Config":{}}]`, "", 0, nil
			}
			t.Errorf("unexpected %v", args)
			return "", "", 1, nil
		},
		stream: func(args []string) (io.ReadCloser, func() (string, int, error), error) {
			return pr, func() (string, int, error) {
				<-release
				return "", 0, nil
			}, nil
		},
	})
	rec := httptest.NewRecorder()
	got := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		h.ServeHTTP(&notifyWriter{ResponseWriter: rec, got: got}, httptest.NewRequest(http.MethodGet, "/containers/abc/logs?follow=1", nil))
		close(finished)
	}()
	select {
	case <-got:
	case <-time.After(2 * time.Second):
		t.Fatal("headers were not written while the container was quiet")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("code %d", rec.Code)
	}
	if rec.Header().Get("Content-Type") != "application/vnd.docker.raw-stream" {
		t.Fatalf("content-type %q", rec.Header().Get("Content-Type"))
	}
	close(release)
	_ = pw.Close()
	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not return")
	}
}

func TestFollowLogsNotFound(t *testing.T) {
	h := NewHandlerWithRunner(&scripted{
		run: func(args []string) (string, string, int, error) {
			return `[{"Config":{}}]`, "", 0, nil
		},
		stream: func(args []string) (io.ReadCloser, func() (string, int, error), error) {
			return io.NopCloser(strings.NewReader("")), func() (string, int, error) {
				return "object missing\n错误代码： WSLC_E_CONTAINER_NOT_FOUND", 1, nil
			}, nil
		},
	})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/containers/abc/logs?follow=1", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}

func TestTTYLogsAreRaw(t *testing.T) {
	h := NewHandlerWithRunner(&scripted{run: func(args []string) (string, string, int, error) {
		switch args[0] {
		case "inspect":
			return `[{"Config":{"Tty":true}}]`, "", 0, nil
		case "logs":
			return "hello\n", "", 0, nil
		default:
			t.Errorf("unexpected %v", args)
			return "", "", 1, nil
		}
	}})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/containers/abc/logs", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "hello\n" {
		t.Fatalf("tty %d %q", rec.Code, rec.Body.String())
	}

	plain := NewHandlerWithRunner(&scripted{run: func(args []string) (string, string, int, error) {
		switch args[0] {
		case "inspect":
			return `[{"Config":{"Tty":false}}]`, "", 0, nil
		case "logs":
			return "hello\n", "", 0, nil
		default:
			return "", "", 1, nil
		}
	}})
	rec = httptest.NewRecorder()
	plain.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/containers/abc/logs", nil))
	if rec.Code != http.StatusOK || len(rec.Body.Bytes()) == 0 || rec.Body.Bytes()[0] != stdoutStream {
		t.Fatalf("mux %d %q", rec.Code, rec.Body.String())
	}
}

type notifyWriter struct {
	http.ResponseWriter
	got  chan struct{}
	once sync.Once
}

func (n *notifyWriter) WriteHeader(code int) {
	n.ResponseWriter.WriteHeader(code)
	n.once.Do(func() { close(n.got) })
}

type scripted struct {
	run    func(args []string) (string, string, int, error)
	stream func(args []string) (io.ReadCloser, func() (string, int, error), error)
}

func (s *scripted) Run(ctx context.Context, args ...string) (string, string, int, error) {
	return s.run(args)
}

func (s *scripted) RunStreaming(ctx context.Context, args ...string) (io.ReadCloser, func() (string, int, error), error) {
	if s.stream != nil {
		return s.stream(args)
	}
	return io.NopCloser(strings.NewReader("")), func() (string, int, error) { return "", 0, nil }, nil
}

func readFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func getJSON(t *testing.T, h http.Handler, path string) string {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("%s -> %d %s", path, rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}

func contains(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}
