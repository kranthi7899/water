package spokenemail

import (
	"errors"
	"testing"
)

// Review of Slice W: one recipient item must be exactly one address. Address
// reads only the last angle-bracket address, so an item like
// "x@therightgmail.com, Kranthi <kranthi@gmail.com>" was checked as
// kranthi@gmail.com while gmail joined the whole item into the To header and
// mailed both, hiding the misheard address from every recipient check.
func TestCheckRecipientRefusesASecondAddressInOneItem(t *testing.T) {
	known := KnownProviders()
	for _, item := range []string{
		"kranthetjob@therightgmail.com, Kranthi <kranthi@gmail.com>",
		"evil@example.com <kranthi@gmail.com>",
		"a@gmail.com, b@gmial.com",
	} {
		for _, confirmed := range []bool{false, true} {
			if _, err := CheckRecipient(item, known, confirmed); !errors.Is(err, ErrInvalid) {
				t.Errorf("CheckRecipient(%q, confirmed=%v) err = %v, want ErrInvalid", item, confirmed, err)
			}
		}
	}
	// One address, with or without a display name, still passes.
	for _, item := range []string{"kranthi@gmail.com", "Dana Lee <dana@fenwick.io>", " kranthi@gmail.com "} {
		if w, err := CheckRecipient(item, known, false); w != "" || err != nil {
			t.Errorf("CheckRecipient(%q) = (%q, %v), want clean", item, w, err)
		}
	}
}

// Review of Slice W: real regional provider domains, and real companies one
// or two edits from a provider, must not be refused as misheard.
func TestNearMissAcceptsRealDomains(t *testing.T) {
	for _, d := range []string{
		"protonmail.ch", "yahoo.ca", "outlook.cz", "outlook.cl", "email.cz",
		"cloud.com", "hotmart.com", "proton.ai",
		// already fine, kept as a guard
		"yahoo.co.uk", "yahoo.co.in", "hotmail.co.uk", "outlook.de", "live.ca", "gmx.de",
		"umass.edu", "fastmail.com", "hey.com", "pm.me", "mac.com", "msn.com",
	} {
		if p, near := NearMiss(d, KnownProviders()); near {
			t.Errorf("NearMiss(%q) = %q: a real domain must not read as misheard", d, p)
		}
	}
	// The typos stay caught.
	for _, d := range []string{"therightgmail.com", "gmail.co", "gmial.com", "yahoo.cm", "outlok.com", "protonmail.con", "icloud.co"} {
		if _, near := NearMiss(d, KnownProviders()); !near {
			t.Errorf("NearMiss(%q) = false, want a near-miss", d)
		}
	}
}
