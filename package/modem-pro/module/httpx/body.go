package httpx

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// DecodeBody 解析 POST JSON 请求体
func DecodeBody(r *http.Request, v any) error {
	if r.Method != http.MethodPost {
		return fmt.Errorf("method not allowed, use POST")
	}
	return json.NewDecoder(r.Body).Decode(v)
}
