package agentruntime

// ReplayDecisions reconstructs the latest pending decision set from durable
// request/resolution records. It is intentionally protocol-neutral; adapters
// remain responsible for decoding their payload fields.
func ReplayDecisions(records []DecisionRecord) map[string]DecisionRecord {
	pending := make(map[string]DecisionRecord)
	for _, record := range records {
		if record.ID == "" {
			continue
		}
		switch record.Status {
		case "pending", "requested":
			pending[record.ID] = record
		default:
			delete(pending, record.ID)
		}
	}
	return pending
}
