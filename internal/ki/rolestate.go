package ki

// Health of a provider as a role reports it.
const (
	HealthOK      = "ok"
	HealthSkipped = "skipped"
	HealthDemoted = "demoted"
)

// RoleProvider is one provider of a role as the next pick sees it.
type RoleProvider struct {
	Agent string `json:"agent"`
	// Model is the provider's own, else the machine's model for the role's
	// class and the agent, "" when the agent chooses.
	Model string `json:"model"`
	// Available is whether the agent is installed here.
	Available bool   `json:"available"`
	Health    string `json:"health"`
	Reason    string `json:"reason,omitempty"`
	// Ask is whether `ki ask` can run it read-only.
	Ask bool `json:"ask"`
}

// RoleState is a role's strategy and its providers in the order written.
type RoleState struct {
	Strategy  string         `json:"strategy"`
	Providers []RoleProvider `json:"providers"`
}

// RoleStates reads every role the machine defines the way a pick would,
// without reserving anything: in carries the ledger, the time and what is
// installed; its Role and R are set per role.
func RoleStates(m *Machine, in PickInput) map[string]RoleState {
	out := map[string]RoleState{}
	for _, class := range roleNames {
		r, ok := m.RoleFor(class)
		if !ok {
			continue
		}
		in.Role, in.R = class, r
		health := Health(in, r.Providers)
		st := RoleState{Strategy: r.Strategy, Providers: make([]RoleProvider, 0, len(r.Providers))}
		if st.Strategy == "" {
			st.Strategy = StrategyOrdered
		}
		for i, p := range r.Providers {
			rp := RoleProvider{
				Agent: p.Agent, Model: p.Model, Health: HealthOK, Ask: AskSupported(p.Agent),
				Available: in.Installed == nil || in.Installed(p.Agent),
			}
			if rp.Model == "" {
				rp.Model = m.ModelFor(modelClass(class), p.Agent)
			}
			switch h := health[i]; {
			case h.Skip != "":
				rp.Health, rp.Reason = HealthSkipped, h.Skip
			case h.Demoted != "":
				rp.Health, rp.Reason = HealthDemoted, h.Demoted
			}
			st.Providers = append(st.Providers, rp)
		}
		out[class] = st
	}
	return out
}
