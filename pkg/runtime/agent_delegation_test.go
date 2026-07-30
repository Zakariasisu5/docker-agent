package runtime

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/docker/docker-agent/pkg/agent"
	"github.com/docker/docker-agent/pkg/chat"
	"github.com/docker/docker-agent/pkg/config/latest"
	"github.com/docker/docker-agent/pkg/model/provider"
	"github.com/docker/docker-agent/pkg/model/provider/base"
	"github.com/docker/docker-agent/pkg/modelsdev"
	"github.com/docker/docker-agent/pkg/permissions"
	"github.com/docker/docker-agent/pkg/runtime/toolexec"
	"github.com/docker/docker-agent/pkg/safety"
	"github.com/docker/docker-agent/pkg/session"
	"github.com/docker/docker-agent/pkg/team"
	"github.com/docker/docker-agent/pkg/tools"
	agenttool "github.com/docker/docker-agent/pkg/tools/builtin/agent"
	"github.com/docker/docker-agent/pkg/tools/builtin/transfertask"
)

func TestBuildTaskSystemMessage(t *testing.T) {
	t.Parallel()

	t.Run("with expected output", func(t *testing.T) {
		msg := buildTaskSystemMessage("do the thing", "a result", nil)
		assert.Contains(t, msg, "<task>\ndo the thing\n</task>")
		assert.Contains(t, msg, "<expected_output>\na result\n</expected_output>")
		assert.NotContains(t, msg, "<attached_files>")
	})

	t.Run("without expected output", func(t *testing.T) {
		msg := buildTaskSystemMessage("do the thing", "", nil)
		assert.Contains(t, msg, "<task>\ndo the thing\n</task>")
		assert.NotContains(t, msg, "expected_output")
		assert.NotContains(t, msg, "<attached_files>")
	})

	t.Run("with attached files", func(t *testing.T) {
		fooPath, _ := filepath.Abs("/abs/foo.go")
		barPath, _ := filepath.Abs("/abs/bar.go")
		msg := buildTaskSystemMessage("do the thing", "", []string{fooPath, barPath})
		assert.Contains(t, msg, "<task>\ndo the thing\n</task>")
		assert.Contains(t, msg, "<attached_files>\n- "+fooPath+"\n- "+barPath+"\n</attached_files>")
	})
}

func TestAgentNames(t *testing.T) {
	t.Parallel()

	agents := []*agent.Agent{
		agent.New("alpha", ""),
		agent.New("beta", ""),
	}
	assert.Equal(t, []string{"alpha", "beta"}, agentNames(agents))
	assert.Empty(t, agentNames(nil))
}

func TestValidateAgentInList(t *testing.T) {
	t.Parallel()

	agents := []*agent.Agent{
		agent.New("sub1", ""),
		agent.New("sub2", ""),
	}

	t.Run("valid agent returns nil", func(t *testing.T) {
		result := validateAgentInList("root", "sub1", "transfer to", "sub-agents", agents)
		assert.Nil(t, result)
	})

	t.Run("invalid agent with non-empty list", func(t *testing.T) {
		result := validateAgentInList("root", "missing", "transfer to", "sub-agents", agents)
		require.NotNil(t, result)
		assert.True(t, result.IsError)
		assert.Contains(t, result.Output, "sub1")
		assert.Contains(t, result.Output, "sub2")
	})

	t.Run("invalid agent with empty list", func(t *testing.T) {
		result := validateAgentInList("root", "missing", "transfer to", "sub-agents", nil)
		require.NotNil(t, result)
		assert.True(t, result.IsError)
		assert.Contains(t, result.Output, "No agents are configured")
	})
}

func TestNewSubSession(t *testing.T) {
	t.Parallel()

	parent := session.New(session.WithUserMessage("hello"))
	childAgent := agent.New("worker", "a worker agent",
		agent.WithMaxIterations(10),
	)

	t.Run("basic config", func(t *testing.T) {
		cfg := SubSessionConfig{
			Task:           "write tests",
			ExpectedOutput: "passing tests",
			AgentName:      "worker",
			Title:          "Test task",
			ToolsApproved:  true,
		}

		s := newSubSession(parent, cfg, childAgent)

		assert.Equal(t, parent.ID, s.ParentID)
		assert.Equal(t, "Test task", s.Title)
		assert.True(t, s.ToolsApproved)
		assert.False(t, s.SendUserMessage)
		assert.Equal(t, 10, s.MaxIterations)
		// AgentName should NOT be set when PinAgent is false
		assert.Empty(t, s.AgentName)
	})

	t.Run("pin agent", func(t *testing.T) {
		cfg := SubSessionConfig{
			Task:      "background work",
			AgentName: "worker",
			Title:     "Background task",
			PinAgent:  true,
		}

		s := newSubSession(parent, cfg, childAgent)

		assert.Equal(t, "worker", s.AgentName)
	})

	t.Run("custom implicit user message", func(t *testing.T) {
		cfg := SubSessionConfig{
			Task:                "bump deps",
			AgentName:           "worker",
			Title:               "Skill task",
			ImplicitUserMessage: "Update all Go dependencies",
		}

		s := newSubSession(parent, cfg, childAgent)

		// The implicit user message should be the custom one, not "Please proceed."
		assert.Equal(t, "Update all Go dependencies", s.GetLastUserMessageContent())
	})

	t.Run("default implicit user message", func(t *testing.T) {
		cfg := SubSessionConfig{
			Task:      "do work",
			AgentName: "worker",
			Title:     "Task",
		}

		s := newSubSession(parent, cfg, childAgent)

		assert.Equal(t, "Please proceed.", s.GetLastUserMessageContent())
	})

	t.Run("custom system message", func(t *testing.T) {
		cfg := SubSessionConfig{
			Task:          "bump deps",
			SystemMessage: "You are a skill sub-agent. Follow these instructions.",
			AgentName:     "worker",
			Title:         "Skill task",
		}

		s := newSubSession(parent, cfg, childAgent)

		// When SystemMessage is set, the default task-based message should not be used.
		// We can verify the user message is still the default.
		assert.Equal(t, "Please proceed.", s.GetLastUserMessageContent())
	})
}

func TestSubSessionConfig_DefaultValues(t *testing.T) {
	t.Parallel()

	// Verify zero-value SubSessionConfig produces a valid session
	parent := session.New(session.WithUserMessage("hello"))
	childAgent := agent.New("worker", "")

	cfg := SubSessionConfig{
		Task:      "minimal task",
		AgentName: "worker",
		Title:     "Minimal",
	}

	s := newSubSession(parent, cfg, childAgent)

	assert.False(t, s.ToolsApproved)
	assert.False(t, s.SendUserMessage)
	assert.Empty(t, s.AgentName)
}

func TestSubSessionConfig_InheritsAgentLimits(t *testing.T) {
	t.Parallel()

	parent := session.New(session.WithUserMessage("hello"))

	t.Run("with custom limits", func(t *testing.T) {
		childAgent := agent.New("worker", "",
			agent.WithMaxIterations(42),
			agent.WithMaxConsecutiveToolCalls(7),
		)

		cfg := SubSessionConfig{
			Task:      "work",
			AgentName: "worker",
			Title:     "test",
		}

		s := newSubSession(parent, cfg, childAgent)
		assert.Equal(t, 42, s.MaxIterations)
		assert.Equal(t, 7, s.MaxConsecutiveToolCalls)
	})

	t.Run("with zero limits (defaults)", func(t *testing.T) {
		childAgent := agent.New("worker", "")

		cfg := SubSessionConfig{
			Task:      "work",
			AgentName: "worker",
			Title:     "test",
		}

		s := newSubSession(parent, cfg, childAgent)
		assert.Equal(t, 0, s.MaxIterations)
		assert.Equal(t, 0, s.MaxConsecutiveToolCalls)
	})
}

func TestSubSessionInheritsAttachedFiles(t *testing.T) {
	t.Parallel()

	fooPath, _ := filepath.Abs("/abs/foo.go")
	barPath, _ := filepath.Abs("/abs/bar.go")

	parent := session.New(session.WithUserMessage("hello"))
	parent.AddAttachedFile(fooPath)
	parent.AddAttachedFile(barPath)
	parent.AddAttachedFile(fooPath) // duplicate, should be ignored

	childAgent := agent.New("worker", "")
	cfg := SubSessionConfig{
		Task:      "refactor",
		AgentName: "worker",
		Title:     "Refactor",
	}

	s := newSubSession(parent, cfg, childAgent)

	// Child session inherits parent's attached files (deduplicated, ordered).
	assert.Equal(t, []string{fooPath, barPath}, s.AttachedFilesSnapshot())

	// The system message lists them so the sub-agent sees them up-front.
	sysMsg := s.GetMessages(childAgent)
	require.NotEmpty(t, sysMsg)
	var joined strings.Builder
	for _, m := range sysMsg {
		joined.WriteString(m.Content)
		joined.WriteString("\n")
	}
	assert.Contains(t, joined.String(), "<attached_files>\n- "+fooPath+"\n- "+barPath+"\n</attached_files>")
}

func TestSubSessionWithoutAttachedFilesOmitsBlock(t *testing.T) {
	t.Parallel()

	parent := session.New(session.WithUserMessage("hello"))
	childAgent := agent.New("worker", "")
	cfg := SubSessionConfig{
		Task:      "refactor",
		AgentName: "worker",
		Title:     "Refactor",
	}

	s := newSubSession(parent, cfg, childAgent)
	assert.Empty(t, s.AttachedFilesSnapshot())

	msgs := s.GetMessages(childAgent)
	require.NotEmpty(t, msgs)
	for _, m := range msgs {
		assert.NotContains(t, m.Content, "<attached_files>")
	}
}

func TestSubSessionInheritsPermissions(t *testing.T) {
	t.Parallel()

	perms := &session.PermissionsConfig{
		Allow: []string{"read_*"},
		Deny:  []string{"write_*"},
		Ask:   []string{"edit_*"},
	}
	parent := session.New(session.WithPermissions(perms))

	childAgent := agent.New("worker", "")
	cfg := SubSessionConfig{
		Task:        "refactor",
		AgentName:   "worker",
		Title:       "Refactor",
		Permissions: parent.ClonePermissions(),
	}

	s := newSubSession(parent, cfg, childAgent)

	require.NotNil(t, s.Permissions)
	assert.Equal(t, perms.Allow, s.Permissions.Allow)
	assert.Equal(t, perms.Deny, s.Permissions.Deny)
	assert.Equal(t, perms.Ask, s.Permissions.Ask)

	// Even with ToolsApproved set (yolo), an inherited Deny must win during dispatch.
	s.ToolsApproved = true

	checker := permissions.NewChecker(&latest.PermissionsConfig{
		Allow: s.Permissions.Allow,
		Ask:   s.Permissions.Ask,
		Deny:  s.Permissions.Deny,
	})
	namedCheckers := []toolexec.NamedChecker{
		{Checker: checker, Source: "session permissions"},
	}

	decision := toolexec.Decide(s.GetSafetyPolicy(), safety.Label{Class: safety.ClassUnknown}, namedCheckers, "write_file", map[string]any{"path": "foo"})
	assert.Equal(t, toolexec.OutcomeDeny, decision.Outcome, "Inherited Deny should override ToolsApproved: true (yolo)")
}

func TestNewSubSession_PermissionsIsolation(t *testing.T) {
	t.Parallel()

	parent := session.New(session.WithUserMessage("hello"))
	childAgent := agent.New("worker", "")

	t.Run("cloned from config", func(t *testing.T) {
		perms := &session.PermissionsConfig{
			Allow: []string{"read_file"},
		}

		cfg := SubSessionConfig{
			Task:        "isolated work",
			AgentName:   "worker",
			Title:       "Task",
			Permissions: perms,
		}

		s := newSubSession(parent, cfg, childAgent)

		require.NotNil(t, s.Permissions)
		assert.Equal(t, []string{"read_file"}, s.Permissions.Allow)

		perms.Allow = append(perms.Allow, "write_file")

		assert.Equal(t, []string{"read_file"}, s.Permissions.Allow)
	})

	t.Run("nil permissions", func(t *testing.T) {
		cfg := SubSessionConfig{
			Task:      "work without permissions",
			AgentName: "worker",
			Title:     "Task",
		}

		s := newSubSession(parent, cfg, childAgent)
		assert.Nil(t, s.Permissions)
	})
}

func TestSession_ClonePermissions(t *testing.T) {
	t.Parallel()

	t.Run("returns deep copy", func(t *testing.T) {
		perms := &session.PermissionsConfig{
			Allow: []string{"read_file"},
			Deny:  []string{"write_file"},
		}
		s := session.New(session.WithPermissions(perms))

		cloned := s.ClonePermissions()
		require.NotNil(t, cloned)
		assert.Equal(t, perms.Allow, cloned.Allow)
		assert.Equal(t, perms.Deny, cloned.Deny)

		cloned.Allow = append(cloned.Allow, "exec_command")
		original := s.ClonePermissions()
		assert.Equal(t, []string{"read_file"}, original.Allow)
	})

	t.Run("returns nil when unset", func(t *testing.T) {
		s := session.New()
		assert.Nil(t, s.ClonePermissions())
	})
}

func TestSession_SetPermissions(t *testing.T) {
	t.Parallel()

	s := session.New()
	assert.Nil(t, s.ClonePermissions())

	perms := &session.PermissionsConfig{
		Allow: []string{"read_file"},
	}
	s.SetPermissions(perms)

	got := s.ClonePermissions()
	require.NotNil(t, got)
	assert.Equal(t, []string{"read_file"}, got.Allow)
}

func TestRunAgent_InheritsParentPermissions(t *testing.T) {
	t.Parallel()

	workerStream := newStreamBuilder().AddContent("done").AddStopWithUsage(10, 5).Build()
	parentProv := &mockProvider{id: "test/mock-model", stream: &mockStream{}}
	workerProv := &mockProvider{id: "test/mock-model", stream: workerStream}

	worker := agent.New("worker", "Worker agent", agent.WithModel(workerProv))
	root := agent.New("root", "Root agent", agent.WithModel(parentProv))
	agent.WithSubAgents(worker)(root)

	tm := team.New(team.WithAgents(root, worker))
	rt, err := NewLocalRuntime(t.Context(), tm,
		WithSessionCompaction(false),
		WithModelStore(mockModelStore{}),
	)
	require.NoError(t, err)

	parentPerms := &session.PermissionsConfig{
		Allow: []string{"read_file", "list_dir"},
		Deny:  []string{"shell:cmd=rm*"},
	}
	parentSession := session.New(
		session.WithUserMessage("Test"),
		session.WithToolsApproved(true),
		session.WithPermissions(parentPerms),
	)

	result := rt.RunAgent(t.Context(), agenttool.RunParams{
		AgentName:     "worker",
		Task:          "do something",
		ParentSession: parentSession,
	})
	require.Empty(t, result.ErrMsg, "RunAgent should succeed")

	var childSession *session.Session
	for _, item := range parentSession.Messages {
		if item.SubSession != nil {
			childSession = item.SubSession
			break
		}
	}
	require.NotNil(t, childSession, "parent must have a sub-session")

	assert.True(t, childSession.ToolsApproved,
		"child session must inherit ToolsApproved from parent")

	require.NotNil(t, childSession.Permissions)
	assert.Equal(t, []string{"read_file", "list_dir"}, childSession.Permissions.Allow)
	assert.Equal(t, []string{"shell:cmd=rm*"}, childSession.Permissions.Deny)

	childSession.Permissions.Allow = append(childSession.Permissions.Allow, "write_file")
	parentClone := parentSession.ClonePermissions()
	assert.Equal(t, []string{"read_file", "list_dir"}, parentClone.Allow,
		"parent permissions must be isolated from child mutations")
}

// TestRunForwarding_DoesNotBackPropagateApprovals locks the "permissions only
// flow downwards" invariant: approvals granted within a sub-session scope must
// not escalate the parent's ToolsApproved gate or permission rules.
func TestRunForwarding_DoesNotBackPropagateApprovals(t *testing.T) {
	t.Parallel()

	childStream := newStreamBuilder().AddContent("done").AddStopWithUsage(10, 5).Build()
	prov := &mockProvider{id: "test/mock-model", stream: childStream}

	librarian := agent.New("librarian", "Library agent", agent.WithModel(prov))
	root := agent.New("root", "Root agent", agent.WithModel(prov))
	agent.WithSubAgents(librarian)(root)

	tm := team.New(team.WithAgents(root, librarian))
	rt, err := NewLocalRuntime(t.Context(), tm,
		WithSessionCompaction(false),
		WithModelStore(mockModelStore{}),
	)
	require.NoError(t, err)

	parent := session.New(
		session.WithUserMessage("Test"),
		session.WithPermissions(&session.PermissionsConfig{Deny: []string{"dangerous_tool"}}),
	)
	require.False(t, parent.IsToolsApproved())

	evts := make(chan Event, 128)
	// Child scope broader than the parent's, as if the user had clicked
	// "approve all" / "always allow" inside the sub-session.
	_, err = rt.runForwarding(t.Context(), parent, NewChannelSink(evts), delegationRequest{
		SubSessionConfig: SubSessionConfig{
			Task:          "find a book",
			AgentName:     "librarian",
			Title:         "Transferred task",
			ToolsApproved: true,
			Permissions: &session.PermissionsConfig{
				Allow: []string{"exploit_tool"},
				Deny:  []string{"dangerous_tool"},
			},
		},
		SwitchCurrentAgent: true,
	})
	require.NoError(t, err)

	assert.False(t, parent.IsToolsApproved(),
		"a sub-session must not escalate the parent's ToolsApproved gate")
	parentPerms := parent.ClonePermissions()
	require.NotNil(t, parentPerms)
	assert.Empty(t, parentPerms.Allow,
		"child-scope approvals must not leak into the parent's Allow list")
	assert.Equal(t, []string{"dangerous_tool"}, parentPerms.Deny)
}

func TestRunAgent_EndToEndPermissions(t *testing.T) {
	t.Parallel()

	var executed bool
	agentTools := []tools.Tool{{
		Name:       "dangerous_tool",
		Parameters: map[string]any{},
		Handler: func(_ context.Context, _ tools.ToolCall, _ tools.Runtime) (*tools.ToolCallResult, error) {
			executed = true
			return tools.ResultSuccess("executed"), nil
		},
	}}

	workerStream := newStreamBuilder().
		AddToolCallName("call_1", "dangerous_tool").
		AddToolCallArguments("call_1", "{}").
		Build()
	parentProv := &mockProvider{id: "test/mock-model", stream: &mockStream{}}
	workerProv := &mockProvider{id: "test/mock-model", stream: workerStream}

	worker := agent.New("worker", "Worker agent",
		agent.WithModel(workerProv),
		agent.WithToolSets(newStubToolSet(nil, agentTools, nil)),
	)
	root := agent.New("root", "Root agent", agent.WithModel(parentProv))
	agent.WithSubAgents(worker)(root)

	tm := team.New(team.WithAgents(root, worker))
	rt, err := NewLocalRuntime(
		t.Context(), tm,
		WithSessionCompaction(false),
		WithModelStore(mockModelStore{}),
	)
	require.NoError(t, err)

	parentPerms := &session.PermissionsConfig{
		Allow: []string{"safe_tool"},
		Deny:  []string{"dangerous_tool"},
	}
	parentSession := session.New(
		session.WithUserMessage("Test"),
		session.WithToolsApproved(true),
		session.WithPermissions(parentPerms),
	)

	result := rt.RunAgent(t.Context(), agenttool.RunParams{
		AgentName:     "worker",
		Task:          "do something",
		ParentSession: parentSession,
	})
	require.Empty(t, result.ErrMsg, "RunAgent should succeed")

	var childSession *session.Session
	for _, item := range parentSession.Messages {
		if item.SubSession != nil {
			childSession = item.SubSession
			break
		}
	}
	require.NotNil(t, childSession, "parent must have a sub-session")
	require.NotNil(t, childSession.Permissions)
	assert.Equal(t, []string{"dangerous_tool"}, childSession.Permissions.Deny,
		"child must inherit the parent's Deny rules")
	require.False(t, executed, "expected dangerous_tool to NOT be executed because it is denied by inherited permissions")
}

func TestTransferTask_PropagatesPermissions(t *testing.T) {
	t.Parallel()

	childStream := newStreamBuilder().AddContent("transferred").AddStopWithUsage(10, 5).Build()
	prov := &mockProvider{id: "test/mock-model", stream: childStream}

	librarian := agent.New("librarian", "Library agent", agent.WithModel(prov))
	root := agent.New("root", "Root agent", agent.WithModel(prov))
	agent.WithSubAgents(librarian)(root)

	tm := team.New(team.WithAgents(root, librarian))
	rt, err := NewLocalRuntime(t.Context(), tm,
		WithSessionCompaction(false),
		WithModelStore(mockModelStore{}),
	)
	require.NoError(t, err)

	parentPerms := &session.PermissionsConfig{
		Allow: []string{"safe_tool"},
		Deny:  []string{"dangerous_tool"},
	}
	sess := session.New(
		session.WithUserMessage("Test"),
		session.WithToolsApproved(true),
		session.WithPermissions(parentPerms),
	)
	evts := make(chan Event, 128)

	toolCall := tools.ToolCall{
		ID:   "call_1",
		Type: "function",
		Function: tools.FunctionCall{
			Name:      "transfer_task",
			Arguments: `{"agent":"librarian","task":"find a book","expected_output":"book title"}`,
		},
	}

	result, err := rt.handleTaskTransfer(t.Context(), sess, toolCall, NewChannelSink(evts))
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.False(t, result.IsError, "transfer to valid sub-agent should succeed")

	var childSession *session.Session
	for _, item := range sess.Messages {
		if item.SubSession != nil {
			childSession = item.SubSession
			break
		}
	}
	require.NotNil(t, childSession, "parent must have a sub-session after transfer_task")

	require.NotNil(t, childSession.Permissions)
	assert.Equal(t, []string{"safe_tool"}, childSession.Permissions.Allow)
	assert.Equal(t, []string{"dangerous_tool"}, childSession.Permissions.Deny)

	assert.True(t, childSession.ToolsApproved,
		"child session must inherit ToolsApproved from parent")

	childSession.Permissions.Allow = append(childSession.Permissions.Allow, "exploit")
	parentClone := sess.ClonePermissions()
	assert.Equal(t, []string{"safe_tool"}, parentClone.Allow,
		"parent permissions must remain isolated from child mutations after transfer_task")
}

// privateTeamFixture models a team imported from a local config file: the
// secondary lead ("specialists") joins the primary team's public registry,
// but its own member ("researcher") stays private — reachable only through
// the lead's SubAgents pointers, never via team.Agent.
type privateTeamFixture struct {
	rt         *LocalRuntime
	tm         *team.Team
	root       *agent.Agent
	lead       *agent.Agent
	researcher *agent.Agent
}

// newPrivateTeamFixture builds root -> specialists -> researcher where only
// root and specialists are registered in team.Team. Providers are supplied
// per agent so each test scripts its own streams.
func newPrivateTeamFixture(t *testing.T, rootProv, leadProv, researcherProv provider.Provider) privateTeamFixture {
	t.Helper()

	researcher := agent.New("researcher", "Researcher of the secondary team", agent.WithModel(researcherProv))
	lead := agent.New("specialists", "Secondary team lead",
		agent.WithModel(leadProv),
		agent.WithToolSets(transfertask.New()),
	)
	agent.WithSubAgents(researcher)(lead)
	root := agent.New("root", "Primary lead",
		agent.WithModel(rootProv),
		agent.WithToolSets(transfertask.New()),
	)
	agent.WithSubAgents(lead)(root)

	tm := team.New(team.WithAgents(root, lead))
	rt, err := NewLocalRuntime(t.Context(), tm,
		WithSessionCompaction(false),
		WithModelStore(mockModelStore{}),
	)
	require.NoError(t, err)

	// The researcher must really be private for these tests to prove anything.
	_, err = tm.Agent("researcher")
	require.Error(t, err, "fixture invariant: researcher must not be in the public team registry")

	return privateTeamFixture{rt: rt, tm: tm, root: root, lead: lead, researcher: researcher}
}

// TestHandleTaskTransfer_NestedPrivateSubAgent proves the imported-team flow:
// while the secondary lead is the current agent, transfer_task("researcher")
// must resolve the researcher through the lead's SubAgents pointers even
// though the researcher is not in team.Team, run it to completion, and
// restore the lead as the current agent afterwards.
func TestHandleTaskTransfer_NestedPrivateSubAgent(t *testing.T) {
	t.Parallel()

	researcherStream := newStreamBuilder().AddContent("research notes").AddStopWithUsage(10, 5).Build()
	fx := newPrivateTeamFixture(t,
		&mockProvider{id: "test/mock-model", stream: &mockStream{}},
		&mockProvider{id: "test/mock-model", stream: &mockStream{}},
		&mockProvider{id: "test/mock-model", stream: researcherStream},
	)

	// Simulate the state mid-delegation: root already transferred to the lead.
	require.NoError(t, fx.rt.SetCurrentAgent(t.Context(), "specialists"))

	sess := session.New(session.WithUserMessage("Test"), session.WithToolsApproved(true))
	evts := make(chan Event, 256)
	toolCall := tools.ToolCall{
		ID:   "call_1",
		Type: "function",
		Function: tools.FunctionCall{
			Name:      "transfer_task",
			Arguments: `{"agent":"researcher","task":"research the topic","expected_output":"notes"}`,
		},
	}

	result, err := fx.rt.handleTaskTransfer(t.Context(), sess, toolCall, NewChannelSink(evts))
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.False(t, result.IsError, "nested transfer to a private sub-agent must succeed")
	assert.Equal(t, "research notes", result.Output)

	assert.Equal(t, "specialists", fx.rt.CurrentAgentName(t.Context()),
		"the secondary lead must be restored as current agent after the nested transfer")

	// The researcher stays private even after being run.
	_, err = fx.tm.Agent("researcher")
	require.Error(t, err)
}

// TestHandleTaskTransfer_NestedPrivateBlocksUntilRelease proves the nested
// transfer is synchronous: handleTaskTransfer must not return before the
// private child's model stream completes. The child provider blocks on a
// channel; timeouts are used only as guards.
func TestHandleTaskTransfer_NestedPrivateBlocksUntilRelease(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	fx := newPrivateTeamFixture(t,
		&mockProvider{id: "test/mock-model", stream: &mockStream{}},
		&mockProvider{id: "test/mock-model", stream: &mockStream{}},
		&activeRootBlockingProvider{id: "test/mock-model", release: release},
	)

	require.NoError(t, fx.rt.SetCurrentAgent(t.Context(), "specialists"))

	sess := session.New(session.WithUserMessage("Test"), session.WithToolsApproved(true))
	evts := make(chan Event, 512)
	toolCall := tools.ToolCall{
		ID:   "call_1",
		Type: "function",
		Function: tools.FunctionCall{
			Name:      "transfer_task",
			Arguments: `{"agent":"researcher","task":"research the topic","expected_output":"notes"}`,
		},
	}

	type outcome struct {
		result *tools.ToolCallResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := fx.rt.handleTaskTransfer(t.Context(), sess, toolCall, NewChannelSink(evts))
		done <- outcome{result: result, err: err}
	}()

	// Wait until the swap to the private child is observable, so the
	// not-yet-returned assertion below checks a transfer that is provably
	// in flight rather than one that has not started.
	guard := time.After(10 * time.Second)
	for fx.rt.CurrentAgentName(t.Context()) != "researcher" {
		select {
		case out := <-done:
			t.Fatalf("transfer returned before the child was released (result=%+v, err=%v)", out.result, out.err)
		case <-guard:
			t.Fatal("timed out waiting for the transfer to switch to the researcher")
		case <-time.After(5 * time.Millisecond):
		}
	}

	select {
	case out := <-done:
		t.Fatalf("transfer returned before the child was released (result=%+v, err=%v)", out.result, out.err)
	default:
	}

	close(release)

	select {
	case out := <-done:
		require.NoError(t, out.err)
		require.NotNil(t, out.result)
		assert.False(t, out.result.IsError)
	case <-time.After(10 * time.Second):
		t.Fatal("transfer did not return after the child was released")
	}

	assert.Equal(t, "specialists", fx.rt.CurrentAgentName(t.Context()),
		"the secondary lead must be restored as current agent after the nested transfer")
}

// TestRunStream_NestedPrivateDelegation drives the full documented chain
// through the run loop: primary root -> transfer_task("specialists")
// (blocking) -> secondary lead -> transfer_task("researcher") (blocking) ->
// back to the lead -> back to root. The researcher exists only as a SubAgents
// pointer of the lead, never in team.Team.
func TestRunStream_NestedPrivateDelegation(t *testing.T) {
	t.Parallel()

	// Each agent's provider serves one stream per model turn: the tool-call
	// turn, then the final answer after the tool result comes back.
	rootProv := &queueProvider{id: "test/mock-model", streams: []chat.MessageStream{
		newStreamBuilder().
			AddToolCallName("call_root", "transfer_task").
			AddToolCallArguments("call_root", `{"agent":"specialists","task":"coordinate the research"}`).
			AddStopWithUsage(10, 5).
			Build(),
		newStreamBuilder().AddContent("root done").AddStopWithUsage(10, 5).Build(),
	}}
	leadProv := &queueProvider{id: "test/mock-model", streams: []chat.MessageStream{
		newStreamBuilder().
			AddToolCallName("call_lead", "transfer_task").
			AddToolCallArguments("call_lead", `{"agent":"researcher","task":"research the topic"}`).
			AddStopWithUsage(10, 5).
			Build(),
		newStreamBuilder().AddContent("lead done").AddStopWithUsage(10, 5).Build(),
	}}
	researcherStream := newStreamBuilder().
		AddContent("research notes").
		AddStopWithUsage(10, 5).
		Build()

	fx := newPrivateTeamFixture(t,
		rootProv,
		leadProv,
		&mockProvider{id: "test/mock-model", stream: researcherStream},
	)

	sess := session.New(session.WithUserMessage("Delegate the research."), session.WithToolsApproved(true))

	var errEvents []string
	for event := range fx.rt.RunStream(t.Context(), sess) {
		if errEvent, ok := event.(*ErrorEvent); ok {
			errEvents = append(errEvents, errEvent.Error)
		}
	}
	require.Empty(t, errEvents, "the nested delegation chain must complete without errors")

	assert.Equal(t, "root done", sess.GetLastAssistantMessageContent())

	// The lead's sub-session is attached to the root session, and the
	// researcher's sub-session is attached to the lead's, mirroring the
	// delegation chain.
	leadSession := findSubSession(sess)
	require.NotNil(t, leadSession, "root session must record the lead's sub-session")
	assert.Equal(t, "lead done", leadSession.GetLastAssistantMessageContent())

	researcherSession := findSubSession(leadSession)
	require.NotNil(t, researcherSession, "lead session must record the researcher's sub-session")
	assert.Equal(t, "research notes", researcherSession.GetLastAssistantMessageContent())

	assert.Equal(t, "root", fx.rt.CurrentAgentName(t.Context()),
		"the primary root must be the current agent again after the chain returns")
}

// findSubSession returns the first sub-session recorded on sess, or nil.
func findSubSession(sess *session.Session) *session.Session {
	for _, item := range sess.Messages {
		if item.SubSession != nil {
			return item.SubSession
		}
	}
	return nil
}

// TestRunAgent_PrivateSubAgentOfCurrentAgent covers run_background_agent from
// a secondary lead: the Runner API only carries a name, so RunAgent must
// resolve "researcher" from the current agent's SubAgents pointers (the team
// registry does not know it) and pin the child session to that exact
// instance.
func TestRunAgent_PrivateSubAgentOfCurrentAgent(t *testing.T) {
	t.Parallel()

	researcherStream := newStreamBuilder().AddContent("research notes").AddStopWithUsage(10, 5).Build()
	fx := newPrivateTeamFixture(t,
		&mockProvider{id: "test/mock-model", stream: &mockStream{}},
		&mockProvider{id: "test/mock-model", stream: &mockStream{}},
		&mockProvider{id: "test/mock-model", stream: researcherStream},
	)

	require.NoError(t, fx.rt.SetCurrentAgent(t.Context(), "specialists"))

	parentSession := session.New(session.WithUserMessage("Test"), session.WithToolsApproved(true))
	result := fx.rt.RunAgent(t.Context(), agenttool.RunParams{
		AgentName:     "researcher",
		Task:          "research the topic",
		ParentSession: parentSession,
	})
	require.Empty(t, result.ErrMsg, "background run of a private sub-agent must succeed")
	assert.Equal(t, "research notes", result.Result)

	childSession := findSubSession(parentSession)
	require.NotNil(t, childSession, "parent must record the background sub-session")
	assert.Equal(t, "researcher", childSession.AgentName)
	assert.Same(t, fx.researcher, childSession.PinnedAgent,
		"the child session must pin the exact private instance so RunStream resolves it")
}

// signallingBlockingProvider closes started when its first model call
// begins, then blocks until release before serving a stream with the given
// content. It lets tests assert runtime state while a child agent's model
// call is provably in flight.
type signallingBlockingProvider struct {
	id      string
	content string
	started chan struct{}
	release <-chan struct{}
	once    sync.Once
}

func (p *signallingBlockingProvider) ID() modelsdev.ID { return modelsdev.ParseIDOrZero(p.id) }

func (p *signallingBlockingProvider) CreateChatCompletionStream(ctx context.Context, _ []chat.Message, _ []tools.Tool) (chat.MessageStream, error) {
	p.once.Do(func() { close(p.started) })
	select {
	case <-p.release:
		return newStreamBuilder().AddContent(p.content).AddStopWithUsage(1, 1).Build(), nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (p *signallingBlockingProvider) BaseConfig() base.Config { return base.Config{} }
func (p *signallingBlockingProvider) MaxTokens() int          { return 0 }

// TestHandleTaskTransfer_PinnedBackgroundSessionKeepsRouterUntouched is the
// regression test for pin-aware delegation: a background session pinned to
// the secondary lead calls transfer_task("researcher") while the global
// router points at root for the whole run. Root has no "researcher"
// sub-agent, so resolving the caller from the router would reject the
// transfer outright; resolving it from the pin must succeed, block until the
// exact private child completes, and never touch the shared router.
func TestHandleTaskTransfer_PinnedBackgroundSessionKeepsRouterUntouched(t *testing.T) {
	t.Parallel()

	started := make(chan struct{})
	release := make(chan struct{})
	fx := newPrivateTeamFixture(t,
		&mockProvider{id: "test/mock-model", stream: &mockStream{}},
		&mockProvider{id: "test/mock-model", stream: &mockStream{}},
		&signallingBlockingProvider{id: "test/mock-model", content: "research notes", started: started, release: release},
	)

	// Only the session pin carries the lead identity, exactly as
	// runCollecting builds background sessions; the router stays on root.
	require.Equal(t, "root", fx.rt.CurrentAgentName(t.Context()))
	sess := session.New(
		session.WithUserMessage("Test"),
		session.WithToolsApproved(true),
		session.WithAgentName("specialists"),
		session.WithPinnedAgent(fx.lead),
	)

	evts := make(chan Event, 512)
	toolCall := tools.ToolCall{
		ID:   "call_1",
		Type: "function",
		Function: tools.FunctionCall{
			Name:      "transfer_task",
			Arguments: `{"agent":"researcher","task":"research the topic","expected_output":"notes"}`,
		},
	}

	type outcome struct {
		result *tools.ToolCallResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := fx.rt.handleTaskTransfer(t.Context(), sess, toolCall, NewChannelSink(evts))
		done <- outcome{result: result, err: err}
	}()

	// Wait until the researcher's model call is provably in flight.
	select {
	case <-started:
	case out := <-done:
		t.Fatalf("transfer returned before the child started (result=%+v, err=%v)", out.result, out.err)
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for the researcher's model call to start")
	}

	assert.Equal(t, "root", fx.rt.CurrentAgentName(t.Context()),
		"a pinned session's transfer_task must not swap the global router while in flight")

	// The nested transfer stays blocking: no return before the child is released.
	select {
	case out := <-done:
		t.Fatalf("transfer returned before the child was released (result=%+v, err=%v)", out.result, out.err)
	default:
	}

	close(release)

	var out outcome
	select {
	case out = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("transfer did not return after the child was released")
	}
	require.NoError(t, out.err)
	require.NotNil(t, out.result)
	assert.False(t, out.result.IsError, "pin-resolved transfer to a private sub-agent must succeed")
	assert.Equal(t, "research notes", out.result.Output)

	assert.Equal(t, "root", fx.rt.CurrentAgentName(t.Context()),
		"the global router must be untouched after the pinned session's transfer")

	// The child ran as the exact private instance, pinned for its RunStream.
	childSession := findSubSession(sess)
	require.NotNil(t, childSession, "the pinned parent must record the child sub-session")
	assert.Equal(t, "researcher", childSession.AgentName)
	assert.Same(t, fx.researcher, childSession.PinnedAgent,
		"the child session must pin the exact private instance")
}

// TestRunAgent_UsesCapturedCallerAndTarget verifies the runtime half of the
// HandleRun snapshot contract: RunAgent runs the exact Caller/Target captured
// at dispatch time even though the shared router points at an agent (root)
// that cannot resolve the target name, instead of re-deriving them from the
// router when the background goroutine finally runs.
func TestRunAgent_UsesCapturedCallerAndTarget(t *testing.T) {
	t.Parallel()

	researcherStream := newStreamBuilder().AddContent("research notes").AddStopWithUsage(10, 5).Build()
	fx := newPrivateTeamFixture(t,
		&mockProvider{id: "test/mock-model", stream: &mockStream{}},
		&mockProvider{id: "test/mock-model", stream: &mockStream{}},
		&mockProvider{id: "test/mock-model", stream: researcherStream},
	)

	// Root's sub-agents do not include the researcher: any late
	// re-derivation from the router would fail or substitute here.
	require.Equal(t, "root", fx.rt.CurrentAgentName(t.Context()))

	parentSession := session.New(session.WithUserMessage("Test"), session.WithToolsApproved(true))
	result := fx.rt.RunAgent(t.Context(), agenttool.RunParams{
		AgentName:     "researcher",
		Task:          "research the topic",
		Caller:        fx.lead,
		Target:        fx.researcher,
		ParentSession: parentSession,
	})
	require.Empty(t, result.ErrMsg, "RunAgent must use the captured target, not re-resolve via the router")
	assert.Equal(t, "research notes", result.Result)

	childSession := findSubSession(parentSession)
	require.NotNil(t, childSession, "parent must record the background sub-session")
	assert.Same(t, fx.researcher, childSession.PinnedAgent,
		"the captured target instance must be pinned on the child session")
	assert.Equal(t, "root", fx.rt.CurrentAgentName(t.Context()),
		"a captured-target background run must leave the router untouched")
}

// TestHandleHandoff_PinnedSessionRepointsPinNotRouter locks the pinned-session
// handoff semantics: the caller is resolved from the pin, the pin itself is
// repointed at the exact handoff target so the session's next turn runs it,
// and the shared router never moves.
func TestHandleHandoff_PinnedSessionRepointsPinNotRouter(t *testing.T) {
	t.Parallel()

	prov := &mockProvider{id: "test/mock-model", stream: &mockStream{}}
	researcher := agent.New("researcher", "Handoff target", agent.WithModel(prov))
	lead := agent.New("specialists", "Secondary team lead", agent.WithModel(prov))
	agent.WithHandoffs(researcher)(lead)
	root := agent.New("root", "Primary lead", agent.WithModel(prov))

	tm := team.New(team.WithAgents(root, lead))
	rt, err := NewLocalRuntime(t.Context(), tm,
		WithSessionCompaction(false),
		WithModelStore(mockModelStore{}),
	)
	require.NoError(t, err)
	require.Equal(t, "root", rt.CurrentAgentName(t.Context()))

	sess := session.New(
		session.WithUserMessage("Test"),
		session.WithAgentName("specialists"),
		session.WithPinnedAgent(lead),
	)
	toolCall := tools.ToolCall{
		ID:   "call_1",
		Type: "function",
		Function: tools.FunctionCall{
			Name:      "handoff",
			Arguments: `{"agent":"researcher"}`,
		},
	}

	evts := make(chan Event, 16)
	result, err := rt.handleHandoff(t.Context(), sess, toolCall, NewChannelSink(evts))
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.False(t, result.IsError, "handoff from the pinned lead to its handoff target must succeed")

	assert.Same(t, researcher, sess.PinnedAgent, "the session pin must be repointed at the exact target")
	assert.Equal(t, "researcher", sess.AgentName)
	assert.Equal(t, "root", rt.CurrentAgentName(t.Context()),
		"a pinned session's handoff must not swap the global router")
}
