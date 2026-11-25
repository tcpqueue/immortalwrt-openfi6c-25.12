package kernel

import (
	"sync"
	"time"
)

// ====================== 去抖 ======================

var (
	pushDebounceMu     sync.Mutex
	pushDebounceTimers = map[string]*time.Timer{}
)

// pushDebounced 同一 key 在 d 时间内只执行最后一次，避免密集推送
func pushDebounced(key string, d time.Duration, fn func()) {
	pushDebounceMu.Lock()
	defer pushDebounceMu.Unlock()
	if t := pushDebounceTimers[key]; t != nil {
		t.Stop()
	}
	pushDebounceTimers[key] = time.AfterFunc(d, func() {
		pushDebounceMu.Lock()
		delete(pushDebounceTimers, key)
		pushDebounceMu.Unlock()
		fn()
	})
}
