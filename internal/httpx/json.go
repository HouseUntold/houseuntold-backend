package httpx

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
)

const MaxJSONBodyBytes = 1 << 20 // 1 MiB

type Envelope map[string]any

func WriteJSON(w http.ResponseWriter, status int, data any) error {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	return json.NewEncoder(w).Encode(data)
}

func ReadJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, MaxJSONBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	if err := dec.Decode(dst); err != nil {
		return NewBadRequest("invalid JSON payload", err)
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return NewBadRequest("request body must contain a single JSON object", nil)
	}
	return nil
}
