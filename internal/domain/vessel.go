package domain

import (
	"sort"
	"time"
)

type VesselState string

const (
	VesselAvailable VesselState = "available"
	VesselReserved  VesselState = "reserved"
	VesselInUse     VesselState = "in_use"
	VesselCleaning  VesselState = "cleaning"
	VesselRetired   VesselState = "retired"
)

type Vessel struct {
	ID          string      `json:"id"`
	Code        string      `json:"code"`
	CapacityL   float64     `json:"capacity_liters"`
	State       VesselState `json:"state"`
	ActiveRunID *string     `json:"active_run_id,omitempty"`
	// MaintenanceWindows is the full schedule history of the vessel, including
	// cancelled and expired windows. It is ordered on every read.
	MaintenanceWindows []MaintenanceWindow `json:"maintenance_windows"`
	// Availability facts derived from state, run association, and schedule at
	// the moment the aggregate is presented. They are recomputed before each
	// read and are never the source of a decision.
	AvailableForReservation bool      `json:"available_for_reservation"`
	BlockedReason           string    `json:"blocked_reason,omitempty"`
	ActiveMaintenanceID     *string   `json:"active_maintenance_window_id,omitempty"`
	NextMaintenanceID       *string   `json:"next_maintenance_window_id,omitempty"`
	Version                 int64     `json:"version"`
	CreatedAt               time.Time `json:"created_at"`
	UpdatedAt               time.Time `json:"updated_at"`
}

func (v Vessel) Clone() Vessel {
	cloned := v
	if v.ActiveRunID != nil {
		runID := *v.ActiveRunID
		cloned.ActiveRunID = &runID
	}
	if v.MaintenanceWindows != nil {
		cloned.MaintenanceWindows = make([]MaintenanceWindow, len(v.MaintenanceWindows))
		for index, window := range v.MaintenanceWindows {
			cloned.MaintenanceWindows[index] = window.Clone()
		}
	}
	cloned.ActiveMaintenanceID = cloneStringPtr(v.ActiveMaintenanceID)
	cloned.NextMaintenanceID = cloneStringPtr(v.NextMaintenanceID)
	return cloned
}

func cloneStringPtr(value *string) *string {
	if value == nil {
		return nil
	}
	copied := *value
	return &copied
}

func (v Vessel) Validate() error {
	if err := ValidateVesselCode(v.Code, "code"); err != nil {
		return err
	}
	if v.CapacityL < 10 || v.CapacityL > 5000 {
		return FieldError(CodeInvalidInput, "capacity_liters",
			"must be between 10 and 5000 liters")
	}
	return nil
}

// SortedWindows returns the maintenance windows ordered by start time, then by
// identifier, without changing the stored slice.
func (v Vessel) SortedWindows() []MaintenanceWindow {
	windows := make([]MaintenanceWindow, len(v.MaintenanceWindows))
	copy(windows, v.MaintenanceWindows)
	sort.Slice(windows, func(i, j int) bool {
		if !windows[i].StartsAt.Equal(windows[j].StartsAt) {
			return windows[i].StartsAt.Before(windows[j].StartsAt)
		}
		return windows[i].ID < windows[j].ID
	})
	return windows
}

func (v Vessel) findWindow(id string) (int, bool) {
	for index := range v.MaintenanceWindows {
		if v.MaintenanceWindows[index].ID == id {
			return index, true
		}
	}
	return 0, false
}

func (v Vessel) activeScheduledWindow(at time.Time) *MaintenanceWindow {
	for index := range v.MaintenanceWindows {
		window := &v.MaintenanceWindows[index]
		if window.ActiveAt(at) {
			return window
		}
	}
	return nil
}

func (v Vessel) nextScheduledWindow(at time.Time) *MaintenanceWindow {
	var next *MaintenanceWindow
	for index := range v.MaintenanceWindows {
		window := &v.MaintenanceWindows[index]
		if !window.FutureAt(at) {
			continue
		}
		if next == nil || window.StartsAt.Before(next.StartsAt) ||
			(window.StartsAt.Equal(next.StartsAt) && window.ID < next.ID) {
			next = window
		}
	}
	return next
}

// HasOpenScheduledMaintenance reports whether a scheduled window is active or
// still in the future. Retiring is blocked while this is true.
func (v Vessel) HasOpenScheduledMaintenance(at time.Time) bool {
	for index := range v.MaintenanceWindows {
		if v.MaintenanceWindows[index].OpenAt(at) {
			return true
		}
	}
	return false
}

// EvaluateAvailability is the single source of the reservation availability
// fact shown in vessel details, lists, and run-reservation responses. It does
// not mutate state; callers present the returned copy.
func (v Vessel) EvaluateAvailability(at time.Time) Vessel {
	presented := v.Clone()
	presented.MaintenanceWindows = presented.SortedWindows()
	presented.ActiveMaintenanceID = nil
	presented.NextMaintenanceID = nil

	var blocked string
	switch {
	case v.State == VesselRetired:
		blocked = BlockedReasonRetired
	case v.ActiveRunID != nil:
		blocked = BlockedReasonActiveRun
	case v.State != VesselAvailable:
		blocked = BlockedReasonVesselState
	}

	if active := presented.activeScheduledWindow(at); active != nil {
		id := active.ID
		presented.ActiveMaintenanceID = &id
		blocked = BlockedReasonMaintenance
	}
	if next := presented.nextScheduledWindow(at); next != nil {
		id := next.ID
		presented.NextMaintenanceID = &id
	}

	presented.AvailableForReservation = blocked == ""
	if blocked != "" {
		presented.BlockedReason = blocked
	} else {
		presented.BlockedReason = ""
	}
	return presented
}

func (v *Vessel) Reserve(runID string, now time.Time) error {
	if v.State != VesselAvailable || v.ActiveRunID != nil {
		return NewError(CodeVesselUnavailable, "vessel is not available for reservation")
	}
	if active := v.activeScheduledWindow(now); active != nil {
		return NewError(CodeMaintenanceConflict,
			"vessel is inside a scheduled maintenance window")
	}
	run := runID
	v.State = VesselReserved
	v.ActiveRunID = &run
	v.UpdatedAt = now.UTC()
	v.Version++
	return nil
}

func (v *Vessel) StartRun(runID string, now time.Time) error {
	if v.State != VesselReserved || v.ActiveRunID == nil || *v.ActiveRunID != runID {
		return NewError(CodeVesselState, "vessel is not reserved by this run")
	}
	v.State = VesselInUse
	v.UpdatedAt = now.UTC()
	v.Version++
	return nil
}

func (v *Vessel) StartCleaning(runID string, now time.Time) error {
	if (v.State != VesselInUse && v.State != VesselReserved) ||
		v.ActiveRunID == nil || *v.ActiveRunID != runID {
		return NewError(CodeVesselState, "vessel cannot enter cleaning for this run")
	}
	v.State = VesselCleaning
	v.UpdatedAt = now.UTC()
	v.Version++
	return nil
}

func (v *Vessel) FinishCleaning(now time.Time) error {
	if v.State != VesselCleaning || v.ActiveRunID == nil {
		return NewError(CodeVesselState, "vessel is not waiting for cleaning")
	}
	v.State = VesselAvailable
	v.ActiveRunID = nil
	v.UpdatedAt = now.UTC()
	v.Version++
	return nil
}

func (v *Vessel) ReleaseReservation(runID string, now time.Time) error {
	if v.State != VesselReserved && v.State != VesselInUse {
		return NewError(CodeVesselState, "vessel does not have a releasable run")
	}
	if v.ActiveRunID == nil || *v.ActiveRunID != runID {
		return NewError(CodeVesselState, "vessel belongs to a different run")
	}
	v.State = VesselAvailable
	v.ActiveRunID = nil
	v.UpdatedAt = now.UTC()
	v.Version++
	return nil
}

// ScheduleMaintenance registers a future window. Maintenance can only be
// planned for an available vessel without a run: an existing run is never
// displaced by a maintenance arrangement.
func (v *Vessel) ScheduleMaintenance(window MaintenanceWindow, now time.Time) error {
	if v.State != VesselAvailable || v.ActiveRunID != nil {
		return NewError(CodeMaintenanceConflict,
			"maintenance can only be scheduled for an available vessel without a run")
	}
	for _, existing := range v.MaintenanceWindows {
		if WindowsOverlap(existing, window) {
			return NewError(CodeMaintenanceConflict,
				"maintenance window overlaps an existing scheduled window")
		}
	}
	v.MaintenanceWindows = append(v.MaintenanceWindows, window)
	v.UpdatedAt = now.UTC()
	v.Version++
	return nil
}

// CancelMaintenance marks a scheduled window cancelled. A window that has
// already ended cannot be cancelled, and cancelling twice is a stable
// conflict rather than a silent no-op.
func (v *Vessel) CancelMaintenance(windowID string, now time.Time) error {
	index, found := v.findWindow(windowID)
	if !found {
		return NewError(CodeMaintenanceNotFound, "maintenance window was not found")
	}
	window := &v.MaintenanceWindows[index]
	if !window.IsScheduled() {
		return NewError(CodeMaintenanceConflict,
			"maintenance window is already cancelled")
	}
	if !window.OpenAt(now) {
		return NewError(CodeMaintenanceConflict,
			"maintenance window has already ended")
	}
	stamp := now.UTC()
	window.State = MaintenanceCancelled
	window.CancelledAt = &stamp
	v.UpdatedAt = stamp
	v.Version++
	return nil
}

func (v *Vessel) Retire(now time.Time) error {
	if v.State == VesselRetired {
		return NewError(CodeVesselState, "vessel is already retired")
	}
	if v.State != VesselAvailable || v.ActiveRunID != nil {
		return NewError(CodeVesselState, "only an available vessel can be retired")
	}
	if v.HasOpenScheduledMaintenance(now) {
		return NewError(CodeMaintenanceConflict,
			"open maintenance windows must be cancelled or expire before retirement")
	}
	v.State = VesselRetired
	v.UpdatedAt = now.UTC()
	v.Version++
	return nil
}
