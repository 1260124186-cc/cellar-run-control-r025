package domain

import "time"

type VesselState string

const (
	VesselAvailable VesselState = "available"
	VesselReserved  VesselState = "reserved"
	VesselInUse     VesselState = "in_use"
	VesselCleaning  VesselState = "cleaning"
	VesselRetired   VesselState = "retired"
)

type Vessel struct {
	ID          string              `json:"id"`
	Code        string              `json:"code"`
	CapacityL   float64             `json:"capacity_liters"`
	State       VesselState         `json:"state"`
	ActiveRunID *string             `json:"active_run_id,omitempty"`
	Maintenance []MaintenanceWindow `json:"maintenance_windows"`
	Version     int64               `json:"version"`
	CreatedAt   time.Time           `json:"created_at"`
	UpdatedAt   time.Time           `json:"updated_at"`
}

func (v Vessel) Clone() Vessel {
	cloned := v
	if v.ActiveRunID != nil {
		runID := *v.ActiveRunID
		cloned.ActiveRunID = &runID
	}
	cloned.Maintenance = append([]MaintenanceWindow{}, v.Maintenance...)
	return cloned
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

func (v *Vessel) Reserve(runID string, now time.Time) error {
	if v.State != VesselAvailable || v.ActiveRunID != nil {
		return NewError(CodeVesselUnavailable, "vessel is not available for reservation")
	}
	if v.ActiveMaintenance(now) != nil {
		return NewError(CodeVesselUnavailable,
			"vessel is under scheduled maintenance")
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
	if v.ActiveMaintenance(now) != nil {
		return NewError(CodeVesselUnavailable,
			"vessel is under scheduled maintenance")
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

func (v *Vessel) Retire(now time.Time) error {
	if v.State == VesselRetired {
		return NewError(CodeVesselState, "vessel is already retired")
	}
	if v.State != VesselAvailable || v.ActiveRunID != nil {
		return NewError(CodeVesselState, "only an available vessel can be retired")
	}
	if v.HasPendingMaintenance(now) {
		return NewError(CodeVesselState,
			"cancel scheduled maintenance before retiring the vessel")
	}
	v.State = VesselRetired
	v.UpdatedAt = now.UTC()
	v.Version++
	return nil
}

// ScheduleMaintenance registers a future maintenance window. Only an
// available vessel accepts new windows, so a reserved or started run can
// never be silently displaced by a maintenance booking.
func (v *Vessel) ScheduleMaintenance(window MaintenanceWindow, now time.Time) error {
	if v.State != VesselAvailable || v.ActiveRunID != nil {
		return NewError(CodeVesselState,
			"maintenance can only be scheduled on an available vessel")
	}
	if err := window.Validate(now); err != nil {
		return err
	}
	for _, existing := range v.Maintenance {
		if WindowsOverlap(existing, window) {
			return NewError(CodeMaintenanceConflict,
				"maintenance window overlaps an existing window on this vessel")
		}
	}
	v.Maintenance = append(v.Maintenance, window)
	SortMaintenanceWindows(v.Maintenance)
	v.UpdatedAt = now.UTC()
	v.Version++
	return nil
}

// CancelMaintenance removes a window by identifier. Canceling is allowed in
// any vessel state so operators can always release a blocked vessel.
func (v *Vessel) CancelMaintenance(windowID string, now time.Time) error {
	for index, window := range v.Maintenance {
		if window.ID == windowID {
			v.Maintenance = append(v.Maintenance[:index], v.Maintenance[index+1:]...)
			v.UpdatedAt = now.UTC()
			v.Version++
			return nil
		}
	}
	return NewError(CodeMaintenanceNotFound, "maintenance window was not found")
}

// ActiveMaintenance returns the window covering the given moment, if any.
func (v Vessel) ActiveMaintenance(now time.Time) *MaintenanceWindow {
	for index := range v.Maintenance {
		if v.Maintenance[index].Covers(now) {
			window := v.Maintenance[index]
			return &window
		}
	}
	return nil
}

// HasPendingMaintenance reports whether any window is still upcoming or in
// progress at the given moment.
func (v Vessel) HasPendingMaintenance(now time.Time) bool {
	instant := now.UTC()
	for _, window := range v.Maintenance {
		if window.EndsAt.UTC().After(instant) {
			return true
		}
	}
	return false
}
