// Package mint creates permits. Go's internal-directory rule makes this
// package importable only from internal/gate/..., so no code outside the gate
// can produce a live permit, and a connector can reach its arguments and
// credential only by redeeming one.
package mint

import (
	"bytes"
	"encoding/json"
	"errors"
	"sync/atomic"

	"water/internal/canon"
	"water/internal/vault"
)

var (
	ErrNoPermit = errors.New("connector invoked without a gate permit")
	ErrRedeemed = errors.New("gate permit already redeemed")
)

// Call is what a connector gets from its permit.
type Call struct {
	Function   string
	Args       map[string]any
	Credential vault.Secret
}

type grant struct {
	function string
	args     []byte
	cred     vault.Secret
	redeemed atomic.Bool
}

// Permit is valid for one invocation of one function with exactly the
// arguments the gate authorized. Its zero value authorizes nothing.
type Permit struct{ g *grant }

// New mints a permit. Args are frozen as canonical JSON so later mutation of
// the caller's map cannot change what executes.
func New(function string, args map[string]any, cred vault.Secret) (Permit, error) {
	if args == nil {
		args = map[string]any{}
	}
	b, err := canon.JSON(args)
	if err != nil {
		return Permit{}, err
	}
	return Permit{&grant{function: function, args: b, cred: cred}}, nil
}

// Open redeems the permit once and returns the authorized call.
func (p Permit) Open() (Call, error) {
	if p.g == nil {
		return Call{}, ErrNoPermit
	}
	if !p.g.redeemed.CompareAndSwap(false, true) {
		return Call{}, ErrRedeemed
	}
	dec := json.NewDecoder(bytes.NewReader(p.g.args))
	dec.UseNumber()
	var args map[string]any
	if err := dec.Decode(&args); err != nil {
		return Call{}, err
	}
	return Call{Function: p.g.function, Args: args, Credential: p.g.cred}, nil
}

// Redeemed reports whether the connector opened the permit.
func Redeemed(p Permit) bool { return p.g != nil && p.g.redeemed.Load() }
