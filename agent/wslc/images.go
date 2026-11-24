package wslc

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
)

type imageEntry struct {
	ID         string
	Repository string
	Tag        string
	CreatedAt  string
	Containers string
}

func (h *Handler) listImages(w http.ResponseWriter, r *http.Request) {
	stdout, stderr, code, err := h.runner.Run(r.Context(), "images", "--format", "json", "--no-trunc")
	if !h.success(w, stdout, stderr, code, err) {
		return
	}
	rows, err := DecodeNDJSONBytes([]byte(stdout))
	if err != nil {
		writeMessage(w, http.StatusInternalServerError, err.Error())
		return
	}
	if len(rows) == 0 {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	ids := imageIDs(rows)
	inspected, err := h.inspectMany(r.Context(), "image", ids)
	if err != nil {
		writeMessage(w, http.StatusInternalServerError, err.Error())
		return
	}
	rawInspect, err := json.Marshal(mapsToSlice(inspected))
	if err != nil {
		writeMessage(w, http.StatusInternalServerError, err.Error())
		return
	}
	list, err := BuildImageList([]byte(stdout), rawInspect)
	if err != nil {
		writeMessage(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, filterImages(list, r.URL.Query().Get("filters")))
}

func imageIDs(rows []json.RawMessage) []string {
	seen := map[string]struct{}{}
	ids := make([]string, 0, len(rows))
	for _, raw := range rows {
		var row imageEntry
		if json.Unmarshal(raw, &row) != nil || row.ID == "" {
			continue
		}
		if _, ok := seen[row.ID]; ok {
			continue
		}
		seen[row.ID] = struct{}{}
		ids = append(ids, row.ID)
	}
	return ids
}

func mapsToSlice(m map[string]map[string]any) []map[string]any {
	out := make([]map[string]any, 0, len(m))
	for _, obj := range m {
		out = append(out, obj)
	}
	return out
}

// BuildImageList merges NDJSON image rows that share an ID. Size comes from
// inspect (integer bytes). The rounded text size on the list row is ignored.
func BuildImageList(listJSON, inspectJSON []byte) ([]map[string]any, error) {
	rows, err := DecodeNDJSONBytes(listJSON)
	if err != nil {
		return nil, err
	}
	inspected := map[string]map[string]any{}
	if len(strings.TrimSpace(string(inspectJSON))) > 0 {
		objs, err := parseInspectArray(string(inspectJSON))
		if err != nil {
			return nil, err
		}
		for _, obj := range objs {
			inspected[objectID(obj, "")] = obj
		}
	}

	type acc struct {
		entry    imageEntry
		tags     []string
		seenTags map[string]struct{}
		created  int64
		count    int
	}
	order := make([]string, 0)
	byID := map[string]*acc{}
	for _, raw := range rows {
		var row imageEntry
		if err := json.Unmarshal(raw, &row); err != nil {
			return nil, err
		}
		if row.ID == "" {
			continue
		}
		item := byID[row.ID]
		if item == nil {
			item = &acc{entry: row, seenTags: map[string]struct{}{}}
			byID[row.ID] = item
			order = append(order, row.ID)
			if t, err := ParseTime(row.CreatedAt); err == nil {
				item.created = t.Unix()
			}
		}
		if n, err := strconv.Atoi(strings.TrimSpace(row.Containers)); err == nil && n > item.count {
			item.count = n
		}
		if row.Repository == "" || row.Repository == "<none>" || row.Tag == "" || row.Tag == "<none>" {
			continue
		}
		tag := row.Repository + ":" + row.Tag
		if _, ok := item.seenTags[tag]; ok {
			continue
		}
		item.seenTags[tag] = struct{}{}
		item.tags = append(item.tags, tag)
	}

	out := make([]map[string]any, 0, len(order))
	for _, id := range order {
		item := byID[id]
		if item.tags == nil {
			item.tags = []string{}
		}
		obj := inspected[id]
		size := asInt64(obj["Size"])
		repoDigests := stringList(obj["RepoDigests"])
		if repoDigests == nil {
			repoDigests = []string{}
		}
		labels := map[string]any{}
		if cfg := asMap(obj["Config"]); cfg != nil {
			if l := asMap(cfg["Labels"]); l != nil {
				labels = l
			}
		}
		out = append(out, map[string]any{
			"Id":          id,
			"ParentId":    asString(obj["Parent"]),
			"RepoTags":    item.tags,
			"RepoDigests": repoDigests,
			"Created":     item.created,
			"Size":        size,
			"VirtualSize": size,
			"SharedSize":  int64(-1),
			"Labels":      labels,
			"Containers":  item.count,
		})
	}
	return out, nil
}

func (h *Handler) imageByName(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !strings.HasSuffix(name, "/json") {
		h.notImplemented(w, r)
		return
	}
	h.inspectImage(w, r, strings.TrimSuffix(name, "/json"))
}

func (h *Handler) inspectImage(w http.ResponseWriter, r *http.Request, name string) {
	stdout, stderr, code, err := h.runner.Run(r.Context(), "inspect", "--type", "image", name)
	if !h.success(w, stdout, stderr, code, err) {
		return
	}
	objs, err := parseInspectArray(stdout)
	if err != nil {
		writeMessage(w, http.StatusInternalServerError, err.Error())
		return
	}
	if len(objs) == 0 {
		writeMessage(w, http.StatusNotFound, "wslc agent: no such image: "+name)
		return
	}
	writeJSON(w, http.StatusOK, objs[0])
}

func (h *Handler) pullImage(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	ref := imageRef(q.Get("fromImage"), q.Get("tag"))
	if ref == "" {
		writeMessage(w, http.StatusBadRequest, "wslc agent: fromImage is required")
		return
	}
	if err := h.acquire(r.Context()); err != nil {
		writeMessage(w, http.StatusTooManyRequests, "wslc agent: too many streaming commands")
		return
	}
	defer h.release()
	// X-Registry-Auth is not decoded. Pull a private image only after wslc login
	// on this machine; credentials stay in the user credential store.
	h.streamText(w, r, []string{"pull", ref}, pullStatusLine)
}

func imageRef(fromImage, tag string) string {
	fromImage = strings.TrimSpace(fromImage)
	tag = strings.TrimSpace(tag)
	if fromImage == "" {
		return ""
	}
	if tag == "" || strings.Contains(fromImage, "@") || refHasTag(fromImage) {
		return fromImage
	}
	return fromImage + ":" + tag
}

func refHasTag(ref string) bool {
	slash := strings.LastIndex(ref, "/")
	return strings.Contains(ref[slash+1:], ":")
}

func pullStatusLine(line string) []byte {
	line = strings.TrimSpace(line)
	if line == "" {
		return nil
	}
	var obj map[string]any
	if json.Unmarshal([]byte(line), &obj) == nil {
		if _, ok := obj["status"]; ok {
			encoded, err := json.Marshal(obj)
			if err == nil {
				return append(encoded, '\n')
			}
		}
	}
	encoded, err := json.Marshal(map[string]string{"status": line})
	if err != nil {
		return nil
	}
	return append(encoded, '\n')
}

func (h *Handler) removeImage(w http.ResponseWriter, r *http.Request) {
	args := []string{"rmi"}
	if truthy(r.URL.Query().Get("force")) {
		args = append(args, "-f")
	}
	args = append(args, r.PathValue("name"))
	stdout, stderr, code, err := h.runner.Run(r.Context(), args...)
	if !h.success(w, stdout, stderr, code, err) {
		return
	}
	// wslc rmi text is not mapped onto Untagged/Deleted yet. An empty array is
	// a legal Docker response.
	writeJSON(w, http.StatusOK, []any{})
}
