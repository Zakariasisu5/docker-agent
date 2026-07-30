package team

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/docker/docker-agent/pkg/agent"
	"github.com/docker/docker-agent/pkg/config/latest"
	"github.com/docker/docker-agent/pkg/config/types"
	"github.com/docker/docker-agent/pkg/permissions"
)

type Team struct {
	agents      []*agent.Agent
	permissions *permissions.Checker
	// runtimeSafety is the config-wide safety-mode default declared under
	// runtime.safety, retained so session constructors can apply it when
	// neither the user nor the selected agent chose a mode. Empty when the
	// config declares none (or the team was built without a config).
	runtimeSafety latest.SafetyMode
	// agentConfigs holds the raw resolved config for each agent, keyed by
	// name. It is retained only when the team is built from a config file
	// (WithAgentConfigs) so surfaces like the agent inspector can show
	// declared toolset allow-lists, limits and flags. Teams built without it
	// (e.g. the remote runtime) leave it nil and AgentConfig returns false.
	agentConfigs map[string]latest.AgentConfig
}

type Opt func(*Team)

func WithAgents(agents ...*agent.Agent) Opt {
	return func(t *Team) {
		t.agents = agents
	}
}

func WithPermissions(checker *permissions.Checker) Opt {
	return func(t *Team) {
		t.permissions = checker
	}
}

// WithAgentConfigs retains the per-agent resolved configs (keyed by agent
// name) on the team. They are read-only reference data used by inspection
// surfaces; the runtime continues to operate on the resolved *agent.Agent.
func WithAgentConfigs(configs map[string]latest.AgentConfig) Opt {
	return func(t *Team) {
		t.agentConfigs = configs
	}
}

// WithRuntimeSafety records the config-wide runtime.safety default the
// team was loaded with.
func WithRuntimeSafety(mode latest.SafetyMode) Opt {
	return func(t *Team) {
		t.runtimeSafety = mode
	}
}

func New(opts ...Opt) *Team {
	t := &Team{}
	for _, opt := range opts {
		opt(t)
	}
	return t
}

func (t *Team) AgentNames() []string {
	var names []string
	for i := range t.agents {
		names = append(names, t.agents[i].Name())
	}
	return names
}

// AllAgents returns the public agents plus scoped descendants, deduplicated by
// pointer. It is for runtime lifecycle/presentation only; Agent and AgentNames
// remain the authoritative public routing registry.
func (t *Team) AllAgents() []*agent.Agent {
	seen := make(map[*agent.Agent]struct{})
	expanded := make(map[*agent.Agent]struct{})
	all := make([]*agent.Agent, 0, len(t.agents))
	for _, a := range t.agents {
		if a == nil {
			continue
		}
		if _, ok := seen[a]; !ok {
			seen[a] = struct{}{}
			all = append(all, a)
		}
	}
	var walk func(*agent.Agent)
	walk = func(a *agent.Agent) {
		if a == nil {
			return
		}
		if _, ok := expanded[a]; ok {
			return
		}
		expanded[a] = struct{}{}
		children := append([]*agent.Agent{}, a.SubAgents()...)
		children = append(children, a.Handoffs()...)
		if forced := a.ForceHandoff(); forced != nil {
			children = append(children, forced)
		}
		for _, child := range children {
			if child == nil {
				continue
			}
			if _, ok := seen[child]; !ok {
				seen[child] = struct{}{}
				all = append(all, child)
			}
			walk(child)
		}
	}
	for _, a := range t.agents {
		walk(a)
	}
	return all
}

// AgentInfo contains information about an agent
type AgentInfo struct {
	Agent       *agent.Agent
	Name        string
	DisplayName string
	Description string
	Provider    string
	Model       string
	Commands    types.Commands
	TeamName    string
	TeamLead    bool
	Internal    bool
}

// AgentsInfo returns the public roster plus private imported-team members for
// presentation. Private members remain absent from AgentNames and Agent.
func (t *Team) AgentsInfo(ctx context.Context) []AgentInfo {
	public := make(map[*agent.Agent]struct{}, len(t.agents))
	for _, a := range t.agents {
		public[a] = struct{}{}
	}
	seen := make(map[*agent.Agent]struct{})
	var infos []AgentInfo
	for _, a := range t.AllAgents() {
		if _, ok := seen[a]; ok {
			continue
		}
		seen[a] = struct{}{}
		_, isPublic := public[a]
		displayName := ""
		if a.DisplayName() != a.Name() {
			displayName = a.DisplayName()
		}
		info := AgentInfo{
			Agent:       a,
			Name:        a.Name(),
			DisplayName: displayName,
			Description: a.Description(),
			Commands:    a.Commands(),
			TeamName:    a.TeamName(),
			TeamLead:    a.TeamLead(),
			Internal:    a.Internal() || !isPublic,
		}
		if model := a.Model(ctx); model != nil {
			id := model.ID()
			info.Provider = id.Provider
			info.Model = id.Model
		} else if harnessType := a.HarnessType(); harnessType != "" {
			info.Model = harnessType
		}
		infos = append(infos, info)
	}
	return infos
}

func (t *Team) DefaultAgent() (*agent.Agent, error) {
	if t.Size() == 0 {
		return nil, errors.New("no agents loaded; ensure your agent configuration defines at least one agent")
	}

	// Before v4, the default agent was the one named "root". If it exists, return it.
	for _, a := range t.agents {
		if a.Name() == "root" {
			return a, nil
		}
	}

	// Otherwise, return the first agent.
	return t.agents[0], nil
}

func (t *Team) Agent(name string) (*agent.Agent, error) {
	if t.Size() == 0 {
		return nil, errors.New("no agents loaded; ensure your agent configuration defines at least one agent")
	}

	for _, a := range t.agents {
		if a.Name() == name {
			return a, nil
		}
	}

	return nil, fmt.Errorf("agent not found: %s (available agents: %s)", name, strings.Join(t.AgentNames(), ", "))
}

// AgentOrDefault returns the agent identified by name, or the team's
// [DefaultAgent] when name is empty. It is a convenience for the many
// call sites that accept an optional agent selector (CLI flag, HTTP
// route, ...) and want "empty means whatever the team considers
// default" semantics without sprinkling the same `if name == ""` check
// everywhere.
func (t *Team) AgentOrDefault(name string) (*agent.Agent, error) {
	if name == "" {
		return t.DefaultAgent()
	}
	return t.Agent(name)
}

func (t *Team) Size() int {
	return len(t.agents)
}

func (t *Team) StopToolSets(ctx context.Context) error {
	for _, agent := range t.AllAgents() {
		if err := agent.StopToolSets(ctx); err != nil {
			return fmt.Errorf("failed to stop tool sets: %w", err)
		}
	}

	return nil
}

// AgentConfig returns the raw resolved config for the named agent and true
// when it was retained at construction (WithAgentConfigs). Teams built
// without configs (e.g. the remote runtime) return the zero value and false,
// letting callers gracefully omit config-derived detail.
func (t *Team) AgentConfig(name string) (latest.AgentConfig, bool) {
	cfg, ok := t.agentConfigs[name]
	return cfg, ok
}

// Permissions returns the permission checker for this team.
// Returns nil if no permissions are configured.
func (t *Team) Permissions() *permissions.Checker {
	return t.permissions
}

// RuntimeSafety returns the config-wide safety-mode default declared under
// runtime.safety, or empty when the config declares none. It is a default
// only: user-owned choices and per-agent safety take precedence.
func (t *Team) RuntimeSafety() latest.SafetyMode {
	return t.runtimeSafety
}

// SetPermissions replaces the team's permission checker.
// This is used to merge additional permission sources (e.g. user-level global
// permissions) into the team's checker after construction.
func (t *Team) SetPermissions(checker *permissions.Checker) {
	t.permissions = checker
}
