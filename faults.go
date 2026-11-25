// Package main registers RFC 9457 error codes under Printer Hawk's namespace.
package main

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/prusa3d/prusa-connect/sdk/fault"
)

// faultDomain is derived from github.com/LN4CY/printer-hawk.
//
//nolint:gochecknoglobals // built once at process startup.
var faultDomain = mustDomain("com.github.aux.ln4cy.printer-hawk")

//nolint:gochecknoglobals // immutable fault definitions registered once at startup.
var (
	// RouteNotFound reports an unserved HTTP path under the module mount.
	RouteNotFound = fault.Register(faultDomain, "ROUTE_NOT_FOUND", http.StatusNotFound, fault.Document(
		"This module does not serve that path",
		"Read openapi.json for the endpoints Printer Hawk serves.",
		fault.RetryNever,
		fault.ShowDetail,
		"path",
	))

	// InvalidPayload reports a malformed or rejected JSON body.
	InvalidPayload = fault.Register(faultDomain, "INVALID_PAYLOAD", http.StatusBadRequest, fault.Document(
		"The request body could not be accepted",
		"Check the field types and values and send the request again.",
		fault.RetryNever,
		fault.ShowDetail,
		"reason",
	))

	// PermissionDenied reports a caller lacking operate permission.
	PermissionDenied = fault.Register(faultDomain, "PERMISSION_DENIED", http.StatusForbidden, fault.Document(
		"You do not have permission to change or control this resource",
		"Ask an administrator for operate permission on Printer Hawk or the printer.",
		fault.RetryNever,
		fault.ShowDetail,
		"resource",
	))

	// PrinterNotFound reports an unknown printer ID.
	PrinterNotFound = fault.Register(faultDomain, "PRINTER_NOT_FOUND", http.StatusNotFound, fault.Document(
		"Printer not found",
		"Select a printer known to this Prusa Connect server.",
		fault.RetryNever,
		fault.ShowDetail,
		"printer",
	))

	// FrameNotFound reports a missing live or historical camera JPEG.
	FrameNotFound = fault.Register(faultDomain, "FRAME_NOT_FOUND", http.StatusNotFound, fault.Document(
		"Camera frame not available",
		"No live snapshot or stored historical frame is currently available for this printer.",
		fault.RetryAllowed,
		fault.ShowDetail,
		"printer",
	))

	// InspectionFailed reports an error fetching a frame or calling the vision model.
	InspectionFailed = fault.Register(faultDomain, "INSPECTION_FAILED", http.StatusBadGateway, fault.Document(
		"Vision inspection failed",
		"Check camera availability and AI provider configuration.",
		fault.RetryAllowed,
		fault.ShowDetail,
		"reason",
	))

	// CommandRejected reports a device pause/stop or provider test failure.
	CommandRejected = fault.Register(faultDomain, "COMMAND_REJECTED", http.StatusBadGateway, fault.Document(
		"Command could not be completed",
		"Verify the printer is connected in a compatible state and the module holds operate permission.",
		fault.RetryNever,
		fault.ShowDetail,
		"reason",
	))
)

func mustDomain(name string) fault.Domain {
	domain, err := fault.NewDomain(name)
	if err != nil {
		panic(fmt.Sprintf("printer-hawk: %s is not a fault domain: %v", name, err))
	}
	return domain
}

func refuse(w http.ResponseWriter, r *http.Request, raised *fault.Fault) {
	problem := raised.ProblemJSON(r.URL.Path)
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(problem.Status)
	_ = json.NewEncoder(w).Encode(problem)
}
