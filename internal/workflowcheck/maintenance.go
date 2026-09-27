package workflowcheck

import (
	"fmt"
	"time"

	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/clock"
	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/domain"
	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/service"
	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/storage"
)

func vesselMaintenance(api *client, dataDir string) error {
	formula, err := ensureApprovedFormula(api, "Maintenance Formula")
	if err != nil {
		return err
	}

	// Registration may carry future maintenance windows; overlapping windows
	// in the same request are rejected.
	now := time.Now().UTC()
	overlappingCreate, err := api.call("POST", "/v1/vessels", map[string]any{
		"code":            "VSL-301",
		"capacity_liters": 200.0,
		"maintenance_windows": []map[string]any{
			windowPayload(now.Add(10*time.Hour), now.Add(12*time.Hour), "first"),
			windowPayload(now.Add(11*time.Hour), now.Add(13*time.Hour), "overlap"),
		},
	}, nil)
	if err != nil {
		return err
	}
	if err := requireCode(overlappingCreate, 409, string(domain.CodeMaintenanceConflict),
		"register vessel with overlapping windows"); err != nil {
		return err
	}

	// A valid, non-overlapping schedule can be supplied at registration time;
	// it is returned in start-time order with the upcoming window surfaced.
	scheduledCreate, err := api.call("POST", "/v1/vessels", map[string]any{
		"code":            "VSL-303",
		"capacity_liters": 150.0,
		"maintenance_windows": []map[string]any{
			windowPayload(now.Add(3*time.Hour), now.Add(4*time.Hour), "later"),
			windowPayload(now.Add(time.Hour), now.Add(2*time.Hour), "sooner"),
		},
	}, nil)
	if err != nil {
		return err
	}
	if err := requireStatus(scheduledCreate, 201, "register vessel with windows"); err != nil {
		return err
	}
	var registered vesselView
	if err := scheduledCreate.decode(&registered); err != nil {
		return err
	}
	if len(registered.MaintenanceWindows) != 2 ||
		!registered.MaintenanceWindows[0].StartsAt.Before(registered.MaintenanceWindows[1].StartsAt) ||
		registered.MaintenanceWindows[0].State != domain.MaintenanceScheduled ||
		registered.NextMaintenanceID == nil ||
		*registered.NextMaintenanceID != registered.MaintenanceWindows[0].ID ||
		!registered.AvailableForReservation {
		return errInvalidWorkflow("registration did not surface ordered future windows")
	}

	// Vessel A exercises scheduling, the active-window reservation block,
	// cancellation idempotency, and retirement rules.
	var vesselA vesselView
	vesselA, err = createCheckVessel(api, "VSL-301", 200)
	if err != nil {
		return err
	}
	if !vesselA.AvailableForReservation || vesselA.BlockedReason != "" {
		return errInvalidWorkflow("fresh vessel should be reservable")
	}
	if len(vesselA.MaintenanceWindows) != 0 {
		return errInvalidWorkflow("fresh vessel should expose an empty window list")
	}

	// Validation: past starts and inverted ranges are rejected.
	pastStart, err := api.call("POST", "/v1/vessels/"+vesselA.ID+"/maintenance",
		windowPayload(now.Add(-time.Hour), now.Add(time.Hour), "past"), ptrInt64(vesselA.Version))
	if err != nil {
		return err
	}
	if err := requireCode(pastStart, 400, string(domain.CodeInvalidInput),
		"schedule window starting in the past"); err != nil {
		return err
	}
	inverted, err := api.call("POST", "/v1/vessels/"+vesselA.ID+"/maintenance",
		windowPayload(now.Add(3*time.Hour), now.Add(2*time.Hour), "inverted"), ptrInt64(vesselA.Version))
	if err != nil {
		return err
	}
	if err := requireCode(inverted, 400, string(domain.CodeInvalidInput),
		"schedule window ending before it starts"); err != nil {
		return err
	}

	// Schedule a near window and a later, non-overlapping window. The near
	// window is wide enough that the check can observe it in effect and cancel
	// it before it expires.
	activeStart := now.Add(700 * time.Millisecond)
	activeEnd := activeStart.Add(60 * time.Second)
	nearResponse, err := api.call("POST", "/v1/vessels/"+vesselA.ID+"/maintenance",
		windowPayload(activeStart, activeEnd, "near service"), ptrInt64(vesselA.Version))
	if err != nil {
		return err
	}
	if err := requireStatus(nearResponse, 201, "schedule near window"); err != nil {
		return err
	}
	var nearView vesselView
	if err := nearResponse.decode(&nearView); err != nil {
		return err
	}
	vesselA = nearView
	nearWindow, err := findScheduledWindow(&vesselA, activeStart)
	if err != nil {
		return fmt.Errorf("near window missing from response: %w", err)
	}

	farStart := activeEnd.Add(time.Hour)
	farResponse, err := api.call("POST", "/v1/vessels/"+vesselA.ID+"/maintenance",
		windowPayload(farStart, farStart.Add(time.Hour), "future service"), ptrInt64(vesselA.Version))
	if err != nil {
		return err
	}
	if err := requireStatus(farResponse, 201, "schedule future window"); err != nil {
		return err
	}
	var farView vesselView
	if err := farResponse.decode(&farView); err != nil {
		return err
	}
	vesselA = farView
	farWindow, err := findScheduledWindow(&vesselA, farStart)
	if err != nil {
		return fmt.Errorf("far window missing from response: %w", err)
	}

	// Before the near window starts the vessel is still reservable, and the
	// next upcoming window is surfaced as the same availability fact.
	if !vesselA.AvailableForReservation || vesselA.BlockedReason != "" {
		return errInvalidWorkflow("vessel with only future windows should be reservable")
	}
	if vesselA.NextMaintenanceID == nil || *vesselA.NextMaintenanceID != nearWindow.ID {
		return errInvalidWorkflow("next upcoming maintenance window was not surfaced")
	}
	if vesselA.ActiveMaintenanceID != nil {
		return errInvalidWorkflow("no window should be active before its start")
	}

	// A reservation landing inside a future window is not created now, but a
	// reservation made before a window starts is accepted; the decisive case
	// is a reservation attempted while the window is in effect.
	runA, err := createPlannedRun(api, formula.ID, "RUN-MAINT-A")
	if err != nil {
		return err
	}
	if err := sleepUntil(activeStart.Add(100 * time.Millisecond)); err != nil {
		return err
	}
	if err := waitForMaintenanceBlock(api, vesselA.ID,
		"vessel should be blocked once maintenance starts"); err != nil {
		return err
	}
	reserveDuring, err := api.call("POST", "/v1/runs/"+runA.ID+"/reserve",
		map[string]any{"vessel_id": vesselA.ID}, ptrInt64(runA.Version))
	if err != nil {
		return err
	}
	if err := requireCode(reserveDuring, 409, string(domain.CodeMaintenanceConflict),
		"reserve vessel inside maintenance window"); err != nil {
		return err
	}
	// The failed write changes nothing.
	vesselA, err = getVesselView(api, vesselA.ID, "read vessel during maintenance")
	if err != nil {
		return err
	}
	if vesselA.State != domain.VesselAvailable || vesselA.ActiveRunID != nil {
		return errInvalidWorkflow("rejected reservation changed vessel state")
	}
	runA, err = getRunView(api, runA.ID, "read rejected run")
	if err != nil {
		return err
	}
	if runA.State != domain.RunPlanned || runA.VesselID != nil {
		return errInvalidWorkflow("rejected reservation changed run state")
	}

	// Cancelling the active window restores availability immediately; the
	// still-far scheduled window remains visible and ordered after it.
	cancelResponse, err := api.call("POST",
		"/v1/vessels/"+vesselA.ID+"/maintenance/"+nearWindow.ID+"/cancel",
		nil, ptrInt64(vesselA.Version))
	if err != nil {
		return err
	}
	if err := requireStatus(cancelResponse, 200, "cancel active window"); err != nil {
		return err
	}
	var cancelledView vesselView
	if err := cancelResponse.decode(&cancelledView); err != nil {
		return err
	}
	vesselA = cancelledView
	if !vesselA.AvailableForReservation || vesselA.BlockedReason != "" {
		return errInvalidWorkflow("cancelled window should restore availability")
	}
	if vesselA.ActiveMaintenanceID != nil {
		return errInvalidWorkflow("cancelled window should not remain active")
	}
	if vesselA.NextMaintenanceID == nil || *vesselA.NextMaintenanceID != farWindow.ID {
		return errInvalidWorkflow("remaining future window should be surfaced")
	}
	if got := requireCancelledWindow(&vesselA, nearWindow.ID); got == nil {
		return errInvalidWorkflow("cancelled window was not recorded")
	}

	// Cancellation is a stable conflict on repeat, unknown ids are 404, and a
	// stale X-Expected-Version is a revision conflict.
	repeatCancel, err := api.call("POST",
		"/v1/vessels/"+vesselA.ID+"/maintenance/"+nearWindow.ID+"/cancel",
		nil, ptrInt64(vesselA.Version))
	if err != nil {
		return err
	}
	if err := requireCode(repeatCancel, 409, string(domain.CodeMaintenanceConflict),
		"cancel window twice"); err != nil {
		return err
	}
	missingCancel, err := api.call("POST",
		"/v1/vessels/"+vesselA.ID+"/maintenance/maint_unknown/cancel",
		nil, nil)
	if err != nil {
		return err
	}
	if err := requireCode(missingCancel, 404, string(domain.CodeMaintenanceNotFound),
		"cancel missing window"); err != nil {
		return err
	}
	staleCancel, err := api.call("POST",
		"/v1/vessels/"+vesselA.ID+"/maintenance/"+farWindow.ID+"/cancel",
		nil, ptrInt64(1))
	if err != nil {
		return err
	}
	if err := requireCode(staleCancel, 412, string(domain.CodeRevisionConflict),
		"cancel with stale version"); err != nil {
		return err
	}

	// Retirement waits until every open scheduled window is cancelled.
	retireBlocked, err := api.call("POST", "/v1/vessels/"+vesselA.ID+"/retire",
		nil, ptrInt64(vesselA.Version))
	if err != nil {
		return err
	}
	if err := requireCode(retireBlocked, 409, string(domain.CodeMaintenanceConflict),
		"retire with an open window"); err != nil {
		return err
	}
	cancelFar, err := api.call("POST",
		"/v1/vessels/"+vesselA.ID+"/maintenance/"+farWindow.ID+"/cancel",
		nil, ptrInt64(vesselA.Version))
	if err != nil {
		return err
	}
	if err := requireStatus(cancelFar, 200, "cancel future window"); err != nil {
		return err
	}
	var farCancelledView vesselView
	if err := cancelFar.decode(&farCancelledView); err != nil {
		return err
	}
	vesselA = farCancelledView
	retiredResponse, err := api.call("POST", "/v1/vessels/"+vesselA.ID+"/retire",
		nil, ptrInt64(vesselA.Version))
	if err != nil {
		return err
	}
	if err := requireStatus(retiredResponse, 200, "retire after windows cancelled"); err != nil {
		return err
	}
	var retiredView vesselView
	if err := retiredResponse.decode(&retiredView); err != nil {
		return err
	}
	vesselA = retiredView
	if vesselA.State != domain.VesselRetired || vesselA.AvailableForReservation {
		return errInvalidWorkflow("retired vessel should be unavailable")
	}
	if vesselA.BlockedReason != domain.BlockedReasonRetired {
		return errInvalidWorkflow("retired vessel should expose retired block reason")
	}
	if len(vesselA.MaintenanceWindows) != 2 {
		return errInvalidWorkflow("retirement should preserve maintenance history")
	}

	// Vessel B proves that once a run has reserved or started, maintenance can
	// never be silently arranged against that vessel, and that adjacent
	// windows are legal while overlapping ones are rejected.
	vesselB, err := createCheckVessel(api, "VSL-302", 220)
	if err != nil {
		return err
	}
	firstStart := now.Add(24 * time.Hour)
	firstEnd := firstStart.Add(2 * time.Hour)
	first, err := api.call("POST", "/v1/vessels/"+vesselB.ID+"/maintenance",
		windowPayload(firstStart, firstEnd, "first"), ptrInt64(vesselB.Version))
	if err != nil {
		return err
	}
	if err := requireStatus(first, 201, "schedule first vessel-B window"); err != nil {
		return err
	}
	var firstView vesselView
	if err := first.decode(&firstView); err != nil {
		return err
	}
	vesselB = firstView
	firstWindow, err := findScheduledWindow(&vesselB, firstStart)
	if err != nil {
		return fmt.Errorf("first B window not recorded: %w", err)
	}

	// Adjacent (touching only at the boundary) is allowed.
	adjacent, err := api.call("POST", "/v1/vessels/"+vesselB.ID+"/maintenance",
		windowPayload(firstEnd, firstEnd.Add(time.Hour), "adjacent"), ptrInt64(vesselB.Version))
	if err != nil {
		return err
	}
	if err := requireStatus(adjacent, 201, "schedule adjacent window"); err != nil {
		return err
	}
	var adjacentView vesselView
	if err := adjacent.decode(&adjacentView); err != nil {
		return err
	}
	vesselB = adjacentView
	// Overlapping the adjacent window is rejected with the same fact.
	overlap, err := api.call("POST", "/v1/vessels/"+vesselB.ID+"/maintenance",
		windowPayload(firstEnd.Add(30*time.Minute), firstEnd.Add(2*time.Hour), "overlap"),
		ptrInt64(vesselB.Version))
	if err != nil {
		return err
	}
	if err := requireCode(overlap, 409, string(domain.CodeMaintenanceConflict),
		"schedule overlapping window"); err != nil {
		return err
	}

	runB, err := createPlannedRun(api, formula.ID, "RUN-MAINT-B")
	if err != nil {
		return err
	}
	reserved, err := api.call("POST", "/v1/runs/"+runB.ID+"/reserve",
		map[string]any{"vessel_id": vesselB.ID}, ptrInt64(runB.Version))
	if err != nil {
		return err
	}
	if err := requireStatus(reserved, 200, "reserve vessel B before its window"); err != nil {
		return err
	}
	var reservedPair pairView
	if err := reserved.decode(&reservedPair); err != nil {
		return err
	}
	pair := reservedPair
	if pair.Vessel.AvailableForReservation || pair.Vessel.BlockedReason != domain.BlockedReasonActiveRun {
		return errInvalidWorkflow("reserve response should show active-run block")
	}

	// The reserved run must not be displaced by a new maintenance arrangement.
	scheduleOnReserved, err := api.call("POST", "/v1/vessels/"+vesselB.ID+"/maintenance",
		windowPayload(now.Add(48*time.Hour), now.Add(49*time.Hour), "late"), nil)
	if err != nil {
		return err
	}
	if err := requireCode(scheduleOnReserved, 409, string(domain.CodeMaintenanceConflict),
		"schedule maintenance against reserved vessel"); err != nil {
		return err
	}

	startResponse, err := api.call("POST", "/v1/runs/"+runB.ID+"/start",
		nil, ptrInt64(pair.Run.Version))
	if err != nil {
		return err
	}
	if err := requireStatus(startResponse, 200, "start run B"); err != nil {
		return err
	}
	var startedPair pairView
	if err := startResponse.decode(&startedPair); err != nil {
		return err
	}
	pair = startedPair
	scheduleOnRunning, err := api.call("POST", "/v1/vessels/"+vesselB.ID+"/maintenance",
		windowPayload(now.Add(72*time.Hour), now.Add(73*time.Hour), "later"), nil)
	if err != nil {
		return err
	}
	if err := requireCode(scheduleOnRunning, 409, string(domain.CodeMaintenanceConflict),
		"schedule maintenance against in-use vessel"); err != nil {
		return err
	}
	// Cancelling an already-registered window remains allowed on a busy vessel;
	// the run itself is untouched.
	cancelBusy, err := api.call("POST",
		"/v1/vessels/"+vesselB.ID+"/maintenance/"+firstWindow.ID+"/cancel", nil, nil)
	if err != nil {
		return err
	}
	if err := requireStatus(cancelBusy, 200, "cancel window on busy vessel"); err != nil {
		return err
	}
	var busyView vesselView
	if err := cancelBusy.decode(&busyView); err != nil {
		return err
	}
	vesselB = busyView
	if vesselB.State != domain.VesselInUse || vesselB.ActiveRunID == nil {
		return errInvalidWorkflow("cancelling maintenance disturbed the running vessel")
	}
	runBAfter, err := getRunView(api, runB.ID, "read run B after cancellation")
	if err != nil {
		return err
	}
	if runBAfter.State != domain.RunFermenting {
		return errInvalidWorkflow("cancelling maintenance disturbed the running task")
	}

	// Details and the list entry must present the identical availability fact.
	detail, err := getVesselView(api, vesselB.ID, "read vessel-B detail")
	if err != nil {
		return err
	}
	listResponse, err := api.call("GET", "/v1/vessels", nil, nil)
	if err != nil {
		return err
	}
	if err := requireStatus(listResponse, 200, "list vessels"); err != nil {
		return err
	}
	var list struct {
		Vessels []vesselView `json:"vessels"`
	}
	if err := listResponse.decode(&list); err != nil {
		return err
	}
	var listEntry *vesselView
	for index := range list.Vessels {
		if list.Vessels[index].ID == vesselB.ID {
			listEntry = &list.Vessels[index]
			break
		}
	}
	if listEntry == nil {
		return errInvalidWorkflow("vessel missing from list")
	}
	if listEntry.AvailableForReservation != detail.AvailableForReservation ||
		listEntry.BlockedReason != detail.BlockedReason ||
		len(listEntry.MaintenanceWindows) != len(detail.MaintenanceWindows) {
		return errInvalidWorkflow("list and detail disagree on availability facts")
	}

	// The schedule and its availability fact survive a full snapshot reload.
	reopened, err := storage.Open(dataDir)
	if err != nil {
		return fmt.Errorf("reopen snapshot for maintenance persistence: %w", err)
	}
	reloaded, err := service.New(reopened, clock.System{}).GetVessel(vesselB.ID)
	if err != nil {
		return fmt.Errorf("read reloaded vessel: %w", err)
	}
	if reloaded.State != domain.VesselInUse ||
		reloaded.AvailableForReservation ||
		reloaded.BlockedReason != domain.BlockedReasonActiveRun {
		return errInvalidWorkflow("reloaded vessel lost its availability fact")
	}
	if len(reloaded.MaintenanceWindows) != 2 {
		return errInvalidWorkflow("reloaded vessel lost its maintenance schedule")
	}
	cancelledCount := 0
	scheduledCount := 0
	for _, window := range reloaded.MaintenanceWindows {
		switch window.State {
		case domain.MaintenanceCancelled:
			cancelledCount++
		case domain.MaintenanceScheduled:
			scheduledCount++
		}
	}
	if cancelledCount != 1 || scheduledCount != 1 {
		return errInvalidWorkflow("reloaded maintenance window states were not preserved")
	}
	return nil
}

func windowPayload(start, end time.Time, note string) map[string]any {
	return map[string]any{
		"starts_at": start.UTC().Format(time.RFC3339Nano),
		"ends_at":   end.UTC().Format(time.RFC3339Nano),
		"note":      note,
	}
}

func createPlannedRun(api *client, formulaID, code string) (runView, error) {
	response, err := api.call("POST", "/v1/runs", createRunPayload(code, formulaID), nil)
	if err != nil {
		return runView{}, err
	}
	if err := requireStatus(response, 201, "create planned run"); err != nil {
		return runView{}, err
	}
	var run runView
	if err := response.decode(&run); err != nil {
		return runView{}, err
	}
	return run, nil
}

func getVesselView(api *client, id, operation string) (vesselView, error) {
	response, err := api.call("GET", "/v1/vessels/"+id, nil, nil)
	if err != nil {
		return vesselView{}, err
	}
	if err := requireStatus(response, 200, operation); err != nil {
		return vesselView{}, err
	}
	var vessel vesselView
	if err := response.decode(&vessel); err != nil {
		return vesselView{}, err
	}
	return vessel, nil
}

func getRunView(api *client, id, operation string) (runView, error) {
	response, err := api.call("GET", "/v1/runs/"+id, nil, nil)
	if err != nil {
		return runView{}, err
	}
	if err := requireStatus(response, 200, operation); err != nil {
		return runView{}, err
	}
	var run runView
	if err := response.decode(&run); err != nil {
		return runView{}, err
	}
	return run, nil
}

func findScheduledWindow(vessel *vesselView, start time.Time) (maintenanceWindowView, error) {
	for _, window := range vessel.MaintenanceWindows {
		if window.StartsAt.Equal(start.UTC()) {
			if window.State != domain.MaintenanceScheduled {
				return maintenanceWindowView{},
					fmt.Errorf("window at %s is not scheduled", start.Format(time.RFC3339Nano))
			}
			return window, nil
		}
	}
	return maintenanceWindowView{},
		fmt.Errorf("no scheduled window starts at %s", start.Format(time.RFC3339Nano))
}

func requireCancelledWindow(vessel *vesselView, id string) *maintenanceWindowView {
	for index := range vessel.MaintenanceWindows {
		if vessel.MaintenanceWindows[index].ID == id {
			if vessel.MaintenanceWindows[index].State != domain.MaintenanceCancelled ||
				vessel.MaintenanceWindows[index].CancelledAt == nil {
				return nil
			}
			return &vessel.MaintenanceWindows[index]
		}
	}
	return nil
}

// sleepUntil waits for wall clock to pass deadline.
func sleepUntil(deadline time.Time) error {
	for time.Now().UTC().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	return nil
}

// waitForMaintenanceBlock polls the vessel detail until it reports the active
// maintenance block, so the check tolerates scheduling jitter without sleeping
// the full worst-case delay.
func waitForMaintenanceBlock(api *client, vesselID, message string) error {
	deadline := time.Now().UTC().Add(3 * time.Second)
	for time.Now().UTC().Before(deadline) {
		vessel, err := getVesselView(api, vesselID, message)
		if err != nil {
			return err
		}
		if !vessel.AvailableForReservation &&
			vessel.BlockedReason == domain.BlockedReasonMaintenance &&
			vessel.ActiveMaintenanceID != nil {
			return nil
		}
		time.Sleep(25 * time.Millisecond)
	}
	return errInvalidWorkflow(message)
}
