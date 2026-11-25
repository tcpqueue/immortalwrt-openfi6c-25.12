package httpx

import (
	"encoding/json"
	"net/http"
)

// writeJSON 统一 JSON 响应
func WriteJSON(w http.ResponseWriter, code int, data any, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"code": code,
		"data": data,
		"msg":  msg,
	})
}
