package main

import (
	"encoding/json"
	"net/http"
)

type errorBody struct {
	Error struct {
		Status  int    `json:"status"`
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func errorCode(status int) string {
	switch status {
	case 400:
		return "invalid_request"
	case 401:
		return "unauthorized"
	case 403:
		return "forbidden"
	case 404:
		return "not_found"
	case 405:
		return "method_not_allowed"
	case 413:
		return "body_too_large"
	case 415:
		return "unsupported_media_type"
	case 422:
		return "validation_failed"
	case 429:
		return "rate_limited"
	case 503:
		return "unavailable"
	default:
		return "internal_error"
	}
}

// Normalize errors from every inner layer, including production/proxy/router,
// without buffering or disclosing their bodies. The API has no streaming endpoints.
type contractWriter struct {
	http.ResponseWriter
	method string
	status int
}

func (w *contractWriter) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
	if status < 400 {
		w.ResponseWriter.WriteHeader(status)
		return
	}
	body := errorBody{}
	body.Error.Status, body.Error.Code, body.Error.Message = status, errorCode(status), http.StatusText(status)
	w.Header().Del("Content-Length")
	w.Header().Del("Content-Encoding")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.ResponseWriter.WriteHeader(status)
	if w.method != http.MethodHead {
		_ = json.NewEncoder(w.ResponseWriter).Encode(body)
	}
}

func (w *contractWriter) Write(value []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	if w.status >= 400 || w.method == http.MethodHead {
		return len(value), nil
	}
	return w.ResponseWriter.Write(value)
}

func errorContract(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		writer := &contractWriter{ResponseWriter: w, method: r.Method}
		defer func() {
			if recover() != nil {
				if writer.status != 0 {
					panic(http.ErrAbortHandler)
				}
				clear(w.Header())
				w.Header().Set("X-Content-Type-Options", "nosniff")
				writer.WriteHeader(http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(writer, r)
	})
}
