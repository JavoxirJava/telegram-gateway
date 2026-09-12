package telegram

import "errors"

// Validate prevents a buggy adapter from turning a filtered page into EOF or
// returning a cursor that can loop/reinsert messages indefinitely.
func (p HistoryPage) Validate(before int64, limit int) error {
	if before < 0 || limit < 1 || limit > 100 || p.SourceCount < 0 || p.SourceCount > 100 || len(p.Items) > limit || len(p.Items) > p.SourceCount {
		return errors.New("invalid history page bounds")
	}
	if p.Exhausted {
		if p.SourceCount != 0 || len(p.Items) != 0 || p.NextBeforeMessageID != 0 {
			return errors.New("invalid exhausted history page")
		}
		return nil
	}
	if p.SourceCount == 0 || p.NextBeforeMessageID <= 0 || (before > 0 && p.NextBeforeMessageID >= before) {
		return errors.New("history source cursor did not advance")
	}
	seen := map[int64]bool{}
	for _, m := range p.Items {
		id := m.TelegramMessageID
		if id <= 0 || id < p.NextBeforeMessageID || (before > 0 && id >= before) || seen[id] || m.SentAt.IsZero() {
			return errors.New("invalid history message identity or cursor")
		}
		seen[id] = true
	}
	return nil
}
