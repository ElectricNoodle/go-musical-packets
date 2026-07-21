package metrics

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestPipelineObserverExportsBoundedOperationalMetrics(t *testing.T) {
	registry := prometheus.NewRegistry()
	observer, err := NewPipelineObserver("musical_packets", registry)
	if err != nil {
		t.Fatalf("NewPipelineObserver() error = %v", err)
	}

	observer.PacketCaptured("tcp", 128)
	observer.Dropped("note_queue", "full")
	observer.PacketQueue(2, 32)
	observer.NoteQueue(1, 16)
	observer.FlowCount(3)
	observer.FlowEvicted("ttl", 2)
	observer.Selected("play", "user")
	observer.Mapped("dorian", "success", time.Millisecond, 250*time.Millisecond, 96)
	observer.Processed(2 * time.Millisecond)

	assertMetricValue(t, observer.packetsCaptured.WithLabelValues("tcp"), 1)
	assertMetricValue(t, observer.packetBytes.WithLabelValues("tcp"), 128)
	assertMetricValue(t, observer.dropped.WithLabelValues("note_queue", "full"), 1)
	assertMetricValue(t, observer.packetQueueDepth, 2)
	assertMetricValue(t, observer.packetQueueCapacity, 32)
	assertMetricValue(t, observer.noteQueueDepth, 1)
	assertMetricValue(t, observer.noteQueueCapacity, 16)
	assertMetricValue(t, observer.flowsActive, 3)
	assertMetricValue(t, observer.flowEvictions.WithLabelValues("ttl"), 2)
	assertMetricValue(t, observer.flowSelections.WithLabelValues("play", "user"), 1)
	assertMetricValue(t, observer.mappingEvents.WithLabelValues("dorian", "success"), 1)

	families, err := registry.Gather()
	if err != nil {
		t.Fatalf("Gather() error = %v", err)
	}
	if len(families) == 0 {
		t.Fatal("Gather() returned no metric families")
	}
}

func TestNewPipelineObserverRejectsDuplicateRegistration(t *testing.T) {
	registry := prometheus.NewRegistry()
	if _, err := NewPipelineObserver("musical_packets", registry); err != nil {
		t.Fatalf("first NewPipelineObserver() error = %v", err)
	}
	if _, err := NewPipelineObserver("musical_packets", registry); err == nil {
		t.Fatal("duplicate NewPipelineObserver() error = nil")
	}
}

func TestMIDIObserverExportsLifecycleAndSchedulerMetrics(t *testing.T) {
	registry := prometheus.NewRegistry()
	observer, err := NewMIDIObserver("musical_packets", registry)
	if err != nil {
		t.Fatalf("NewMIDIObserver() error = %v", err)
	}
	observer.DeviceState("connected")
	observer.Reconnect("success")
	observer.DeviceError("send")
	observer.Note(4, "played")
	observer.Write("note_on", "success", time.Millisecond)
	observer.Active(4, 2, 3)

	assertMetricValue(t, observer.deviceConnected, 1)
	assertMetricValue(t, observer.reconnects.WithLabelValues("success"), 1)
	assertMetricValue(t, observer.deviceErrors.WithLabelValues("send"), 1)
	assertMetricValue(t, observer.notes.WithLabelValues("4", "played"), 1)
	assertMetricValue(t, observer.writes.WithLabelValues("note_on", "success"), 1)
	assertMetricValue(t, observer.activeByChannel.WithLabelValues("4"), 2)
	assertMetricValue(t, observer.activeTotal, 3)
}

func TestManagementObserverExportsRequestAndUpdateMetrics(t *testing.T) {
	registry := prometheus.NewRegistry()
	observer, err := NewManagementObserver("musical_packets", registry)
	if err != nil {
		t.Fatalf("NewManagementObserver() error = %v", err)
	}
	observer.Request("/api/v1/config", "PUT", "success", 2*time.Millisecond)
	observer.ConfigUpdate("success")

	assertMetricValue(t, observer.requests.WithLabelValues("/api/v1/config", "PUT", "success"), 1)
	assertMetricValue(t, observer.configUpdates.WithLabelValues("success"), 1)
	families, err := registry.Gather()
	if err != nil {
		t.Fatalf("Gather() error = %v", err)
	}
	foundDuration := false
	for _, family := range families {
		if family.GetName() == "musical_packets_management_api_request_duration_seconds" {
			foundDuration = true
			break
		}
	}
	if !foundDuration {
		t.Fatal("management request duration histogram was not exported")
	}
}

func TestNewManagementObserverRejectsDuplicateRegistration(t *testing.T) {
	registry := prometheus.NewRegistry()
	if _, err := NewManagementObserver("musical_packets", registry); err != nil {
		t.Fatalf("first NewManagementObserver() error = %v", err)
	}
	if _, err := NewManagementObserver("musical_packets", registry); err == nil {
		t.Fatal("duplicate NewManagementObserver() error = nil")
	}
}

func TestBundleIncludesManagementObserver(t *testing.T) {
	bundle, err := New("musical_packets")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if bundle.Registry == nil || bundle.Pipeline == nil || bundle.MIDI == nil || bundle.Management == nil {
		t.Fatalf("New() returned incomplete bundle: %#v", bundle)
	}
}

func assertMetricValue(t *testing.T, collector prometheus.Collector, want float64) {
	t.Helper()
	if got := testutil.ToFloat64(collector); got != want {
		t.Fatalf("metric value = %v, want %v", got, want)
	}
}
