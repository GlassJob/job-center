package httpapi

import (
	"encoding/json"
	"net/http"
)

type envelope struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id,omitempty"`
	Data      any    `json:"data,omitempty"`
	Details   any    `json:"details,omitempty"`
}

func requestID(r *http.Request) string {
	if v := r.Header.Get("X-Request-Id"); v != "" {
		return v
	}
	if op, ok := OperatorFrom(r.Context()); ok && op.RequestID != "" {
		return op.RequestID
	}
	return ""
}

func writeOK(w http.ResponseWriter, r *http.Request, status int, data any) {
	writeJSON(w, status, envelope{Code: "OK", Message: "ok", RequestID: requestID(r), Data: data})
}

func writeAPIError(w http.ResponseWriter, r *http.Request, status int, code, msg string, details any) {
	writeJSON(w, status, envelope{Code: code, Message: msg, RequestID: requestID(r), Details: details})
}

func decodeJSONBody(r *http.Request, dst any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	return dec.Decode(dst)
}
