// Package permit is the connector-facing name for the gate's permits.
// Connectors can accept and redeem a Permit but cannot create one; only the
// gate can import the package that mints them.
package permit

import "water/internal/gate/internal/mint"

type (
	Permit = mint.Permit
	Call   = mint.Call
)

var (
	ErrNoPermit = mint.ErrNoPermit
	ErrRedeemed = mint.ErrRedeemed
)
