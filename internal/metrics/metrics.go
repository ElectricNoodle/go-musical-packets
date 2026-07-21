// Package metrics implements Prometheus observers for application pipelines.
package metrics

import (
	"fmt"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
)

// Bundle owns an isolated registry and its application observers.
type Bundle struct {
	Registry *prometheus.Registry
	Pipeline *PipelineObserver
}

// New constructs an isolated registry with Go, process, and pipeline metrics.
func New(namespace string) (*Bundle, error) {
	registry := prometheus.NewRegistry()
	if err := registry.Register(collectors.NewGoCollector()); err != nil {
		return nil, fmt.Errorf("register Go collector: %w", err)
	}
	if err := registry.Register(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{})); err != nil {
		return nil, fmt.Errorf("register process collector: %w", err)
	}
	observer, err := NewPipelineObserver(namespace, registry)
	if err != nil {
		return nil, err
	}
	return &Bundle{Registry: registry, Pipeline: observer}, nil
}

// PipelineObserver is the Prometheus implementation of pipeline.Observer.
type PipelineObserver struct {
	packetsCaptured     *prometheus.CounterVec
	packetBytes         *prometheus.CounterVec
	captureErrors       *prometheus.CounterVec
	dropped             *prometheus.CounterVec
	packetQueueDepth    prometheus.Gauge
	packetQueueCapacity prometheus.Gauge
	noteQueueDepth      prometheus.Gauge
	noteQueueCapacity   prometheus.Gauge
	flowsActive         prometheus.Gauge
	flowEvictions       *prometheus.CounterVec
	flowSelections      *prometheus.CounterVec
	mappingEvents       *prometheus.CounterVec
	mappingDuration     prometheus.Histogram
	mappingNoteVelocity prometheus.Histogram
	mappingNoteDuration prometheus.Histogram
	processingDuration  prometheus.Histogram
}

// NewPipelineObserver registers pipeline collectors with a caller-owned
// registry. Namespace must be a valid Prometheus metric prefix.
func NewPipelineObserver(namespace string, registerer prometheus.Registerer) (*PipelineObserver, error) {
	if registerer == nil {
		return nil, fmt.Errorf("Prometheus registerer is required")
	}
	observer := &PipelineObserver{
		packetsCaptured: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace, Name: "packets_captured_total", Help: "Packets accepted from the capture source by normalized protocol.",
		}, []string{"protocol"}),
		packetBytes: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace, Name: "packet_bytes_captured_total", Help: "Wire bytes accepted from the capture source by normalized protocol.",
		}, []string{"protocol"}),
		captureErrors: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace, Name: "packet_capture_errors_total", Help: "Terminal capture-source errors by bounded reason.",
		}, []string{"reason"}),
		dropped: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace, Name: "packet_events_dropped_total", Help: "Events deliberately dropped by pipeline stage and bounded reason.",
		}, []string{"stage", "reason"}),
		packetQueueDepth: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace, Name: "packet_queue_depth", Help: "Current packets waiting for processing.",
		}),
		packetQueueCapacity: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace, Name: "packet_queue_capacity", Help: "Configured packet queue capacity.",
		}),
		noteQueueDepth: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace, Name: "note_queue_depth", Help: "Current mapped notes waiting for delivery.",
		}),
		noteQueueCapacity: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace, Name: "note_queue_capacity", Help: "Configured note queue capacity.",
		}),
		flowsActive: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace, Name: "flows_active", Help: "Current flows retained by the bounded registry.",
		}),
		flowEvictions: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace, Name: "flow_registry_evictions_total", Help: "Flows removed from the registry by bounded reason.",
		}, []string{"reason"}),
		flowSelections: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace, Name: "flow_selections_total", Help: "Packet flow-selection decisions by state and precedence tier.",
		}, []string{"state", "tier"}),
		mappingEvents: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace, Name: "mapping_events_total", Help: "Musical mapping attempts by bounded mode and result.",
		}, []string{"mode", "result"}),
		mappingDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Namespace: namespace, Name: "mapping_duration_seconds", Help: "Time spent converting selected packets into notes.",
			Buckets: prometheus.ExponentialBuckets(0.000001, 2, 16),
		}),
		mappingNoteVelocity: prometheus.NewHistogram(prometheus.HistogramOpts{
			Namespace: namespace, Name: "mapping_note_velocity", Help: "Velocity of successfully mapped notes.",
			Buckets: []float64{1, 16, 32, 48, 64, 80, 96, 112, 127},
		}),
		mappingNoteDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Namespace: namespace, Name: "mapping_note_duration_seconds", Help: "Duration of successfully mapped notes.",
			Buckets: []float64{0.025, 0.05, 0.1, 0.25, 0.5, 1, 2, 5},
		}),
		processingDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Namespace: namespace, Name: "packet_processing_duration_seconds", Help: "End-to-end selection and mapping time per processed packet.",
			Buckets: prometheus.ExponentialBuckets(0.000001, 2, 18),
		}),
	}

	collectors := []prometheus.Collector{
		observer.packetsCaptured, observer.packetBytes, observer.captureErrors, observer.dropped,
		observer.packetQueueDepth, observer.packetQueueCapacity, observer.noteQueueDepth,
		observer.noteQueueCapacity, observer.flowsActive, observer.flowEvictions,
		observer.flowSelections, observer.mappingEvents, observer.mappingDuration,
		observer.mappingNoteVelocity, observer.mappingNoteDuration, observer.processingDuration,
	}
	registered := make([]prometheus.Collector, 0, len(collectors))
	for _, collector := range collectors {
		if err := registerer.Register(collector); err != nil {
			for _, previous := range registered {
				registerer.Unregister(previous)
			}
			return nil, fmt.Errorf("register pipeline metrics: %w", err)
		}
		registered = append(registered, collector)
	}
	return observer, nil
}

func (o *PipelineObserver) PacketCaptured(protocol string, bytes int) {
	o.packetsCaptured.WithLabelValues(protocol).Inc()
	if bytes > 0 {
		o.packetBytes.WithLabelValues(protocol).Add(float64(bytes))
	}
}

func (o *PipelineObserver) CaptureError(reason string) {
	o.captureErrors.WithLabelValues(reason).Inc()
}

func (o *PipelineObserver) Dropped(stage, reason string) {
	o.dropped.WithLabelValues(stage, reason).Inc()
}

func (o *PipelineObserver) PacketQueue(depth, capacity int) {
	o.packetQueueDepth.Set(float64(depth))
	o.packetQueueCapacity.Set(float64(capacity))
}

func (o *PipelineObserver) NoteQueue(depth, capacity int) {
	o.noteQueueDepth.Set(float64(depth))
	o.noteQueueCapacity.Set(float64(capacity))
}

func (o *PipelineObserver) FlowCount(active int) {
	o.flowsActive.Set(float64(active))
}

func (o *PipelineObserver) FlowEvicted(reason string, count int) {
	if count > 0 {
		o.flowEvictions.WithLabelValues(reason).Add(float64(count))
	}
}

func (o *PipelineObserver) Selected(state, tier string) {
	o.flowSelections.WithLabelValues(state, tier).Inc()
}

func (o *PipelineObserver) Mapped(mode, result string, elapsed, noteDuration time.Duration, velocity uint8) {
	o.mappingEvents.WithLabelValues(mode, result).Inc()
	o.mappingDuration.Observe(elapsed.Seconds())
	if result == "success" {
		o.mappingNoteDuration.Observe(noteDuration.Seconds())
		o.mappingNoteVelocity.Observe(float64(velocity))
	}
}

func (o *PipelineObserver) Processed(elapsed time.Duration) {
	o.processingDuration.Observe(elapsed.Seconds())
}
