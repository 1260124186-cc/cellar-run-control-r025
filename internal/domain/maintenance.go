package domain

import (
	"sort"
	"time"
)

// MaintenanceWindow reserves a vessel for planned upkeep. Windows use
// half-open semantics: a window covers starts_at <= t < ends_at.
type MaintenanceWindow struct {
	ID        string    `json:"id"`
	StartsAt  time.Time `json:"starts_at"`
	EndsAt    time.Time `json:"ends_at"`
	Reason    string    `json:"reason,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

func (w MaintenanceWindow) Validate(now time.Time) error {
	startsAt := w.StartsAt.UTC()
	endsAt := w.EndsAt.UTC()
	if !startsAt.After(now.UTC()) {
		return FieldError(CodeInvalidInput, "starts_at",
			"must be in the future")
	}
	if !endsAt.After(startsAt) {
		return FieldError(CodeInvalidInput, "ends_at",
			"must be later than starts_at")
	}
	if len([]rune(w.Reason)) > 500 {
		return FieldError(CodeInvalidInput, "reason",
			"must not exceed 500 characters")
	}
	return nil
}

// Covers reports whether the window contains the given moment.
func (w MaintenanceWindow) Covers(moment time.Time) bool {
	instant := moment.UTC()
	return !instant.Before(w.StartsAt.UTC()) && instant.Before(w.EndsAt.UTC())
}

// WindowsOverlap reports whether two windows share any moment. Windows that
// only touch at a boundary do not overlap.
func WindowsOverlap(a, b MaintenanceWindow) bool {
	return a.StartsAt.UTC().Before(b.EndsAt.UTC()) &&
		b.StartsAt.UTC().Before(a.EndsAt.UTC())
}

// SortMaintenanceWindows orders windows by start time so every read of a
// vessel presents the same deterministic sequence.
func SortMaintenanceWindows(windows []MaintenanceWindow) {
	sort.Slice(windows, func(i, j int) bool {
		if windows[i].StartsAt.Equal(windows[j].StartsAt) {
			return windows[i].ID < windows[j].ID
		}
		return windows[i].StartsAt.Before(windows[j].StartsAt)
	})
}
