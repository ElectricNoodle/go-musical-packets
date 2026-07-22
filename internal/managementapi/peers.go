package managementapi

import (
	"net/http"
	"time"
)

const peersPath = "/api/v1/peers"

// PeerQueue is one bounded outgoing event queue.
type PeerQueue struct {
	Depth    int `json:"depth"`
	Capacity int `json:"capacity"`
}

// OutboundPeer is the edge view of its configured host connection.
type OutboundPeer struct {
	Enabled         bool       `json:"enabled"`
	Target          string     `json:"target"`
	RemoteInstance  string     `json:"remote_instance,omitempty"`
	State           string     `json:"state"`
	ProtocolVersion string     `json:"protocol_version,omitempty"`
	MappingVersion  string     `json:"mapping_version,omitempty"`
	Queue           PeerQueue  `json:"queue"`
	SentTotal       uint64     `json:"sent_total"`
	DroppedFull     uint64     `json:"dropped_full"`
	DroppedStale    uint64     `json:"dropped_stale"`
	Reconnects      uint64     `json:"reconnects"`
	SendRate        float64    `json:"send_rate"`
	LastSentAt      *time.Time `json:"last_sent_at,omitempty"`
	ConnectedAt     *time.Time `json:"connected_at,omitempty"`
	LastAttemptAt   *time.Time `json:"last_attempt_at,omitempty"`
	NextRetryAt     *time.Time `json:"next_retry_at,omitempty"`
	RTTMilliseconds int64      `json:"rtt_ms"`
	LastError       string     `json:"last_error,omitempty"`
	ActiveChannels  []uint8    `json:"active_channels"`
}

// ConnectedNode is the host view of one current or recent authenticated edge.
type ConnectedNode struct {
	InstanceID      string     `json:"instance_id"`
	RemoteAddress   string     `json:"remote_address"`
	State           string     `json:"state"`
	Authenticated   bool       `json:"authenticated"`
	ProtocolVersion string     `json:"protocol_version"`
	MappingVersion  string     `json:"mapping_version"`
	ConnectedAt     time.Time  `json:"connected_at"`
	DisconnectedAt  *time.Time `json:"disconnected_at,omitempty"`
	LastSeenAt      time.Time  `json:"last_seen_at"`
	NoteRate        float64    `json:"note_rate"`
	ReceivedTotal   uint64     `json:"received_total"`
	AcceptedTotal   uint64     `json:"accepted_total"`
	RejectedTotal   uint64     `json:"rejected_total"`
	DuplicateTotal  uint64     `json:"duplicate_total"`
	StaleTotal      uint64     `json:"stale_total"`
	ActiveChannels  []uint8    `json:"active_channels"`
}

// PeersDocument is a complete bounded role-aware peer snapshot.
type PeersDocument struct {
	Role     string          `json:"role"`
	Enabled  bool            `json:"enabled"`
	Outbound *OutboundPeer   `json:"outbound,omitempty"`
	Nodes    []ConnectedNode `json:"nodes"`
}

func (handler *handler) servePeers(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		methodNotAllowed(response, request, "GET, HEAD")
		return
	}
	if request.URL.RawQuery != "" {
		writeProblem(response, request, http.StatusBadRequest, "invalid_query", "peer status does not accept a query string", nil)
		return
	}
	document, err := handler.backend.Peers(request.Context())
	if err != nil {
		writeBackendError(response, request, err)
		return
	}
	if document.Nodes == nil {
		document.Nodes = []ConnectedNode{}
	}
	writeJSON(response, request, http.StatusOK, document)
}
