package netprov

import (
	"strings"
)

// WANCommandText 把一条命令拼成一行（日志与页面提示用）
func WANCommandText(argv []string) string {
	parts := make([]string, 0, len(argv))
	for _, a := range argv {
		if strings.ContainsAny(a, " \t") {
			parts = append(parts, "'"+a+"'")
			continue
		}
		parts = append(parts, a)
	}
	return strings.Join(parts, " ")
}
