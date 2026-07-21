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

func assertMetricValue(t *testing.T, collector prometheus.Collector, want float64) {
	t.Helper()
	if got := testutil.ToFloat64(collector); got != want {
		t.Fatalf("metric value = %v, want %v", got, want)
	}
}
