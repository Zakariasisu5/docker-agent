package root

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunTeamFlagIsRepeatable(t *testing.T) {
	cmd := newRunCmd()
	require.NoError(t, cmd.ParseFlags([]string{
		"--team", "Research team=./secondary.yaml",
		"--team", "QA team=./qa.hcl",
	}))

	flag := cmd.Flags().Lookup("team")
	require.NotNil(t, flag)
	assert.Equal(t, "[Research team=./secondary.yaml,QA team=./qa.hcl]", flag.Value.String())
}

func TestLoadTeamRequestCarriesExternalTeams(t *testing.T) {
	flags := &runExecFlags{teams: []string{"Research team=./secondary.yaml", "QA team=./qa.hcl"}}
	req := flags.loadTeamRequest(nil)
	assert.Equal(t, flags.teams, req.ExternalTeams)
}

func TestRunSecondPositionalRemainsMessage(t *testing.T) {
	flags := &runExecFlags{}
	args := []string{"./primary.yaml", "./secondary.yaml"}
	assert.Equal(t, "./primary.yaml", flags.resolveRunAgentFileName(args))
	assert.Equal(t, "./secondary.yaml", args[1], "the second positional remains a message; use --team to compose teams")
}
