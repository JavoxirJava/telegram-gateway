package tdjson

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"time"
)

// Error deliberately does not retain upstream messages: they may contain phone
// numbers, paths or authentication details and must not leak into logs.
type Error struct {
	Code       int
	RetryAfter time.Duration
}

func (e *Error) Error() string { return fmt.Sprintf("TDLib request failed (code %d)", e.Code) }

var floodPattern = regexp.MustCompile(`(?i)(?:FLOOD_WAIT_|retry after\s+)([0-9]{1,8})`)

func decodeError(raw []byte) error {
	var v struct {
		Type    string `json:"@type"`
		Code    int    `json:"code"`
		Message string `json:"message"`
	}
	if json.Unmarshal(raw, &v) != nil || v.Type != "error" {
		return nil
	}
	result := &Error{Code: v.Code}
	if v.Code == 429 {
		if match := floodPattern.FindStringSubmatch(v.Message); len(match) == 2 {
			if n, err := strconv.ParseInt(match[1], 10, 64); err == nil && n > 0 {
				result.RetryAfter = time.Duration(n) * time.Second
			}
		}
		// Unknown rate-limit errors must not cause an immediate retry storm.
		if result.RetryAfter == 0 {
			result.RetryAfter = time.Minute
		}
	}
	return result
}
