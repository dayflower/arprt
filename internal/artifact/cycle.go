package artifact

import (
	"bytes"
	"encoding/json"

	api "github.com/dayflower/arprt/api/v1alpha1"
)

// persistedCycle requires an explicit position when decoding PVC turns. Missing
// positions are not legacy position zero; cycle-state migration is unsupported.
// Keep this disk-only check separate from the public API's admission validation.
type persistedCycle api.ScreenCycleStatus

func (c *persistedCycle) UnmarshalJSON(data []byte) error {
	type plain persistedCycle
	var decoded struct {
		plain
		EntryIndex *int32 `json:"entryIndex"`
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&decoded); err != nil {
		return err
	}
	if decoded.EntryIndex == nil || *decoded.EntryIndex < 0 || *decoded.EntryIndex > 63 {
		return ErrCorrupt
	}
	*c = persistedCycle(decoded.plain)
	c.EntryIndex = *decoded.EntryIndex
	return nil
}

func (p *CornerPending) UnmarshalJSON(data []byte) error {
	type plain CornerPending
	var decoded struct {
		plain
		Previous *persistedCycle `json:"previous"`
		Proposed *persistedCycle `json:"proposed"`
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&decoded); err != nil {
		return err
	}
	*p = CornerPending(decoded.plain)
	p.Previous = (*api.ScreenCycleStatus)(decoded.Previous)
	p.Proposed = (*api.ScreenCycleStatus)(decoded.Proposed)
	return nil
}

func (r *DeliveryRecord) UnmarshalJSON(data []byte) error {
	type plain DeliveryRecord
	var decoded struct {
		plain
		Cycle *persistedCycle `json:"cycle"`
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&decoded); err != nil {
		return err
	}
	*r = DeliveryRecord(decoded.plain)
	r.Cycle = (*api.ScreenCycleStatus)(decoded.Cycle)
	return nil
}
