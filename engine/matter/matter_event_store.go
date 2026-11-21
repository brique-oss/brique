/* Copyright 2026 Nicolas Cassan. Licensed under the Apache License, Version 2.0. */

package matter

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"brique_engine/engine/circulation"
	"brique_engine/engine/shared"
	"brique_engine/platform/filesystem"
)

const (
	matterEventDirName             = ".events"
	matterSubscriptionsStoreFormat = "brique.matter-subscriptions/2"
	matterOutboxStoreFormat        = "brique.matter-outbox/2"
	matterOutboxManifestFormat     = "brique.matter-outbox/3"
	matterOutboxEventFormat        = "brique.matter-outbox-event/3"
	matterOutboxDirName            = "outbox"
	matterEventMarkerFormat        = "brique.matter-event-marker/2"
	matterQuarantineFormat         = "brique.matter-event-quarantine/1"
)

var outboxFileNamePattern = regexp.MustCompile(`^[0-9]{20}\.json$`)

// Durable-event retention bounds. They are far above any healthy backlog; they
// exist so that a subscriber that never acknowledges cannot fill the disk.
var (
	maxOutboxPerSubscription = 1024
	maxOutboxTotal           = 16384
)

// defaultOutboxTTL is how long a durable event may wait for a subscriber that
// never acknowledges it before it is dropped and traced (D-19).
const defaultOutboxTTL = 7 * 24 * time.Hour

// expireOutbox drops the undelivered durable events older than the configured
// age and persists the result. It returns how many were dropped. A subscriber that
// later acknowledges an expired event is told it was not pending.
func (l *MatterLoop) expireOutbox(now time.Time) int {
	if l == nil || l.outboxTTL <= 0 {
		return 0
	}
	cutoff := now.Add(-l.outboxTTL).UnixNano()
	l.outboxMu.Lock()
	defer l.outboxMu.Unlock()
	expired := make([]MatterPendingEvent, 0)
	for _, event := range l.outbox {
		if event.CreatedAt > 0 && event.CreatedAt < cutoff {
			expired = append(expired, event)
		}
	}
	if len(expired) == 0 {
		return 0
	}
	removed, _ := l.removeOutboxEventsLocked(expired)
	for _, event := range removed {
		l.traceResponseError(event.EventID, "outbox_expired", "durable event dropped: it was not acknowledged in time")
	}
	return len(removed)
}

type MatterPendingEvent struct {
	EventID    string              `json:"event_id"`
	SubID      string              `json:"sub_id"`
	MatterID   string              `json:"matter_id"`
	Target     circulation.Address `json:"target"`
	Params     map[string]any      `json:"params"`
	CreatedAt  int64               `json:"created_at"`
	Owner      string              `json:"owner,omitempty"`
	OwnerActor string              `json:"owner_actor,omitempty"`

	// seq is the position of the event in the durable outbox: the name of its file and the order
	// of delivery within a subscription. It is not part of the event and never leaves the engine.
	seq int64
}

// matterOutboxManifest replaces outbox.json from format /3 on. It holds no event: the events are
// one file each under outbox/, so adding or acknowledging one costs one small file, whatever the
// backlog. An engine that only knows /2 refuses this manifest instead of reading it as an empty
// outbox, which is what keeps the downgrade controlled (INV-11).
type matterOutboxManifest struct {
	Format    string `json:"format"`
	Directory string `json:"directory"`
}

type matterOutboxEventFile struct {
	Format string             `json:"format"`
	Seq    int64              `json:"seq"`
	Event  MatterPendingEvent `json:"event"`
}

type matterSubscriptionsStore struct {
	Format        string               `json:"format"`
	Subscriptions []MatterSubscription `json:"subscriptions"`
}

type matterOutboxStore struct {
	Format string               `json:"format"`
	Events []MatterPendingEvent `json:"events"`
}

type matterEventQuarantine struct {
	Format        string               `json:"format"`
	CreatedAt     int64                `json:"created_at"`
	Reason        string               `json:"reason"`
	Subscriptions []MatterSubscription `json:"subscriptions,omitempty"`
	Events        []MatterPendingEvent `json:"events,omitempty"`
	Markers       []matterEventMarker  `json:"markers,omitempty"`
}

type matterEventMarker struct {
	Format        string               `json:"format"`
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
	return filesystem.WriteFileAtomic(path, encoded, 0o600)
}

func (l *MatterLoop) persistMatterSubscriptions() error {
	dir := l.matterEventDir()
	if dir == "" {
		return fmt.Errorf("missing matter event directory")
	}
	l.eventStoreMu.Lock()
	defer l.eventStoreMu.Unlock()
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
	return atomicWriteEventJSON(filepath.Join(dir, "subscriptions.json"), matterSubscriptionsStore{
		Format: matterSubscriptionsStoreFormat, Subscriptions: durable,
	})
}

// outboxDirectory is the directory of the one-file-per-event outbox of this Context.
func (l *MatterLoop) outboxDirectory() string {
	dir := l.matterEventDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, matterOutboxDirName)
}

func outboxIdentity(event MatterPendingEvent) string {
	return event.MatterID + "\x00" + event.SubID + "\x00" + event.EventID
}

func outboxEventFileName(seq int64) string { return fmt.Sprintf("%020d.json", seq) }

// ensureOutboxManifestLocked publishes the /3 manifest before the first event file exists, so that
// no event can sit in a directory an older engine would ignore. Callers hold outboxMu.
func (l *MatterLoop) ensureOutboxManifestLocked() error {
	if l.outboxManifested {
		return nil
	}
	dir := l.matterEventDir()
	if dir == "" {
		return fmt.Errorf("missing matter event directory")
	}
	if err := os.MkdirAll(filepath.Join(dir, matterOutboxDirName), 0o700); err != nil {
		return err
	}
	if err := atomicWriteEventJSON(filepath.Join(dir, "outbox.json"), matterOutboxManifest{
		Format: matterOutboxManifestFormat, Directory: matterOutboxDirName}); err != nil {
		return err
	}
	l.outboxManifested = true
	return nil
}

// persistOutboxAddLocked writes one event file and gives the event its position. It costs one small
// atomic write whatever the size of the backlog. Callers hold outboxMu.
func (l *MatterLoop) persistOutboxAddLocked(event *MatterPendingEvent) error {
	if err := l.ensureOutboxManifestLocked(); err != nil {
		return err
	}
	if l.outboxNextSeq < 1 {
		l.outboxNextSeq = 1
	}
	seq := l.outboxNextSeq
	if err := atomicWriteEventJSON(filepath.Join(l.outboxDirectory(), outboxEventFileName(seq)), matterOutboxEventFile{
		Format: matterOutboxEventFormat, Seq: seq, Event: *event}); err != nil {
		return err
	}
	event.seq = seq
	l.outboxNextSeq = seq + 1
	return nil
}

// removeOutboxEventsLocked deletes the files of events, then drops them from memory. It stops at the
// first failure and returns the events actually removed, so memory never claims more than the disk.
// A file already gone counts as removed. After a crash a deleted-but-unsynced file may come back: the
// subscriber then receives that event once more, never loses one. Callers hold outboxMu.
func (l *MatterLoop) removeOutboxEventsLocked(events []MatterPendingEvent) ([]MatterPendingEvent, error) {
	directory := l.outboxDirectory()
	removed := make([]MatterPendingEvent, 0, len(events))
	var firstErr error
	for _, event := range events {
		if directory == "" {
			firstErr = fmt.Errorf("missing matter event directory")
			break
		}
		if err := os.Remove(filepath.Join(directory, outboxEventFileName(event.seq))); err != nil && !os.IsNotExist(err) {
			firstErr = err
			break
		}
		removed = append(removed, event)
	}
	if len(removed) != 0 {
		gone := make(map[string]struct{}, len(removed))
		for _, event := range removed {
			gone[outboxIdentity(event)] = struct{}{}
		}
		kept := l.outbox[:0]
		for _, event := range l.outbox {
			if _, drop := gone[outboxIdentity(event)]; !drop {
				kept = append(kept, event)
			}
		}
		l.outbox = kept
		filesystem.SyncDir(directory)
	}
	return removed, firstErr
}

func (l *MatterLoop) loadMatterEventState() error {
	dir := l.matterEventDir()
	if dir == "" {
		return nil
	}
	subscriptions, legacySubscriptions, err := readMatterSubscriptions(filepath.Join(dir, "subscriptions.json"))
	if err != nil {
		return err
	}
	validSubscriptions, rejectedSubscriptions := validateStoredSubscriptions(subscriptions)
	for _, subscription := range validSubscriptions {
		l.addSub(subscription.MatterID, subscription)
	}
	events, migrate, legacyOutbox, err := l.readOutboxState(dir)
	if err != nil {
		return err
	}
	validEvents, rejectedEvents := validateStoredEvents(events, validSubscriptions, legacyOutbox)
	l.outboxMu.Lock()
	l.outbox = validEvents
	l.outboxMu.Unlock()
	if len(rejectedSubscriptions) != 0 || len(rejectedEvents) != 0 {
		if err := l.quarantineMatterEventState(rejectedSubscriptions, rejectedEvents); err != nil {
			return err
		}
	}
	// Re-publish a legacy array, or a file from which ambiguous records were
	// removed, in the versioned format. Subscriptions go first: after a crash a
	// legacy outbox can still derive its owner from the new subscription store.
	if legacySubscriptions || len(rejectedSubscriptions) != 0 {
		if err := l.persistMatterSubscriptions(); err != nil {
			return err
		}
	}
	l.outboxMu.Lock()
	defer l.outboxMu.Unlock()
	if migrate {
		return l.migrateOutboxLocked(dir)
	}
	if len(rejectedEvents) != 0 {
		// Quarantined events are now in the quarantine file; their files leave the active outbox.
		_, err := l.removeOutboxEventsLocked(rejectedEvents)
		return err
	}
	return nil
}

// readOutboxState returns the events found on disk. migrate is true when they come from a /2 file
// or a legacy array and must be rewritten as one file per event; legacy is true for the bare array.
func (l *MatterLoop) readOutboxState(dir string) (events []MatterPendingEvent, migrate, legacy bool, err error) {
	encoded, readErr := os.ReadFile(filepath.Join(dir, "outbox.json"))
	if os.IsNotExist(readErr) {
		if _, statErr := os.Stat(filepath.Join(dir, matterOutboxDirName)); statErr == nil {
			// The manifest is written before the first event file, so a directory without it is not
			// something this engine produces: refuse rather than guess whose it is.
			return nil, false, false, fmt.Errorf("matter outbox directory without its manifest")
		}
		return nil, false, false, nil
	}
	if readErr != nil {
		return nil, false, false, readErr
	}
	trimmed := bytes.TrimSpace(encoded)
	if len(trimmed) != 0 && trimmed[0] == '[' {
		pending, isLegacy, err := readMatterOutbox(filepath.Join(dir, "outbox.json"))
		return pending, true, isLegacy, err
	}
	var header struct {
		Format string `json:"format"`
	}
	if err := json.Unmarshal(encoded, &header); err != nil {
		return nil, false, false, err
	}
	if header.Format == matterOutboxStoreFormat {
		pending, isLegacy, err := readMatterOutbox(filepath.Join(dir, "outbox.json"))
		return pending, true, isLegacy, err
	}
	if header.Format != matterOutboxManifestFormat {
		return nil, false, false, fmt.Errorf("unsupported matter outbox store format %q", header.Format)
	}
	var manifest matterOutboxManifest
	if err := decodeStrictEventJSON(encoded, &manifest); err != nil || manifest.Directory != matterOutboxDirName {
		return nil, false, false, fmt.Errorf("invalid matter outbox manifest")
	}
	l.outboxMu.Lock()
	l.outboxManifested = true
	l.outboxMu.Unlock()
	events, err = l.readOutboxEventFiles(filepath.Join(dir, matterOutboxDirName))
	return events, false, false, err
}

func (l *MatterLoop) readOutboxEventFiles(directory string) ([]MatterPendingEvent, error) {
	entries, err := os.ReadDir(directory)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	events := make([]MatterPendingEvent, 0, len(entries))
	var maxSeq int64
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".") {
			if strings.Contains(name, ".tmp-") {
				_ = os.Remove(filepath.Join(directory, name)) // a write interrupted before its rename
			}
			continue
		}
		if entry.IsDir() || !outboxFileNamePattern.MatchString(name) {
			continue
		}
		encoded, err := os.ReadFile(filepath.Join(directory, name))
		if err != nil {
			return nil, err
		}
		var file matterOutboxEventFile
		if err := decodeStrictEventJSON(encoded, &file); err != nil {
			return nil, fmt.Errorf("invalid matter outbox event %s: %w", name, err)
		}
		if file.Format != matterOutboxEventFormat || outboxEventFileName(file.Seq) != name {
			return nil, fmt.Errorf("unsupported or misplaced matter outbox event %s", name)
		}
		file.Event.seq = file.Seq
		events = append(events, file.Event)
		if file.Seq > maxSeq {
			maxSeq = file.Seq
		}
	}
	sort.Slice(events, func(i, j int) bool { return events[i].seq < events[j].seq })
	l.outboxMu.Lock()
	if maxSeq+1 > l.outboxNextSeq {
		l.outboxNextSeq = maxSeq + 1
	}
	l.outboxMu.Unlock()
	return events, nil
}

// migrateOutboxLocked rewrites the events held in memory as one file each, then publishes the /3
// manifest over the old file: that single atomic replace is the commit. Until it happens the /2 file
// stays the truth, so a crash anywhere before it only means the next start redoes this from scratch.
func (l *MatterLoop) migrateOutboxLocked(dir string) error {
	directory := filepath.Join(dir, matterOutboxDirName)
	if err := os.RemoveAll(directory); err != nil {
		return err
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	l.outboxNextSeq = 1
	for index := range l.outbox {
		seq := l.outboxNextSeq
		if err := atomicWriteEventJSON(filepath.Join(directory, outboxEventFileName(seq)), matterOutboxEventFile{
			Format: matterOutboxEventFormat, Seq: seq, Event: l.outbox[index]}); err != nil {
			return err
		}
		l.outbox[index].seq = seq
		l.outboxNextSeq = seq + 1
	}
	if err := atomicWriteEventJSON(filepath.Join(dir, "outbox.json"), matterOutboxManifest{
		Format: matterOutboxManifestFormat, Directory: matterOutboxDirName}); err != nil {
		return err
	}
	l.outboxManifested = true
	return nil
}

func decodeStrictEventJSON(encoded []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("trailing matter event state")
	}
	return nil
}

func readMatterSubscriptions(path string) ([]MatterSubscription, bool, error) {
	encoded, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if len(bytes.TrimSpace(encoded)) != 0 && bytes.TrimSpace(encoded)[0] == '[' {
		var legacy []MatterSubscription
		if err := decodeStrictEventJSON(encoded, &legacy); err != nil {
			return nil, true, err
		}
		return legacy, true, nil
	}
	var store matterSubscriptionsStore
	if err := decodeStrictEventJSON(encoded, &store); err != nil {
		return nil, false, err
	}
	if store.Format != matterSubscriptionsStoreFormat {
		return nil, false, fmt.Errorf("unsupported matter subscription store format %q", store.Format)
	}
	return store.Subscriptions, false, nil
}

func readMatterOutbox(path string) ([]MatterPendingEvent, bool, error) {
	encoded, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if len(bytes.TrimSpace(encoded)) != 0 && bytes.TrimSpace(encoded)[0] == '[' {
		var legacy []MatterPendingEvent
		if err := decodeStrictEventJSON(encoded, &legacy); err != nil {
			return nil, true, err
		}
		return legacy, true, nil
	}
	var store matterOutboxStore
	if err := decodeStrictEventJSON(encoded, &store); err != nil {
		return nil, false, err
	}
	if store.Format != matterOutboxStoreFormat {
		return nil, false, fmt.Errorf("unsupported matter outbox store format %q", store.Format)
	}
	return store.Events, false, nil
}

func subscriptionStoreKey(matterID, subID string) string { return matterID + "\x00" + subID }

func validStoredLocalTarget(value string) bool {
	return strings.HasPrefix(value, "/") && value != "/" && !strings.HasSuffix(value, "/") &&
		!strings.Contains(value, "//") && !strings.ContainsAny(value, "@\\\x00\r\n") && path.Clean(value) == value
}

func validStoredSubscription(subscription MatterSubscription) bool {
	if !subscription.Durable || strings.TrimSpace(subscription.MatterID) == "" || strings.TrimSpace(subscription.ID) == "" ||
		subscription.OwnerChannel != "" || subscription.OwnerChannelKind != "" || subscription.OwnerRoute != "" {
		return false
	}
	if subscription.Owner == "" {
		return subscription.OwnerActor == "" && validStoredLocalTarget(string(subscription.Target.Context))
	}
	if subscription.Owner == "public.web" || subscription.Owner == "local.ui" {
		return false
	}
	return externalTargetDeliverable(subscription.Owner, subscription.Target)
}

func validateStoredSubscriptions(subscriptions []MatterSubscription) ([]MatterSubscription, []MatterSubscription) {
	counts := make(map[string]int, len(subscriptions))
	for _, subscription := range subscriptions {
		counts[subscriptionStoreKey(subscription.MatterID, subscription.ID)]++
	}
	valid := make([]MatterSubscription, 0, len(subscriptions))
	rejected := make([]MatterSubscription, 0)
	for _, subscription := range subscriptions {
		if counts[subscriptionStoreKey(subscription.MatterID, subscription.ID)] != 1 || !validStoredSubscription(subscription) {
			rejected = append(rejected, subscription)
			continue
		}
		valid = append(valid, subscription)
	}
	return valid, rejected
}

func validateStoredEvents(events []MatterPendingEvent, subscriptions []MatterSubscription, legacy bool) ([]MatterPendingEvent, []MatterPendingEvent) {
	byKey := make(map[string]MatterSubscription, len(subscriptions))
	for _, subscription := range subscriptions {
		byKey[subscriptionStoreKey(subscription.MatterID, subscription.ID)] = subscription
	}
	seen := make(map[string]struct{}, len(events))
	valid := make([]MatterPendingEvent, 0, len(events))
	rejected := make([]MatterPendingEvent, 0)
	for _, event := range events {
		subscription, exists := byKey[subscriptionStoreKey(event.MatterID, event.SubID)]
		identity := event.MatterID + "\x00" + event.SubID + "\x00" + event.EventID
		_, duplicate := seen[identity]
		if !exists || duplicate || strings.TrimSpace(event.EventID) == "" || event.Target != subscription.Target {
			rejected = append(rejected, event)
			continue
		}
		if legacy {
			event.Owner, event.OwnerActor = subscription.Owner, subscription.OwnerActor
		} else if event.Owner != subscription.Owner || event.OwnerActor != subscription.OwnerActor {
			rejected = append(rejected, event)
			continue
		}
		seen[identity] = struct{}{}
		valid = append(valid, event)
	}
	return valid, rejected
}

func (l *MatterLoop) quarantineMatterEventState(subscriptions []MatterSubscription, events []MatterPendingEvent, markers ...matterEventMarker) error {
	dir := l.matterEventDir()
	if dir == "" {
		return fmt.Errorf("missing matter event directory")
	}
	quarantine := matterEventQuarantine{Format: matterQuarantineFormat, Reason: "ambiguous_or_invalid_legacy_owner",
		Subscriptions: subscriptions, Events: events, Markers: markers}
	identity, err := json.Marshal(quarantine)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(identity)
	quarantine.CreatedAt = time.Now().UnixNano()
	return atomicWriteEventJSON(filepath.Join(dir, fmt.Sprintf("quarantine-%x.json", digest[:12])), quarantine)
}

func (l *MatterLoop) enqueueDurableEventWithID(subscription MatterSubscription, params map[string]any, eventID string) error {
	if !l.subscriptionStillAuthorized(subscription) {
		l.endSubscription(subscription)
		return nil
	}
	cloned := make(map[string]any, len(params)+2)
	for key, value := range eventParamsFor(subscription, params) {
		cloned[key] = value
	}
	cloned[circulation.KeyEventId] = eventID
	cloned[circulation.KeySubID] = subscription.ID
	pending := MatterPendingEvent{EventID: eventID, SubID: subscription.ID, MatterID: subscription.MatterID,
		Target: subscription.Target, Params: cloned, CreatedAt: time.Now().UnixNano(),
		Owner: subscription.Owner, OwnerActor: subscription.OwnerActor}
	// The outbox is written to a file under its lock: a deferred Unlock keeps a
	// panic of that code from leaving the lock taken.
	overflow := false
	duplicate, shouldDeliver, err := func() (bool, bool, error) {
		l.outboxMu.Lock()
		defer l.outboxMu.Unlock()
		pendingForSubscription := 0
		for _, existing := range l.outbox {
			if existing.EventID == eventID && existing.SubID == subscription.ID {
				return true, false, nil
			}
			if existing.SubID == subscription.ID {
				pendingForSubscription++
			}
		}
		// An unreachable subscriber must not grow the store without bound (D-19).
		// The event in flight at the head is never disturbed: the new one is dropped
		// and the drop is traced below.
		if pendingForSubscription >= maxOutboxPerSubscription || len(l.outbox) >= maxOutboxTotal {
			overflow = true
			return false, false, nil
		}
		if err := l.persistOutboxAddLocked(&pending); err != nil {
			return false, false, err
		}
		l.outbox = append(l.outbox, pending)
		var err error
		shouldDeliver := true
		for index := 0; index < len(l.outbox)-1; index++ {
			if l.outbox[index].SubID == subscription.ID {
				shouldDeliver = false
				break
			}
		}
		return false, shouldDeliver, err
	}()
	if duplicate {
		return nil
	}
	if overflow {
		l.traceResponseError(eventID, "outbox_full", "durable event dropped: the subscription outbox is full")
		return nil
	}
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
	marker := &matterEventMarker{Format: matterEventMarkerFormat, ID: circulation.NewIntentionID(), MatterID: matterID,
		Revision: revision, Deleted: deleted, Params: params, Subscriptions: durable}
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
		if err := decodeStrictEventJSON(encoded, &marker); err != nil || marker.ID == "" || marker.MatterID == "" {
			return fmt.Errorf("invalid matter event marker %s", entry.Name())
		}
		legacy := marker.Format == ""
		if !legacy && marker.Format != matterEventMarkerFormat {
			return fmt.Errorf("unsupported matter event marker format %q", marker.Format)
		}
		validSubscriptions, rejectedSubscriptions := validateStoredSubscriptions(marker.Subscriptions)
		if len(rejectedSubscriptions) != 0 || len(validSubscriptions) != len(marker.Subscriptions) {
			if err := l.quarantineMatterEventState(nil, nil, marker); err != nil {
				return err
			}
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				return err
			}
			continue
		}
		marker.Subscriptions = validSubscriptions
		if legacy {
			marker.Format = matterEventMarkerFormat
			if err := atomicWriteEventJSON(path, marker); err != nil {
				return err
			}
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
	subscription, exists := l.getSub(event.MatterID, event.SubID)
	if exists && !l.subscriptionStillAuthorized(subscription) {
		l.endSubscription(subscription)
		return
	}
	if !exists && event.MatterID != "" && event.SubID != "" && l.hasSubscriptionTable() {
		// The subscription was removed (revocation, unsubscribe): an event queued
		// before that must not be delivered afterwards.
		return
	}
	l.emitToComm(circulation.Message{Kind: circulation.ValueKindIntention, Intention: circulation.Intention{
		IntentionID: event.EventID,
		To:          event.Target,
		From:        circulation.Address{Context: circulation.ContextID(l.frame.CtxId), Type: circulation.ValueTypeMatter, Cap: circulation.ValueCapMatterEvent},
		Params:      event.Params,
	}})
}

func (l *MatterLoop) emitMatterSubscriptionEvent(subscription MatterSubscription, params map[string]any) {
	if !l.subscriptionStillAuthorized(subscription) {
		l.endSubscription(subscription)
		return
	}
	if subscription.Durable {
		// Durable mutation events are staged before the Matter commit and
		// completed from their marker afterward (or during startup recovery).
		return
	}
	message := circulation.Message{Kind: circulation.ValueKindIntention, Intention: circulation.Intention{
		IntentionID: circulation.NewIntentionID(),
		To:          subscription.Target,
		From:        circulation.Address{Context: circulation.ContextID(l.frame.CtxId), Type: circulation.ValueTypeMatter, Cap: circulation.ValueCapMatterEvent},
		Params:      withSubscriptionID(eventParamsFor(subscription, params), subscription.ID),
	}}
	if subscription.OwnerRoute != "" {
		l.emitToCommOnRoute(message, subscription.OwnerRoute)
		return
	}
	l.emitToComm(message)
}

// withSubscriptionID returns a copy of params carrying the id of the subscription
// the event is delivered for (durable events already carry it). Without it a
// client following one Matter under several keys cannot tell which of its
// subscriptions an event belongs to. The input map is never modified: it may be
// shared by the events of several subscriptions.
func withSubscriptionID(params map[string]any, subID string) map[string]any {
	cloned := make(map[string]any, len(params)+1)
	for key, value := range params {
		cloned[key] = value
	}
	if subID != "" {
		cloned[circulation.KeySubID] = subID
	}
	return cloned
}

func (l *MatterLoop) ackPendingMatterEvent(subID, eventID string) (bool, error) {
	removed, next, err := l.ackOutbox(subID, eventID)
	if err != nil {
		return false, err
	}
	if next != nil {
		l.emitPendingMatterEvent(*next)
	}
	return removed, nil
}

// ackOutbox removes an acknowledged event from the outbox and persists it, under
// the outbox lock (released by defer, whatever happens in the file write). It
// returns the next pending event of the subscription, if any, for the caller to
// emit once the lock is released.
func (l *MatterLoop) ackOutbox(subID, eventID string) (removed bool, next *MatterPendingEvent, err error) {
	l.outboxMu.Lock()
	defer l.outboxMu.Unlock()
	var target *MatterPendingEvent
	for index := range l.outbox {
		if l.outbox[index].SubID == subID && l.outbox[index].EventID == eventID {
			copy := l.outbox[index]
			target = &copy
			break
		}
	}
	if target == nil {
		return false, nil, nil
	}
	if gone, err := l.removeOutboxEventsLocked([]MatterPendingEvent{*target}); err != nil || len(gone) == 0 {
		if err == nil {
			err = fmt.Errorf("matter outbox event could not be removed")
		}
		return false, nil, err
	}
	for index := range l.outbox {
		if l.outbox[index].SubID == subID {
			copy := l.outbox[index]
			next = &copy
			break
		}
	}
	return true, next, nil
}

func (l *MatterLoop) removePendingEventsForSubscription(subID string) error {
	l.outboxMu.Lock()
	defer l.outboxMu.Unlock()
	doomed := make([]MatterPendingEvent, 0)
	for _, event := range l.outbox {
		if event.SubID == subID {
			doomed = append(doomed, event)
		}
	}
	if len(doomed) == 0 {
		return nil
	}
	_, err := l.removeOutboxEventsLocked(doomed)
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
			l.expireOutbox(time.Now())
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
