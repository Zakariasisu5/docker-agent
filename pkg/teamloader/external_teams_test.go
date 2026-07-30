package teamloader

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/docker/docker-agent/pkg/config"
)

func TestWithExternalTeamsComposesLocalManifest(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "dummy")
	dir := t.TempDir()
	primary := `models:
  model: {provider: openai, model: gpt-4o}
agents:
  root:
    model: model
    description: Primary lead
    instruction: Coordinate teams.
  helper:
    model: model
    description: Existing helper
    instruction: Help.
`
	secondary := `models:
  model: {provider: openai, model: gpt-4o}
agents:
  root:
    model: model
    description: Secondary lead
    instruction: Coordinate specialists.
    sub_agents: [researcher]
  researcher:
    model: model
    description: Researcher
    instruction: Research.
`
	primaryPath := filepath.Join(dir, "primary.yaml")
	require.NoError(t, os.WriteFile(primaryPath, []byte(primary), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "secondary.yaml"), []byte(secondary), 0o644))

	tm, err := Load(t.Context(), config.NewFileSource(primaryPath), &config.RuntimeConfig{}, append(withTestProviderRegistry(), WithExternalTeams([]string{"Research team=./secondary.yaml"}))...)
	require.NoError(t, err)

	root, err := tm.Agent("root")
	require.NoError(t, err)
	require.Len(t, root.SubAgents(), 1)
	assert.Equal(t, "research-team", root.SubAgents()[0].Name())
	assert.Equal(t, "root", root.SubAgents()[0].DisplayName())
	assert.ElementsMatch(t, []string{"root", "helper", "research-team"}, tm.AgentNames())
	_, err = tm.Agent("researcher")
	require.Error(t, err, "private members must not become public switch targets")

	infos := tm.AgentsInfo(t.Context())
	byName := map[string]struct {
		team     string
		lead     bool
		internal bool
	}{}
	for _, info := range infos {
		byName[info.Name] = struct {
			team     string
			lead     bool
			internal bool
		}{info.TeamName, info.TeamLead, info.Internal}
	}
	assert.Equal(t, "Primary team", byName["root"].team)
	assert.Equal(t, "Research team", byName["research-team"].team)
	assert.True(t, byName["research-team"].lead)
	assert.Equal(t, "Research team", byName["researcher"].team)
	assert.True(t, byName["researcher"].internal)
}

func TestWithExternalTeamsRejectsInvalidAndDuplicateRefs(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "dummy")
	dir := t.TempDir()
	primary := `models:
  model: {provider: openai, model: gpt-4o}
agents:
  root:
    model: model
    description: Primary lead
    instruction: Coordinate.
    sub_agents: [specialists:./secondary.yaml]
`
	secondary := `models:
  model: {provider: openai, model: gpt-4o}
agents:
  root: {model: model, description: Secondary, instruction: Help.}
`
	primaryPath := filepath.Join(dir, "primary.yaml")
	require.NoError(t, os.WriteFile(primaryPath, []byte(primary), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "secondary.yaml"), []byte(secondary), 0o644))

	for _, tc := range []struct {
		name string
		ref  string
		want string
	}{
		{"url", "Research team=https://example.com/team.yaml", "local"},
		{"oci", "Research team=myorg/team:latest", "local"},
		{"duplicate name", "Specialists=specialists:./other.yaml", "duplicate agent ID"},
		{"duplicate ref", "Specialists=specialists:./secondary.yaml", "already configured"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(t.Context(), config.NewFileSource(primaryPath), &config.RuntimeConfig{}, append(withTestProviderRegistry(), WithExternalTeams([]string{tc.ref}))...)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}
