// Package app composes standalone, edge, and host musical-packets runtimes.
package app

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/ElectricNoodle/go-musical-packets/internal/capture"
	"github.com/ElectricNoodle/go-musical-packets/internal/config"
	"github.com/ElectricNoodle/go-musical-packets/internal/flow"
	"github.com/ElectricNoodle/go-musical-packets/internal/httpserver"
	"github.com/ElectricNoodle/go-musical-packets/internal/managementapi"
	"github.com/ElectricNoodle/go-musical-packets/internal/metrics"
	"github.com/ElectricNoodle/go-musical-packets/internal/midi"
	"github.com/ElectricNoodle/go-musical-packets/internal/music"
	"github.com/ElectricNoodle/go-musical-packets/internal/peer"
	"github.com/ElectricNoodle/go-musical-packets/internal/pipeline"
	"github.com/ElectricNoodle/go-musical-packets/internal/uistream"
	"github.com/ElectricNoodle/go-musical-packets/internal/webui"
)

const (
	captureReadTimeout = 250 * time.Millisecond
	safetySourceRuleID = "__musical_packets_http_source"
	safetyDestRuleID   = "__musical_packets_http_destination"
)

var errMIDIDisabled = errors.New("MIDI output is disabled")

// Dependencies contains operating-system boundaries that tests and embedding
// programs may replace. Zero fields use the production implementations.
type Dependencies struct {
	Interfaces           func() ([]capture.Interface, error)
	OpenLive             func(capture.LiveConfig) (capture.Source, error)
	OpenReplayFile       func(path string) (capture.Source, error)
	OpenConfigRepository func(path string) (ConfigRepository, error)
	NewMIDIDriver        func() (midi.Driver, error)
	Listen               func(network, address string) (net.Listener, error)
	LookupIP             func(context.Context, string) ([]net.IP, error)
	ReplayNow            func() time.Time
	ReplayWait           func(context.Context, time.Duration) error
	ReplayObserver       pipeline.Observer
	WebHandler           http.Handler
}

// RunOptions selects optional runtime capabilities. Supplying a
// config path makes that repository authoritative and enables persisted hot
// configuration updates. The zero value retains the legacy read-only runtime.
type RunOptions struct {
	ConfigPath       string
	ExpectedRevision config.Revision
}

func (dependencies Dependencies) withDefaults() Dependencies {
	if dependencies.Interfaces == nil {
		dependencies.Interfaces = capture.Interfaces
	}
	if dependencies.OpenLive == nil {
		dependencies.OpenLive = capture.OpenLive
	}
	if dependencies.OpenReplayFile == nil {
		dependencies.OpenReplayFile = capture.OpenReplayFile
	}
	if dependencies.OpenConfigRepository == nil {
		dependencies.OpenConfigRepository = func(path string) (ConfigRepository, error) {
			return config.NewFileRepository(path)
		}
	}
	if dependencies.NewMIDIDriver == nil {
		dependencies.NewMIDIDriver = midi.NewDriver
	}
	if dependencies.Listen == nil {
		dependencies.Listen = net.Listen
	}
	if dependencies.LookupIP == nil {
		dependencies.LookupIP = func(ctx context.Context, host string) ([]net.IP, error) {
			return net.DefaultResolver.LookupIP(ctx, "ip", host)
		}
	}
	if dependencies.ReplayNow == nil {
		dependencies.ReplayNow = time.Now
	}
	if dependencies.ReplayWait == nil {
		dependencies.ReplayWait = waitReplayDuration
	}
	if dependencies.WebHandler == nil {
		dependencies.WebHandler = webui.NewHandler()
	}
	return dependencies
}

// Run validates and runs the configured runtime role until cancellation or a
// terminal component result. Context cancellation is a normal shutdown.
func Run(ctx context.Context, configuration config.Config) error {
	return RunWithOptionsAndDependencies(ctx, configuration, RunOptions{}, Dependencies{})
}

// RunWithOptions is Run with optional persisted runtime-policy support.
func RunWithOptions(ctx context.Context, configuration config.Config, options RunOptions) error {
	return RunWithOptionsAndDependencies(ctx, configuration, options, Dependencies{})
}

// RunWithDependencies is Run with injectable capture, MIDI, and listener
// boundaries. It is intended for application-level integration tests and
// callers that embed the runtime.
func RunWithDependencies(ctx context.Context, configuration config.Config, dependencies Dependencies) (runErr error) {
	return RunWithOptionsAndDependencies(ctx, configuration, RunOptions{}, dependencies)
}

// RunWithOptionsAndDependencies combines optional persisted runtime-policy
// support with injectable operating-system boundaries.
func RunWithOptionsAndDependencies(
	ctx context.Context,
	configuration config.Config,
	options RunOptions,
	dependencies Dependencies,
) (runErr error) {
	if ctx == nil {
		return errors.New("application context is required")
	}
	dependencies = dependencies.withDefaults()

	var repository ConfigRepository
	if options.ConfigPath != "" {
		var err error
		repository, err = dependencies.OpenConfigRepository(options.ConfigPath)
		if err != nil {
			return fmt.Errorf("open config repository %q: %w", options.ConfigPath, err)
		}
		if repository == nil {
			return fmt.Errorf("open config repository %q: repository is unavailable", options.ConfigPath)
		}
	} else if err := configuration.Validate(); err != nil {
		return fmt.Errorf("validate configuration: %w", err)
	}

	controller, err := newController(configuration, repository, nil)
	if err != nil {
		return fmt.Errorf("initialize runtime policy: %w", err)
	}
	initialPolicy := controller.Current()
	if options.ExpectedRevision != "" && options.ExpectedRevision != initialPolicy.Revision {
		return fmt.Errorf("initialize runtime policy: %w", &config.ConflictError{
			Expected: options.ExpectedRevision,
			Actual:   initialPolicy.Revision,
		})
	}
	configuration = initialPolicy.Config
	if err := ctx.Err(); err != nil {
		return nil
	}

	bundle, err := metrics.New(configuration.Metrics.Namespace)
	if err != nil {
		return fmt.Errorf("initialize metrics: %w", err)
	}
	viewerStream := uistream.New(configuration.Performance.UIQueueCapacity, bundle.UI)
	var manager *midi.Manager
	var peers runtimePeers
	var runtimeReady atomic.Bool
	readiness := func(context.Context) error {
		if !runtimeReady.Load() {
			return errors.New("application is starting or stopping")
		}
		policy := controller.store.current.Load()
		if policy.state != ControllerStateReady && policy.state != ControllerStateRestartPending {
			return fmt.Errorf("runtime configuration state is %s", policy.state)
		}
		if configuration.MIDI.Enabled {
			if manager == nil {
				return midi.ErrOutputUnavailable
			}
			if _, connected := manager.Current(); !connected {
				return midi.ErrOutputUnavailable
			}
		}
		if peers.edge != nil {
			outbound := peers.edge.Snapshot().Outbound
			if outbound == nil || outbound.State != "connected" {
				return errors.New("peer host is unavailable")
			}
		}
		return nil
	}
	listener, err := dependencies.Listen("tcp", configuration.Server.ListenAddress)
	if err != nil {
		return fmt.Errorf("listen on %q: %w", configuration.Server.ListenAddress, err)
	}
	listenerOwnedByServer := false
	defer func() {
		if !listenerOwnedByServer {
			runErr = errors.Join(runErr, listener.Close())
		}
	}()

	httpPort, err := listenerPort(listener.Addr())
	if err != nil {
		return fmt.Errorf("resolve HTTP listener port: %w", err)
	}
	var capturePeer *peerCaptureEndpoint
	if configuration.Capture.Enabled {
		if configuration.Instance.Role == config.RoleEdge {
			resolved, resolveErr := resolvePeerCaptureEndpoint(ctx, configuration.Peer.URL, dependencies.LookupIP)
			if resolveErr != nil {
				return fmt.Errorf("resolve peer capture exclusion: %w", resolveErr)
			}
			capturePeer = &resolved
		}
		if err := controller.configureSafety(func(userRules []flow.Rule) []flow.Rule {
			rules := httpSafetyRules(httpPort, userRules)
			if capturePeer != nil {
				rules = append(rules, peerSafetyRules(*capturePeer, append(userRules, rules...))...)
			}
			return rules
		}); err != nil {
			return fmt.Errorf("configure capture safety policy: %w", err)
		}
	}
	processing, err := newProcessingComponents(configuration, controller)
	if err != nil {
		return err
	}
	operationalHandler, err := httpserver.NewHandler(bundle.Registry, nil, readiness)
	if err != nil {
		return fmt.Errorf("initialize HTTP handler: %w", err)
	}
	managementContext, cancelManagement := context.WithCancel(ctx)
	defer cancelManagement()

	var midiRuntime *midi.Runtime
	var acceptedMIDI managementMIDI
	var managerCancel context.CancelFunc
	var managerDone chan error
	if configuration.MIDI.Enabled {
		components, midiErr := newMIDIComponents(configuration, bundle, dependencies.NewMIDIDriver)
		if midiErr != nil {
			return midiErr
		}
		manager = components.manager
		midiRuntime = components.runtime
		acceptedMIDI = &viewerMIDI{runtime: midiRuntime, stream: viewerStream}

		managerContext, cancel := context.WithCancel(context.WithoutCancel(ctx))
		managerCancel = cancel
		managerDone = make(chan error, 1)
		go func() { managerDone <- manager.Run(managerContext) }()

		select {
		case <-manager.Ready():
		case managerErr := <-managerDone:
			return errors.Join(componentStopped("MIDI manager", managerErr), closeMIDIRuntime(midiRuntime))
		case <-ctx.Done():
			closeErr := closeMIDIRuntime(midiRuntime)
			managerCancel()
			managerErr := <-managerDone
			return errors.Join(closeErr, normalizeComponentError(managerErr))
		}
	}
	hostSink := pipeline.Sink(discardSink{})
	if acceptedMIDI != nil {
		hostSink = acceptedMIDI
	}
	peers, err = newRuntimePeers(configuration, bundle, hostSink)
	if err != nil {
		return shutdownStartup(err, nil, midiRuntime, managerCancel, managerDone)
	}
	managementBackend, managementErr := newManagementBackend(
		controller,
		processing.registry,
		dependencies.Interfaces,
		acceptedMIDI,
		&runtimeReady,
		managementContext,
	)
	if managementErr != nil {
		return shutdownStartup(
			fmt.Errorf("initialize management backend: %w", managementErr),
			peers.host,
			midiRuntime,
			managerCancel,
			managerDone,
		)
	}
	managementBackend.peers = peers.snapshotter
	managementHandler, managementErr := managementapi.NewHandler(
		managementBackend,
		managementapi.Options{AllowedPort: httpPort, Observer: bundle.Management},
	)
	if managementErr != nil {
		return shutdownStartup(
			fmt.Errorf("initialize management API: %w", managementErr),
			peers.host,
			midiRuntime,
			managerCancel,
			managerDone,
		)
	}
	eventHandler := uistream.NewHandler(managementContext, viewerStream, httpPort)
	handler := http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if peers.host != nil && request.URL.EscapedPath() == peer.Path {
			peers.host.ServeHTTP(response, request)
			return
		}
		if request.URL.EscapedPath() == "/api/v1/events" {
			eventHandler.ServeHTTP(response, request)
			return
		}
		if request.URL.Path == "/api" || strings.HasPrefix(request.URL.Path, "/api/") {
			managementHandler.ServeHTTP(response, request)
			return
		}
		switch request.URL.Path {
		case "/metrics", "/healthz", "/readyz":
			operationalHandler.ServeHTTP(response, request)
		default:
			if !requestIsLoopback(request) {
				http.NotFound(response, request)
				return
			}
			dependencies.WebHandler.ServeHTTP(response, request)
		}
	})

	var source capture.Source
	var processor *pipeline.Processor
	if configuration.Capture.Enabled {
		interfaces, interfacesErr := dependencies.Interfaces()
		if interfacesErr != nil {
			return shutdownStartup(fmt.Errorf("list capture interfaces: %w", interfacesErr), peers.host, midiRuntime, managerCancel, managerDone)
		}
		selected, selectErr := capture.SelectInterface(interfaces, configuration.Capture.Interface)
		if selectErr != nil {
			return shutdownStartup(fmt.Errorf("select capture interface: %w", selectErr), peers.host, midiRuntime, managerCancel, managerDone)
		}
		source, err = dependencies.OpenLive(capture.LiveConfig{
			Device:         selected.Name,
			SnapshotLength: configuration.Capture.SnapshotLength,
			Promiscuous:    configuration.Capture.Promiscuous,
			Timeout:        captureReadTimeout,
			BPF:            captureBPFWithPeer(configuration.Capture.BPF, httpPort, capturePeer),
		})
		if err != nil {
			return shutdownStartup(fmt.Errorf("open live capture: %w", err), peers.host, midiRuntime, managerCancel, managerDone)
		}

		sink := pipeline.Sink(discardSink{})
		if peers.edge != nil {
			sink = &viewerSink{sink: peers.edge, stream: viewerStream}
		} else if acceptedMIDI != nil {
			sink = acceptedMIDI
		}
		processor, err = newProcessor(configuration, processing, source, sink, viewerPipelineObserver{
			metrics: bundle.Pipeline,
			stream:  viewerStream,
		})
		if err != nil {
			pipelineErr := errors.Join(err, source.Close())
			return shutdownStartup(pipelineErr, peers.host, midiRuntime, managerCancel, managerDone)
		}
	}

	server := &http.Server{
		Handler:      handler,
		ReadTimeout:  configuration.Server.ReadTimeout,
		WriteTimeout: configuration.Server.WriteTimeout,
	}
	serverDone := make(chan error, 1)
	listenerOwnedByServer = true
	go func() { serverDone <- server.Serve(listener) }()

	var edgeCancel context.CancelFunc
	var edgeDone chan error
	if peers.edge != nil {
		edgeContext, cancel := context.WithCancel(context.WithoutCancel(ctx))
		edgeCancel = cancel
		edgeDone = make(chan error, 1)
		go func() { edgeDone <- peers.edge.Run(edgeContext) }()
	}

	var processorCancel context.CancelFunc
	var processorDone chan error
	if processor != nil {
		processorContext, cancel := context.WithCancel(context.WithoutCancel(ctx))
		processorCancel = cancel
		processorDone = make(chan error, 1)
		go func() { processorDone <- processor.Run(processorContext) }()
	}
	runtimeReady.Store(true)

	return supervise(
		ctx, configuration.Server.WriteTimeout, server, serverDone, &runtimeReady, cancelManagement,
		processorCancel, processorDone, edgeCancel, edgeDone, peers.host,
		midiRuntime, managerCancel, managerDone,
	)
}

type discardSink struct{}

func (discardSink) Write(context.Context, music.NoteEvent) error { return errMIDIDisabled }

func listenerPort(address net.Addr) (uint16, error) {
	if address == nil {
		return 0, errors.New("listener address is unavailable")
	}
	_, portText, err := net.SplitHostPort(address.String())
	if err != nil {
		return 0, fmt.Errorf("parse listener address %q: %w", address, err)
	}
	port, err := strconv.ParseUint(portText, 10, 16)
	if err != nil {
		return 0, fmt.Errorf("parse listener port %q: %w", portText, err)
	}
	if port == 0 {
		return 0, errors.New("listener reported port zero")
	}
	return uint16(port), nil
}

func requestIsLoopback(request *http.Request) bool {
	host, _, err := net.SplitHostPort(request.RemoteAddr)
	if err != nil {
		return false
	}
	address := net.ParseIP(host)
	return address != nil && address.IsLoopback()
}

func httpSafetyRules(port uint16, existing []flow.Rule) []flow.Rule {
	sourceID := uniqueRuleID(safetySourceRuleID, existing)
	destinationID := uniqueRuleID(safetyDestRuleID, append(existing, flow.Rule{ID: sourceID}))
	sourcePort := &flow.PortRange{Minimum: port, Maximum: port}
	destinationPort := &flow.PortRange{Minimum: port, Maximum: port}
	return []flow.Rule{
		{
			ID:      sourceID,
			Name:    "application HTTP source traffic",
			Enabled: true,
			Match:   flow.Match{Protocol: "tcp", SourcePorts: sourcePort},
			Action:  flow.Action{State: flow.StateIgnore},
		},
		{
			ID:      destinationID,
			Name:    "application HTTP destination traffic",
			Enabled: true,
			Match:   flow.Match{Protocol: "tcp", DestinationPorts: destinationPort},
			Action:  flow.Action{State: flow.StateIgnore},
		},
	}
}

func uniqueRuleID(base string, existing []flow.Rule) string {
	used := make(map[string]struct{}, len(existing))
	for _, rule := range existing {
		used[rule.ID] = struct{}{}
	}
	for suffix := 0; ; suffix++ {
		candidate := base
		if suffix > 0 {
			candidate += "_" + strconv.Itoa(suffix)
		}
		if _, found := used[candidate]; !found {
			return candidate
		}
	}
}

func captureBPF(configured string, port uint16) string {
	exclusion := fmt.Sprintf("not (tcp src port %d or tcp dst port %d)", port, port)
	configured = strings.TrimSpace(configured)
	if configured == "" {
		return exclusion
	}
	return fmt.Sprintf("(%s) and (%s)", configured, exclusion)
}

func shutdownStartup(startupErr error, host *peer.Host, midiRuntime *midi.Runtime, managerCancel context.CancelFunc, managerDone <-chan error) error {
	if host != nil {
		host.Close()
	}
	closeErr := closeMIDIRuntime(midiRuntime)
	var managerErr error
	if managerCancel != nil {
		managerCancel()
		managerErr = <-managerDone
	}
	return errors.Join(startupErr, closeErr, normalizeComponentError(managerErr))
}

func supervise(
	ctx context.Context,
	shutdownTimeout time.Duration,
	server *http.Server,
	serverDone <-chan error,
	runtimeReady *atomic.Bool,
	cancelManagement context.CancelFunc,
	processorCancel context.CancelFunc,
	processorDone <-chan error,
	edgeCancel context.CancelFunc,
	edgeDone <-chan error,
	host *peer.Host,
	midiRuntime *midi.Runtime,
	managerCancel context.CancelFunc,
	managerDone <-chan error,
) error {
	var result error
	processorFinished := processorDone == nil
	edgeFinished := edgeDone == nil
	managerFinished := managerDone == nil
	serverFinished := false

	select {
	case <-ctx.Done():
	case processorErr := <-processorDone:
		processorFinished = true
		result = componentStopped("packet pipeline", processorErr)
	case edgeErr := <-edgeDone:
		edgeFinished = true
		result = componentStopped("edge peer", edgeErr)
	case managerErr := <-managerDone:
		managerFinished = true
		result = componentStopped("MIDI manager", managerErr)
	case serverErr := <-serverDone:
		serverFinished = true
		result = componentStopped("HTTP server", normalizeHTTPError(serverErr))
	}

	runtimeReady.Store(false)
	cancelManagement()
	// Keep these phases deliberately sequential: MIDI reset depends on the
	// manager output remaining alive until the pipeline can no longer write.
	if processorCancel != nil {
		processorCancel()
	}
	if !processorFinished {
		result = errors.Join(result, normalizeComponentError(<-processorDone))
	}
	if edgeCancel != nil {
		edgeCancel()
	}
	if !edgeFinished {
		result = errors.Join(result, normalizeComponentError(<-edgeDone))
	}
	if host != nil {
		host.Close()
	}
	result = errors.Join(result, closeMIDIRuntime(midiRuntime))
	if managerCancel != nil {
		managerCancel()
	}
	if !managerFinished {
		result = errors.Join(result, normalizeComponentError(<-managerDone))
	}

	shutdownContext, cancelShutdown := context.WithTimeout(context.Background(), shutdownTimeout)
	shutdownErr := server.Shutdown(shutdownContext)
	cancelShutdown()
	if shutdownErr != nil {
		shutdownErr = errors.Join(shutdownErr, server.Close())
	}
	result = errors.Join(result, shutdownErr)
	if !serverFinished {
		result = errors.Join(result, normalizeHTTPError(<-serverDone))
	}
	return result
}

func closeMIDIRuntime(midiRuntime *midi.Runtime) error {
	if midiRuntime == nil {
		return nil
	}
	return midiRuntime.Close()
}

func componentStopped(name string, err error) error {
	if normalized := normalizeComponentError(err); normalized != nil {
		return fmt.Errorf("%s: %w", name, normalized)
	}
	return fmt.Errorf("%s stopped unexpectedly", name)
}

func normalizeComponentError(err error) error {
	normalized, _ := filterExpectedErrors(err, func(candidate error) bool {
		return errors.Is(candidate, context.Canceled) || errors.Is(candidate, context.DeadlineExceeded)
	})
	return normalized
}

func normalizeHTTPError(err error) error {
	normalized, _ := filterExpectedErrors(err, func(candidate error) bool {
		return errors.Is(candidate, http.ErrServerClosed) ||
			errors.Is(candidate, context.Canceled) ||
			errors.Is(candidate, context.DeadlineExceeded)
	})
	return normalized
}

func filterExpectedErrors(err error, expected func(error) bool) (error, bool) {
	if err == nil {
		return nil, false
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		children := joined.Unwrap()
		remaining := make([]error, 0, len(children))
		changed := false
		for _, child := range children {
			normalized, childChanged := filterExpectedErrors(child, expected)
			changed = changed || childChanged
			if normalized != nil {
				remaining = append(remaining, normalized)
			}
		}
		if !changed {
			return err, false
		}
		return errors.Join(remaining...), true
	}
	if wrapped, ok := err.(interface{ Unwrap() error }); ok {
		normalized, changed := filterExpectedErrors(wrapped.Unwrap(), expected)
		if !changed {
			return err, false
		}
		return normalized, true
	}
	if expected(err) {
		return nil, true
	}
	return err, false
}
