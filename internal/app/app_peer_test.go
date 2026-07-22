package app

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ElectricNoodle/go-musical-packets/internal/capture"
	"github.com/ElectricNoodle/go-musical-packets/internal/config"
	"github.com/ElectricNoodle/go-musical-packets/internal/managementapi"
	"github.com/ElectricNoodle/go-musical-packets/internal/midi"
)

func TestRunComposesEdgeAndHostWithLivePeerManagement(t *testing.T) {
	const token = "stage-fourteen-token"

	hostConfiguration := testConfig()
	hostConfiguration.Instance.ID = "host-stage-14"
	hostConfiguration.Instance.Role = config.RoleHost
	hostConfiguration.Capture.Enabled = false
	hostConfiguration.Peer.Enabled = true
	hostConfiguration.Peer.Token = token
	hostConfiguration.Peer.StaleAfter = 5 * time.Second

	var hostLog operationLog
	hostDriver := &fakeDriver{devices: []midi.Device{{Number: 1, Name: "host synth"}}, log: &hostLog}
	hostReady := make(chan net.Listener, 1)
	hostContext, cancelHost := context.WithCancel(context.Background())
	hostDone := make(chan error, 1)
	go func() {
		hostDone <- RunWithDependencies(hostContext, hostConfiguration, Dependencies{
			NewMIDIDriver: func() (midi.Driver, error) { return hostDriver, nil },
			Listen:        notifyingRealListen(hostReady),
		})
	}()
	hostListener := awaitListener(t, hostReady, hostDone)
	hostAddress := hostListener.Addr().String()
	eventuallyRealHTTP(t, hostAddress, "/readyz", http.StatusOK, "ok")

	edgeConfiguration := testConfig()
	edgeConfiguration.Instance.ID = "edge-stage-14"
	edgeConfiguration.Instance.Role = config.RoleEdge
	edgeConfiguration.Mapping.DefaultState = config.FlowPlay
	edgeConfiguration.Mapping.DefaultChannel = 13
	edgeConfiguration.MIDI.Enabled = false
	edgeConfiguration.Peer.Enabled = true
	edgeConfiguration.Peer.URL = "ws://" + hostAddress + "/api/v1/peer"
	edgeConfiguration.Peer.Token = token
	edgeConfiguration.Peer.StaleAfter = 5 * time.Second
	edgeConfiguration.Peer.ReconnectBase = 10 * time.Millisecond
	edgeConfiguration.Peer.ReconnectLimit = 50 * time.Millisecond

	delivered := make(chan struct{})
	edgeReady := make(chan net.Listener, 1)
	var edgeMIDICalls atomic.Int32
	var opened capture.LiveConfig
	edgeContext, cancelEdge := context.WithCancel(context.Background())
	edgeDone := make(chan error, 1)
	go func() {
		edgeDone <- RunWithDependencies(edgeContext, edgeConfiguration, Dependencies{
			Interfaces: testInterfaces,
			OpenLive: func(configuration capture.LiveConfig) (capture.Source, error) {
				opened = configuration
				return &blockingAfterSource{event: testPacket(41000, 443, time.Now().UTC()), delivered: delivered}, nil
			},
			NewMIDIDriver: func() (midi.Driver, error) {
				edgeMIDICalls.Add(1)
				return nil, nil
			},
			Listen: notifyingRealListen(edgeReady),
		})
	}()
	edgeListener := awaitListener(t, edgeReady, edgeDone)
	edgeAddress := edgeListener.Addr().String()
	eventuallyRealHTTP(t, edgeAddress, "/readyz", http.StatusOK, "ok")

	observerConfiguration := edgeConfiguration.Clone()
	observerConfiguration.Instance.ID = "edge-z-observer"
	observerConfiguration.Capture.Enabled = false
	observerReady := make(chan net.Listener, 1)
	observerContext, cancelObserver := context.WithCancel(context.Background())
	observerDone := make(chan error, 1)
	go func() {
		observerDone <- RunWithDependencies(observerContext, observerConfiguration, Dependencies{
			NewMIDIDriver: func() (midi.Driver, error) {
				edgeMIDICalls.Add(1)
				return nil, nil
			},
			Listen: notifyingRealListen(observerReady),
		})
	}()
	observerListener := awaitListener(t, observerReady, observerDone)
	eventuallyRealHTTP(t, observerListener.Addr().String(), "/readyz", http.StatusOK, "ok")

	select {
	case <-delivered:
	case <-time.After(3 * time.Second):
		t.Fatal("edge capture did not consume its packet")
	}
	eventuallyCondition(t, func() bool { return countPrefix(hostLog.snapshot(), "midi.send:9c") == 1 })

	edgePeers := getPeersDocument(t, edgeAddress)
	if edgePeers.Role != "edge" || !edgePeers.Enabled || edgePeers.Outbound == nil || edgePeers.Outbound.RemoteInstance != "host-stage-14" || edgePeers.Outbound.SentTotal != 1 {
		t.Fatalf("edge peers = %#v", edgePeers)
	}
	hostPeers := getPeersDocument(t, hostAddress)
	if hostPeers.Role != "host" || !hostPeers.Enabled || len(hostPeers.Nodes) != 2 {
		t.Fatalf("host peers = %#v", hostPeers)
	}
	playedNode := connectedNode(hostPeers.Nodes, "edge-stage-14")
	if playedNode == nil || playedNode.AcceptedTotal != 1 || len(playedNode.ActiveChannels) != 1 || playedNode.ActiveChannels[0] != 13 {
		t.Fatalf("played host node = %#v, want one accepted note on channel 13", playedNode)
	}
	if got := edgeMIDICalls.Load(); got != 0 {
		t.Fatalf("edge MIDI initialization calls = %d, want 0", got)
	}
	hostPort := portFromAddress(t, hostAddress)
	if !strings.Contains(opened.BPF, "host 127.0.0.1") || !strings.Contains(opened.BPF, "tcp port "+strconv.Itoa(hostPort)) {
		t.Fatalf("edge BPF = %q, want address-scoped peer exclusion", opened.BPF)
	}

	cancelHost()
	if err := await(t, hostDone); err != nil {
		t.Fatalf("host RunWithDependencies() error = %v", err)
	}
	if !hostDriver.closed.Load() {
		t.Fatal("host MIDI driver was not closed")
	}
	eventuallyRealHTTP(t, edgeAddress, "/readyz", http.StatusServiceUnavailable, "peer host is unavailable")
	eventuallyCondition(t, func() bool {
		document := getPeersDocument(t, edgeAddress)
		return document.Outbound != nil && document.Outbound.Reconnects > 0
	})
	cancelEdge()
	if err := await(t, edgeDone); err != nil {
		t.Fatalf("edge RunWithDependencies() error = %v", err)
	}
	cancelObserver()
	if err := await(t, observerDone); err != nil {
		t.Fatalf("observer edge RunWithDependencies() error = %v", err)
	}
}

func connectedNode(nodes []managementapi.ConnectedNode, instanceID string) *managementapi.ConnectedNode {
	for index := range nodes {
		if nodes[index].InstanceID == instanceID {
			return &nodes[index]
		}
	}
	return nil
}

func notifyingRealListen(ready chan<- net.Listener) func(string, string) (net.Listener, error) {
	return func(network, address string) (net.Listener, error) {
		listener, err := net.Listen(network, address)
		if err == nil {
			ready <- listener
		}
		return listener, err
	}
}

func eventuallyRealHTTP(t *testing.T, address, path string, status int, bodyContains string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		response, err := http.Get("http://" + address + path)
		if err == nil {
			body, _ := io.ReadAll(response.Body)
			_ = response.Body.Close()
			if response.StatusCode == status && strings.Contains(string(body), bodyContains) {
				return
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("GET %s%s did not return %d containing %q", address, path, status, bodyContains)
}

func getPeersDocument(t *testing.T, address string) managementapi.PeersDocument {
	t.Helper()
	response, err := http.Get("http://" + address + "/api/v1/peers")
	if err != nil {
		t.Fatalf("GET peers: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("GET peers status = %d", response.StatusCode)
	}
	var document managementapi.PeersDocument
	if err := json.NewDecoder(response.Body).Decode(&document); err != nil {
		t.Fatalf("decode peers: %v", err)
	}
	return document
}

func portFromAddress(t *testing.T, address string) int {
	t.Helper()
	_, text, err := net.SplitHostPort(address)
	if err != nil {
		t.Fatalf("SplitHostPort(%q): %v", address, err)
	}
	port, err := strconv.Atoi(text)
	if err != nil {
		t.Fatalf("Atoi(%q): %v", text, err)
	}
	return port
}

func eventuallyCondition(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition was not satisfied")
}
