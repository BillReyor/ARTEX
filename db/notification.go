package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/Autumn-27/artex/notify"
)

// This document is the channel configuration and event layer for IM push. Delivery task collection and status flow
// db/notification_delivery.go。
//
// Two invariants must be maintained when changing this file:
//
// 1. The transaction that writes the vulnerability (RecordFindingTx) only calls InsertNotificationEventTx to perform a blind insertion.
// Do not read any notification-related tables or perform filtering and matching. Any read operations introduced here may be caused by
// The user mismatches the filter conditions and contaminates or even aborts the vulnerable write transaction.
// 2. Filter matching will never report an error: configuration malformations will be treated as "hits" (see notify.Match). I'd rather push more,
// Don't miss it.

// ErrNotificationChannelNotFound Channel does not exist.
var ErrNotificationChannelNotFound = errors.New("Notification channel does not exist")

// Delivery status.
const (
	NotifyStatePending = "pending" // Ready to go
	NotifyStateSending = "sending" // Has been claimed by a dispatcher and the lease has not expired
	NotifyStateSent    = "sent"    // Delivered
	NotifyStateFailed  = "failed"  // Retries are exhausted or permanent failure, you can manually resend
	NotifyStateSkipped = "skipped" // The channel has been deactivated and will no longer be sent.
)

// Push mode.
const (
	NotifyModeRealtime = "realtime"
	NotifyModeDigest   = "digest"
)

// ValidNotifyMode whitelist verification push mode (same as findings.status: no need for DB CHECK,
// to facilitate subsequent expansion).
func ValidNotifyMode(m string) bool {
	return m == NotifyModeRealtime || m == NotifyModeDigest
}

// NotificationChannel is a channel instance configuration. Config and Filter keep the original JSON,
// Parsing is left to the notify package - the db layer does not understand the meaning of their fields.
type NotificationChannel struct {
	ID     int64           `json:"id"`
	Name   string          `json:"name"`
	Kind   string          `json:"kind"`
	Mode   string          `json:"mode"`
	Config json.RawMessage `json:"config"`
	Filter json.RawMessage `json:"filter"`
	// Enabled uses pointers to distinguish between "not passing this field" and "explicitly passing false"——
	// The front-end switch control only submits the changed fields.
	Enabled    *bool     `json:"enabled,omitempty"`
	RatePerMin int       `json:"rate_per_min"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// IsEnabled returns whether the channel is enabled; when Enabled is nil (not loaded), it is treated as enabled.
func (c *NotificationChannel) IsEnabled() bool { return c.Enabled == nil || *c.Enabled }

// NotificationEvent is an event fact.
type NotificationEvent struct {
	ID        int64           `json:"id"`
	Kind      string          `json:"kind"`
	FindingID int64           `json:"finding_id"`
	Snapshot  json.RawMessage `json:"snapshot"`
	CreatedAt time.Time       `json:"created_at"`
}

const notificationChannelCols = `id, name, kind, enabled, config, mode, filter, rate_per_min, created_at, updated_at`

func scanNotificationChannel(sc interface{ Scan(...any) error }) (*NotificationChannel, error) {
	var c NotificationChannel
	var enabled bool
	if err := sc.Scan(&c.ID, &c.Name, &c.Kind, &enabled, &c.Config, &c.Mode, &c.Filter, &c.RatePerMin, &c.CreatedAt, &c.UpdatedAt); err != nil {
		return nil, err
	}
	c.Enabled = &enabled
	return &c, nil
}

// ListNotificationChannels returns all channel instances, with enabled ones listed first and peers by id.
// Sorting is placed in SQL to allow the UI and dispatcher to see the same stable order.
func (d *DB) ListNotificationChannels(ctx context.Context) ([]*NotificationChannel, error) {
	rows, err := d.QueryContext(ctx, `SELECT `+notificationChannelCols+` FROM notification_channels
ORDER BY enabled DESC, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*NotificationChannel{}
	for rows.Next() {
		c, err := scanNotificationChannel(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// NotificationChannelByID takes a single channel.
func (d *DB) NotificationChannelByID(ctx context.Context, id int64) (*NotificationChannel, error) {
	row := d.QueryRowContext(ctx, `SELECT `+notificationChannelCols+` FROM notification_channels WHERE id=$1`, id)
	c, err := scanNotificationChannel(row)
	if err == sql.ErrNoRows {
		return nil, ErrNotificationChannelNotFound
	}
	return c, err
}

// SaveNotificationChannel creates or updates a channel.
//
// When updating, only the fields explicitly given by the caller (non-nil / non-empty) are overwritten, so that the front end can submit partial
// The modified drawer form does not have to return the fields in the config that it does not display - passing back will cause
// "The mask value overwrites the real key" accident.
func (d *DB) SaveNotificationChannel(ctx context.Context, c *NotificationChannel) (int64, error) {
	if c.Mode == "" {
		c.Mode = NotifyModeRealtime
	}
	// Here we deliberately do not do any processing to 0: 0 is a legal configuration, which means "no current limit".
	//
	// It was once written as `if c.RatePerMin <= 0 { c.RatePerMin = defaultRate }`, which originally meant "when unspecified
	// Give a safe default", but that "explicitly set to 0" is also swallowed - documentation, UI prompts and
	// takeTokens all interprets 0 as no limit, but here it is quietly changed to 20 (DingTalk/Qiwei/Telegram)
	// Or 100 (Feishu), the operator thought the current limit was released, but was actually stuck at 20/minute without any prompt.
	//
	// The difference between "unspecified" and "explicit 0" is only known by the caller (default fields in the request body vs. explicitly passing 0),
	// Therefore, the default value is filled in by the server layer when the field is defaulted, see notifyCreateChannel.
	if c.RatePerMin < 0 {
		return 0, errors.New("Rate limit value cannot be negative")
	}
	if c.Config == nil {
		c.Config = json.RawMessage(`{}`)
	}
	if c.Filter == nil {
		c.Filter = json.RawMessage(`{}`)
	}
	enabled := c.IsEnabled()

	if c.ID == 0 {
		var id int64
		err := d.QueryRowContext(ctx, `INSERT INTO notification_channels(name,kind,enabled,config,mode,filter,rate_per_min)
VALUES($1,$2,$3,$4,$5,$6,$7) RETURNING id`,
			c.Name, c.Kind, enabled, string(c.Config), c.Mode, string(c.Filter), c.RatePerMin).Scan(&id)
		return id, err
	}
	res, err := d.ExecContext(ctx, `UPDATE notification_channels
SET name=$2, kind=$3, enabled=$4, config=$5, mode=$6, filter=$7, rate_per_min=$8
WHERE id=$1`,
		c.ID, c.Name, c.Kind, enabled, string(c.Config), c.Mode, string(c.Filter), c.RatePerMin)
	if err != nil {
		return 0, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return 0, ErrNotificationChannelNotFound
	}
	return c.ID, nil
}

// SetNotificationChannelEnabled switches on and off.
//
// When deactivating a channel, mark its unsent deliveries as skipped: otherwise, after re-enabling
// You will suddenly receive a batch of old vulnerabilities "backlogged during the deactivation period", which have expired and are easily misjudged as new ones.
func (d *DB) SetNotificationChannelEnabled(ctx context.Context, id int64, enabled bool) error {
	return d.WithEvidenceTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE notification_channels SET enabled=$2 WHERE id=$1`, id, enabled)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotificationChannelNotFound
		}
		if !enabled {
			if _, err := tx.ExecContext(ctx, `UPDATE notification_deliveries SET state=$2, last_error=$3
WHERE channel_id=$1 AND state IN ($4,$5)`,
				id, NotifyStateSkipped, "Channel is disabled", NotifyStatePending, NotifyStateSending); err != nil {
				return err
			}
		}
		return nil
	})
}

// DeleteNotificationChannel deletes the channel. Its delivery history is deleted along with the foreign key cascade
// (The channel configuration is gone, and history cannot be interpreted).
func (d *DB) DeleteNotificationChannel(ctx context.Context, id int64) error {
	res, err := d.ExecContext(ctx, `DELETE FROM notification_channels WHERE id=$1`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotificationChannelNotFound
	}
	return nil
}

// RecordNotificationEventTx writes a push event on a **best effort** in the caller's transaction.
//
// This is the only notification-related change on the vulnerability write path: an INSERT, no table is read, no channel is recognized,
// No filtering is performed. Transaction submission ensures that "vulnerability is logged" and "push task exists" are atomically consistent.
// There is no window where the submission is successful but not queued, and the message is permanently lost.
//
// The two key designs were not written casually:
//
// 1. **Why use SAVEPOINT**: An error in any statement within a transaction in PostgreSQL will cause the entire transaction to enter
// aborted status, all subsequent statements (including COMMIT) will fail. So "Ignore this INSERT
// error and let the caller continue to submit" is not possible in PG - unless a savepoint is used to isolate the error in
// on this statement. Without save points, the only option left is "total rollback".
//
// 2. **Why the whole rollback is wrong**: Push is a convenience function, while vulnerability records are the product itself. a notice
// Table problems (old database not migrated, transient disk failure) should not prevent high-risk vulnerabilities from being stored in the database. So here is isolation
// Error, log, return false, and let the vulnerability write submit as usual - at the cost of losing this push.
// Returning bool instead of error is intentional: the caller should not treat it as an error that affects the success or failure of the write.
func RecordNotificationEventTx(ctx context.Context, tx *sql.Tx, kind string, findingID int64, snap notify.Snapshot) bool {
	raw, err := json.Marshal(snap)
	if err != nil {
		log.Printf("[notify] failed to serialize push event finding=%d: %v", findingID, err)
		return false
	}
	if _, err := tx.ExecContext(ctx, `SAVEPOINT notify_event`); err != nil {
		log.Printf("[notify] failed to create savepoint finding=%d: %v", findingID, err)
		return false
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO notification_events(kind,finding_id,snapshot) VALUES($1,$2,$3)`,
		kind, findingID, string(raw)); err != nil {
		log.Printf("[notify] failed to write push event finding=%d (finding record unaffected): %v", findingID, err)
		// Roll back to the savepoint to rescue the transaction from the aborted state.
		if _, rbErr := tx.ExecContext(ctx, `ROLLBACK TO SAVEPOINT notify_event`); rbErr != nil {
			log.Printf("[notify] failed to roll back to savepoint finding=%d: %v", findingID, rbErr)
		}
		return false
	}
	// Release save points to avoid accumulation of useless save points in long transactions.
	_, _ = tx.ExecContext(ctx, `RELEASE SAVEPOINT notify_event`)
	return true
}

// AddNotificationEvent is a transaction-independent version of InsertNotificationEventTx that is not
// Use call points in existing transactions (such as the channel's "Send Test Message", which has no actual finding).
func (d *DB) AddNotificationEvent(ctx context.Context, kind string, findingID int64, snap notify.Snapshot) (int64, error) {
	raw, err := json.Marshal(snap)
	if err != nil {
		return 0, fmt.Errorf("Failed to serialize notification event snapshot: %w", err)
	}
	var id int64
	err = d.QueryRowContext(ctx, `INSERT INTO notification_events(kind,finding_id,snapshot) VALUES($1,$2,$3) RETURNING id`,
		kind, findingID, string(raw)).Scan(&id)
	return id, err
}

// FanOutPendingEvents expands undispatched vulnerability events into delivery tasks according to the currently enabled channels.
// Returns the number of events processed this round and the number of new deliveries.
//
// The entire round of operations is in one transaction: the event is received using FOR UPDATE SKIP LOCKED, and multiple processes run at the same time.
// They also received different rows (the archive queue in the project uses the same method, see
// completeNextArchiveJob of db/task_archives.go).
//
// Filter matching is deliberately placed on the Go side rather than SQL: the filter condition of the channel is a JSONB of optional fields,
// Using SQL to express the matching of six combinations will make the query difficult to maintain, and the number of channels is "a few manually allocated".
// After fully loading, comparing items one by one in memory is faster and more testable.
//
// Events that do not hit any channel will also be marked fanned_out - otherwise it will remain in the pending collection forever,
// Each tick is rescanned.
func (d *DB) FanOutPendingEvents(ctx context.Context, limit int) (eventCount, deliveryCount int, err error) {
	if limit <= 0 {
		limit = 200
	}
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback() // nolint:errcheck // no-op after successful submission

	channels, err := listEnabledNotificationChannelsTx(ctx, tx)
	if err != nil {
		return 0, 0, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id, kind, finding_id, snapshot FROM notification_events
WHERE NOT fanned_out ORDER BY id FOR UPDATE SKIP LOCKED LIMIT $1`, limit)
	if err != nil {
		return 0, 0, err
	}
	var (
		events      []NotificationEvent
		parsedSnaps []notify.Snapshot
	)
	for rows.Next() {
		var ev NotificationEvent
		if err := rows.Scan(&ev.ID, &ev.Kind, &ev.FindingID, &ev.Snapshot); err != nil {
			rows.Close()
			return 0, 0, err
		}
		var snap notify.Snapshot
		// The snapshot is written by ourselves and must be parsable in theory; failure to parse will not block the delivery process.
		// But this event will be skipped by all channels with filter conditions because the fields are all empty - I would rather push one less
		// Don't let one bad row block the entire queue.
		_ = json.Unmarshal(ev.Snapshot, &snap)
		// kind is based on the inline value: the copy in the snapshot is the copy used for rendering and may have been written by an older version.
		snap.Kind = ev.Kind
		events = append(events, ev)
		parsedSnaps = append(parsedSnaps, snap)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, 0, err
	}
	if len(events) == 0 {
		return 0, 0, tx.Commit()
	}

	type pending struct {
		eventID   int64
		channelID int64
	}
	var toInsert []pending
	for i, snap := range parsedSnaps {
		for _, ch := range channels {
			if !notify.Match(notify.ParseFilter(ch.Filter), snap) {
				continue
			}
			toInsert = append(toInsert, pending{eventID: events[i].ID, channelID: ch.ID})
		}
	}
	if len(toInsert) > 0 {
		var (
			vals []string
			args []any
		)
		for _, p := range toInsert {
			vals = append(vals, fmt.Sprintf("($%d,$%d)", len(args)+1, len(args)+2))
			args = append(args, p.eventID, p.channelID)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO notification_deliveries(event_id,channel_id) VALUES `+strings.Join(vals, ","), args...); err != nil {
			return 0, 0, err
		}
	}

	// Mark this round of events as dispatched. Events that do not hit any channel are also marked together (see function comments).
	ids := make([]string, 0, len(events))
	markArgs := make([]any, 0, len(events))
	for _, ev := range events {
		markArgs = append(markArgs, ev.ID)
		ids = append(ids, fmt.Sprintf("$%d", len(markArgs)))
	}
	if _, err := tx.ExecContext(ctx, `UPDATE notification_events SET fanned_out=true WHERE id IN (`+strings.Join(ids, ",")+`)`, markArgs...); err != nil {
		return 0, 0, err
	}
	return len(events), len(toInsert), tx.Commit()
}

// listEnabledNotificationChannelsTx Gets the enabled channels in the transaction. The quantity is very small,
// No paging and no caching - caching will introduce an additional timing issue of "when will the changed configuration take effect?"
func listEnabledNotificationChannelsTx(ctx context.Context, tx *sql.Tx) ([]*NotificationChannel, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id, name, kind, config, mode, filter, rate_per_min
FROM notification_channels WHERE enabled ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*NotificationChannel{}
	for rows.Next() {
		var c NotificationChannel
		if err := rows.Scan(&c.ID, &c.Name, &c.Kind, &c.Config, &c.Mode, &c.Filter, &c.RatePerMin); err != nil {
			return nil, err
		}
		out = append(out, &c)
	}
	return out, rows.Err()
}

// NotificationAssetNames parses asset ids into short display names for use in push messages.
//
// The return order is consistent with the input parameters, and the length may be smaller than the input parameters (non-existent ids are skipped). Keep the order of input parameters as
// In order to ensure that the asset sequence of the same vulnerability message is stable in multiple deliveries - otherwise in the message received after retrying
// If the order of assets has changed, it will be misinterpreted as "the assets have changed."
func (d *DB) NotificationAssetNames(ctx context.Context, ids []int64) ([]string, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	ph, args := placeholders(1, ids)
	rows, err := d.QueryContext(ctx, `SELECT id, type, domain, ip, url, app_name, bundle_id FROM assets WHERE id IN (`+ph+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	labels := map[int64]string{}
	for rows.Next() {
		var (
			id                int64
			typ               string
			domain, ip, url   sql.NullString
			appName, bundleID sql.NullString
		)
		if err := rows.Scan(&id, &typ, &domain, &ip, &url, &appName, &bundleID); err != nil {
			return nil, err
		}
		labels[id] = assetDisplayName(typ, domain.String, ip.String, url.String, appName.String, bundleID.String)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(ids))
	seen := map[int64]bool{}
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		if label, ok := labels[id]; ok && label != "" {
			out = append(out, label)
		}
	}
	return out, nil
}

// assetDisplayName selects the most identifiable identifier by asset type.
// The empty string is returned, and the caller decides how to present the "assets whose names cannot be retrieved" - this function does not create placeholders.
// Otherwise, noise like "Asset #42" will be mixed into the push message, and readers will think it is a real domain name.
func assetDisplayName(typ, domain, ip, url, appName, bundleID string) string {
	pick := func(vals ...string) string {
		for _, v := range vals {
			if strings.TrimSpace(v) != "" {
				return v
			}
		}
		return ""
	}
	switch typ {
	case "root_domain", "subdomain":
		return domain
	case "ip":
		return ip
	case "app":
		return pick(appName, bundleID)
	case "service", "endpoint":
		return pick(url, domain, ip)
	default:
		return pick(domain, ip, url, appName)
	}
}

// SetFindingStatusWithNotify updates the vulnerability handling status and registers a status change in the same transaction
// Push events.
//
// Return from=the state before the change; found=whether the vulnerability exists; notified=whether the event was successfully registered.
//
// Three deliberate actions:
// - Events are not registered when the status has not actually changed. The front-end drawer repeatedly submits the same value or automated scripts
// Idempotent replay should not produce push noise.
// - If the vulnerability does not exist, return found=false and do not write anything, which will be translated into 404 by the caller.
// - Event registration failure does not affect status update (see the save point description of RecordNotificationEventTx),
// Therefore, when notified=false, the status has been changed successfully, and the caller should not report an error because of this.
func (d *DB) SetFindingStatusWithNotify(ctx context.Context, id int64, status string) (from string, found bool, notified bool, err error) {
	err = d.WithEvidenceTx(ctx, func(tx *sql.Tx) error {
		var txErr error
		from, found, _, notified, txErr = SetFindingStatusTx(ctx, tx, id, status)
		return txErr
	})
	return from, found, notified, err
}

// SetFindingStatusTx updates the vulnerability status within the caller's transaction and registers status change push events.
//
// The purpose of extracting transaction-level functions is to make all paths that change state share the same set of semantics - previously only
// patchFinding The version of the tape transport notification, and **when the retest conclusion is "fixed"** (finding_retests
// The one in `UPDATE findings SET status=...`) is written directly into the library, so it is configured
// `on_status_change`’s channel cannot receive push notifications at all for this type of status transfer: the status on the interface has quietly changed.
// Operation and maintenance won’t be discovered until the platform is opened.
//
// Return from=status before change, found=whether the vulnerability exists, changed=whether the status has really changed,
// notified=Whether the event is successfully registered (failure to register does not affect status update, see RecordNotificationEventTx).
func SetFindingStatusTx(ctx context.Context, tx *sql.Tx, id int64, status string) (from string, found bool, changed bool, notified bool, err error) {
	var (
		vulnclass, name, severity, summary string
		taskID                             sql.NullInt64
		assetIDs                           []byte
	)
	scanErr := tx.QueryRowContext(ctx, `SELECT vulnclass, name, severity, summary, task_id, asset_ids, status
FROM findings WHERE id=$1 FOR UPDATE`, id).
		Scan(&vulnclass, &name, &severity, &summary, &taskID, &assetIDs, &from)
	if scanErr == sql.ErrNoRows {
		return "", false, false, false, nil
	}
	if scanErr != nil {
		return "", false, false, false, scanErr
	}
	found = true
	if from == status {
		// Events will not be registered if the state does not really change: repeatedly submitting the same value and idempotent replay are not allowed.
		// Produces push noise.
		return from, true, false, false, nil
	}
	if _, err := tx.ExecContext(ctx, `UPDATE findings SET status=$2 WHERE id=$1`, id, status); err != nil {
		return from, true, false, false, err
	}
	var assets []int64
	_ = json.Unmarshal(assetIDs, &assets)
	notified = RecordNotificationEventTx(ctx, tx, notify.EventFindingStatusChanged, id, notify.Snapshot{
		Kind:       notify.EventFindingStatusChanged,
		FindingID:  id,
		TaskID:     taskID.Int64,
		VulnClass:  vulnclass,
		Name:       name,
		Severity:   severity,
		Summary:    summary,
		AssetIDs:   assets,
		FromStatus: from,
		ToStatus:   status,
	})
	return from, true, true, notified, nil
}

// NotificationStats is the overview count at the top of the notification page.
type NotificationStats struct {
	Channels     int   `json:"channels"`
	ChannelsOn   int   `json:"channels_on"`
	Pending      int   `json:"pending"`
	Failed       int   `json:"failed"`
	SentToday    int   `json:"sent_today"`
	BacklogAgeMS int64 `json:"backlog_age_ms"` // The number of milliseconds since the oldest pending delivery
}

// NotificationStatsSnapshot summarizes the health of the notification system.
// BacklogAgeMS is the most direct indicator of "whether the push is stuck" - much more useful than the pending count.
// Because the difference between a backlog of 3 items and a backlog of 3 items can be from 3 seconds to 3 hours.
func (d *DB) NotificationStatsSnapshot(ctx context.Context) (*NotificationStats, error) {
	var s NotificationStats
	if err := d.QueryRowContext(ctx, `SELECT
    (SELECT count(*) FROM notification_channels),
    (SELECT count(*) FROM notification_channels WHERE enabled),
    (SELECT count(*) FROM notification_deliveries WHERE state IN ($1,$2)),
    (SELECT count(*) FROM notification_deliveries WHERE state=$3),
    (SELECT count(*) FROM notification_deliveries WHERE state=$4 AND sent_at >= date_trunc('day', now())),
    COALESCE((SELECT EXTRACT(EPOCH FROM (now() - min(created_at))) * 1000 FROM notification_deliveries WHERE state=$1), 0)::bigint`,
		NotifyStatePending, NotifyStateSending, NotifyStateFailed, NotifyStateSent).
		Scan(&s.Channels, &s.ChannelsOn, &s.Pending, &s.Failed, &s.SentToday, &s.BacklogAgeMS); err != nil {
		return nil, err
	}
	return &s, nil
}
