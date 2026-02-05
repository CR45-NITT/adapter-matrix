package events

type DeliveryFailed struct {
	OriginalEventID string `json:"original_event_id"`
	Adapter         string `json:"adapter"`
	Reason          string `json:"reason"`
}
