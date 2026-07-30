package runtime

import (
	"log/slog"
	"sync/atomic"

	"github.com/docker/docker-agent/pkg/agent"
	"github.com/docker/docker-agent/pkg/session"
	"github.com/docker/docker-agent/pkg/team"
)

// routedAgent is the router's current-agent record: the agent name, plus
// the exact instance when the setter had one. Carrying the instance matters
// for agents that are not in the public team registry — e.g. members of a
// team imported from a local config file, which stay private to their own
// lead — where a name lookup against the team cannot resolve them.
type routedAgent struct {
	name string
	// agent is non-nil when the current agent was set by instance
	// (SetAgent); Current then returns it directly instead of resolving
	// name against the team.
	agent *agent.Agent
}

// agentRouter owns the runtime's notion of "which agent is currently
// driving the conversation". It is a thin wrapper around a team plus an
// atomically-updated current-agent record, but pulling it out of *LocalRuntime
// turns four methods (CurrentAgentName, SetCurrentAgent, CurrentAgent,
// resolveSessionAgent) that all touched the same two raw fields into
// delegations to one type, and lets tests exercise the
// session-pin-vs-current-agent fallback without instantiating a runtime.
//
// All methods are safe for concurrent use.
type agentRouter struct {
	team *team.Team
	// current is the only mutable field; team is set once at construction
	// and read-only after, so an atomic pointer suffices to guard it.
	current atomic.Pointer[routedAgent]
}

// newAgentRouter builds an agentRouter with team t and an initial current
// agent name. Callers are responsible for pre-validating that the initial
// name exists in t (NewLocalRuntime does this).
func newAgentRouter(t *team.Team, initial string) *agentRouter {
	r := &agentRouter{team: t}
	r.current.Store(&routedAgent{name: initial})
	return r
}

// Name returns the name of the currently active agent.
func (r *agentRouter) Name() string {
	if cur := r.current.Load(); cur != nil {
		return cur.name
	}
	return ""
}

// Set replaces the current agent name without validating that it exists
// in the team. Used from agent_delegation.go where the validation has
// already been performed against the team's transfer/handoff lists.
func (r *agentRouter) Set(name string) {
	r.current.Store(&routedAgent{name: name})
}

// SetAgent replaces the current agent with an exact instance (must be
// non-nil). Unlike Set, Current then returns that instance without a team
// lookup, which is required for agents that are private to the team
// registry (e.g. sub-agents of an imported local-file team lead). Callers
// have already validated a against the caller's sub-agent/handoff lists.
func (r *agentRouter) SetAgent(a *agent.Agent) {
	r.current.Store(&routedAgent{name: a.Name(), agent: a})
}

// SetValidated checks that name exists in the team, then sets it as the
// current agent. Returns the team's lookup error unchanged so callers
// (e.g. the TUI's switch-agent flow) can propagate the same message.
func (r *agentRouter) SetValidated(name string) error {
	if _, err := r.team.Agent(name); err != nil {
		return err
	}
	r.Set(name)
	slog.Debug("Switched current agent", "agent", name)
	return nil
}

// Current returns the current agent. The returned agent is non-nil
// because NewLocalRuntime validates the initial name and Set callers
// either use SetValidated, SetAgent (which carries the instance), or have
// already validated against the team.
func (r *agentRouter) Current() *agent.Agent {
	cur := r.current.Load()
	if cur == nil {
		return nil
	}
	if cur.agent != nil {
		return cur.agent
	}
	a, _ := r.team.Agent(cur.name)
	return a
}

// ResolveSession returns the agent for sess: when sess pins an exact agent
// instance (e.g. background tasks targeting an imported team's private
// member), that instance wins; when sess pins an agent name (e.g.
// background agent tasks), that agent is returned directly instead of
// reading the shared current-agent field; otherwise Current is returned.
func (r *agentRouter) ResolveSession(sess *session.Session) *agent.Agent {
	if sess.PinnedAgent != nil {
		return sess.PinnedAgent
	}
	if sess.AgentName != "" {
		if a, err := r.team.Agent(sess.AgentName); err == nil {
			return a
		}
	}
	return r.Current()
}
