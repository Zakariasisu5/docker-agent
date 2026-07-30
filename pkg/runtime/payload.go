package runtime

import (
	"github.com/docker/docker-agent/pkg/config"
	"github.com/docker/docker-agent/pkg/permissions"
	"github.com/docker/docker-agent/pkg/session"
)

// LoadTeamRequest is the typed input to a backend's team load. It carries
// everything the team loader needs: the resolved agent source plus the
// runtime config and the per-flag knobs the user supplied.
//
// LocalBackend consumes this struct directly. The same struct is the
// payload a future RemoteBackend marshals over the wire so the server
// runs teamloader.LoadWithConfig with identical inputs.
//
// JSON tags reflect the wire contract being designed. Fields that don't
// yet have a wire-friendly representation (the Source interface, the
// RuntimeConfig with its embedded sync.Mutex and environment.Provider)
// are tagged json:"-" until they get one. Round-trip tests live in
// payload_test.go.
type LoadTeamRequest struct {
	Source         config.Source         `json:"-"`
	ModelOverrides []string              `json:"model_overrides,omitempty"`
	PromptFiles    []string              `json:"prompt_files,omitempty"`
	ExternalTeams  []string              `json:"external_teams,omitempty"`
	RunConfig      *config.RuntimeConfig `json:"-"`
}

// CreateSessionRequest is the typed input to a backend's session creation.
// It carries the agent selection and every CLI-flag-driven session knob.
//
// The same forward-compatibility argument as LoadTeamRequest applies: this
// is the payload a remote backend will eventually send over the wire.
//
// Boolean fields intentionally do NOT use `omitempty`. With `omitempty`,
// an explicit `false` is wire-indistinguishable from "unset", so a future
// server-side default flip from `false` to `true` would silently change
// the semantics for clients that omitted the field on purpose. Always
// serialising the boolean keeps the sender's choice authoritative.
//
// GlobalPermissions is currently json:"-" (the permissions.Checker is an
// opaque struct without a wire form yet); it'll get a serializable
// representation in a follow-up.
type CreateSessionRequest struct {
	AgentName string `json:"agent_name,omitempty"`
	// ToolsApproved is the legacy --yolo signal. New callers should
	// prefer SafetyPolicy; option setters keep both in sync.
	ToolsApproved bool `json:"tools_approved"`
	// SafetyPolicy is the user-owned per-session safety preference (CLI
	// flags, alias options, user settings — never author YAML); empty
	// falls back to the ToolsApproved-derived default or, for fresh local
	// sessions, to the author-declared config defaults. See
	// [session.SafetyPolicy].
	SafetyPolicy session.SafetyPolicy `json:"safety_policy,omitempty"`
	// SafetyExplicit marks a SafetyPolicy chosen explicitly on the command
	// line (--safety / --yolo) rather than resolved from alias or
	// user-settings defaults. Resuming a stored session only honours
	// explicit values: defaults must never replace the persisted mode.
	// Not serialized: the remote protocol has no resume path yet (--remote
	// is mutually exclusive with --session).
	SafetyExplicit    bool                 `json:"-"`
	HideToolResults   bool                 `json:"hide_tool_results"`
	SessionDB         string               `json:"session_db,omitempty"`
	ResumeSessionID   string               `json:"resume_session_id,omitempty"`
	SnapshotsEnabled  bool                 `json:"snapshots_enabled"`
	GlobalPermissions *permissions.Checker `json:"-"`
	WorkingDir        string               `json:"working_dir,omitempty"`
}
