package agent

import (
	"context"
	"testing"
	"time"

	"github.com/startvibecoding/mothx/internal/tools"
)

func TestRequestQuestionReturnsOnContextCancel(t *testing.T) {
	a := New(Config{Mode: "plan"}, tools.NewRegistry(t.TempDir(), nil))
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan Event, 1)

	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	if answer := a.RequestQuestion(ctx, ch, "pick one", []string{"a", "b"}, ""); answer != "" {
		t.Fatalf("answer = %q, want empty on context cancel", answer)
	}

	a.questionMu.Lock()
	pending := len(a.pendingQuestions)
	a.questionMu.Unlock()
	if pending != 0 {
		t.Fatalf("pending questions leaked: %d", pending)
	}
}

func TestRequestQuestionStillAnswers(t *testing.T) {
	a := New(Config{Mode: "plan"}, tools.NewRegistry(t.TempDir(), nil))
	ch := make(chan Event, 1)
	go func() {
		time.Sleep(50 * time.Millisecond)
		a.HandleQuestionResponse("question-1", "option a")
	}()
	if answer := a.RequestQuestion(context.Background(), ch, "pick one", []string{"a", "b"}, ""); answer != "option a" {
		t.Fatalf("answer = %q, want %q", answer, "option a")
	}
}
