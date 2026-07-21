package app

import (
	"context"
	"errors"
	"net/netip"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/ElectricNoodle/go-musical-packets/internal/capture"
	"github.com/ElectricNoodle/go-musical-packets/internal/config"
	"github.com/ElectricNoodle/go-musical-packets/internal/managementapi"
)

func TestManagementInterfacesReturnsStableDiscoveryAndSelection(t *testing.T) {
	configuration := managementTestConfig()
	configuration.Capture.Interface = "auto"
	backend := newManagementInterfaceTestBackend(t, configuration, func() ([]capture.Interface, error) {
		return []capture.Interface{
			{Name: "lo0", Addresses: []netip.Prefix{netip.MustParsePrefix("::1/128")}, Up: true, Loopback: true},
			{Name: "en1", Description: "USB Ethernet", Addresses: []netip.Prefix{
				netip.MustParsePrefix("2001:db8::10/64"),
				netip.MustParsePrefix("192.0.2.10/24"),
			}, Up: true},
		}, nil
	})

	document, err := backend.Interfaces(context.Background())
	if err != nil {
		t.Fatalf("Interfaces() error = %v", err)
	}
	want := managementapi.InterfacesDocument{
		Configured: "auto",
		Selected:   "en1",
		Interfaces: []managementapi.CaptureInterface{
			{Name: "en1", Description: "USB Ethernet", Addresses: []string{"192.0.2.10/24", "2001:db8::10/64"}, Up: true},
			{Name: "lo0", Addresses: []string{"::1/128"}, Up: true, Loopback: true},
		},
	}
	if !reflect.DeepEqual(document, want) {
		t.Fatalf("Interfaces() = %#v, want %#v", document, want)
	}
}

func TestManagementInterfacesRepresentsUnresolvedSelection(t *testing.T) {
	configuration := managementTestConfig()
	configuration.Capture.Interface = "missing0"
	backend := newManagementInterfaceTestBackend(t, configuration, func() ([]capture.Interface, error) {
		return []capture.Interface{}, nil
	})
	document, err := backend.Interfaces(context.Background())
	if err != nil {
		t.Fatalf("Interfaces() error = %v", err)
	}
	if document.Configured != "missing0" || document.Selected != "" || document.Interfaces == nil || len(document.Interfaces) != 0 {
		t.Fatalf("Interfaces() = %#v, want unresolved empty discovery", document)
	}
}

func TestManagementInterfacesMapsDiscoveryAndStateErrors(t *testing.T) {
	configuration := managementTestConfig()
	private := errors.New("private libpcap failure")
	backend := newManagementInterfaceTestBackend(t, configuration, func() ([]capture.Interface, error) {
		return nil, private
	})
	assertManagementBackendError(t, interfaceError(backend.Interfaces(context.Background())), managementapi.ErrorUnavailable, "interfaces_unavailable")

	backend.interfaces = func() ([]capture.Interface, error) {
		return []capture.Interface{{Name: "en0"}, {Name: "en0"}}, nil
	}
	assertManagementBackendError(t, interfaceError(backend.Interfaces(context.Background())), managementapi.ErrorUnavailable, "interfaces_unavailable")

	assertManagementBackendError(t, interfaceError(backend.Interfaces(nil)), managementapi.ErrorInvalid, "invalid_interface_request")
	backend.ready.Store(false)
	assertManagementBackendError(t, interfaceError(backend.Interfaces(context.Background())), managementapi.ErrorUnavailable, "runtime_unavailable")
}

func newManagementInterfaceTestBackend(t *testing.T, configuration config.Config, discover func() ([]capture.Interface, error)) *managementBackend {
	t.Helper()
	controller := mustController(t, configuration, nil, nil)
	var ready atomic.Bool
	ready.Store(true)
	backend := newTestManagementBackend(controller, &ready, context.Background())
	backend.interfaces = discover
	return backend
}

func interfaceError(_ managementapi.InterfacesDocument, err error) error { return err }
