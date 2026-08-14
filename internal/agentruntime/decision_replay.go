package agentruntime

import "time"

// ReplayDecisions reconstructs the latest pending decision set from durable
// request/resolution records. It is intentionally protocol-neutral; adapters
// remain responsible for decoding their payload fields.
func ReplayDecisions(records []DecisionRecord) map[string]DecisionRecord {
	return ReplayDecisionsAt(records, time.Now())
}

// ReplayDecisionsAt reconstructs pending decisions at a stable clock instant.
// Expired records are omitted so callers can terminalize them durably.
func ReplayDecisionsAt(records []DecisionRecord, now time.Time) map[string]DecisionRecord {
	pending := make(map[string]DecisionRecord)
	for _, record := range records {
		if record.ID == "" {
			continue
		}
		switch record.Status {
		case "pending", "requested":
			if !record.ExpiresAt.IsZero() && !now.Before(record.ExpiresAt) {
				delete(pending, record.ID)
				continue
			}
			pending[record.ID] = record
		default:
			delete(pending, record.ID)
		}
	}
	return pending
}

func ExpiredDecisions(records []DecisionRecord, now time.Time) []DecisionRecord {
	latest := make(map[string]DecisionRecord)
	for _, record := range records {
		if record.ID == "" {
			continue
		}
		switch record.Status {
		case "pending", "requested":
			latest[record.ID] = record
		default:
			delete(latest, record.ID)
		}
	}
	result := make([]DecisionRecord, 0)
	for _, record := range latest {
		if !record.ExpiresAt.IsZero() && !now.Before(record.ExpiresAt) {
			result = append(result, record)
		}
	}
	return result
}
