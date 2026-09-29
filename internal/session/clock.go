package session

import "time"

// tick 独立成函数，方便演示时调整节奏。
func tick() <-chan time.Time { return time.After(40 * time.Millisecond) }
