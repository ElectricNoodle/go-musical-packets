package capture

import (
	"net/netip"
	"testing"
)

func TestSelectInterface(t *testing.T) {
	interfaces := []Interface{
		{Name: "lo0", Up: true, Loopback: true},
		{Name: "down0"},
		{Name: "en0", Up: true, Addresses: mustPrefixes(t, "192.0.2.10/24")},
	}

	selected, err := SelectInterface(interfaces, "auto")
	if err != nil {
		t.Fatalf("SelectInterface(auto) error = %v", err)
	}
	if selected.Name != "en0" {
		t.Fatalf("SelectInterface(auto) = %q, want en0", selected.Name)
	}

	selected, err = SelectInterface(interfaces, "lo0")
	if err != nil {
		t.Fatalf("SelectInterface(lo0) error = %v", err)
	}
	if selected.Name != "lo0" {
		t.Fatalf("SelectInterface(lo0) = %q", selected.Name)
	}
}

func mustPrefixes(t *testing.T, values ...string) []netip.Prefix {
	t.Helper()
	prefixes := make([]netip.Prefix, 0, len(values))
	for _, value := range values {
		prefix, err := netip.ParsePrefix(value)
		if err != nil {
			t.Fatalf("ParsePrefix(%q) error = %v", value, err)
		}
		prefixes = append(prefixes, prefix)
	}
	return prefixes
}
