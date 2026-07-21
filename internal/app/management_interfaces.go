package app

import (
	"context"
	"errors"
	"sort"
	"unicode/utf8"

	"github.com/ElectricNoodle/go-musical-packets/internal/capture"
	"github.com/ElectricNoodle/go-musical-packets/internal/managementapi"
)

// Interfaces enumerates capture devices without exposing native backend
// errors. Discovery is independent of an active capture source and is useful
// when capture is disabled during initial setup.
func (backend *managementBackend) Interfaces(ctx context.Context) (managementapi.InterfacesDocument, error) {
	if ctx == nil {
		return managementapi.InterfacesDocument{}, managementInterfaceInvalid(errors.New("management interface context is required"))
	}
	if err := backend.flowRuntimeAvailable(ctx, false); err != nil {
		return managementapi.InterfacesDocument{}, err
	}

	interfaces, err := backend.interfaces()
	if err != nil {
		return managementapi.InterfacesDocument{}, managementInterfaceUnavailable(err)
	}
	if err := ctx.Err(); err != nil {
		return managementapi.InterfacesDocument{}, managementInterfaceUnavailable(err)
	}

	configured := backend.controller.Current().Config.Capture.Interface
	document := managementapi.InterfacesDocument{
		Configured: configured,
		Interfaces: make([]managementapi.CaptureInterface, 0, len(interfaces)),
	}
	seen := make(map[string]struct{}, len(interfaces))
	for _, candidate := range interfaces {
		if candidate.Name == "" || !utf8.ValidString(candidate.Name) || !utf8.ValidString(candidate.Description) {
			return managementapi.InterfacesDocument{}, managementInterfaceUnavailable(errors.New("capture discovery returned invalid interface state"))
		}
		if _, duplicate := seen[candidate.Name]; duplicate {
			return managementapi.InterfacesDocument{}, managementInterfaceUnavailable(errors.New("capture discovery returned duplicate interface names"))
		}
		seen[candidate.Name] = struct{}{}
		addresses := make([]string, 0, len(candidate.Addresses))
		for _, address := range candidate.Addresses {
			addresses = append(addresses, address.String())
		}
		sort.Strings(addresses)
		document.Interfaces = append(document.Interfaces, managementapi.CaptureInterface{
			Name:        candidate.Name,
			Description: candidate.Description,
			Addresses:   addresses,
			Up:          candidate.Up,
			Loopback:    candidate.Loopback,
		})
	}
	sort.Slice(document.Interfaces, func(i, j int) bool {
		return document.Interfaces[i].Name < document.Interfaces[j].Name
	})
	if selected, err := capture.SelectInterface(interfaces, configured); err == nil {
		document.Selected = selected.Name
	}
	return document, nil
}

func managementInterfaceInvalid(err error) error {
	return &managementapi.BackendError{
		Kind:   managementapi.ErrorInvalid,
		Code:   "invalid_interface_request",
		Detail: "capture interface request is invalid",
		Err:    err,
	}
}

func managementInterfaceUnavailable(err error) error {
	return &managementapi.BackendError{
		Kind:   managementapi.ErrorUnavailable,
		Code:   "interfaces_unavailable",
		Detail: "capture interface discovery is temporarily unavailable",
		Err:    err,
	}
}
