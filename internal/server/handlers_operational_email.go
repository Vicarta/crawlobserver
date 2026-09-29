package server

import "net/http"

func (s *Server) handleOperationalEmailHistory(w http.ResponseWriter, r *http.Request) {
	if !requireAdminAccess(w, r) {
		return
	}
	if s.keyStore == nil {
		writeError(w, http.StatusServiceUnavailable, "email history is unavailable")
		return
	}
	limit, offset := clampPagination(queryInt(r, "limit", 100), queryInt(r, "offset", 0))
	if limit > 200 {
		limit = 200
	}
	receipts, total, err := s.keyStore.ListOperationalEmailReceipts(limit, offset)
	if err != nil {
		internalError(w, r, err)
		return
	}
	writeJSON(w, map[string]interface{}{
		"receipts": receipts,
		"total":    total,
		"status":   s.operationalEmailStatus(),
	})
}
