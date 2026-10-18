/* Copyright 2026 Nicolas Cassan. Licensed under the Apache License, Version 2.0. */

package matter

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"brique_engine/circulation"
	"brique_engine/shared"
)

const matterEventDirName = ".events"

type MatterPendingEvent struct {
	EventID   string              `json:"event_id"`
	SubID     string              `json:"sub_id"`
	MatterID  string              `json:"matter_id"`
	Target    circulation.Address `json:"target"`
	Params    map[string]any      `json:"params"`
	CreatedAt int64               `json:"created_at"`
}

type matterEventMarker struct {
	ID            string               `json:"id"`
	MatterID      string               `json:"matter_id"`
	Revision      int64                `json:"revision,omitempty"`
	Deleted       bool                 `json:"deleted,omitempty"`
	Params        map[string]any       `json:"params"`
	Subscriptions []MatterSubscription `json:"subscriptions"`
}

func (l *MatterLoop) matterEventDir() string {
	if l == nil || l.frame == nil || strings.TrimSpace(l.frame.ContextDir) == "" {
		return ""
	}
	return filepath.Join(l.frame.ContextDir, matterDirName, matterEventDirName)
}

func atomicWriteEventJSON(path string, value any) error {
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + tmpSuffix
	if err := os.WriteFile(tmp, encoded, 0o600); err != nil {
		return err
	}
	return shared.AtomicReplace(tmp, path)
}

func (l *MatterLoop) persistMatterSubscriptions() error {
	dir := l.matterEventDir()
	if dir == "" {
		return fmt.Errorf("missing matter event directory")
	}
	l.subsMu.RLock()
	durable := make([]MatterSubscription, 0)
	for _, byID := range l.subs {
		for _, subscription := range byID {
			if subscription.Durable {
				durable = append(durable, subscription)
			}
		}
	}
	l.subsMu.RUnlock()
	sort.Slice(durable, func(i, j int) bool {
		if durable[i].MatterID == durable[j].MatterID {
			return durable[i].ID < durable[j].ID
		}
		return durable[i].MatterID < durable[j].MatterID
	})
	return atomicWriteEventJSON(filepath.Join(dir, "subscriptions.json"), durable)
}

func (l *MatterLoop) persistMatterOutboxLocked() error {
	dir := l.matterEventDir()
	if dir == "" {
		return fmt.Errorf("missing matter event directory")
	}
	return atomicWriteEventJSON(filepath.Join(dir, "outbox.json"), l.outbox)
}

func (l *MatterLoop) loadMatterEventState() error {
	dir := l.matterEventDir()
	if dir == "" {
		return nil
	}
	var subscriptions []MatterSubscription
	if encoded, err := os.ReadFile(filepath.Join(dir, "subscriptions.json")); err == nil {
		if err := json.Unmarshal(encoded, &subscriptions); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	for _, subscription := range subscriptions {
		if subscription.Durable {
			l.addSub(subscription.MatterID, subscription)
		}
	}
	var pending []MatterPendingEvent
	if encoded, err := os.ReadFile(filepath.Join(dir, "outbox.json")); err == nil {
		if err := json.Unmarshal(encoded, &pending); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	l.outboxMu.Lock()
	l.outbox = pending
	l.outboxMu.Unlock()
	return nil
}

func (l *MatterLoop) enqueueDurableEventWithID(subscription MatterSubscription, params map[string]any, eventID string) error {
	cloned := make(map[string]any, len(params)+2)
	for key, value := range params {
		cloned[key] = value
	}
	cloned[circulation.KeyEventId] = eventID
	cloned[circulation.KeySubID] = subscription.ID
	pending := MatterPendingEvent{EventID: eventID, SubID: subscription.ID, MatterID: subscription.MatterID, Target: subscription.Target, Params: cloned, CreatedAt: time.Now().UnixNano()}
	l.outboxMu.Lock()
	for _, existing := range l.outbox {
		if existing.EventID == eventID && existing.SubID == subscription.ID {
			l.outboxMu.Unlock()
			return nil
		}
	}
	l.outbox = append(l.outbox, pending)
	err := l.persistMatterOutboxLocked()
	if err != nil {
		l.outbox = l.outbox[:len(l.outbox)-1]
	}
	shouldDeliver := true
	for index := 0; index < len(l.outbox)-1; index++ {
		if l.outbox[index].SubID == subscription.ID {
			shouldDeliver = false
			break
		}
	}
	l.outboxMu.Unlock()
	if err == nil && shouldDeliver {
		l.emitPendingMatterEvent(pending)
	}
	return err
}

func (l *MatterLoop) prepareMatterEventMarker(matterID string, revision int64, deleted bool, params map[string]any) (*matterEventMarker, error) {
	durable := make([]MatterSubscription, 0)
	for _, subscription := range l.listSubs(matterID) {
		if subscription.Durable {
			durable = append(durable, subscription)
		}
	}
	if len(durable) == 0 {
		return nil, nil
	}
	dir := l.matterEventDir()
	if dir == "" {
		return nil, fmt.Errorf("missing matter event directory")
	}
	marker := &matterEventMarker{ID: circulation.NewIntentionID(), MatterID: matterID, Revision: revision, Deleted: deleted, Params: params, Subscriptions: durable}
	if err := atomicWriteEventJSON(filepath.Join(dir, "event-"+marker.ID+".json"), marker); err != nil {
		return nil, err
	}
	return marker, nil
}

func (l *MatterLoop) completeMatterEventMarker(marker *matterEventMarker) error {
	if marker == nil {
		return nil
	}
	for _, subscription := range marker.Subscriptions {
		if err := l.enqueueDurableEventWithID(subscription, marker.Params, marker.ID+":"+subscription.ID); err != nil {
			return err
		}
	}
	return os.Remove(filepath.Join(l.matterEventDir(), "event-"+marker.ID+".json"))
}

func (l *MatterLoop) abandonMatterEventMarker(marker *matterEventMarker) {
	if marker == nil || l.matterEventDir() == "" {
		return
	}
	_ = os.Remove(filepath.Join(l.matterEventDir(), "event-"+marker.ID+".json"))
}

func (l *MatterLoop) reconcileMatterEventMarkers(removeUncommitted bool) error {
	dir := l.matterEventDir()
	if dir == "" {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), "event-") || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		encoded, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var marker matterEventMarker
		if err := json.Unmarshal(encoded, &marker); err != nil || marker.ID == "" || marker.MatterID == "" {
			return fmt.Errorf("invalid matter event marker %s", entry.Name())
		}
		descriptor := filepath.Join(l.frame.ContextDir, matterDirName, marker.MatterID+matterDescriptorSuffix)
		committed := false
		if marker.Deleted {
			_, statErr := os.Stat(descriptor)
			committed = os.IsNotExist(statErr)
		} else if doc, readErr := readJSONMapFile(descriptor); readErr == nil {
			if brique, _ := doc[circulation.KeyBrique].(map[string]any); brique != nil {
				committed = shared.AnyToInt64(brique[circulation.KeyRevision]) >= marker.Revision
			}
		}
		if !committed {
			if removeUncommitted {
				if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
					return err
				}
			}
			continue
		}
		if err := l.completeMatterEventMarker(&marker); err != nil {
			return err
		}
	}
	return nil
}

func (l *MatterLoop) emitPendingMatterEvent(event MatterPendingEvent) {
	l.emitToComm(circulation.Message{Kind: circulation.ValueKindIntention, Intention: circulation.Intention{
		IntentionID: event.EventID,
		To:          event.Target,
		From:        circulation.Address{Context: circulation.ContextID(l.frame.CtxId), Type: circulation.ValueTypeMatter, Cap: circulation.ValueCapMatterEvent},
		Params:      event.Params,
	}})
}

func (l *MatterLoop) emitMatterSubscriptionEvent(subscription MatterSubscription, params map[string]any) {
	if subscription.Durable {
		// Durable mutation events are staged before the Matter commit and
		// completed from their marker afterward (or during startup recovery).
		return
	}
	l.emitToComm(circulation.Message{Kind: circulation.ValueKindIntention, Intention: circulation.Intention{
		IntentionID: circulation.NewIntentionID(),
		To:          subscription.Target,
		From:        circulation.Address{Context: circulation.ContextID(l.frame.CtxId), Type: circulation.ValueTypeMatter, Cap: circulation.ValueCapMatterEvent},
		Params:      params,
	}})
}

func (l *MatterLoop) ackPendingMatterEvent(subID, eventID string) (bool, error) {
	l.outboxMu.Lock()
	original := append([]MatterPendingEvent(nil), l.outbox...)
	removed := false
	for index, event := range l.outbox {
		if event.SubID == subID && event.EventID == eventID {
			l.outbox = append(l.outbox[:index], l.outbox[index+1:]...)
			removed = true
			break
		}
	}
	if removed {
		if err := l.persistMatterOutboxLocked(); err != nil {
			l.outbox = original
			l.outboxMu.Unlock()
			return false, err
		}
	}
	var next *MatterPendingEvent
	if removed {
		for index := range l.outbox {
			if l.outbox[index].SubID == subID {
				copy := l.outbox[index]
				next = &copy
				break
			}
		}
	}
	l.outboxMu.Unlock()
	if next != nil {
		l.emitPendingMatterEvent(*next)
	}
	return removed, nil
}

func (l *MatterLoop) removePendingEventsForSubscription(subID string) error {
	l.outboxMu.Lock()
	original := append([]MatterPendingEvent(nil), l.outbox...)
	filtered := l.outbox[:0]
	for _, event := range l.outbox {
		if event.SubID != subID {
			filtered = append(filtered, event)
		}
	}
	l.outbox = filtered
	err := l.persistMatterOutboxLocked()
	if err != nil {
		l.outbox = original
	}
	l.outboxMu.Unlock()
	return err
}

func (l *MatterLoop) matterEventRetryLoop() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-l.done:
			return
		case <-ticker.C:
			// Keep pre-commit markers: a mutation may currently be between marker
			// persistence and its transaction commit. Completed markers whose first
			// outbox write transiently failed are retried here.
			_ = l.reconcileMatterEventMarkers(false)
			l.outboxMu.Lock()
			first := make(map[string]MatterPendingEvent)
			for _, event := range l.outbox {
				if _, exists := first[event.SubID]; !exists {
					first[event.SubID] = event
				}
			}
			l.outboxMu.Unlock()
			for _, event := range first {
				l.emitPendingMatterEvent(event)
			}
		}
	}
}
