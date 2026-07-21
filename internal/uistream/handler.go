package uistream

import (
	"context"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

const batchInterval = 100 * time.Millisecond

// NewHandler serves the local, same-origin live event WebSocket.
func NewHandler(lifecycle context.Context, hub *Hub, allowedPort uint16) http.Handler {
	return &handler{lifecycle: lifecycle, hub: hub, allowedPort: allowedPort}
}

type handler struct {
	lifecycle   context.Context
	hub         *Hub
	allowedPort uint16
}

func (handler *handler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Cache-Control", "no-store")
	response.Header().Set("X-Content-Type-Options", "nosniff")
	if request.Method != http.MethodGet {
		response.Header().Set("Allow", "GET")
		http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if handler.lifecycle == nil || handler.hub == nil || handler.allowedPort == 0 {
		http.Error(response, "event stream unavailable", http.StatusServiceUnavailable)
		return
	}
	if !localRequest(request, handler.allowedPort) {
		http.Error(response, "event stream requires a matching local origin", http.StatusForbidden)
		return
	}

	connection, err := websocket.Accept(response, request, &websocket.AcceptOptions{
		CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		return
	}
	defer connection.CloseNow()

	ctx := connection.CloseRead(handler.lifecycle)
	subscription := handler.hub.Subscribe()
	defer subscription.Close()

	ticker := time.NewTicker(batchInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			_ = connection.Close(websocket.StatusNormalClosure, "viewer disconnected")
			return
		case sentAt := <-ticker.C:
			notes := drain(subscription.Events(), handler.hub.capacity)
			dropped := subscription.TakeDrops()
			packetTotal, noteTotal := handler.hub.totals()
			if err := wsjson.Write(ctx, connection, Batch{
				Type: "notes", SentAt: sentAt.UTC(), Dropped: dropped,
				PacketTotal: packetTotal, NoteTotal: noteTotal, Notes: notes,
			}); err != nil {
				return
			}
			handler.hub.observer.Events("sent", len(notes))
		}
	}
}

func drain(events <-chan Note, maximum int) []Note {
	notes := make([]Note, 0, maximum)
	for len(notes) < maximum {
		select {
		case event := <-events:
			notes = append(notes, event)
		default:
			return notes
		}
	}
	return notes
}

func localRequest(request *http.Request, allowedPort uint16) bool {
	host, portText, err := net.SplitHostPort(request.RemoteAddr)
	if err != nil || portText == "" {
		return false
	}
	remotePort, err := strconv.ParseUint(portText, 10, 16)
	if err != nil || remotePort == 0 {
		return false
	}
	remoteIP := net.ParseIP(host)
	scheme := "http"
	if request.TLS != nil {
		scheme = "https"
	}
	if remoteIP == nil || !remoteIP.IsLoopback() || !loopbackAuthority(request.Host, scheme, allowedPort) {
		return false
	}
	origins, present := request.Header["Origin"]
	if !present {
		return true
	}
	if len(origins) != 1 || origins[0] == "" {
		return false
	}
	originText := origins[0]
	origin, err := url.Parse(originText)
	return err == nil && origin.User == nil && origin.RawQuery == "" && origin.Fragment == "" && origin.Path == "" &&
		origin.Scheme == scheme && loopbackAuthority(origin.Host, scheme, allowedPort) && originText == scheme+"://"+request.Host
}

func loopbackAuthority(authority, scheme string, allowedPort uint16) bool {
	if authority == "" || strings.ContainsAny(authority, " \t\r\n/@") {
		return false
	}
	host := authority
	portText := ""
	if parsedHost, parsedPort, err := net.SplitHostPort(authority); err == nil {
		if parsedPort == "" {
			return false
		}
		host, portText = parsedHost, parsedPort
	} else if strings.HasPrefix(authority, "[") && strings.HasSuffix(authority, "]") {
		host = strings.TrimSuffix(strings.TrimPrefix(authority, "["), "]")
	} else if strings.Contains(authority, ":") {
		return false
	}
	if !loopbackHost(host) {
		return false
	}
	if portText == "" {
		return scheme == "http" && allowedPort == 80 || scheme == "https" && allowedPort == 443
	}
	port, err := strconv.ParseUint(portText, 10, 16)
	return err == nil && port != 0 && uint16(port) == allowedPort
}

func loopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}
