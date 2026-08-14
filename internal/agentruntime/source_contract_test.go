package agentruntime

import (
	"testing"

	"github.com/startvibecoding/mothx/internal/session"
)

func TestResolveSourceBindingWinsOverRequestAndRuntime(t *testing.T) {
	resolved := ResolveSource(SourceResolutionInput{
		Binding: &session.Binding{ChannelType: "wechat"},
		Current: SourceWebUI, Requested: SourceACP,
	})
	if resolved.Source != SourceWeChat {
		t.Fatalf("source = %q, want %q", resolved.Source, SourceWeChat)
	}
}

func TestResolvePolicyBoundChannelCannotDowngradeMode(t *testing.T) {
	resolved, mode, err := ResolvePolicy(SourceResolutionInput{
		Binding: &session.Binding{ChannelType: "feishu"},
		Current: SourceWebUI, Requested: SourceWebUI,
	}, ModeAgent, ModePlan, ModeAgent)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Source != SourceFeishu || mode != ModeYolo {
		t.Fatalf("resolution = %#v mode = %q, want feishu/yolo", resolved, mode)
	}
}

func TestResolvePolicyUnboundWebUIUsesRequestedMode(t *testing.T) {
	_, mode, err := ResolvePolicy(SourceResolutionInput{
		Current: SourceWebUI, Requested: SourceWebUI,
	}, ModeAgent, ModeYolo, ModeAgent)
	if err != nil {
		t.Fatal(err)
	}
	if mode != ModeYolo {
		t.Fatalf("mode = %q, want yolo", mode)
	}
}
