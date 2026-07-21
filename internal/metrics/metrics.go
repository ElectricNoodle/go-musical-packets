// Package metrics implements Prometheus observers for application pipelines.
package metrics

import (
	"fmt"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
)

// Bundle owns an isolated registry and its application observers.
type Bundle struct {
	Registry   *prometheus.Registry
	Pipeline   *PipelineObserver
	MIDI       *MIDIObserver
	Management *ManagementObserver
	UI         *UIObserver
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
	midiObserver, err := NewMIDIObserver(namespace, registry)
	if err != nil {
		return nil, err
	}
	managementObserver, err := NewManagementObserver(namespace, registry)
	if err != nil {
		return nil, err
	}
	uiObserver, err := NewUIObserver(namespace, registry)
	if err != nil {
		return nil, err
	}
	return &Bundle{Registry: registry, Pipeline: observer, MIDI: midiObserver, Management: managementObserver, UI: uiObserver}, nil
}

// UIObserver implements the browser-event stream observer contract.
type UIObserver struct {
	clients prometheus.Gauge
	events  *prometheus.CounterVec
}

// NewUIObserver registers bounded live-viewer collectors.
func NewUIObserver(namespace string, registerer prometheus.Registerer) (*UIObserver, error) {
	if registerer == nil {
		return nil, fmt.Errorf("Prometheus registerer is required")
	}
	observer := &UIObserver{
		clients: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace, Name: "ui_clients", Help: "Currently connected live-viewer clients.",
		}),
		events: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace, Name: "ui_events_total", Help: "Live-viewer note events by bounded delivery result.",
		}, []string{"result"}),
	}
	if err := registerer.Register(observer.clients); err != nil {
		return nil, fmt.Errorf("register UI client metric: %w", err)
	}
	if err := registerer.Register(observer.events); err != nil {
		registerer.Unregister(observer.clients)
		return nil, fmt.Errorf("register UI event metric: %w", err)
	}
	return observer, nil
}

func (observer *UIObserver) Clients(count int) {
	observer.clients.Set(float64(count))
}

func (observer *UIObserver) Events(result string, count int) {
	if count > 0 {
		observer.events.WithLabelValues(result).Add(float64(count))
	}
}

// ManagementObserver implements the management API observer contract using
// only route, method, and result values normalized by that transport boundary.
type ManagementObserver struct {
	requests      *prometheus.CounterVec
	requestTime   *prometheus.HistogramVec
	configUpdates *prometheus.CounterVec
}

// NewManagementObserver registers bounded-cardinality management collectors.
func NewManagementObserver(namespace string, registerer prometheus.Registerer) (*ManagementObserver, error) {
	if registerer == nil {
		return nil, fmt.Errorf("Prometheus registerer is required")
	}
	observer := &ManagementObserver{
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace, Name: "management_api_requests_total", Help: "Management API requests by normalized route, method, and bounded result.",
		}, []string{"route", "method", "result"}),
		requestTime: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: namespace, Name: "management_api_request_duration_seconds", Help: "Management API request duration by normalized route and method.",
			Buckets: prometheus.ExponentialBuckets(0.00001, 2, 18),
		}, []string{"route", "method"}),
		configUpdates: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace, Name: "management_config_updates_total", Help: "Configuration update attempts by bounded result.",
		}, []string{"result"}),
	}
	collectors := []prometheus.Collector{observer.requests, observer.requestTime, observer.configUpdates}
	registered := make([]prometheus.Collector, 0, len(collectors))
	for _, collector := range collectors {
		if err := registerer.Register(collector); err != nil {
			for _, previous := range registered {
				registerer.Unregister(previous)
			}
			return nil, fmt.Errorf("register management metrics: %w", err)
		}
		registered = append(registered, collector)
	}
	return observer, nil
}

// Request records one completed API request. Callers supply normalized labels.
func (observer *ManagementObserver) Request(route, method, result string, elapsed time.Duration) {
	observer.requests.WithLabelValues(route, method, result).Inc()
	observer.requestTime.WithLabelValues(route, method).Observe(elapsed.Seconds())
}

// ConfigUpdate records the terminal classification of one PUT configuration
// request.
func (observer *ManagementObserver) ConfigUpdate(result string) {
	observer.configUpdates.WithLabelValues(result).Inc()
}

// MIDIObserver implements the MIDI scheduler and manager observer contracts.
type MIDIObserver struct {
	deviceConnected prometheus.Gauge
	reconnects      *prometheus.CounterVec
	deviceErrors    *prometheus.CounterVec
	notes           *prometheus.CounterVec
	writes          *prometheus.CounterVec
	writeDuration   *prometheus.HistogramVec
	activeByChannel *prometheus.GaugeVec
	activeTotal     prometheus.Gauge
}

// NewMIDIObserver registers bounded-cardinality MIDI collectors.
func NewMIDIObserver(namespace string, registerer prometheus.Registerer) (*MIDIObserver, error) {
	if registerer == nil {
		return nil, fmt.Errorf("Prometheus registerer is required")
	}
	observer := &MIDIObserver{
		deviceConnected: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace, Name: "midi_device_connected", Help: "Whether a selected MIDI output is currently connected.",
		}),
		reconnects: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace, Name: "midi_reconnects_total", Help: "MIDI discovery and reconnect attempts by bounded result.",
		}, []string{"result"}),
		deviceErrors: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace, Name: "midi_errors_total", Help: "MIDI device errors by bounded operation.",
		}, []string{"operation"}),
		notes: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace, Name: "midi_notes_total", Help: "MIDI note scheduler decisions by channel and bounded result.",
		}, []string{"channel", "result"}),
		writes: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace, Name: "midi_writes_total", Help: "MIDI writes by bounded operation and result.",
		}, []string{"operation", "result"}),
		writeDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: namespace, Name: "midi_write_duration_seconds", Help: "Time spent writing MIDI messages.",
			Buckets: prometheus.ExponentialBuckets(0.00001, 2, 16),
		}, []string{"operation"}),
		activeByChannel: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: namespace, Name: "midi_active_notes", Help: "Currently active scheduled notes by channel.",
		}, []string{"channel"}),
		activeTotal: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace, Name: "midi_active_notes_current", Help: "Currently active scheduled notes across all channels.",
		}),
	}
	metricCollectors := []prometheus.Collector{
		observer.deviceConnected, observer.reconnects, observer.deviceErrors,
		observer.notes, observer.writes, observer.writeDuration,
		observer.activeByChannel, observer.activeTotal,
	}
	registered := make([]prometheus.Collector, 0, len(metricCollectors))
	for _, collector := range metricCollectors {
		if err := registerer.Register(collector); err != nil {
			for _, previous := range registered {
				registerer.Unregister(previous)
			}
			return nil, fmt.Errorf("register MIDI metrics: %w", err)
		}
		registered = append(registered, collector)
	}
	return observer, nil
}

func (o *MIDIObserver) DeviceState(state string) {
	if state == "connected" {
		o.deviceConnected.Set(1)
		return
	}
	o.deviceConnected.Set(0)
}

func (o *MIDIObserver) Reconnect(result string) {
	o.reconnects.WithLabelValues(result).Inc()
}

func (o *MIDIObserver) DeviceError(operation string) {
	o.deviceErrors.WithLabelValues(operation).Inc()
}

func (o *MIDIObserver) Note(channel uint8, result string) {
	o.notes.WithLabelValues(strconv.Itoa(int(channel)), result).Inc()
}

func (o *MIDIObserver) Write(operation, result string, elapsed time.Duration) {
	o.writes.WithLabelValues(operation, result).Inc()
	o.writeDuration.WithLabelValues(operation).Observe(elapsed.Seconds())
}

func (o *MIDIObserver) Active(channel uint8, count, total int) {
	o.activeByChannel.WithLabelValues(strconv.Itoa(int(channel))).Set(float64(count))
	o.activeTotal.Set(float64(total))
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
