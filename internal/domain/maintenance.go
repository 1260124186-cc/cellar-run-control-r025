package domain

import "time"

// MaintenanceState tracks whether a registered maintenance window still
// constrains vessel availability.
type MaintenanceState string

const (
	MaintenanceScheduled MaintenanceState = "scheduled"
	MaintenanceCancelled MaintenanceState = "cancelled"
)

// Blocked reasons expose the single availability fact computed for a vessel.
const (
	BlockedReasonRetired     = "retired"
	BlockedReasonActiveRun   = "active_run"
	BlockedReasonMaintenance = "maintenance_window"
	BlockedReasonVesselState = "vessel_state"
)

// MaintenanceWindow is a future or historical maintenance interval attached to
// a vessel. Time comparisons use the half-open interval [StartsAt, EndsAt).
type MaintenanceWindow struct {
	ID          string           `json:"id"`
	StartsAt    time.Time        `json:"starts_at"`
	EndsAt      time.Time        `json:"ends_at"`
	Note        string           `json:"note,omitempty"`
	State       MaintenanceState `json:"state"`
	CreatedAt   time.Time        `json:"created_at"`
	CancelledAt *time.Time       `json:"cancelled_at,omitempty"`
}

func (w MaintenanceWindow) Clone() MaintenanceWindow {
	cloned := w
	if w.CancelledAt != nil {
		value := *w.CancelledAt
		cloned.CancelledAt = &value
	}
	return cloned
}

func (w MaintenanceWindow) IsScheduled() bool {
	return w.State == MaintenanceScheduled
}

// ActiveAt reports whether the window is scheduled and currently in effect.
// A reservation at exactly StartsAt is blocked; a reservation at EndsAt is not.
func (w MaintenanceWindow) ActiveAt(at time.Time) bool {
	stamp := at.UTC()
	return w.IsScheduled() && !stamp.Before(w.StartsAt) && stamp.Before(w.EndsAt)
}

// FutureAt reports whether the window is scheduled and has not started.
func (w MaintenanceWindow) FutureAt(at time.Time) bool {
	return w.IsScheduled() && w.StartsAt.After(at.UTC())
}

// OpenAt reports whether the scheduled window has not yet ended.
func (w MaintenanceWindow) OpenAt(at time.Time) bool {
	return w.IsScheduled() && w.EndsAt.After(at.UTC())
}

// WindowsOverlap reports whether two half-open intervals intersect. Windows
// that touch only at a boundary (one ends when the other starts) do not
// overlap, and cancelled windows never constrain anything.
func WindowsOverlap(a, b MaintenanceWindow) bool {
	if !a.IsScheduled() || !b.IsScheduled() {
		return false
	}
	return a.StartsAt.Before(b.EndsAt) && b.StartsAt.Before(a.EndsAt)
}

// ValidateMaintenanceWindow checks the shape of a window requested at now.
// Scheduling must be done ahead of time: StartsAt must be strictly in the
// future, and EndsAt must be strictly after StartsAt.
func ValidateMaintenanceWindow(start, end time.Time, note string, now time.Time) error {
	from := start.UTC()
	to := end.UTC()
	stamp := now.UTC()
	if from.IsZero() || to.IsZero() {
		return FieldError(CodeInvalidInput, "starts_at",
			"maintenance window start and end are required")
	}
	if !from.After(stamp) {
		return FieldError(CodeInvalidInput, "starts_at",
			"maintenance window must begin in the future")
	}
	if !to.After(from) {
		return FieldError(CodeInvalidInput, "ends_at",
			"maintenance window must end after it starts")
	}
	if len([]rune(note)) > 1000 {
		return FieldError(CodeInvalidInput, "note",
			"must not exceed 1000 characters")
	}
	return nil
}
