package auth

import (
	"context"
	"database/sql"
	"fmt"
)

// audit is the append-only record of who did what. Rows are never edited and
// the actor is only ever nulled by a deletes chain, never this code.
func (s *Service) audit(ctx context.Context, actor sql.NullInt64, event, target, ip, ua string, result int, detail string) error {
	_, err := s.st.SQL().ExecContext(ctx, sqlInsertAudit, s.now(), actor, event, target, ip, ua, result, detail)
	if err != nil {
		return fmt.Errorf("writing to the audit log: %w", err)
	}
	return nil
}

// Audit writes one append-only row. It is exported for the surfaces that own
// non-login events (an admin change, a revocation outside the login flow).
func (s *Service) Audit(ctx context.Context, actor sql.NullInt64, event, target, ip, ua string, result int) error {
	return s.audit(ctx, actor, event, target, ip, ua, result, "")
}
