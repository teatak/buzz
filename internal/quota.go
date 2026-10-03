package buzz

import (
	"net/http"
	"time"
)

func (s *Server) enforceUserQuota(w http.ResponseWriter, inbound string, user AuthToken) bool {
	if user.UserID <= 0 || (user.WeeklyQuotaCredits == 0 && user.LifetimeQuotaCredits == 0) {
		return true
	}
	status, err := s.store.UserQuotaStatus(user.UserID, time.Now().UTC())
	if err != nil {
		writeInboundError(w, inbound, http.StatusInternalServerError, "quota_error", "failed to check quota")
		return false
	}
	if status.WeeklyRemainingMicrocredits <= 0 && status.LifetimeRemainingMicrocredits <= 0 {
		writeInboundError(w, inbound, http.StatusTooManyRequests, "quota_exceeded", "quota exceeded")
		return false
	}
	return true
}
