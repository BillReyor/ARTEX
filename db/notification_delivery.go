package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// This document is the collection and status transfer of delivery tasks.
//
// Use a "lease" instead of a long transaction: set the line to sending and push next_attempt_at to the future as
// The lease expiration time, after submitting the transaction, do network delivery. In this way, the database lock is not held during delivery——
// The network request may take several seconds (the client times out 15 seconds), and holding the row lock will drag down other write operations in the same database.
//
// The price is that if the process crashes during delivery, the line will stop at sending. This is **self-healing**: after the lease expires
// next_attempt_at falls into the past, and the same line will be picked up again in the next round of collection (see the collection conditions
// state IN ('pending','sending')). The retry count is already +1 when claimed, so crashes will not be caused
// Infinite retries - MaxNotifyAttempts will be manually processed after the MaxNotifyAttempts opportunity is used up.

// MaxNotifyAttempts is the maximum number of attempts for a delivery (including the first time).
// It is defined here rather than in the delivery engine: it is the strategy of the state machine itself, and the engine is just the executor.
const MaxNotifyAttempts = 3

// MaxDigestBatchSize is the maximum number of deliveries that can be merged into a single summary batch at one time.
//
// The reason for existence is resources: if tens of thousands of vulnerabilities are scanned out in a summary cycle (completely possible - a full scan
// can be done), if there is no upper bound, the collection will read all the lines into the memory and render them into a very long message.
// Then most of it is cut off by the maximum length of the channel - which wastes memory and **silently loses** the truncated loopholes.
// After setting the upper limit, the excess part will be kept in the library and become the next batch, and will be sent out naturally in the next cycle without being lost.
//
// The basis for choosing 500: it is the magnitude that after rendering into a message, there is still "readable content" within the upper limit of 4096 bytes in Qiwei;
// No matter how big it is, it will just make the truncation occur further back.
const MaxDigestBatchSize = 500

// NotificationDelivery is a delivery task that contains channel configuration and event snapshots required for rendering.
type NotificationDelivery struct {
	ID            int64           `json:"id"`
	EventID       int64           `json:"event_id"`
	ChannelID     int64           `json:"channel_id"`
	State         string          `json:"state"`
	Attempts      int             `json:"attempts"`
	NextAttemptAt time.Time       `json:"next_attempt_at"`
	LastError     string          `json:"last_error"`
	BatchID       *int64          `json:"batch_id,omitempty"`
	CreatedAt     time.Time       `json:"created_at"`
	SentAt        *time.Time      `json:"sent_at,omitempty"`
	Snapshot      json.RawMessage `json:"snapshot,omitempty"`
	// Jointly loaded rendering context, not into JSON (DTO assembled by server layer).
	Channel *NotificationChannel `json:"-"`
	// FindingID/EventKind is brought out from the event for the history list to directly jump to the vulnerability details.
	FindingID int64  `json:"finding_id,string"`
	EventKind string `json:"event_kind"`
	// ChannelName/ChannelKind are redundant fields used for list display, eliminating the need for secondary queries on the front end.
	ChannelName string `json:"channel_name"`
	ChannelKind string `json:"channel_kind"`
}

const notificationDeliveryCols = `d.id, d.event_id, d.channel_id, d.state, d.attempts, d.next_attempt_at,
       d.last_error, d.batch_id, d.created_at, d.sent_at`

// joinedDeliveryQuery is the unified read shape of the delivery row: delivery + event snapshot + channel configuration.
// All three are indispensable to render a message, and checking them separately will require three round trips.
const joinedDeliveryQuery = `SELECT ` + notificationDeliveryCols + `,
       e.snapshot, e.kind, e.finding_id,
       c.id, c.name, c.kind, c.enabled, c.config, c.mode, c.filter, c.rate_per_min
FROM notification_deliveries d
JOIN notification_events e ON e.id = d.event_id
JOIN notification_channels c ON c.id = d.channel_id`

func scanNotificationDelivery(sc interface{ Scan(...any) error }) (*NotificationDelivery, error) {
	var (
		dl        NotificationDelivery
		lastErr   sql.NullString
		batchID   sql.NullInt64
		sentAt    sql.NullTime
		snapshot  []byte
		eventKind string
		channel   NotificationChannel
		chEnabled bool
	)
	if err := sc.Scan(&dl.ID, &dl.EventID, &dl.ChannelID, &dl.State, &dl.Attempts, &dl.NextAttemptAt,
		&lastErr, &batchID, &dl.CreatedAt, &sentAt,
		&snapshot, &eventKind, &dl.FindingID,
		&channel.ID, &channel.Name, &channel.Kind, &chEnabled, &channel.Config, &channel.Mode, &channel.Filter, &channel.RatePerMin); err != nil {
		return nil, err
	}
	dl.LastError = lastErr.String
	if batchID.Valid {
		dl.BatchID = &batchID.Int64
	}
	if sentAt.Valid {
		dl.SentAt = &sentAt.Time
	}
	dl.Snapshot = json.RawMessage(snapshot)
	dl.EventKind = eventKind
	dl.ChannelName = channel.Name
	dl.ChannelKind = channel.Kind
	channel.Enabled = &chEnabled
	dl.Channel = &channel
	return &dl, nil
}

// claimQuery describes a claim: first press sel to select candidates and lock them, then set them to sending and
// Extend the lease. The lease position in sel is occupied by the caller with $n and passes the parameters by itself.
type claimQuery struct {
	sql  string
	args []any
}

// ClaimRealtimeDeliveries Receive a batch of expired real-time deliveries from a certain channel, up to limit.
//
// Deliberately collect according to **single channel** instead of "collect a batch globally and then distribute it": the current limiter is based on the channel in the delivery engine
// Maintenance, only by first knowing how many lines this channel can send this round, and then collecting the same number of lines, will the current limit not be consumed
// Number of retries. If the opposite is true, the row blocked by the current limit has already been counted once for attempts.
// Three times the budget will be consumed by pure waiting, and finally it will fall into failed.
//
// The condition includes "sending whose lease has expired" - that is the point of crash and self-healing. lease must be significantly larger than single
// The worst-case delivery time (channel HTTP client timeout 15 seconds), otherwise the same row will be sent by two dispatchers
// Delivered simultaneously. Also block disabled channels: The deactivation operation has marked the inventory delivery as skipped.
// Here is another block to avoid leakage when deactivation and collection are concurrent.
func (d *DB) ClaimRealtimeDeliveries(ctx context.Context, channelID int64, limit int, lease time.Duration) ([]*NotificationDelivery, error) {
	if limit <= 0 {
		return nil, nil
	}
	return d.claimDeliveries(ctx, lease, claimQuery{
		sql: `SELECT dd.id FROM notification_deliveries dd
JOIN notification_channels c ON c.id = dd.channel_id
WHERE dd.channel_id = $1 AND dd.state IN ($2,$3) AND dd.next_attempt_at <= now()
  AND c.enabled AND c.mode = $4
ORDER BY dd.next_attempt_at, dd.id
FOR UPDATE OF dd SKIP LOCKED
LIMIT $5`,
		args: []any{channelID, NotifyStatePending, NotifyStateSending, NotifyModeRealtime, limit},
	}, nil)
}

// DigestBatchDue reports whether the channel has accumulated an expired batch: there are pending deliveries and the **oldest one**
// The age has reached the aggregation period.
//
// The determination is based on the age of the oldest delivery rather than the wall clock: this way the newly built channel will not be affected by being aligned to the hour
// Immediately spit out a "summary" of only one item, and batches that have been backlogged for a long time will not wait in vain for another round.
//
// It is separated from ClaimDigestBatch because of different semantics: this function only answers "should it be sent?"
// To receive it, you need to take away **all** of the channels to be released (including those who have not yet reached the age of 10) - otherwise one cycle
// It will be split into multiple messages, and the summary will be meaningless.
func (d *DB) DigestBatchDue(ctx context.Context, channelID int64, minAge time.Duration) (bool, error) {
	var due bool
	err := d.QueryRowContext(ctx, `SELECT EXISTS (
  SELECT 1 FROM notification_deliveries d
  JOIN notification_channels c ON c.id = d.channel_id
  WHERE d.channel_id = $1 AND d.state IN ($2,$3) AND c.enabled
  GROUP BY d.channel_id
  HAVING min(d.created_at) <= now() - make_interval(secs => $4)
)`, channelID, NotifyStatePending, NotifyStateSending, int64(minAge.Seconds())).Scan(&due)
	return due, err
}

// ClaimDigestBatch collects the currently due delivery of a certain channel as a summary batch.
// Maximum MaxDigestBatchSize items in a single batch.
//
// All deliveries in the same batch share batch_id, and use the smallest id in the set as the batch number (stable, readable,
// No additional sequence required). Use COALESCE to retain the original batch number when retrying, so that "this batch of N items are shipped together"
// Still holds after multiple retries.
//
// Take the first N items in ascending order by id instead of randomly: the earliest delivery will be sent out first and will not appear in the backlog.
// The hunger of "new vulnerabilities appear first, and old vulnerabilities always come last".
func (d *DB) ClaimDigestBatch(ctx context.Context, channelID int64, limit int, lease time.Duration) ([]*NotificationDelivery, error) {
	if limit <= 0 {
		return nil, nil
	}
	// limit is the **memory upper bound**, the caller passes MaxDigestBatchSize; here is another link,
	// Prevents the caller from passing in a larger value.
	//
	// Deliberately do not accept the "current limit quota" as the batch size: the unit of current limit is the number of messages - only one batch is sent
	// One message consumes one token, which is deducted by takeTokens on the server layer - the same as "loading several messages in one batch"
	// "Vulnerability" are two different dimensions. In order to make rate_per_min effective for digest, each round
	// The request budget is passed in as the batch size. As a result, the channel with rate=20/min only installs 1 vulnerability in each batch.
	// The digest degenerates into a real-time push with summary copy. To change the current limit, please change the want of takeTokens.
	// Don't move here.
	if limit > MaxDigestBatchSize {
		limit = MaxDigestBatchSize
	}
	out, err := d.claimDeliveries(ctx, lease, claimQuery{
		sql: `SELECT dd.id FROM notification_deliveries dd
JOIN notification_channels c ON c.id = dd.channel_id
WHERE dd.channel_id = $1 AND dd.state IN ($2,$3) AND dd.next_attempt_at <= now() AND c.enabled
ORDER BY dd.id
FOR UPDATE OF dd SKIP LOCKED
LIMIT $4`,
		args: []any{channelID, NotifyStatePending, NotifyStateSending, limit},
	}, func(tx *sql.Tx, ids []int64) error {
		batchID := ids[0]
		for _, id := range ids {
			if id < batchID {
				batchID = id
			}
		}
		ph, idArgs := placeholders(2, ids)
		_, err := tx.ExecContext(ctx, `UPDATE notification_deliveries SET batch_id = COALESCE(batch_id, $1)
WHERE id IN (`+ph+`)`, append([]any{batchID}, idArgs...)...)
		return err
	})
	return out, err
}

// claimDeliveries performs "select + set sending extending lease + reading full row", all in one transaction.
// postClaim is an optional additional step (which is used to write batch_id for aggregating batches).
func (d *DB) claimDeliveries(ctx context.Context, lease time.Duration, cq claimQuery, postClaim func(*sql.Tx, []int64) error) ([]*NotificationDelivery, error) {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() // nolint:errcheck // no-op after successful submission

	ids, err := selectForClaim(ctx, tx, cq.sql, cq.args...)
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, tx.Commit()
	}
	// Set sending and push next_attempt_at to the future: this future moment is the lease expiration time,
	// "The lease has not expired" and "The retry time has not yet expired" therefore share the same conditional expression, and there is no need to add a new column.
	ph, idArgs := placeholders(3, ids)
	if _, err := tx.ExecContext(ctx, `UPDATE notification_deliveries
SET state=$1, attempts=attempts+1, next_attempt_at=now()+make_interval(secs => $2)
WHERE id IN (`+ph+`)`,
		append([]any{NotifyStateSending, lease.Seconds()}, idArgs...)...); err != nil {
		return nil, err
	}
	if postClaim != nil {
		if err := postClaim(tx, ids); err != nil {
			return nil, err
		}
	}
	out, err := loadDeliveriesTx(ctx, tx, ids)
	if err != nil {
		return nil, err
	}
	return out, tx.Commit()
}

func selectForClaim(ctx context.Context, tx *sql.Tx, query string, args ...any) ([]int64, error) {
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func loadDeliveriesTx(ctx context.Context, tx *sql.Tx, ids []int64) ([]*NotificationDelivery, error) {
	ph, args := placeholders(1, ids)
	rows, err := tx.QueryContext(ctx, joinedDeliveryQuery+` WHERE d.id IN (`+ph+`) ORDER BY d.id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*NotificationDelivery{}
	for rows.Next() {
		dl, err := scanNotificationDelivery(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, dl)
	}
	return out, rows.Err()
}

// MarkDeliveriesSent Marks a batch of deliveries as delivered.
func (d *DB) MarkDeliveriesSent(ctx context.Context, ids []int64) error {
	ph, args := placeholders(2, ids)
	if len(args) == 0 {
		return nil
	}
	_, err := d.ExecContext(ctx, `UPDATE notification_deliveries
SET state=$1, sent_at=now(), last_error='' WHERE id IN (`+ph+`)`, append([]any{NotifyStateSent}, args...)...)
	return err
}

// RescheduleDeliveries returns a batch of deliveries to pending and postpones the retry time.
//
// Returning to pending instead of introducing a new intermediate state is to make "how many opportunities are left" only come from one place
// Expression (MaxNotifyAttempts) to avoid branches of the state machine from swelling with the retry strategy.
func (d *DB) RescheduleDeliveries(ctx context.Context, ids []int64, delay time.Duration, errMsg string) error {
	ph, args := placeholders(4, ids)
	if len(args) == 0 {
		return nil
	}
	_, err := d.ExecContext(ctx, `UPDATE notification_deliveries
SET state=$1, next_attempt_at=now()+make_interval(secs => $2), last_error=$3
WHERE id IN (`+ph+`)`,
		append([]any{NotifyStatePending, delay.Seconds(), truncateNotifyError(errMsg)}, args...)...)
	return err
}

// DeferDeliveries returns a batch of deliveries to pending, immediately available for redemption, and **cancels the attempt** to claim the timepiece.
//
// There is only one purpose: when the summary message is sent in segments according to the upper limit of the channel length, the entries that are not included in this article must be reserved for the next batch.
// That's not a failure, so the retry budget shouldn't be consumed - attempts is already optimistically +1 when claimed,
// Must be reduced here. Otherwise, a backlog of 500 items will be divided into 25 segments based on 20 items per segment.
// The tail entries were judged failed by MaxNotifyAttempts in paragraph 3, and they never had any errors.
//
// GREATEST(...,0) covers the situation where "someone manually resets attempts and then comes here again",
// Don't let the count become negative.
func (d *DB) DeferDeliveries(ctx context.Context, ids []int64, reason string) error {
	ph, args := placeholders(3, ids)
	if len(args) == 0 {
		return nil
	}
	_, err := d.ExecContext(ctx, `UPDATE notification_deliveries
SET state=$1, attempts=GREATEST(attempts-1, 0), next_attempt_at=now(), last_error=$2
WHERE id IN (`+ph+`)`,
		append([]any{NotifyStatePending, truncateNotifyError(reason)}, args...)...)
	return err
}

// FailDeliveries marks a batch of deliveries as ultimately failed, waiting for manual resending in the delivery history.
func (d *DB) FailDeliveries(ctx context.Context, ids []int64, errMsg string) error {
	// The placeholders start at $3: $1 is state, $2 is last_error.
	ph, args := placeholders(3, ids)
	if len(args) == 0 {
		return nil
	}
	_, err := d.ExecContext(ctx, `UPDATE notification_deliveries SET state=$1, last_error=$2 WHERE id IN (`+ph+`)`,
		append([]any{NotifyStateFailed, truncateNotifyError(errMsg)}, args...)...)
	return err
}

// RetryNotificationDelivery manually resends a delivery: reset to pending, clear retry count,
// Expires immediately. Clearing the count is deliberate - manually clicking "Resend" means that the reasons for the previous failures have been dealt with.
// It doesn't make sense to take the old count and limit it.
func (d *DB) RetryNotificationDelivery(ctx context.Context, id int64) error {
	res, err := d.ExecContext(ctx, `UPDATE notification_deliveries
SET state=$2, attempts=0, next_attempt_at=now(), last_error=''
WHERE id=$1 AND state IN ($3,$4)`, id, NotifyStatePending, NotifyStateFailed, NotifyStateSkipped)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("Delivery %d does not exist or its current state does not allow resending", id)
	}
	return nil
}

// NotificationDeliveryFilter is the query condition for delivery history.
type NotificationDeliveryFilter struct {
	ChannelID int64
	State     string
	EventKind string
}

func (f NotificationDeliveryFilter) where() (string, []any) {
	var conds []string
	var args []any
	if f.ChannelID > 0 {
		args = append(args, f.ChannelID)
		conds = append(conds, fmt.Sprintf("d.channel_id=$%d", len(args)))
	}
	if f.State != "" {
		args = append(args, f.State)
		conds = append(conds, fmt.Sprintf("d.state=$%d", len(args)))
	}
	if f.EventKind != "" {
		args = append(args, f.EventKind)
		conds = append(conds, fmt.Sprintf("e.kind=$%d", len(args)))
	}
	if len(conds) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}

// ListNotificationDeliveries returns the delivery history in pagination, with the newest first.
func (d *DB) ListNotificationDeliveries(ctx context.Context, f NotificationDeliveryFilter, page, pageSize int) ([]*NotificationDelivery, int, error) {
	if page < 1 {
		page = 1
	}
	if pageSize <= 0 || pageSize > 200 {
		pageSize = 50
	}
	where, args := f.where()

	var total int
	if err := d.QueryRowContext(ctx, `SELECT count(*) FROM notification_deliveries d
JOIN notification_events e ON e.id = d.event_id`+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	q := fmt.Sprintf("%s%s ORDER BY d.id DESC LIMIT $%d OFFSET $%d",
		joinedDeliveryQuery, where, len(args)+1, len(args)+2)
	rows, err := d.QueryContext(ctx, q, append(args, pageSize, (page-1)*pageSize)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []*NotificationDelivery{}
	for rows.Next() {
		dl, err := scanNotificationDelivery(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, dl)
	}
	return out, total, rows.Err()
}

// truncateNotifyError truncates the error message to the acceptable length of the column. The response body returned by the channel may be very long
// (This is especially true when general webhooks hit self-built services.) Failure to truncate will expand the load of the history list.
func truncateNotifyError(msg string) string {
	const max = 500
	if len(msg) <= max {
		return msg
	}
	// Rewind according to character boundaries to avoid leaving half a UTF-8 character and causing the front end to display garbled characters.
	cut := max
	for cut > 0 && !isUTF8Start(msg[cut]) {
		cut--
	}
	return msg[:cut] + "…"
}

func isUTF8Start(b byte) bool { return b&0xC0 != 0x80 }

// placeholders generates $n placeholder strings starting from start and corresponding parameters for use by IN (...).
// For example start=3, ids=[7,8] → "$3,$4", [7,8].
func placeholders(start int, ids []int64) (string, []any) {
	ph := make([]string, 0, len(ids))
	args := make([]any, 0, len(ids))
	for i, id := range ids {
		ph = append(ph, fmt.Sprintf("$%d", start+i))
		args = append(args, id)
	}
	return strings.Join(ph, ","), args
}
