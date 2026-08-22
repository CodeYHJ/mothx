package agentruntime

import (
	"context"

	"github.com/startvibecoding/mothx/internal/session"
)

// ForkOptions is the front-end-neutral request for a Session prefix fork.
// RequestID is mandatory so retries can reconcile the original child.
type ForkOptions = session.ForkOptions

type ForkResult = session.ForkResult

// Fork performs the canonical Session fork operation. The data layer owns the
// SQLite snapshot/copy transaction; this Runtime boundary keeps adapters from
// implementing their own copy or Agent lifecycle.
func Fork(ctx context.Context, sessionDir string, options ForkOptions) (ForkResult, error) {
	return session.ForkSession(ctx, sessionDir, options)
}
