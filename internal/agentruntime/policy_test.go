package agentruntime

import (
	"testing"

	"github.com/startvibecoding/mothx/internal/session"
)

func TestPolicyResolveMode(t *testing.T) {
	tests := []struct {
		name      string
		policy    Policy
		session   string
		requested string
		want      string
	}{
		{
			name:      "wechat cannot be downgraded",
			policy:    Policy{Source: SourceWeChat, DefaultMode: ModeAgent},
			session:   ModeAgent,
			requested: ModePlan,
			want:      ModeYolo,
		},
		{
			name:   "feishu empty session uses yolo",
			policy: Policy{Source: SourceFeishu, DefaultMode: ModeAgent},
			want:   ModeYolo,
		},
		{
			name:      "regular request overrides session",
			policy:    Policy{Source: SourceWebUI, DefaultMode: ModeAgent},
			session:   ModePlan,
			requested: ModeYolo,
			want:      ModeYolo,
		},
		{
			name:   "regular empty session uses default",
			policy: Policy{Source: SourceWebUI, DefaultMode: ModeAgent},
			want:   ModeAgent,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.policy.ResolveMode(tt.session, tt.requested)
			if err != nil {
				t.Fatalf("ResolveMode: %v", err)
			}
			if got != tt.want {
				t.Fatalf("ResolveMode() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestModeResolver(t *testing.T) {
	got, err := (ModeResolver{Policy: ExecutionPolicy{Source: SourceFeishu, DefaultMode: ModeAgent}}).Resolve(ModePlan, ModeAgent)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != ModeYolo {
		t.Fatalf("Resolve() = %q, want yolo", got)
	}
}

func TestSourceFromSessionHeader(t *testing.T) {
	if got := SourceFromSessionHeader(&session.Header{ChannelType: "feishu"}); got != SourceFeishu {
		t.Fatalf("source = %q, want %q", got, SourceFeishu)
	}
	if got := SourceFromSessionHeader(&session.Header{ChannelType: "local"}); got != SourceUnknown {
		t.Fatalf("local source = %q, want unknown", got)
	}
}
