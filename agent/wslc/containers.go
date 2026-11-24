package wslc

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/rs/zerolog/log"
)

type psEntry struct {
	ID        string
	Names     string
	Image     string
	CreatedAt string
	State     string
	Status    string
}

func (h *Handler) listContainers(w http.ResponseWriter, r *http.Request) {
	args := []string{"ps", "--format", "json", "--no-trunc"}
	if truthy(r.URL.Query().Get("all")) {
		args = []string{"ps", "-a", "--format", "json", "--no-trunc"}
	}
	stdout, stderr, code, err := h.runner.Run(r.Context(), args...)
	if !h.success(w, stdout, stderr, code, err) {
		return
	}
	list, err := h.containerListFrom(r.Context(), stdout)
	if err != nil {
		writeMessage(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (h *Handler) containerListFrom(ctx context.Context, psStdout string) ([]any, error) {
	rows, err := DecodeNDJSONBytes([]byte(psStdout))
	if err != nil {
		return nil, err
	}
	parsed := make([]psEntry, 0, len(rows))
	ids := make([]string, 0, len(rows))
	for _, raw := range rows {
		var row psEntry
		if err := json.Unmarshal(raw, &row); err != nil {
			return nil, err
		}
		if row.ID == "" {
			continue
		}
		parsed = append(parsed, row)
		ids = append(ids, row.ID)
	}
	if len(parsed) == 0 {
		return []any{}, nil
	}
	inspected, err := h.inspectMany(ctx, "container", ids)
	if err != nil {
		return nil, err
	}
	list := make([]any, 0, len(parsed))
	for _, row := range parsed {
		obj, ok := inspected[row.ID]
		if !ok {
			log.Warn().Str("id", row.ID).Msg("skipping container that disappeared during inspect")
			continue
		}
		list = append(list, containerSummary(row, obj))
	}
	return list, nil
}

func (h *Handler) inspectMany(ctx context.Context, kind string, ids []string) (map[string]map[string]any, error) {
	args := make([]string, 0, len(ids)+3)
	args = append(args, "inspect", "--type", kind)
	args = append(args, ids...)
	stdout, stderr, code, err := h.runner.Run(ctx, args...)
	if err == nil && code == 0 {
		objs, perr := parseInspectArray(stdout)
		if perr == nil {
			return indexInspect(objs, ids), nil
		}
		log.Warn().Err(perr).Str("type", kind).Msg("batch inspect JSON parse failed, falling back")
	} else {
		log.Warn().Err(err).Int("code", code).Str("stderr", strings.TrimSpace(stderr)).Str("type", kind).Msg("batch inspect failed, falling back")
	}

	out := map[string]map[string]any{}
	for _, id := range ids {
		stdout, stderr, code, err := h.runner.Run(ctx, "inspect", "--type", kind, id)
		if err != nil || code != 0 {
			log.Warn().Err(err).Int("code", code).Str("id", id).Str("stderr", strings.TrimSpace(stderr)).Msg("inspect failed")
			continue
		}
		objs, perr := parseInspectArray(stdout)
		if perr != nil || len(objs) == 0 {
			log.Warn().Err(perr).Str("id", id).Msg("inspect returned no object")
			continue
		}
		out[objectID(objs[0], id)] = objs[0]
	}
	return out, nil
}

func indexInspect(objs []map[string]any, ids []string) map[string]map[string]any {
	out := make(map[string]map[string]any, len(objs))
	for i, obj := range objs {
		fallback := ""
		if i < len(ids) {
			fallback = ids[i]
		}
		out[objectID(obj, fallback)] = obj
	}
	return out
}

func containerSummary(row psEntry, inspect map[string]any) map[string]any {
	name := asString(inspect["Name"])
	if name == "" {
		name = row.Names
	}
	if name != "" && !strings.HasPrefix(name, "/") {
		name = "/" + name
	}
	created := unixCreated(inspect, row.CreatedAt)
	state := strings.ToLower(row.State)
	if state == "" {
		state = strings.ToLower(asString(asMap(inspect["State"])["Status"]))
	}
	labels := asMap(asMap(inspect["Config"])["Labels"])
	if labels == nil {
		labels = asMap(inspect["Labels"])
	}
	if labels == nil {
		labels = map[string]any{}
	}
	networks := map[string]any{}
	if nets := asMap(inspect["NetworkSettings"]); nets != nil {
		if n := asMap(nets["Networks"]); n != nil {
			networks = n
		}
	}
	networkMode := ""
	if hc := asMap(inspect["HostConfig"]); hc != nil {
		networkMode = asString(hc["NetworkMode"])
	}
	mounts := listMounts(inspect)
	if mounts == nil {
		mounts = []any{}
	}
	ports := dockerPorts(inspect["Ports"])
	return map[string]any{
		"Id":         row.ID,
		"Names":      []string{name},
		"Image":      row.Image,
		"ImageID":    asString(inspect["Image"]),
		"Command":    commandString(inspect),
		"Created":    created,
		"Ports":      ports,
		"Labels":     labels,
		"State":      state,
		"Status":     row.Status,
		"HostConfig": map[string]any{"NetworkMode": networkMode},
		"NetworkSettings": map[string]any{
			"Networks": networks,
		},
		"Mounts": mounts,
	}
}

func unixCreated(inspect map[string]any, fallback string) int64 {
	if created := asString(inspect["Created"]); created != "" {
		if t, err := ParseTime(created); err == nil {
			return t.Unix()
		}
	}
	if fallback != "" {
		if t, err := ParseTime(fallback); err == nil {
			return t.Unix()
		}
	}
	return 0
}

func commandString(inspect map[string]any) string {
	cfg := asMap(inspect["Config"])
	if cfg == nil {
		return ""
	}
	parts := append([]string{}, stringList(cfg["Entrypoint"])...)
	parts = append(parts, stringList(cfg["Cmd"])...)
	return strings.Join(parts, " ")
}

func listMounts(inspect map[string]any) []any {
	raw, ok := inspect["Mounts"].([]any)
	if !ok {
		return []any{}
	}
	out := make([]any, 0, len(raw))
	for _, item := range raw {
		m := asMap(item)
		if m == nil {
			continue
		}
		rw, _ := m["ReadWrite"].(bool)
		out = append(out, map[string]any{
			"Type":        m["Type"],
			"Name":        m["Name"],
			"Source":      m["Source"],
			"Destination": m["Destination"],
			"Driver":      "",
			"Mode":        "",
			"RW":          rw,
			"Propagation": "",
		})
	}
	return out
}

func dockerPorts(raw any) []map[string]any {
	m := asMap(raw)
	if len(m) == 0 {
		return []map[string]any{}
	}
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]map[string]any, 0, len(keys))
	for _, key := range keys {
		privateText, proto, _ := strings.Cut(key, "/")
		private, _ := strconv.Atoi(privateText)
		bindings, _ := m[key].([]any)
		if len(bindings) == 0 {
			out = append(out, map[string]any{
				"PrivatePort": private,
				"Type":        proto,
			})
			continue
		}
		for _, item := range bindings {
			bm := asMap(item)
			public, _ := strconv.Atoi(asString(bm["HostPort"]))
			out = append(out, map[string]any{
				"IP":          asString(bm["HostIp"]),
				"PrivatePort": private,
				"PublicPort":  public,
				"Type":        proto,
			})
		}
	}
	return out
}

// NormalizeContainer turns a wslc container inspect object into a Docker
// ContainerJSON object: the top-level Ports table moves onto NetworkSettings
// and HostConfig, and fields Portainer deserializes are present.
func NormalizeContainer(obj map[string]any) {
	var ports any
	if p, ok := obj["Ports"]; ok {
		ports = p
		delete(obj, "Ports")
	}
	ns := ensureMap(obj, "NetworkSettings")
	if _, ok := ns["Ports"]; !ok {
		if ports != nil {
			ns["Ports"] = ports
		} else {
			ns["Ports"] = map[string]any{}
		}
	}
	hc := ensureMap(obj, "HostConfig")
	if _, ok := hc["PortBindings"]; !ok {
		if ports != nil {
			hc["PortBindings"] = ports
		} else {
			hc["PortBindings"] = map[string]any{}
		}
	}
	if _, ok := hc["Binds"]; !ok {
		hc["Binds"] = []any{}
	}
	st := ensureMap(obj, "State")
	defaults := map[string]any{
		"Pid":        0,
		"OOMKilled":  false,
		"Error":      "",
		"Dead":       false,
		"Paused":     false,
		"Restarting": false,
	}
	for key, value := range defaults {
		if _, ok := st[key]; !ok {
			st[key] = value
		}
	}
}

func (h *Handler) inspectContainer(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	stdout, stderr, code, err := h.runner.Run(r.Context(), "inspect", "--type", "container", id)
	if !h.success(w, stdout, stderr, code, err) {
		return
	}
	objs, err := parseInspectArray(stdout)
	if err != nil {
		writeMessage(w, http.StatusInternalServerError, err.Error())
		return
	}
	if len(objs) == 0 {
		writeMessage(w, http.StatusNotFound, "wslc agent: no such container: "+id)
		return
	}
	NormalizeContainer(objs[0])
	writeJSON(w, http.StatusOK, objs[0])
}

func (h *Handler) startContainer(w http.ResponseWriter, r *http.Request) {
	h.execNoContent(w, r, "start", r.PathValue("id"))
}

func (h *Handler) stopContainer(w http.ResponseWriter, r *http.Request) {
	h.timedAction(w, r, "stop")
}

func (h *Handler) restartContainer(w http.ResponseWriter, r *http.Request) {
	h.timedAction(w, r, "restart")
}

func (h *Handler) killContainer(w http.ResponseWriter, r *http.Request) {
	args := []string{"kill"}
	if sig := r.URL.Query().Get("signal"); sig != "" {
		args = append(args, "-s", sig)
	}
	args = append(args, r.PathValue("id"))
	h.execNoContent(w, r, args...)
}

func (h *Handler) removeContainer(w http.ResponseWriter, r *http.Request) {
	args := []string{"rm"}
	q := r.URL.Query()
	if truthy(q.Get("force")) {
		args = append(args, "-f")
	}
	if truthy(q.Get("v")) {
		args = append(args, "-v")
	}
	args = append(args, r.PathValue("id"))
	h.execNoContent(w, r, args...)
}

func (h *Handler) timedAction(w http.ResponseWriter, r *http.Request, command string) {
	args := []string{command}
	if t := r.URL.Query().Get("t"); t != "" {
		args = append(args, "-t", t)
	}
	args = append(args, r.PathValue("id"))
	h.execNoContent(w, r, args...)
}

func (h *Handler) execNoContent(w http.ResponseWriter, r *http.Request, args ...string) {
	stdout, stderr, code, err := h.runner.Run(r.Context(), args...)
	if !h.success(w, stdout, stderr, code, err) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
