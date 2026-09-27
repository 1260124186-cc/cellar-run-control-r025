package service

import (
	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/domain"
)

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
		windowID, err := generateEntityID("mnt")
		if err != nil {
			return domain.NewError(domain.CodeStorage, err.Error())
		}
		now := s.clock.Now()
		window := domain.MaintenanceWindow{
			ID:        windowID,
			StartsAt:  input.StartsAt.UTC(),
			EndsAt:    input.EndsAt.UTC(),
			Reason:    input.Reason,
			CreatedAt: now.UTC(),
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
	return updated, nil
}

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
		if err := vessel.CancelMaintenance(windowID, s.clock.Now()); err != nil {
			return err
		}
		snapshot.Vessels[id] = vessel
		updated = vessel
		return nil
	})
	if err != nil {
		return domain.Vessel{}, err
	}
	return updated, nil
}
