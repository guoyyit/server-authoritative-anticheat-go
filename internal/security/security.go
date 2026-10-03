// Package security authenticates client operations with an in-process HMAC.
// The shared key never leaves the process; the MAC lets the server detect
// tampered or forged operations without any external service.
package security

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/guoyyit/server-authoritative-anticheat-go/internal/model"
)

// payload is the canonical, MAC-covered view of an operation.
type payload struct {
	MatchID    string       `json:"match_id"`
	PlayerID   string       `json:"player_id"`
	Seq        uint64       `json:"seq"`
	ClientTime int64        `json:"client_time"`
	Kind       model.OpKind `json:"kind"`
	Position   model.Vec3   `json:"position"`
	TargetID   string       `json:"target_id"`
	WeaponID   string       `json:"weapon_id"`
	Damage     float64      `json:"damage"`
	Nonce      uint64       `json:"nonce"`
}

func canonical(op *model.Operation) []byte {
	data, _ := json.Marshal(payload{
		MatchID:    op.MatchID,
		PlayerID:   op.PlayerID,
		Seq:        op.Seq,
		ClientTime: op.ClientTime,
		Kind:       op.Kind,
		Position:   op.Position,
		TargetID:   op.TargetID,
		WeaponID:   op.WeaponID,
		Damage:     op.Damage,
		Nonce:      op.Nonce,
	})
	return data
}

// Sign computes the MAC for an operation.
func Sign(op *model.Operation, key string) string {
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write(canonical(op))
	return hex.EncodeToString(mac.Sum(nil))
}

// Verify reports whether the operation's MAC matches its contents.
func Verify(op *model.Operation, key string) bool {
	if op == nil || op.MAC == "" {
		return false
	}
	expected := Sign(op, key)
	return hmac.Equal([]byte(expected), []byte(op.MAC))
}
