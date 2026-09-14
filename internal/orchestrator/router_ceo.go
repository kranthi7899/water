package orchestrator

// CEOFanoutRouter: entry → CEO decomposes → parallel fan-out to delegates →
// fan-in → CEO synthesis → done. Phase is derived from State.Visits so the
// router holds no hidden counters and can resume from a checkpoint.
type CEOFanoutRouter struct {
	CEO       string
	Delegates []string
}

// CEOFanoutName is the config value selecting this router.
const CEOFanoutName = "ceo-fanout"

func (r *CEOFanoutRouter) Name() string  { return CEOFanoutName }
func (r *CEOFanoutRouter) Entry() string { return r.CEO }

func (r *CEOFanoutRouter) Next(s *State) []string {
	ceoVisits := s.Visits(r.CEO)
	switch {
	case ceoVisits == 0:
		return []string{r.CEO} // decompose
	case ceoVisits == 1:
		var pending []string
		for _, d := range r.Delegates {
			if s.Visits(d) == 0 {
				pending = append(pending, d)
			}
		}
		if len(pending) > 0 {
			return pending // fan-out
		}
		return []string{r.CEO} // fan-in + synthesis
	default:
		return nil // done
	}
}

// RouterFactory builds a Router given the orchestrator slug and delegate slugs.
type RouterFactory func(ceo string, delegates []string) Router

var routerFactories = map[string]RouterFactory{
	CEOFanoutName: func(ceo string, delegates []string) Router {
		return &CEOFanoutRouter{CEO: ceo, Delegates: delegates}
	},
}

// RegisterRouter adds a router strategy by name. Later routers (debate,
// sequential, budget-constrained) are new factories, not engine changes.
func RegisterRouter(name string, f RouterFactory) { routerFactories[name] = f }

// NewRouter constructs the named router.
func NewRouter(name, ceo string, delegates []string) (Router, bool) {
	f, ok := routerFactories[name]
	if !ok {
		return nil, false
	}
	return f(ceo, delegates), true
}

// RouterNames lists registered strategies.
func RouterNames() []string {
	out := make([]string, 0, len(routerFactories))
	for n := range routerFactories {
		out = append(out, n)
	}
	return out
}
