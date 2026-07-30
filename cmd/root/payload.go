package root

import (
	"github.com/docker/docker-agent/pkg/config"
	"github.com/docker/docker-agent/pkg/runtime"
)

// loadTeamRequest builds a runtime.LoadTeamRequest from the current flags.
func (f *runExecFlags) loadTeamRequest(agentSource config.Source) runtime.LoadTeamRequest {
	return runtime.LoadTeamRequest{
		Source:         agentSource,
		ModelOverrides: f.modelOverrides,
		PromptFiles:    f.promptFiles,
		ExternalTeams:  f.teams,
		RunConfig:      &f.runConfig,
	}
}

// createSessionRequest builds a runtime.CreateSessionRequest from the
// current flags and the supplied working directory. SafetyPolicy carries
// the user-owned mode only (explicit CLI flags, then alias/settings
// defaults); author-declared YAML defaults are resolved later, when a
// fresh session is actually built, so they can never masquerade as a
// user choice.
func (f *runExecFlags) createSessionRequest(workingDir string) runtime.CreateSessionRequest {
	return runtime.CreateSessionRequest{
		AgentName:         f.agentName,
		ToolsApproved:     f.autoApprove,
		SafetyPolicy:      f.userSafetyPolicy(),
		SafetyExplicit:    f.explicitCLISafety() != "",
		HideToolResults:   f.hideToolResults,
		SessionDB:         sessionDBPath(f.sessionDB),
		ResumeSessionID:   f.sessionID,
		SnapshotsEnabled:  f.snapshotsEnabled,
		GlobalPermissions: f.globalPermissions,
		WorkingDir:        workingDir,
	}
}
