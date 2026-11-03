package compat

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/agentshield-ai/agentshield/internal/weft/refs"
	"github.com/agentshield-ai/agentshield/internal/weft/store"
)

// weave.init() talks to the W&B GraphQL API (WANDB_BASE_URL/graphql) to
// resolve the default entity and create the project, then sends traces to
// the trace server (WF_TRACE_SERVER_URL). Pointing both at Weft makes the
// stock client work. Only the handful of operations the client issues are
// answered.

type graphqlReq struct {
	Query         string         `json:"query"`
	Variables     map[string]any `json:"variables"`
	OperationName string         `json:"operationName"`
}

func (a *Adapter) graphql(w http.ResponseWriter, r *http.Request) {
	var req graphqlReq
	if err := decodeBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	user := "default"
	if u := a.host.User(r, nil); u != nil {
		user = *u
	}
	q := req.Query
	data := map[string]any{}
	handled := false
	str := func(k string) string { v, _ := req.Variables[k].(string); return v }

	if strings.Contains(q, "serverInfo") {
		scheme := "http"
		if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
			scheme = "https"
		}
		data["serverInfo"] = map[string]any{"frontendHost": scheme + "://" + r.Host}
		handled = true
	}
	if strings.Contains(q, "viewer") {
		data["viewer"] = map[string]any{"username": user, "defaultEntity": map[string]any{"name": user}}
		handled = true
	}
	if strings.Contains(q, "entity(") {
		data["entity"] = map[string]any{"name": str("entityName"), "readOnly": false}
		handled = true
	}
	if strings.Contains(q, "upsertModel") {
		ent, name := str("entity"), str("name")
		if ent == "" {
			ent = user
		}
		pid, err := refs.NormalizeProject(ent + "/" + name)
		if err != nil {
			writeGraphQLError(w, err.Error())
			return
		}
		if err := a.host.Store().EnsureProject(r.Context(), pid); err != nil {
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		data["upsertModel"] = map[string]any{"model": map[string]any{"name": name, "description": req.Variables["description"]}, "inserted": true}
		handled = true
	}
	if strings.Contains(q, "project(") && !strings.Contains(q, "upsertModel") {
		data["project"] = map[string]any{"name": str("name"), "entityName": str("entityName")}
		handled = true
	}
	if !handled {
		writeGraphQLError(w, "operation not supported by Weft's compatibility adapter")
		return
	}
	writeJSON(w, map[string]any{"data": data})
}

func writeGraphQLError(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"data": nil, "errors": []any{map[string]any{"message": msg}}})
}

// ---------------------------------------------------------------- plumbing

const maxJSONBody = 64 << 20

func decodeBody(r *http.Request, v any) error {
	body, err := io.ReadAll(http.MaxBytesReader(nil, r.Body, maxJSONBody))
	if err != nil {
		return err
	}
	if len(bytes.TrimSpace(body)) == 0 {
		body = []byte("{}")
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	return dec.Decode(v)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func writeError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"reason": msg})
}

func writeStoreErr(w http.ResponseWriter, err error) {
	var inv *store.InvalidError
	if ok := asInvalid(err, &inv); ok {
		writeError(w, http.StatusBadRequest, inv.Msg)
		return
	}
	writeError(w, http.StatusInternalServerError, "internal error")
}
