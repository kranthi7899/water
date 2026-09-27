package needsyou

// itemOrigin is Item.Origin's pure computation (docs/slices/UI.md Phase 3a).
// Exactly one of its three cases ever wins, checked in this priority order:
//
//  1. sourceCardTitle set: the item traces to a decision card (an approval
//     staged from one, via Envelope.SourceCardID) -- "From <title>". This is
//     the most specific case, so it wins over the other two whenever it's
//     available at all.
//  2. requestedByName set: the item is someone else's request (a
//     person-request approval's RequestedBy, resolved through the roster)
//     -- "Requested by <name>".
//  3. untrusted: neither of the above applies, but the item was built from
//     content tagged untrusted (a decision card computed from an inbound
//     email or similar external source) -- a generic "Built from an
//     outside email" rather than naming the specific message, since
//     nothing more specific is known at this point.
//
// "" when none apply. Origin is optional, unlike a decision card's Gaps
// (never hidden) -- Today and the Approvals queue simply show no origin
// line for an item with none.
func itemOrigin(sourceCardTitle, requestedByName string, untrusted bool) string {
	switch {
	case sourceCardTitle != "":
		return "From " + sourceCardTitle
	case requestedByName != "":
		return "Requested by " + requestedByName
	case untrusted:
		return "Built from an outside email"
	default:
		return ""
	}
}
