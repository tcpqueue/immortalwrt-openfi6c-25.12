package kernel

import (
	"crypto/subtle"
	"net"
	"net/http"
	"time"
)

// ====================== 失败节流 ======================

func smsClientIP(r *http.Request) string {
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

// smsUnlockAllowed 当前是否允许尝试解锁（返回还要等多久）
func smsUnlockAllowed(ip string) (time.Duration, bool) {
	smsAuthMu.Lock()
	defer smsAuthMu.Unlock()
	if until, ok := smsFailUntil[ip]; ok {
		if left := time.Until(until); left > 0 {
			return left, false
		}
		delete(smsFailUntil, ip)
		delete(smsFailCount, ip)
	}
	return 0, true
}

// smsUnlockFailed 记一次失败，返回（已连续失败次数, 是否刚触发锁定）
func smsUnlockFailed(ip string) (int, bool) {
	smsAuthMu.Lock()
	defer smsAuthMu.Unlock()
	smsFailCount[ip]++
	if smsFailCount[ip] >= smsFailMax {
		smsFailUntil[ip] = time.Now().Add(smsFailLockFor)
		smsFailCount[ip] = 0
		return smsFailMax, true
	}
	return smsFailCount[ip], false
}

func smsUnlockSucceeded(ip string) {
	smsAuthMu.Lock()
	delete(smsFailCount, ip)
	delete(smsFailUntil, ip)
	smsAuthMu.Unlock()
}

// smsPasswordMatch 常量时间比较（长度不同也走同样的分支）
func smsPasswordMatch(got string) bool {
	want := smsPasswordText()
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}
