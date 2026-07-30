package sidebar

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"

	"github.com/docker/docker-agent/pkg/runtime"
)

func TestAgentPanelSingleTeamKeepsLegacyRendering(t *testing.T) {
	m := newCompactPanelSidebar(t, 40,
		runtime.AgentDetails{Name: "root", Provider: "openai", Model: "gpt-4o", TeamName: "plain-config"},
		runtime.AgentDetails{Name: "helper", Provider: "openai", Model: "gpt-4o", TeamName: "plain-config"},
	)
	m.sessionState.SetAvailableAgents(m.availableAgents)
	m.sessionState.SetCurrentAgentName("root")

	out := ansi.Strip(m.agentInfo(m.contentWidth(false)))
	assert.Contains(t, out, "Agents")
	assert.NotContains(t, out, "Teams")
	assert.NotContains(t, out, "PLAIN-CONFIG")
	assert.NotContains(t, strings.ToLower(out), "plain-config ·")
}

func TestAgentPanelGroupsTeamsAndKeepsInternalMembersUnclickable(t *testing.T) {
	m := newCompactPanelSidebar(t, 40,
		runtime.AgentDetails{Name: "root", Provider: "openai", Model: "gpt-4o", TeamName: "Primary team"},
		runtime.AgentDetails{Name: "research-team", DisplayName: "root", Provider: "openai", Model: "gpt-4o", TeamName: "Research team", TeamLead: true},
		runtime.AgentDetails{Name: "researcher", Provider: "openai", Model: "gpt-4o", TeamName: "Research team", Internal: true},
		runtime.AgentDetails{Name: "writer", Provider: "openai", Model: "gpt-4o", TeamName: "Research team", Internal: true},
	)
	m.sessionState.SetAvailableAgents(m.availableAgents)
	m.sessionState.SetCurrentAgentName("root")

	out := ansi.Strip(m.agentInfo(m.contentWidth(false)))
	assert.Contains(t, out, "Primary team")
	assert.Contains(t, out, "Research team")
	assert.NotContains(t, out, "Teams")
	assert.Contains(t, out, "root")
	assert.NotContains(t, out, "research-team", "the routing ID stays hidden; the team title provides identity")
	assert.Equal(t, 2, strings.Count(out, "root"), "each team keeps the lead name from its own YAML")
	assert.Contains(t, out, "researcher")
	assert.Contains(t, out, "writer")
	assert.Contains(t, out, "openai/gpt-4o", "internal agents keep the standard agent card")
	assert.NotContains(t, out, "researcher                    ^")

	_ = m.View()
	for _, owner := range m.agentLineOwners {
		assert.NotEqual(t, "researcher", owner)
		assert.NotEqual(t, "writer", owner)
	}
	foundResearchLead := false
	for _, target := range m.agentClickZones {
		if target == "research-team" {
			foundResearchLead = true
		}
		assert.NotEqual(t, "researcher", target)
		assert.NotEqual(t, "writer", target)
	}
	assert.True(t, foundResearchLead, "the second team lead remains clickable under its titled section")
}

func TestCollapsedAgentSummaryOmitsPrivateMembers(t *testing.T) {
	m := newCompactPanelSidebar(t, 80,
		runtime.AgentDetails{Name: "root", TeamName: "Primary team"},
		runtime.AgentDetails{Name: "research-team", DisplayName: "root", TeamName: "Research team", TeamLead: true},
		runtime.AgentDetails{Name: "researcher", TeamName: "Research team", Internal: true},
		runtime.AgentDetails{Name: "writer", TeamName: "Research team", Internal: true},
	)
	m.sessionState.SetAvailableAgents(m.availableAgents)
	m.sessionState.SetCurrentAgentName("root")

	out := ansi.Strip(m.agentSummaryCollapsed())
	assert.Contains(t, out, "Research team: root")
	assert.NotContains(t, out, "researcher")
	assert.NotContains(t, out, "writer")
}

func TestAgentPanelMarksActiveInternalMember(t *testing.T) {
	m := newCompactPanelSidebar(t, 40,
		runtime.AgentDetails{Name: "root", TeamName: "Primary team"},
		runtime.AgentDetails{Name: "research-team", DisplayName: "root", TeamName: "Research team", TeamLead: true},
		runtime.AgentDetails{Name: "researcher", TeamName: "Research team", Internal: true},
	)
	m.sessionState.SetAvailableAgents(m.availableAgents)
	m.sessionState.SetCurrentAgentName("researcher")

	out := ansi.Strip(m.agentInfo(m.contentWidth(false)))
	line := ""
	for candidate := range strings.SplitSeq(out, "\n") {
		if strings.Contains(candidate, "researcher") {
			line = candidate
			break
		}
	}
	assert.Contains(t, line, "▶")
}
