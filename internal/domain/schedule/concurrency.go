package schedule

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
)

// SlotKey SKIP/QUEUE 单槽位键（docs: task:<id> / service:<id> / task:<id>:key:<sha1>）。
func SlotKey(scope string, taskID, serviceID int64, idempotencyKey string) string {
	switch scope {
	case "SERVICE":
		return fmt.Sprintf("service:%d", serviceID)
	case "IDEMPOTENCY_KEY":
		sum := sha1.Sum([]byte(idempotencyKey))
		return fmt.Sprintf("task:%d:key:%s", taskID, hex.EncodeToString(sum[:]))
	default:
		return fmt.Sprintf("task:%d", taskID)
	}
}
