package team

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/docker/docker-agent/pkg/agent"
	"github.com/docker/docker-agent/pkg/config/latest"
)

func newAgent(name string) *agent.Agent {
	return agent.New(name, "")
}

// RuntimeSafety returns the config-wide default set via WithRuntimeSafety,
// empty when unset.
func TestRuntimeSafety(t *testing.T) {
	t.Parallel()
	assert.Equal(t, latest.SafetyMode(""), New().RuntimeSafety())
	assert.Equal(t, latest.SafetyModeBalanced, New(WithRuntimeSafety(latest.SafetyModeBalanced)).RuntimeSafety())
}

func TestDefaultAgent(t *testing.T) {
	t.Parallel()
	t.Run("empty team returns error", func(t *testing.T) {
		_, err := New().DefaultAgent()
		require.Error(t, err)
	})

	t.Run("returns the agent named root when present", func(t *testing.T) {
		team := New(WithAgents(newAgent("first"), newAgent("root"), newAgent("other")))

		got, err := team.DefaultAgent()
		require.NoError(t, err)
		assert.Equal(t, "root", got.Name())
	})

	t.Run("falls back to the first agent when there is no root", func(t *testing.T) {
		team := New(WithAgents(newAgent("alice"), newAgent("bob")))

		got, err := team.DefaultAgent()
		require.NoError(t, err)
		assert.Equal(t, "alice", got.Name())
	})
}

func TestAgentOrDefault(t *testing.T) {
	t.Parallel()
	t.Run("empty name resolves to the default agent", func(t *testing.T) {
		team := New(WithAgents(newAgent("alice"), newAgent("root")))

		got, err := team.AgentOrDefault("")
		require.NoError(t, err)
		assert.Equal(t, "root", got.Name())
	})

	t.Run("empty name without root falls back to the first agent", func(t *testing.T) {
		team := New(WithAgents(newAgent("alice"), newAgent("bob")))

		got, err := team.AgentOrDefault("")
		require.NoError(t, err)
		assert.Equal(t, "alice", got.Name())
	})

	t.Run("explicit name is honored even when a root exists", func(t *testing.T) {
		team := New(WithAgents(newAgent("root"), newAgent("alice")))

		got, err := team.AgentOrDefault("alice")
		require.NoError(t, err)
		assert.Equal(t, "alice", got.Name())
	})

	t.Run("unknown name returns an error listing the available agents", func(t *testing.T) {
		team := New(WithAgents(newAgent("alice"), newAgent("bob")))

		_, err := team.AgentOrDefault("missing")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "missing")
		assert.Contains(t, err.Error(), "alice")
		assert.Contains(t, err.Error(), "bob")
	})

	t.Run("empty team returns an error for both empty and explicit names", func(t *testing.T) {
		team := New()

		_, err := team.AgentOrDefault("")
		require.Error(t, err)

		_, err = team.AgentOrDefault("anything")
		require.Error(t, err)
	})
}

// TestAgentsInfoIncludesPrivateImportedMembers verifies the presentation roster
// includes scoped members without adding them to the public routing registry.
func TestAgentsInfoIncludesPrivateImportedMembers(t *testing.T) {
	t.Parallel()
	private := agent.New("researcher", "", agent.WithTeamInfo("specialists", false, true))
	lead := agent.New("specialists", "", agent.WithTeamInfo("specialists", true, false), agent.WithSubAgents(private))
	root := agent.New("root", "", agent.WithTeamInfo("primary", false, false), agent.WithSubAgents(lead))
	tm := New(WithAgents(root, lead))

	assert.Equal(t, []string{"root", "specialists"}, tm.AgentNames())
	_, err := tm.Agent("researcher")
	require.Error(t, err)

	infos := tm.AgentsInfo(t.Context())
	require.Len(t, infos, 3)
	assert.Equal(t, "root", infos[0].Name)
	assert.Equal(t, "specialists", infos[1].Name)
	assert.True(t, infos[1].TeamLead)
	assert.Equal(t, "researcher", infos[2].Name)
	assert.True(t, infos[2].Internal)
	assert.Equal(t, "specialists", infos[2].TeamName)
}

// TestAgentConfig verifies the raw per-agent config retained via
// WithAgentConfigs is returned by name, and that callers can distinguish a
// team built without configs (remote runtime) from one built with them.
func TestAgentConfig(t *testing.T) {
	t.Parallel()

	configs := map[string]latest.AgentConfig{
		"root": {Name: "root", Model: "openai/gpt-5", MaxIterations: 42},
	}

	t.Run("returns retained config by name", func(t *testing.T) {
		t.Parallel()
		tm := New(WithAgents(newAgent("root")), WithAgentConfigs(configs))

		cfg, ok := tm.AgentConfig("root")
		require.True(t, ok)
		assert.Equal(t, "openai/gpt-5", cfg.Model)
		assert.Equal(t, 42, cfg.MaxIterations)
	})

	t.Run("unknown agent returns false", func(t *testing.T) {
		t.Parallel()
		tm := New(WithAgents(newAgent("root")), WithAgentConfigs(configs))

		_, ok := tm.AgentConfig("missing")
		assert.False(t, ok)
	})

	t.Run("team built without configs returns false", func(t *testing.T) {
		t.Parallel()
		tm := New(WithAgents(newAgent("root")))

		_, ok := tm.AgentConfig("root")
		assert.False(t, ok)
	})
}
