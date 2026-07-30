package runtime_test

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/docker/docker-agent/pkg/chat"
	"github.com/docker/docker-agent/pkg/config"
	"github.com/docker/docker-agent/pkg/config/latest"
	"github.com/docker/docker-agent/pkg/environment"
	"github.com/docker/docker-agent/pkg/model/provider"
	"github.com/docker/docker-agent/pkg/model/provider/base"
	"github.com/docker/docker-agent/pkg/model/provider/options"
	"github.com/docker/docker-agent/pkg/modelsdev"
	"github.com/docker/docker-agent/pkg/runtime"
	"github.com/docker/docker-agent/pkg/session"
	"github.com/docker/docker-agent/pkg/teamloader"
	"github.com/docker/docker-agent/pkg/tools"
)

// This file lives in the external runtime_test package because it imports
// teamloader, which itself (transitively, via runtime/jscommands) imports
// pkg/runtime — an in-package test file would create an import cycle.

// scriptedStream replays a fixed sequence of stream responses, then io.EOF.
type scriptedStream struct {
	responses []chat.MessageStreamResponse
	idx       int
}

func (s *scriptedStream) Recv() (chat.MessageStreamResponse, error) {
	if s.idx >= len(s.responses) {
		return chat.MessageStreamResponse{}, io.EOF
	}
	r := s.responses[s.idx]
	s.idx++
	return r, nil
}

func (s *scriptedStream) Close() {}

// toolCallTurn scripts one model turn that calls transfer_task with args.
func toolCallTurn(callID, args string) *scriptedStream {
	return &scriptedStream{responses: []chat.MessageStreamResponse{
		{Choices: []chat.MessageStreamChoice{{
			Delta: chat.MessageDelta{ToolCalls: []tools.ToolCall{{
				ID:       callID,
				Type:     "function",
				Function: tools.FunctionCall{Name: "transfer_task", Arguments: args},
			}}},
		}}},
		{
			Choices: []chat.MessageStreamChoice{{FinishReason: chat.FinishReasonStop}},
			Usage:   &chat.Usage{InputTokens: 10, OutputTokens: 5},
		},
	}}
}

// finalTurn scripts one model turn that answers with content and stops.
func finalTurn(content string) *scriptedStream {
	return &scriptedStream{responses: []chat.MessageStreamResponse{
		{Choices: []chat.MessageStreamChoice{{Delta: chat.MessageDelta{Content: content}}}},
		{
			Choices: []chat.MessageStreamChoice{{FinishReason: chat.FinishReasonStop}},
			Usage:   &chat.Usage{InputTokens: 10, OutputTokens: 5},
		},
	}}
}

// scriptedProvider serves one scripted stream per model turn, in order.
type scriptedProvider struct {
	id      string
	mu      sync.Mutex
	streams []*scriptedStream
}

func (p *scriptedProvider) ID() modelsdev.ID { return modelsdev.ParseIDOrZero(p.id) }

func (p *scriptedProvider) CreateChatCompletionStream(context.Context, []chat.Message, []tools.Tool) (chat.MessageStream, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.streams) == 0 {
		return &scriptedStream{}, nil
	}
	s := p.streams[0]
	p.streams = p.streams[1:]
	return s, nil
}

func (p *scriptedProvider) BaseConfig() base.Config { return base.Config{} }

// stubModelStore satisfies runtime.ModelStore for models the catalogue does
// not know; only GetModel is exercised by these flows.
type stubModelStore struct{ runtime.ModelStore }

func (stubModelStore) GetModel(context.Context, modelsdev.ID) (*modelsdev.Model, error) {
	return nil, nil
}

// TestLocalTeamImport_NestedDelegation is the end-to-end proof of the
// local-file team import feature: two YAML files loaded via teamloader.Load,
// then the full documented chain through the runtime — primary root ->
// transfer_task("specialists") (blocking) -> secondary lead ->
// transfer_task("researcher") (blocking) -> back to the lead -> back to the
// primary root. The researcher never joins the public team registry; it is
// reachable only through the imported lead's own sub-agent pointers.
func TestLocalTeamImport_NestedDelegation(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "dummy")

	secondary := `models:
  lead-model:
    provider: openai
    model: lead-model
  researcher-model:
    provider: openai
    model: researcher-model
agents:
  root:
    model: lead-model
    description: Secondary team lead
    instruction: Coordinate your own team to answer tasks.
    sub_agents: [researcher]
  researcher:
    model: researcher-model
    description: Researcher of the secondary team
    instruction: Research topics and report back.
`
	primary := `models:
  root-model:
    provider: openai
    model: root-model
agents:
  root:
    model: root-model
    description: Primary lead
    instruction: Delegate specialist work to the secondary team lead.
    sub_agents:
      - specialists:./secondary-team.yaml
`

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "secondary-team.yaml"), []byte(secondary), 0o644))
	primaryPath := filepath.Join(dir, "primary-team.yaml")
	require.NoError(t, os.WriteFile(primaryPath, []byte(primary), 0o644))

	// One scripted provider per model: the leads run two turns each (the
	// transfer_task call, then the final answer once the transfer returns),
	// the researcher answers directly.
	provs := map[string]provider.Provider{
		"root-model": &scriptedProvider{id: "openai/root-model", streams: []*scriptedStream{
			toolCallTurn("call_root", `{"agent":"specialists","task":"coordinate the research"}`),
			finalTurn("root done"),
		}},
		"lead-model": &scriptedProvider{id: "openai/lead-model", streams: []*scriptedStream{
			toolCallTurn("call_lead", `{"agent":"researcher","task":"research the topic"}`),
			finalTurn("lead done"),
		}},
		"researcher-model": &scriptedProvider{id: "openai/researcher-model", streams: []*scriptedStream{
			finalTurn("research notes"),
		}},
	}
	registry := provider.NewRegistry(map[string]provider.Factory{
		"openai": func(_ context.Context, cfg *latest.ModelConfig, _ environment.Provider, _ ...options.Opt) (provider.Provider, error) {
			p, ok := provs[cfg.Model]
			if !ok {
				return nil, fmt.Errorf("no scripted provider for model %q", cfg.Model)
			}
			return p, nil
		},
	})

	tm, err := teamloader.Load(t.Context(), config.NewFileSource(primaryPath), &config.RuntimeConfig{}, teamloader.WithProviderRegistry(registry))
	require.NoError(t, err)

	// The imported lead is public; its member stays private to it.
	_, err = tm.Agent("specialists")
	require.NoError(t, err)
	_, err = tm.Agent("researcher")
	require.Error(t, err, "the secondary team's member must not join the public registry")

	rt, err := runtime.NewLocalRuntime(t.Context(), tm,
		runtime.WithSessionCompaction(false),
		runtime.WithModelStore(stubModelStore{}),
	)
	require.NoError(t, err)

	sess := session.New(session.WithUserMessage("Delegate the research."), session.WithToolsApproved(true))

	var errEvents []string
	for event := range rt.RunStream(t.Context(), sess) {
		if errEvent, ok := event.(*runtime.ErrorEvent); ok {
			errEvents = append(errEvents, errEvent.Error)
		}
	}
	require.Empty(t, errEvents, "the nested delegation chain must complete without errors")

	assert.Equal(t, "root done", sess.GetLastAssistantMessageContent())

	// The sub-session chain mirrors the delegation: root records the lead's
	// session, which records the researcher's.
	leadSession := firstSubSession(sess)
	require.NotNil(t, leadSession, "root session must record the lead's sub-session")
	assert.Equal(t, "lead done", leadSession.GetLastAssistantMessageContent())

	researcherSession := firstSubSession(leadSession)
	require.NotNil(t, researcherSession, "lead session must record the researcher's sub-session")
	assert.Equal(t, "research notes", researcherSession.GetLastAssistantMessageContent())

	assert.Equal(t, "root", rt.CurrentAgentName(t.Context()),
		"the primary root must be current again after the chain returns")
}

// firstSubSession returns the first sub-session recorded on sess, or nil.
func firstSubSession(sess *session.Session) *session.Session {
	for _, item := range sess.Messages {
		if item.SubSession != nil {
			return item.SubSession
		}
	}
	return nil
}
