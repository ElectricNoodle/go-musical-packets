package uistream

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

func TestHandlerStreamsAggregatedAcceptedNotes(t *testing.T) {
	lifecycle, cancel := context.WithCancel(context.Background())
	defer cancel()
	hub := New(4, nil)
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Skipf("loopback listener unavailable: %v", err)
	}
	_, portText, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.ParseUint(portText, 10, 16)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: NewHandler(lifecycle, hub, uint16(port))}
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.Serve(listener) }()
	defer func() {
		_ = server.Shutdown(context.Background())
		<-serverDone
	}()

	ctx, stop := context.WithTimeout(context.Background(), 2*time.Second)
	defer stop()
	connection, _, err := websocket.Dial(ctx, "ws://"+listener.Addr().String(), nil)
	if err != nil {
		t.Fatalf("Dial() error = %v", err)
	}
	defer connection.CloseNow()

	hub.RecordPacket()
	hub.RecordPacket()
	hub.Publish(testNote("accepted", 67))
	for {
		var batch Batch
		if err := wsjson.Read(ctx, connection, &batch); err != nil {
			t.Fatalf("Read() error = %v", err)
		}
		if len(batch.Notes) == 0 {
			continue
		}
		if batch.Type != "notes" || batch.PacketTotal != 2 || batch.NoteTotal != 1 {
			t.Fatalf("batch = %#v", batch)
		}
		if batch.Notes[0].ID != "accepted" || batch.Notes[0].AcceptedAt.IsZero() {
			t.Fatalf("note = %#v", batch.Notes[0])
		}
		break
	}
}

func TestHandlerRejectsForeignOriginBeforeUpgrade(t *testing.T) {
	handler := NewHandler(context.Background(), New(1, nil), 8080)
	request := httptest.NewRequest(http.MethodGet, "http://localhost:8080/api/v1/events", nil)
	request.RemoteAddr = "127.0.0.1:41000"
	request.Host = "localhost:8080"
	request.Header.Set("Origin", "http://evil.example")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusForbidden)
	}
}
