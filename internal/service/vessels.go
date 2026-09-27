package service

import (
	"sort"
	"time"

	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/domain"
)

func (s *Service) CreateVessel(input CreateVesselInput) (domain.Vessel, error) {
	now := s.clock.Now()
	vessel := domain.Vessel{
		Code:               domain.NormalizeVesselCode(input.Code),
		CapacityL:          input.CapacityL,
		State:              domain.VesselAvailable,
		MaintenanceWindows: []domain.MaintenanceWindow{},
		Version:            1,
		CreatedAt:          now,
		UpdatedAt:          now,
	}
	if err := vessel.Validate(); err != nil {
		return domain.Vessel{}, err
	}
	windows, err := s.buildInitialWindows(input.Windows, now)
	if err != nil {
		return domain.Vessel{}, err
	}
	vessel.MaintenanceWindows = windows
	id, err := generateEntityID("vessel")
	if err != nil {
		return domain.Vessel{}, domain.NewError(domain.CodeStorage, err.Error())
	}
	vessel.ID = id

	err = s.store.Execute(func(snapshot *domain.Snapshot) error {
		if _, exists := snapshot.VesselIndex[vessel.Code]; exists {
			return domain.NewError(domain.CodeVesselConflict,
				"a vessel with the same code already exists")
		}
		snapshot.Vessels[vessel.ID] = vessel
		snapshot.VesselIndex[vessel.Code] = vessel.ID
		return nil
	})
	if err != nil {
		return domain.Vessel{}, err
	}
	return vessel.EvaluateAvailability(now), nil
}

// buildInitialWindows validates a maintenance schedule supplied at registration
// and assigns durable identifiers before the aggregate is persisted.
func (s *Service) buildInitialWindows(
	inputs []MaintenanceWindowInput,
	now time.Time,
) ([]domain.MaintenanceWindow, error) {
	windows := make([]domain.MaintenanceWindow, 0, len(inputs))
	for _, input := range inputs {
		if err := domain.ValidateMaintenanceWindow(input.StartsAt, input.EndsAt, input.Note, now); err != nil {
			return nil, err
		}
		id, err := generateEntityID("maint")
		if err != nil {
			return nil, domain.NewError(domain.CodeStorage, err.Error())
		}
		windows = append(windows, domain.MaintenanceWindow{
			ID:        id,
			StartsAt:  input.StartsAt.UTC(),
			EndsAt:    input.EndsAt.UTC(),
			Note:      input.Note,
			State:     domain.MaintenanceScheduled,
			CreatedAt: now.UTC(),
		})
	}
	for i := range windows {
		for j := i + 1; j < len(windows); j++ {
			if domain.WindowsOverlap(windows[i], windows[j]) {
				return nil, domain.NewError(domain.CodeMaintenanceConflict,
					"maintenance windows overlap each other")
			}
		}
	}
	return windows, nil
}

func (s *Service) GetVessel(id string) (domain.Vessel, error) {
	var found domain.Vessel
	err := s.store.View(func(snapshot domain.Snapshot) error {
		vessel, ok := snapshot.Vessels[id]
		if !ok {
			return domain.NewError(domain.CodeVesselNotFound, "vessel was not found")
		}
		found = vessel
		return nil
	})
	if err != nil {
		return domain.Vessel{}, err
	}
	return found.EvaluateAvailability(s.clock.Now()), nil
}

func (s *Service) ListVessels() ([]domain.Vessel, error) {
	var vessels []domain.Vessel
	err := s.store.View(func(snapshot domain.Snapshot) error {
		vessels = make([]domain.Vessel, 0, len(snapshot.Vessels))
		for _, vessel := range snapshot.Vessels {
			vessels = append(vessels, vessel)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	now := s.clock.Now()
	for index := range vessels {
		vessels[index] = vessels[index].EvaluateAvailability(now)
	}
	sort.Slice(vessels, func(i, j int) bool {
		if vessels[i].Code == vessels[j].Code {
			return vessels[i].ID < vessels[j].ID
		}
		return vessels[i].Code < vessels[j].Code
	})
	return vessels, nil
}

// ScheduleMaintenance registers a future maintenance window for a vessel.
func (s *Service) ScheduleMaintenance(
	id string,
	input ScheduleMaintenanceInput,
) (domain.Vessel, error) {
	var updated domain.Vessel
	err := s.store.Execute(func(snapshot *domain.Snapshot) error {
		vessel, ok := snapshot.Vessels[id]
		if !ok {
			return domain.NewError(domain.CodeVesselNotFound, "vessel was not found")
		}
		if err := s.checkExpected(vessel.Version, input.ExpectedVersion); err != nil {
			return err
		}
		now := s.clock.Now()
		if err := domain.ValidateMaintenanceWindow(input.StartsAt, input.EndsAt, input.Note, now); err != nil {
			return err
		}
		windowID, err := generateEntityID("maint")
		if err != nil {
			return domain.NewError(domain.CodeStorage, err.Error())
		}
		window := domain.MaintenanceWindow{
			ID:        windowID,
			StartsAt:  input.StartsAt.UTC(),
			EndsAt:    input.EndsAt.UTC(),
			Note:      input.Note,
			State:     domain.MaintenanceScheduled,
			CreatedAt: now,
		}
		if err := vessel.ScheduleMaintenance(window, now); err != nil {
			return err
		}
		snapshot.Vessels[id] = vessel
		updated = vessel
		return nil
	})
	if err != nil {
		return domain.Vessel{}, err
	}
	return updated.EvaluateAvailability(s.clock.Now()), nil
}

// CancelMaintenance releases a scheduled future window.
func (s *Service) CancelMaintenance(
	id string,
	windowID string,
	input VersionInput,
) (domain.Vessel, error) {
	var updated domain.Vessel
	err := s.store.Execute(func(snapshot *domain.Snapshot) error {
		vessel, ok := snapshot.Vessels[id]
		if !ok {
			return domain.NewError(domain.CodeVesselNotFound, "vessel was not found")
		}
		if err := s.checkExpected(vessel.Version, input.ExpectedVersion); err != nil {
			return err
		}
		now := s.clock.Now()
		if err := vessel.CancelMaintenance(windowID, now); err != nil {
			return err
		}
		snapshot.Vessels[id] = vessel
		updated = vessel
		return nil
	})
	if err != nil {
		return domain.Vessel{}, err
	}
	return updated.EvaluateAvailability(s.clock.Now()), nil
}

func (s *Service) FinishCleaning(
	id string,
	input FinishCleaningInput,
) (domain.Vessel, error) {
	var updated domain.Vessel
	err := s.store.Execute(func(snapshot *domain.Snapshot) error {
		vessel, ok := snapshot.Vessels[id]
		if !ok {
			return domain.NewError(domain.CodeVesselNotFound, "vessel was not found")
		}
		if err := s.checkExpected(vessel.Version, input.ExpectedVersion); err != nil {
			return err
		}
		if vessel.ActiveRunID == nil {
			return domain.NewError(domain.CodeVesselState,
				"cleaning vessel has no associated run")
		}
		run, ok := snapshot.Runs[*vessel.ActiveRunID]
		if !ok {
			return domain.NewError(domain.CodeRunNotFound,
				"associated run was not found")
		}
		if run.State != domain.RunCompleted && run.State != domain.RunAborted {
			return domain.NewError(domain.CodeRunState,
				"associated run is still open")
		}
		if err := vessel.FinishCleaning(s.clock.Now()); err != nil {
			return err
		}
		snapshot.Vessels[id] = vessel
		updated = vessel
		return nil
	})
	if err != nil {
		return domain.Vessel{}, err
	}
	return updated.EvaluateAvailability(s.clock.Now()), nil
}

func (s *Service) RetireVessel(id string, input VersionInput) (domain.Vessel, error) {
	var updated domain.Vessel
	err := s.store.Execute(func(snapshot *domain.Snapshot) error {
		vessel, ok := snapshot.Vessels[id]
		if !ok {
			return domain.NewError(domain.CodeVesselNotFound, "vessel was not found")
		}
		if err := s.checkExpected(vessel.Version, input.ExpectedVersion); err != nil {
			return err
		}
		if err := vessel.Retire(s.clock.Now()); err != nil {
			return err
		}
		snapshot.Vessels[id] = vessel
		updated = vessel
		return nil
	})
	if err != nil {
		return domain.Vessel{}, err
	}
	return updated.EvaluateAvailability(s.clock.Now()), nil
}
