package workflowcheck

import (
	"fmt"
	"time"

	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/domain"
)

func vesselMaintenance(api *client) error {
	formula, err := ensureApprovedFormula(api, "Maintenance Formula")
	if err != nil {
		return err
	}
	vessel, err := createCheckVessel(api, "VSL-303", 260)
	if err != nil {
		return err
	}

	now := time.Now().UTC()
	firstStart := now.Add(2 * time.Hour)
	firstEnd := now.Add(4 * time.Hour)

	scheduled, err := scheduleWindow(api, vessel.ID, firstStart, firstEnd, "line inspection")
	if err != nil {
		return err
	}
	if len(scheduled.Maintenance) != 1 {
		return errInvalidWorkflow("scheduled maintenance window was not recorded")
	}
	firstWindow := scheduled.Maintenance[0]

	overlap, err := api.call("POST", "/v1/vessels/"+vessel.ID+"/maintenance",
		maintenancePayload(firstStart.Add(time.Hour), firstEnd.Add(time.Hour), "overlap"), nil)
	if err != nil {
		return err
	}
	if err := requireCode(overlap, 409, string(domain.CodeMaintenanceConflict),
		"schedule overlapping window"); err != nil {
		return err
	}

	stale, err := api.call("POST", "/v1/vessels/"+vessel.ID+"/maintenance",
		maintenancePayload(firstEnd.Add(2*time.Hour), firstEnd.Add(4*time.Hour), "stale"),
		ptrInt64(scheduled.Version-1))
	if err != nil {
		return err
	}
	if err := requireCode(stale, 412, string(domain.CodeRevisionConflict),
		"schedule with stale version"); err != nil {
		return err
	}

	touching, err := scheduleWindow(api, vessel.ID, firstEnd, firstEnd.Add(2*time.Hour),
		"gasket replacement")
	if err != nil {
		return err
	}
	if len(touching.Maintenance) != 2 {
		return errInvalidWorkflow("touching maintenance window was not accepted")
	}
	secondWindow := touching.Maintenance[1]
	if touching.Maintenance[0].ID != firstWindow.ID ||
		!touching.Maintenance[0].StartsAt.Before(touching.Maintenance[1].StartsAt) {
		return errInvalidWorkflow("maintenance windows are not ordered by start time")
	}

	reversed, err := api.call("POST", "/v1/vessels/"+vessel.ID+"/maintenance",
		maintenancePayload(firstEnd.Add(4*time.Hour), firstEnd.Add(3*time.Hour), "reversed"), nil)
	if err != nil {
		return err
	}
	if err := requireCode(reversed, 400, string(domain.CodeInvalidInput),
		"schedule window ending before it starts"); err != nil {
		return err
	}

	past, err := api.call("POST", "/v1/vessels/"+vessel.ID+"/maintenance",
		maintenancePayload(now.Add(-time.Hour), now.Add(time.Hour), "past"), nil)
	if err != nil {
		return err
	}
	if err := requireCode(past, 400, string(domain.CodeInvalidInput),
		"schedule window starting in the past"); err != nil {
		return err
	}

	detail, err := getVessel(api, vessel.ID)
	if err != nil {
		return err
	}
	if err := requireWindowIDs(detail, firstWindow.ID, secondWindow.ID); err != nil {
		return fmt.Errorf("vessel detail: %w", err)
	}
	listResponse, err := api.call("GET", "/v1/vessels", nil, nil)
	if err != nil {
		return err
	}
	if err := requireStatus(listResponse, 200, "list vessels"); err != nil {
		return err
	}
	var listBody struct {
		Vessels []vesselView `json:"vessels"`
	}
	if err := listResponse.decode(&listBody); err != nil {
		return err
	}
	listed := vesselView{}
	for _, candidate := range listBody.Vessels {
		if candidate.ID == vessel.ID {
			listed = candidate
		}
	}
	if listed.ID != vessel.ID {
		return errInvalidWorkflow("vessel list did not contain the registered vessel")
	}
	if err := requireWindowIDs(listed, firstWindow.ID, secondWindow.ID); err != nil {
		return fmt.Errorf("vessel list: %w", err)
	}

	retireBlocked, err := api.call("POST", "/v1/vessels/"+vessel.ID+"/retire", nil, nil)
	if err != nil {
		return err
	}
	if err := requireCode(retireBlocked, 409, string(domain.CodeVesselState),
		"retire with pending maintenance"); err != nil {
		return err
	}

	runAResponse, err := api.call("POST", "/v1/runs",
		createRunPayload("RUN-MAINTENANCE-A", formula.ID), nil)
	if err != nil {
		return err
	}
	if err := requireStatus(runAResponse, 201, "create first maintenance run"); err != nil {
		return err
	}
	var runA runView
	if err := runAResponse.decode(&runA); err != nil {
		return err
	}
	reserveA, err := api.call("POST", "/v1/runs/"+runA.ID+"/reserve",
		map[string]any{"vessel_id": vessel.ID}, nil)
	if err != nil {
		return err
	}
	if err := requireStatus(reserveA, 200, "reserve outside maintenance"); err != nil {
		return err
	}

	scheduleWhileReserved, err := api.call("POST", "/v1/vessels/"+vessel.ID+"/maintenance",
		maintenancePayload(firstEnd.Add(6*time.Hour), firstEnd.Add(8*time.Hour), "reserved"), nil)
	if err != nil {
		return err
	}
	if err := requireCode(scheduleWhileReserved, 409, string(domain.CodeVesselState),
		"schedule on reserved vessel"); err != nil {
		return err
	}

	startA, err := api.call("POST", "/v1/runs/"+runA.ID+"/start", nil, nil)
	if err != nil {
		return err
	}
	if err := requireStatus(startA, 200, "start outside maintenance"); err != nil {
		return err
	}

	scheduleWhileInUse, err := api.call("POST", "/v1/vessels/"+vessel.ID+"/maintenance",
		maintenancePayload(firstEnd.Add(6*time.Hour), firstEnd.Add(8*time.Hour), "in use"), nil)
	if err != nil {
		return err
	}
	if err := requireCode(scheduleWhileInUse, 409, string(domain.CodeVesselState),
		"schedule on in-use vessel"); err != nil {
		return err
	}

	abortA, err := api.call("POST", "/v1/runs/"+runA.ID+"/abort", nil, nil)
	if err != nil {
		return err
	}
	if err := requireStatus(abortA, 200, "abort first maintenance run"); err != nil {
		return err
	}
	cleaned, err := api.call("POST", "/v1/vessels/"+vessel.ID+"/finish-cleaning", nil, nil)
	if err != nil {
		return err
	}
	if err := requireStatus(cleaned, 200, "finish cleaning after abort"); err != nil {
		return err
	}

	runBResponse, err := api.call("POST", "/v1/runs",
		createRunPayload("RUN-MAINTENANCE-B", formula.ID), nil)
	if err != nil {
		return err
	}
	if err := requireStatus(runBResponse, 201, "create second maintenance run"); err != nil {
		return err
	}
	var runB runView
	if err := runBResponse.decode(&runB); err != nil {
		return err
	}

	nearStart := time.Now().UTC().Add(1 * time.Second)
	near, err := scheduleWindow(api, vessel.ID, nearStart, nearStart.Add(time.Hour),
		"sanitization")
	if err != nil {
		return err
	}
	if len(near.Maintenance) != 3 {
		return errInvalidWorkflow("active maintenance window was not recorded")
	}
	nearWindow := near.Maintenance[0]

	detail, err = getVessel(api, vessel.ID)
	if err != nil {
		return err
	}
	if err := requireWindowIDs(detail, nearWindow.ID, firstWindow.ID, secondWindow.ID); err != nil {
		return fmt.Errorf("vessel detail before blocked reserve: %w", err)
	}

	time.Sleep(1500 * time.Millisecond)

	blocked, err := api.call("POST", "/v1/runs/"+runB.ID+"/reserve",
		map[string]any{"vessel_id": vessel.ID}, nil)
	if err != nil {
		return err
	}
	if err := requireCode(blocked, 409, string(domain.CodeVesselUnavailable),
		"reserve during maintenance"); err != nil {
		return err
	}

	canceled, err := cancelWindow(api, vessel.ID, nearWindow.ID)
	if err != nil {
		return err
	}
	if err := requireWindowIDs(canceled, firstWindow.ID, secondWindow.ID); err != nil {
		return fmt.Errorf("vessel after cancel: %w", err)
	}

	repeat, err := api.call("POST",
		"/v1/vessels/"+vessel.ID+"/maintenance/"+nearWindow.ID+"/cancel", nil, nil)
	if err != nil {
		return err
	}
	if err := requireCode(repeat, 404, string(domain.CodeMaintenanceNotFound),
		"cancel maintenance twice"); err != nil {
		return err
	}

	missing, err := api.call("POST",
		"/v1/vessels/"+vessel.ID+"/maintenance/mnt_missing/cancel", nil, nil)
	if err != nil {
		return err
	}
	if err := requireCode(missing, 404, string(domain.CodeMaintenanceNotFound),
		"cancel unknown maintenance"); err != nil {
		return err
	}

	reserveB, err := api.call("POST", "/v1/runs/"+runB.ID+"/reserve",
		map[string]any{"vessel_id": vessel.ID}, nil)
	if err != nil {
		return err
	}
	if err := requireStatus(reserveB, 200, "reserve after maintenance cancel"); err != nil {
		return err
	}
	abortB, err := api.call("POST", "/v1/runs/"+runB.ID+"/abort", nil, nil)
	if err != nil {
		return err
	}
	if err := requireStatus(abortB, 200, "abort second maintenance run"); err != nil {
		return err
	}

	if _, err := cancelWindow(api, vessel.ID, firstWindow.ID); err != nil {
		return err
	}
	cleared, err := cancelWindow(api, vessel.ID, secondWindow.ID)
	if err != nil {
		return err
	}
	if len(cleared.Maintenance) != 0 {
		return errInvalidWorkflow("canceled maintenance windows were not removed")
	}

	retired, err := api.call("POST", "/v1/vessels/"+vessel.ID+"/retire", nil, nil)
	if err != nil {
		return err
	}
	if err := requireStatus(retired, 200, "retire after canceling maintenance"); err != nil {
		return err
	}
	var retiredVessel vesselView
	if err := retired.decode(&retiredVessel); err != nil {
		return err
	}
	if err := requireVesselState(retiredVessel, domain.VesselRetired); err != nil {
		return err
	}
	return nil
}

func maintenancePayload(start, end time.Time, reason string) map[string]any {
	return map[string]any{
		"starts_at": start,
		"ends_at":   end,
		"reason":    reason,
	}
}

func scheduleWindow(
	api *client,
	vesselID string,
	start, end time.Time,
	reason string,
) (vesselView, error) {
	response, err := api.call("POST", "/v1/vessels/"+vesselID+"/maintenance",
		maintenancePayload(start, end, reason), nil)
	if err != nil {
		return vesselView{}, err
	}
	if err := requireStatus(response, 201, "schedule maintenance"); err != nil {
		return vesselView{}, err
	}
	var vessel vesselView
	if err := response.decode(&vessel); err != nil {
		return vesselView{}, err
	}
	return vessel, nil
}

func cancelWindow(api *client, vesselID, windowID string) (vesselView, error) {
	response, err := api.call("POST",
		"/v1/vessels/"+vesselID+"/maintenance/"+windowID+"/cancel", nil, nil)
	if err != nil {
		return vesselView{}, err
	}
	if err := requireStatus(response, 200, "cancel maintenance"); err != nil {
		return vesselView{}, err
	}
	var vessel vesselView
	if err := response.decode(&vessel); err != nil {
		return vesselView{}, err
	}
	return vessel, nil
}

func getVessel(api *client, vesselID string) (vesselView, error) {
	response, err := api.call("GET", "/v1/vessels/"+vesselID, nil, nil)
	if err != nil {
		return vesselView{}, err
	}
	if err := requireStatus(response, 200, "read vessel"); err != nil {
		return vesselView{}, err
	}
	var vessel vesselView
	if err := response.decode(&vessel); err != nil {
		return vesselView{}, err
	}
	return vessel, nil
}

func requireWindowIDs(vessel vesselView, ids ...string) error {
	if len(vessel.Maintenance) != len(ids) {
		return fmt.Errorf("expected %d maintenance windows, got %d",
			len(ids), len(vessel.Maintenance))
	}
	for index, id := range ids {
		if vessel.Maintenance[index].ID != id {
			return fmt.Errorf("maintenance window %d: expected %q, got %q",
				index, id, vessel.Maintenance[index].ID)
		}
	}
	return nil
}
