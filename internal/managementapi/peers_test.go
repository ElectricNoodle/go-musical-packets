package managementapi

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"testing"
	"time"
)

func TestPeersGetAndHead(t *testing.T) {
	now := time.Date(2026, 7, 22, 10, 0, 0, 0, time.UTC)
	want := PeersDocument{
		Role: "host",
		Nodes: []ConnectedNode{{
			InstanceID: "edge-1", RemoteAddress: "192.0.2.4:53000", State: "connected", Authenticated: true,
			ProtocolVersion: "peer-v1", MappingVersion: "flow-mode-v1", ConnectedAt: now, LastSeenAt: now,
			NoteRate: 2.5, ReceivedTotal: 4, AcceptedTotal: 3, RejectedTotal: 1, ActiveChannels: []uint8{2, 7},
		}},
	}
	calls := 0
	handler := mustHandler(t, &stubBackend{peersFunc: func(ctx context.Context) (PeersDocument, error) {
		calls++
		if ctx == nil {
			t.Fatal("Peers context = nil")
		}
		return want, nil
	}})
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		response := serve(handler, localRequestFor(method, peersPath, ""))
		assertStatus(t, response, http.StatusOK)
		if method == http.MethodHead {
			if response.Body.Len() != 0 || response.Header().Get("Content-Length") == "" {
				t.Fatalf("HEAD response body/length = %q/%q", response.Body.String(), response.Header().Get("Content-Length"))
			}
			continue
		}
		var got PeersDocument
		if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
			t.Fatalf("decode peers: %v", err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("peers = %#v, want %#v", got, want)
		}
	}
	if calls != 2 {
		t.Fatalf("Peers calls = %d, want 2", calls)
	}
}

func TestPeersRejectsQueryAndUnsupportedMethod(t *testing.T) {
	handler := mustHandler(t, &stubBackend{})
	assertProblem(t, serve(handler, localRequestFor(http.MethodGet, peersPath+"?detail=1", "")), http.StatusBadRequest, "invalid_query")
	response := serve(handler, localRequestFor(http.MethodPost, peersPath, ""))
	assertStatus(t, response, http.StatusMethodNotAllowed)
	if got := response.Header().Get("Allow"); got != "GET, HEAD" {
		t.Fatalf("Allow = %q", got)
	}
}
