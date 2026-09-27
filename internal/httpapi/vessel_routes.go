package httpapi

import (
	"net/http"
	"time"

	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/service"
)

type createVesselRequest struct {
	Code      string  `json:"code"`
	CapacityL float64 `json:"capacity_liters"`
}

type scheduleMaintenanceRequest struct {
	StartsAt time.Time `json:"starts_at"`
	EndsAt   time.Time `json:"ends_at"`
	Reason   string    `json:"reason"`
}

func registerVesselRoutes(mux *http.ServeMux, app *service.Service) {
	mux.HandleFunc("POST /v1/vessels", func(response http.ResponseWriter, request *http.Request) {
		body, err := decodeJSON[createVesselRequest](response, request)
		if !handleDecode(response, err) {
			return
		}
		vessel, err := app.CreateVessel(service.CreateVesselInput{
			Code:      body.Code,
			CapacityL: body.CapacityL,
		})
		if err != nil {
			writeDomainError(response, err)
			return
		}
		writeJSON(response, http.StatusCreated, vessel)
	})

	mux.HandleFunc("GET /v1/vessels", func(response http.ResponseWriter, _ *http.Request) {
		vessels, err := app.ListVessels()
		if err != nil {
			writeDomainError(response, err)
			return
		}
		writeJSON(response, http.StatusOK, map[string]any{"vessels": vessels})
	})

	mux.HandleFunc("GET /v1/vessels/{id}", func(response http.ResponseWriter, request *http.Request) {
		vessel, err := app.GetVessel(request.PathValue("id"))
		if err != nil {
			writeDomainError(response, err)
			return
		}
		writeJSON(response, http.StatusOK, vessel)
	})

	mux.HandleFunc("POST /v1/vessels/{id}/finish-cleaning", func(response http.ResponseWriter, request *http.Request) {
		expected, err := expectedVersion(request)
		if !handleDecode(response, err) {
			return
		}
		vessel, err := app.FinishCleaning(request.PathValue("id"),
			service.FinishCleaningInput{ExpectedVersion: expected})
		if err != nil {
			writeDomainError(response, err)
			return
		}
		writeJSON(response, http.StatusOK, vessel)
	})

	mux.HandleFunc("POST /v1/vessels/{id}/maintenance", func(response http.ResponseWriter, request *http.Request) {
		body, err := decodeJSON[scheduleMaintenanceRequest](response, request)
		if !handleDecode(response, err) {
			return
		}
		expected, err := expectedVersion(request)
		if !handleDecode(response, err) {
			return
		}
		vessel, err := app.ScheduleMaintenance(request.PathValue("id"),
			service.ScheduleMaintenanceInput{
				ExpectedVersion: expected,
				StartsAt:        body.StartsAt,
				EndsAt:          body.EndsAt,
				Reason:          body.Reason,
			})
		if err != nil {
			writeDomainError(response, err)
			return
		}
		writeJSON(response, http.StatusCreated, vessel)
	})

	mux.HandleFunc("POST /v1/vessels/{id}/maintenance/{windowId}/cancel", func(response http.ResponseWriter, request *http.Request) {
		expected, err := expectedVersion(request)
		if !handleDecode(response, err) {
			return
		}
		vessel, err := app.CancelMaintenance(request.PathValue("id"),
			request.PathValue("windowId"),
			service.VersionInput{ExpectedVersion: expected})
		if err != nil {
			writeDomainError(response, err)
			return
		}
		writeJSON(response, http.StatusOK, vessel)
	})

	mux.HandleFunc("POST /v1/vessels/{id}/retire", func(response http.ResponseWriter, request *http.Request) {
		expected, err := expectedVersion(request)
		if !handleDecode(response, err) {
			return
		}
		vessel, err := app.RetireVessel(request.PathValue("id"),
			service.VersionInput{ExpectedVersion: expected})
		if err != nil {
			writeDomainError(response, err)
			return
		}
		writeJSON(response, http.StatusOK, vessel)
	})
}
