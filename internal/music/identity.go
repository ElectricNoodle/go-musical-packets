package music

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
)

// FlowIdentity is the stable musical identity assigned to a pseudonymous flow.
type FlowIdentity struct {
	Mode Mode
	Root uint8
}

// IdentityForFlowID returns the mode and root used by flow-mode-v1 without
// requiring a packet to be mapped into a note.
func IdentityForFlowID(flowID string) (FlowIdentity, error) {
	decoded, err := hex.DecodeString(flowID)
	if err != nil || len(decoded) != 12 || hex.EncodeToString(decoded) != flowID {
		return FlowIdentity{}, errors.New("flow ID must be 24 lowercase hexadecimal characters")
	}
	digest := sha256.Sum256([]byte(flowID))
	return FlowIdentity{Mode: Mode(digest[0] % uint8(modeCount)), Root: digest[1] % 12}, nil
}
