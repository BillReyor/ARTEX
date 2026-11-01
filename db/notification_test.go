package db

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Autumn-27/artex/notify"
)

// The use cases in this document will all be connected to PostgreSQL (skipped if there is no database). These SQLs are used
// FOR UPDATE SKIP LOCKED, make_interval, JSONB, multi-line IN(...) placeholder splicing,
// They are all written in a way of "compilation passes but an error may occur when running", and it must be run to be considered verified.

func notifyTestDB(t *testing.T) *DB {
	t.Helper()
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

// newTestChannel creates a channel and automatically deletes it after the test.
func newTestChannel(t *testing.T, d *DB, kind, mode string, filter string) *NotificationChannel {
	t.Helper()
	if filter == "" {
		filter = `{}`
	}
	ch := &NotificationChannel{
		Name:       "测试渠道-" + t.Name(),
		Kind:       kind,
		Mode:       mode,
		Config:     json.RawMessage(`{"webhook":"https://example.com/hook"}`),
		Filter:     json.RawMessage(filter),
		RatePerMin: 100,
	}
	id, err := d.SaveNotificationChannel(context.Background(), ch)
	if err != nil {
		t.Fatalf("Failed to create channel: %v", err)
	}
	t.Cleanup(func() { d.Exec(`DELETE FROM notification_channels WHERE id=$1`, id) })
	ch.ID = id
	return ch
}

// addTestEvent writes an event directly (without finding) for testing dispatch and delivery.
func addTestEvent(t *testing.T, d *DB, kind string, findingID int64, snap notify.Snapshot) int64 {
	t.Helper()
	snap.Kind = kind
	snap.FindingID = findingID
	id, err := d.AddNotificationEvent(context.Background(), kind, findingID, snap)
	if err != nil {
		t.Fatalf("Failed to write event: %v", err)
	}
	t.Cleanup(func() { d.Exec(`DELETE FROM notification_events WHERE id=$1`, id) })
	return id
}

func TestNotificationAssetNamesResolvesAndPreservesOrder(t *testing.T) {
	d := notifyTestDB(t)
	ctx := context.Background()

	// Each of the three types of assets has its own display caliber: domain name, IP, and URL.
	insertAsset := func(query, value string) int64 {
		t.Helper()
		var id int64
		if err := d.QueryRow(query, value).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	domID := insertAsset(`INSERT INTO assets(type, domain) VALUES('subdomain',$1) RETURNING id`, "a.example.com")
	ipID := insertAsset(`INSERT INTO assets(type, ip) VALUES('ip',$1) RETURNING id`, "10.1.2.3")
	svcID := insertAsset(`INSERT INTO assets(type, url) VALUES('service',$1) RETURNING id`, "https://a.example.com/admin")
	t.Cleanup(func() {
		d.Exec(`DELETE FROM assets WHERE id IN ($1,$2,$3)`, domID, ipID, svcID)
	})

	// The order passed in is deliberately out of order and contains a non-existent id.
	got, err := d.NotificationAssetNames(ctx, []int64{svcID, 999999999, domID, ipID, svcID})
	if err != nil {
		t.Fatalf("Failed to parse asset name: %v", err)
	}
	want := []string{"https://a.example.com/admin", "a.example.com", "10.1.2.3"}
	if len(got) != len(want) {
		t.Fatalf("The number of asset names does not match, expecting %v and getting %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("The order/value does not match, expecting %v and getting %v", want, got)
		}
	}
}

// TestRecordNotificationEventTxUnwindsOnFailure is the core use case of the savepoint mechanism:
// In the transaction, first let the writing of notification_events inevitably fail (temporarily add a constant false constraint),
// Assertion ① This function reports false ② The transaction does not enter the aborted state, and subsequent statements can still be executed.
//
// Without a savepoint, PostgreSQL will invalidate the entire transaction, and any subsequent statements will end with
// "current transaction is aborted" failed - that's exactly what happened due to a problem with the notification table
// The fault path is that the vulnerability cannot be stored in the database.
//
// Here we deliberately end with **ROLLBACK instead of COMMIT**: ALTER TABLE is transactional in PG,
// Once submitted, the temporary constraint will remain in the schema permanently, suspending all subsequent use cases.
// Rollback automatically undoes DDL, eliminating the need for manual cleanup. The assertion only requires "the transaction is alive",
// No actual submission is required.
func TestRecordNotificationEventTxUnwindsOnFailure(t *testing.T) {
	d := notifyTestDB(t)
	ctx := context.Background()

	// Defensive cleanup: If this constraint has been left in historical operations, remove it first.
	if _, err := d.Exec(`ALTER TABLE notification_events DROP CONSTRAINT IF EXISTS notify_test_never`); err != nil {
		t.Fatal(err)
	}

	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback() // nolint:errcheck // Cancel temporary constraints, see function comments

	// NOT VALID: Only constrains rows written thereafter, and does not verify existing historical events in the library.
	// (Otherwise, existing row violations will result in constraints not being added).
	if _, err := tx.ExecContext(ctx, `ALTER TABLE notification_events ADD CONSTRAINT notify_test_never CHECK (false) NOT VALID`); err != nil {
		t.Fatalf("Failed to add temporary constraints: %v", err)
	}
	if RecordNotificationEventTx(ctx, tx, notify.EventFindingCreated, 1, notify.Snapshot{Severity: "high"}) {
		t.Fatal("Report write success despite the constraint that it must fail")
	}
	// Key assertion: The transaction is still available.
	var one int
	if err := tx.QueryRowContext(ctx, `SELECT 1`).Scan(&one); err != nil {
		t.Fatalf("Transaction has been tainted (savepoint is not valid): %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("Rollback failed: %v", err)
	}
	// Confirm that the DDL has been revoked with the rollback, leaving no room for subsequent use cases.
	var exists bool
	if err := d.QueryRow(`SELECT EXISTS(SELECT 1 FROM pg_constraint WHERE conname='notify_test_never')`).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Fatal("Temporary constraints are not revoked by rollback, which will pollute subsequent use cases.")
	}
}

func TestFanOutRoutesEventsByFilter(t *testing.T) {
	d := notifyTestDB(t)
	ctx := context.Background()

	all := newTestChannel(t, d, notify.KindDingTalk, NotifyModeRealtime, `{}`)
	onlyCritical := newTestChannel(t, d, notify.KindDingTalk, NotifyModeRealtime, `{"min_severity":"critical"}`)
	sqlOnly := newTestChannel(t, d, notify.KindDingTalk, NotifyModeRealtime, `{"vulnclass_include":["SQL"]}`)

	highSQL := addTestEvent(t, d, notify.EventFindingCreated, 1001, notify.Snapshot{Severity: "high", VulnClass: "SQL注入"})
	lowXSS := addTestEvent(t, d, notify.EventFindingCreated, 1002, notify.Snapshot{Severity: "low", VulnClass: "XSS"})
	criticalXSS := addTestEvent(t, d, notify.EventFindingCreated, 1003, notify.Snapshot{Severity: "critical", VulnClass: "XSS"})

	if _, _, err := d.FanOutPendingEvents(ctx, 100); err != nil {
		t.Fatalf("Dispatch failed: %v", err)
	}

	cases := []struct {
		name    string
		eventID int64
		channel int64
		want    bool
	}{
		{"全收渠道收到 high", highSQL, all.ID, true},
		{"全收渠道收到 low", lowXSS, all.ID, true},
		{"仅严重渠道跳过 high", highSQL, onlyCritical.ID, false},
		{"仅严重渠道收到 critical", criticalXSS, onlyCritical.ID, true},
		{"仅SQL渠道收到 SQL", highSQL, sqlOnly.ID, true},
		{"仅SQL渠道跳过 XSS", lowXSS, sqlOnly.ID, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var exists bool
			if err := d.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM notification_deliveries WHERE event_id=$1 AND channel_id=$2)`,
				tc.eventID, tc.channel).Scan(&exists); err != nil {
				t.Fatal(err)
			}
			if exists != tc.want {
				t.Fatalf("Delivery exists: Expected %v got %v", tc.want, exists)
			}
		})
	}

	// Dispatching again should not result in a duplicate delivery (fanned_out is idempotent).
	events, deliveries, err := d.FanOutPendingEvents(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	if events != 0 || deliveries != 0 {
		t.Fatalf("Dispatched events should not be processed again, getting events=%d deliveries=%d", events, deliveries)
	}
}

// TestFanOutMarksEventsWithNoMatchingChannel covers the case where the event does not hit any channel.
// This type of event must still be marked as dispatched, otherwise it will remain in the to-be-dispatched set forever and be rescanned every tick.
func TestFanOutMarksEventsWithNoMatchingChannel(t *testing.T) {
	d := notifyTestDB(t)
	ctx := context.Background()
	pick := newTestChannel(t, d, notify.KindDingTalk, NotifyModeRealtime, `{"vulnclass_include":["绝不匹配的类型"]}`)
	_ = pick

	ev := addTestEvent(t, d, notify.EventFindingCreated, 2001, notify.Snapshot{Severity: "high", VulnClass: "XSS"})
	_, deliveries, err := d.FanOutPendingEvents(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	if deliveries != 0 {
		t.Fatalf("Delivery should not have occurred, got %d", deliveries)
	}
	var fanned bool
	if err := d.QueryRowContext(ctx, `SELECT fanned_out FROM notification_events WHERE id=$1`, ev).Scan(&fanned); err != nil {
		t.Fatal(err)
	}
	if !fanned {
		t.Fatal("Events that miss the channel must also be marked as dispatched, otherwise they will be infinitely rescanned.")
	}
}

func TestClaimRealtimeDeliveriesHonorsLeaseAndMode(t *testing.T) {
	d := notifyTestDB(t)
	ctx := context.Background()

	realtime := newTestChannel(t, d, notify.KindDingTalk, NotifyModeRealtime, `{}`)
	digest := newTestChannel(t, d, notify.KindDingTalk, NotifyModeDigest, `{}`)

	addTestEvent(t, d, notify.EventFindingCreated, 3001, notify.Snapshot{Severity: "high", VulnClass: "XSS"})
	if _, _, err := d.FanOutPendingEvents(ctx, 100); err != nil {
		t.Fatal(err)
	}

	// For real-time payment, you should only get the one from the realtime channel, and do not touch the digest channel.
	got, err := d.ClaimRealtimeDeliveries(ctx, realtime.ID, 10, time.Minute)
	if err != nil {
		t.Fatalf("Failed to collect: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("Should receive 1 item and get %d", len(got))
	}
	if got[0].State != NotifyStateSending || got[0].Attempts != 1 {
		t.Fatalf("After receiving it, it should be sending and attempts=1, and get state=%s attempts=%d", got[0].State, got[0].Attempts)
	}
	// The associated loading rendering context must be complete (channel configuration + event snapshot + finding id).
	if got[0].Channel == nil || len(got[0].Channel.Config) == 0 {
		t.Fatal("The channel configuration is missing when receiving results, and rendering will fail.")
	}
	if got[0].FindingID != 3001 {
		t.Fatalf("The finding id was not brought out from the event and got %d", got[0].FindingID)
	}

	// The lease has not expired, and the second collection should be empty - this is "the same line will not be delivered by two dispatchers at the same time"
	// guarantee.
	again, err := d.ClaimRealtimeDeliveries(ctx, realtime.ID, 10, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 0 {
		t.Fatalf("You should not receive it repeatedly during the lease period, and you will get %d", len(again))
	}

	// Deliveries from the digest channel should not be hit by real-time pickups.
	left, err := d.ClaimRealtimeDeliveries(ctx, digest.ID, 10, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 0 {
		t.Fatalf("Real-time collection should not get the delivery of the digest channel, and get %d", len(left))
	}
}

// TestClaimExpiredLeaseRecovers covers crash self-healing: if the process hangs up during delivery, sending will be left
// OK, it must be picked up again after the lease expires, otherwise this delivery will be stuck forever.
func TestClaimExpiredLeaseRecovers(t *testing.T) {
	d := notifyTestDB(t)
	ctx := context.Background()
	ch := newTestChannel(t, d, notify.KindDingTalk, NotifyModeRealtime, `{}`)
	addTestEvent(t, d, notify.EventFindingCreated, 4001, notify.Snapshot{Severity: "high"})
	if _, _, err := d.FanOutPendingEvents(ctx, 100); err != nil {
		t.Fatal(err)
	}
	first, err := d.ClaimRealtimeDeliveries(ctx, ch.ID, 10, time.Minute)
	if err != nil || len(first) != 1 {
		t.Fatalf("Failed to collect for the first time: %v (%d)", err, len(first))
	}
	// Manually push the lease to the past to simulate "the lease has expired".
	if _, err := d.Exec(`UPDATE notification_deliveries SET next_attempt_at = now() - interval '1 minute' WHERE id=$1`, first[0].ID); err != nil {
		t.Fatal(err)
	}
	second, err := d.ClaimRealtimeDeliveries(ctx, ch.ID, 10, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 1 {
		t.Fatalf("The sending row whose lease has expired should be re-claimed and get %d", len(second))
	}
	if second[0].Attempts != 2 {
		t.Fatalf("The number of attempts to re-claim should be accumulated to get %d", second[0].Attempts)
	}
}

func TestClaimSkipsDisabledChannel(t *testing.T) {
	d := notifyTestDB(t)
	ctx := context.Background()
	ch := newTestChannel(t, d, notify.KindDingTalk, NotifyModeRealtime, `{}`)
	addTestEvent(t, d, notify.EventFindingCreated, 5001, notify.Snapshot{Severity: "high"})
	if _, _, err := d.FanOutPendingEvents(ctx, 100); err != nil {
		t.Fatal(err)
	}
	// Deactivating will mark all available deliveries as skipped.
	if err := d.SetNotificationChannelEnabled(ctx, ch.ID, false); err != nil {
		t.Fatal(err)
	}
	var state string
	if err := d.QueryRow(`SELECT state FROM notification_deliveries WHERE channel_id=$1`, ch.ID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != NotifyStateSkipped {
		t.Fatalf("Inventory pending delivery for deactivated channels should be marked skipped, resulting in %s", state)
	}
	got, err := d.ClaimRealtimeDeliveries(ctx, ch.ID, 10, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("Deactivated channels should not be claimed and received %d items", len(got))
	}
}

func TestDigestBatchDueAndStableBatchID(t *testing.T) {
	d := notifyTestDB(t)
	ctx := context.Background()
	ch := newTestChannel(t, d, notify.KindDingTalk, NotifyModeDigest, `{}`)
	for i := 0; i < 3; i++ {
		addTestEvent(t, d, notify.EventFindingCreated, int64(6000+i), notify.Snapshot{Severity: "high"})
	}
	if _, _, err := d.FanOutPendingEvents(ctx, 100); err != nil {
		t.Fatal(err)
	}

	// The batch has just been created, the age is 0, and it should not expire under the 30-minute cycle.
	due, err := d.DigestBatchDue(ctx, ch.ID, 30*time.Minute)
	if err != nil {
		t.Fatalf("Failed to determine batch expiration: %v", err)
	}
	if due {
		t.Fatal("Newly created batches should not expire immediately")
	}

	// Push the creation times of the three deliveries together to simulate a batch that lasts for a sufficient period.
	if _, err := d.Exec(`UPDATE notification_deliveries SET created_at = now() - interval '40 minutes' WHERE channel_id=$1`, ch.ID); err != nil {
		t.Fatal(err)
	}
	due, err = d.DigestBatchDue(ctx, ch.ID, 30*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if !due {
		t.Fatal("Batch that exceeds the cycle should be judged as expired")
	}

	batch, err := d.ClaimDigestBatch(ctx, ch.ID, MaxDigestBatchSize, time.Minute)
	if err != nil {
		t.Fatalf("Failed to receive summary batch: %v", err)
	}
	if len(batch) != 3 {
		t.Fatalf("In summary, all 3 items should be collected at one time to get %d items.", len(batch))
	}
	if batch[0].BatchID == nil {
		t.Fatal("The batch_id must be written when summarizing batches, otherwise it will not be seen in the history that these items were sent together.")
	}
	firstBatchID := *batch[0].BatchID
	for _, dl := range batch {
		if dl.BatchID == nil || *dl.BatchID != firstBatchID {
			t.Fatalf("The same batch should share batch_id, get %v vs %d", dl.BatchID, firstBatchID)
		}
	}

	// Let this batch of **whole** fail and be rearranged before receiving it. The batch_id must maintain the original value (the role of COALESCE):
	// Otherwise, one retry will erase the fact that "this batch was sent together".
	//
	// The entire batch must be rearranged instead of just one - this is how the delivery engine handles it when sending summary messages.
	// (One message represents the entire batch, success or failure is shared). If only one item is rescheduled, the rest will still be within the lease period.
	// Naturally, you will only get that one item if you regain the title.
	allIDs := make([]int64, 0, len(batch))
	for _, dl := range batch {
		allIDs = append(allIDs, dl.ID)
	}
	if err := d.RescheduleDeliveries(ctx, allIDs, time.Second, "模拟失败"); err != nil {
		t.Fatal(err)
	}
	// Push the lease into the past and simulate that the backoff time has expired.
	if _, err := d.Exec(`UPDATE notification_deliveries SET next_attempt_at = now() - interval '1 minute' WHERE channel_id=$1`, ch.ID); err != nil {
		t.Fatal(err)
	}
	reclaimed, err := d.ClaimDigestBatch(ctx, ch.ID, MaxDigestBatchSize, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(reclaimed) != 3 {
		t.Fatalf("Reclaim should get all 3 items and get %d", len(reclaimed))
	}
	if reclaimed[0].BatchID == nil || *reclaimed[0].BatchID != firstBatchID {
		t.Fatalf("After retrying, batch_id should keep the original value %d, and get %v", firstBatchID, reclaimed[0].BatchID)
	}
}

func TestDeliveryStateTransitions(t *testing.T) {
	d := notifyTestDB(t)
	ctx := context.Background()
	ch := newTestChannel(t, d, notify.KindDingTalk, NotifyModeRealtime, `{}`)
	addTestEvent(t, d, notify.EventFindingCreated, 7001, notify.Snapshot{Severity: "high"})
	if _, _, err := d.FanOutPendingEvents(ctx, 100); err != nil {
		t.Fatal(err)
	}
	got, err := d.ClaimRealtimeDeliveries(ctx, ch.ID, 10, time.Minute)
	if err != nil || len(got) != 1 {
		t.Fatalf("Failed to collect: %v (%d)", err, len(got))
	}
	id := got[0].ID

	if err := d.RescheduleDeliveries(ctx, []int64{id}, time.Second, "网络抖动"); err != nil {
		t.Fatal(err)
	}
	var state, lastErr string
	if err := d.QueryRow(`SELECT state, last_error FROM notification_deliveries WHERE id=$1`, id).Scan(&state, &lastErr); err != nil {
		t.Fatal(err)
	}
	if state != NotifyStatePending || lastErr != "网络抖动" {
		t.Fatalf("After rearrangement, it should be pending and the reason should be recorded. We get state=%s err=%q.", state, lastErr)
	}

	if err := d.FailDeliveries(ctx, []int64{id}, "重试耗尽"); err != nil {
		t.Fatal(err)
	}
	if err := d.QueryRow(`SELECT state FROM notification_deliveries WHERE id=$1`, id).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != NotifyStateFailed {
		t.Fatalf("Should be failed, got %s", state)
	}

	// Manual resends need to clear the retry count and expire immediately, otherwise the old failure budget will be inherited.
	if err := d.RetryNotificationDelivery(ctx, id); err != nil {
		t.Fatalf("Resend failed: %v", err)
	}
	var attempts int
	var next time.Time
	if err := d.QueryRow(`SELECT state, attempts, next_attempt_at FROM notification_deliveries WHERE id=$1`, id).Scan(&state, &attempts, &next); err != nil {
		t.Fatal(err)
	}
	if state != NotifyStatePending || attempts != 0 {
		t.Fatalf("After resending, it should be pending and attempts=0, and get state=%s attempts=%d", state, attempts)
	}
	if next.After(time.Now().Add(time.Second)) {
		t.Fatal("Resend should be available immediately")
	}

	// Deliveries that have been delivered should not be resent.
	if err := d.MarkDeliveriesSent(ctx, []int64{id}); err != nil {
		t.Fatal(err)
	}
	if err := d.RetryNotificationDelivery(ctx, id); err == nil {
		t.Fatal("Deliveries that have been delivered should not be allowed to be resent")
	}
}

func TestListNotificationDeliveriesPagingAndFilter(t *testing.T) {
	d := notifyTestDB(t)
	ctx := context.Background()
	ch := newTestChannel(t, d, notify.KindDingTalk, NotifyModeRealtime, `{}`)
	for i := 0; i < 5; i++ {
		addTestEvent(t, d, notify.EventFindingCreated, int64(8000+i), notify.Snapshot{Severity: "high", Name: "分页测试"})
	}
	if _, _, err := d.FanOutPendingEvents(ctx, 100); err != nil {
		t.Fatal(err)
	}
	if _, err := d.ClaimRealtimeDeliveries(ctx, ch.ID, 10, time.Minute); err != nil {
		t.Fatal(err)
	}

	page1, total, err := d.ListNotificationDeliveries(ctx, NotificationDeliveryFilter{ChannelID: ch.ID, State: NotifyStateSending}, 1, 2)
	if err != nil {
		t.Fatalf("Query failed: %v", err)
	}
	if total != 5 {
		t.Fatalf("The total should be 5, getting %d", total)
	}
	if len(page1) != 2 {
		t.Fatalf("2 items per page, get %d", len(page1))
	}
	// New first: The ID of the first item on the first page should be greater than the first item on the second page.
	page2, _, err := d.ListNotificationDeliveries(ctx, NotificationDeliveryFilter{ChannelID: ch.ID, State: NotifyStateSending}, 2, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(page2) != 2 || page2[0].ID >= page1[0].ID {
		t.Fatalf("The paging order should be new first, getting page1[0]=%d page2[0]=%d", page1[0].ID, page2[0].ID)
	}
	// The rendering context must be returned with the history, otherwise the list cannot show "what was pushed".
	if page1[0].ChannelName == "" || page1[0].FindingID == 0 {
		t.Fatalf("History item is missing display field: %+v", page1[0])
	}

	// Filter by status: None pending.
	pending, totalPending, err := d.ListNotificationDeliveries(ctx, NotificationDeliveryFilter{ChannelID: ch.ID, State: NotifyStatePending}, 1, 50)
	if err != nil {
		t.Fatal(err)
	}
	if totalPending != 0 || len(pending) != 0 {
		t.Fatalf("There should be no pending delivery, and %d items were obtained (total=%d)", len(pending), totalPending)
	}
}

func TestSetFindingStatusWithNotifyOnlyEmitsOnRealChange(t *testing.T) {
	d := notifyTestDB(t)
	ctx := context.Background()

	tk, err := d.CreateTask("通知状态变更测试", "目标", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(tk.ID)
	es := d.Exploration(tk.ExplorationID)
	f, err := es.RecordFinding(ctx, RecordFindingInput{
		TaskID: tk.ID, Worker: "test", VulnClass: "SQL注入", Name: "状态变更用例",
		Severity: "high", Summary: "Summary",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Exec(`DELETE FROM notification_events WHERE finding_id=$1`, f.FindingID) })

	// A finding_created event has been registered when the library was dropped. Count it first as the baseline.
	var base int
	if err := d.QueryRow(`SELECT count(*) FROM notification_events WHERE finding_id=$1`, f.FindingID).Scan(&base); err != nil {
		t.Fatal(err)
	}
	if base < 1 {
		t.Fatal("The vulnerability database should register a push event in the same transaction")
	}

	// Change to the same state: no event should be generated (to avoid repeated submissions and push noise).
	from, found, notified, err := d.SetFindingStatusWithNotify(ctx, f.FindingID, "pending")
	if err != nil || !found {
		t.Fatalf("Status setting failed: found=%v err=%v", found, err)
	}
	if notified {
		t.Fatal("Push events should not be registered when the status has not changed")
	}
	if from != "pending" {
		t.Fatalf("Should return to the pre-change status pending and get %q", from)
	}

	// Real change: Events should be registered and from/to logged.
	from, found, notified, err = d.SetFindingStatusWithNotify(ctx, f.FindingID, "fixed")
	if err != nil || !found {
		t.Fatalf("Status setting failed: found=%v err=%v", found, err)
	}
	if !notified {
		t.Fatal("Push events should be registered when the status actually changes")
	}
	if from != "pending" {
		t.Fatalf("from should be pending and get %q", from)
	}
	var snapshot []byte
	if err := d.QueryRow(`SELECT snapshot FROM notification_events WHERE finding_id=$1 AND kind=$2`,
		f.FindingID, notify.EventFindingStatusChanged).Scan(&snapshot); err != nil {
		t.Fatalf("Status change event not found: %v", err)
	}
	var snap notify.Snapshot
	if err := json.Unmarshal(snapshot, &snap); err != nil {
		t.Fatal(err)
	}
	if snap.FromStatus != "pending" || snap.ToStatus != "fixed" {
		t.Fatalf("The status flow in the snapshot is incorrect: %s → %s", snap.FromStatus, snap.ToStatus)
	}
	// The snapshot must bring the fields required for rendering, otherwise the status change message will be an empty shell.
	if snap.VulnClass != "SQL注入" || snap.Severity != "high" || snap.Name != "状态变更用例" {
		t.Fatalf("Snapshot is missing rendering fields: %+v", snap)
	}
	var status string
	if err := d.QueryRow(`SELECT status FROM findings WHERE id=$1`, f.FindingID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "fixed" {
		t.Fatalf("The status should have been updated to fixed, getting %s", status)
	}

	// Vulnerabilities that do not exist: found=false, no error is reported.
	if _, found, _, err := d.SetFindingStatusWithNotify(ctx, 999999999, "fixed"); err != nil || found {
		t.Fatalf("Vulnerabilities that do not exist should return found=false and no errors, get found=%v err=%v", found, err)
	}
}

func TestNotificationStatsSnapshot(t *testing.T) {
	d := notifyTestDB(t)
	ctx := context.Background()
	ch := newTestChannel(t, d, notify.KindDingTalk, NotifyModeRealtime, `{}`)
	addTestEvent(t, d, notify.EventFindingCreated, 9001, notify.Snapshot{Severity: "high"})
	if _, _, err := d.FanOutPendingEvents(ctx, 100); err != nil {
		t.Fatal(err)
	}
	stats, err := d.NotificationStatsSnapshot(ctx)
	if err != nil {
		t.Fatalf("Statistics failed: %v", err)
	}
	if stats.Channels < 1 || stats.ChannelsOn < 1 {
		t.Fatalf("Incorrect channel count: %+v", stats)
	}
	if stats.Pending < 1 {
		t.Fatalf("Should be counted to be delivered: %+v", stats)
	}
	// The newly created delivery backlog age should be close to 0, not negative or huge.
	if stats.BacklogAgeMS < 0 || stats.BacklogAgeMS > int64(time.Hour/time.Millisecond) {
		t.Fatalf("Backlog age is illegal: %d ms", stats.BacklogAgeMS)
	}
	_ = ch
}

func TestNotificationChannelCRUDRoundTrip(t *testing.T) {
	d := notifyTestDB(t)
	ctx := context.Background()

	ch := &NotificationChannel{
		Name:       "CRUD 往返",
		Kind:       notify.KindEmail,
		Mode:       NotifyModeDigest,
		Config:     json.RawMessage(`{"host":"smtp.example.com","port":587,"from":"a@b.c","to":["x@y.z"]}`),
		Filter:     json.RawMessage(`{"min_severity":"medium","on_status_change":true}`),
		RatePerMin: 42,
	}
	id, err := d.SaveNotificationChannel(ctx, ch)
	if err != nil {
		t.Fatalf("New creation failed: %v", err)
	}
	t.Cleanup(func() { d.Exec(`DELETE FROM notification_channels WHERE id=$1`, id) })

	got, err := d.NotificationChannelByID(ctx, id)
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}
	if got.Mode != NotifyModeDigest || got.RatePerMin != 42 || got.Name != "CRUD 往返" {
		t.Fatalf("Round trip field inconsistency: %+v", got)
	}
	if !got.IsEnabled() {
		t.Fatal("The default should be enabled")
	}
	var cfg map[string]any
	if err := json.Unmarshal(got.Config, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg["host"] != "smtp.example.com" {
		t.Fatalf("The configuration is not correctly dropped into the library: %v", cfg)
	}
	var filter notify.Filter
	if err := json.Unmarshal(got.Filter, &filter); err != nil {
		t.Fatal(err)
	}
	if filter.MinSeverity != "medium" || !filter.OnStatusChange {
		t.Fatalf("The filter conditions are not correctly entered into the library: %+v", filter)
	}

	// Read again after updating.
	got.Name = "改名了"
	off := false
	got.Enabled = &off
	if _, err := d.SaveNotificationChannel(ctx, got); err != nil {
		t.Fatal(err)
	}
	after, err := d.NotificationChannelByID(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if after.Name != "改名了" || after.IsEnabled() {
		t.Fatalf("Update not taking effect: %+v", after)
	}

	// After deletion, "does not exist" should be reported instead of silent success.
	if err := d.DeleteNotificationChannel(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := d.NotificationChannelByID(ctx, id); err != ErrNotificationChannelNotFound {
		t.Fatalf("Expected ErrNotificationChannelNotFound, got %v", err)
	}
	if err := d.DeleteNotificationChannel(ctx, id); err != ErrNotificationChannelNotFound {
		t.Fatalf("The duplicate deletion report does not exist and %v is obtained.", err)
	}
}

// TestSaveNotificationChannelKeepsExplicitZeroRate locks a place where it was written incorrectly:
// **0 is a legal configuration, meaning "no current limit" and cannot be overridden to the default value by the db layer as "unspecified"**.
//
// Historical bug: `if RatePerMin <= 0 { 取默认值 }` is written in SaveNotificationChannel,
// So the documentation, UI prompts, and takeTokens were all interpreted as "0 = no current limit", but the writing library layer was quietly changed to
// 20 (DingTalk/Qiwei/Telegram) or 100 (Feishu) - the operator thought the current limit was released, but was actually stuck.
// And there are no hints. The difference between "unspecified" and "explicit 0" is only expressed by the request body.
// Therefore, the default value is filled in at the server layer (see notifyCreateChannel), and the db layer only manages storage.
func TestSaveNotificationChannelKeepsExplicitZeroRate(t *testing.T) {
	d := notifyTestDB(t)
	ctx := context.Background()

	// Explicit 0 (no current limit): must be saved as is.
	unlimited := &NotificationChannel{
		Name: "不限流", Kind: notify.KindDingTalk, RatePerMin: 0,
		Config: json.RawMessage(`{"webhook":"https://example.com/h"}`),
	}
	id, err := d.SaveNotificationChannel(ctx, unlimited)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Exec(`DELETE FROM notification_channels WHERE id=$1`, id) })
	got, err := d.NotificationChannelByID(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.RatePerMin != 0 {
		t.Fatalf("Explicit 0 means no current limit and must be saved as is, resulting in %d", got.RatePerMin)
	}
	if got.Mode != NotifyModeRealtime {
		t.Fatalf("Default mode should be realtime, get %s", got.Mode)
	}

	// Negative values are illegal input and should be rejected rather than quietly changed to another value.
	bad := &NotificationChannel{
		Name: "负限流", Kind: notify.KindDingTalk, RatePerMin: -1,
		Config: json.RawMessage(`{"webhook":"https://example.com/h"}`),
	}
	if _, err := d.SaveNotificationChannel(ctx, bad); err == nil {
		t.Fatal("Negative current limits should be rejected")
	}
}

// TestDeleteChannelCascadesDeliveries locks foreign key behavior: after the channel is deleted, its delivery history disappears
// (The configuration is gone, and the history cannot be interpreted), but the event itself must remain - it may be quoted by other channels.
func TestDeleteChannelCascadesDeliveries(t *testing.T) {
	d := notifyTestDB(t)
	ctx := context.Background()
	ch := newTestChannel(t, d, notify.KindDingTalk, NotifyModeRealtime, `{}`)
	ev := addTestEvent(t, d, notify.EventFindingCreated, 9101, notify.Snapshot{Severity: "high"})
	if _, _, err := d.FanOutPendingEvents(ctx, 100); err != nil {
		t.Fatal(err)
	}
	var before int
	if err := d.QueryRow(`SELECT count(*) FROM notification_deliveries WHERE channel_id=$1`, ch.ID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if before == 0 {
		t.Fatal("The precondition is not met: no delivery occurs")
	}
	if err := d.DeleteNotificationChannel(ctx, ch.ID); err != nil {
		t.Fatal(err)
	}
	var after int
	if err := d.QueryRow(`SELECT count(*) FROM notification_deliveries WHERE channel_id=$1`, ch.ID).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != 0 {
		t.Fatalf("After the channel is deleted, its delivery should be deleted in cascade, and there are still %d", after)
	}
	var evExists bool
	if err := d.QueryRow(`SELECT EXISTS(SELECT 1 FROM notification_events WHERE id=$1)`, ev).Scan(&evExists); err != nil {
		t.Fatal(err)
	}
	if !evExists {
		t.Fatal("Deleting a channel should not also delete the event itself")
	}
}

// TestClaimDigestBatchHonorsCallerLimit covers a hole pointed out by the audit:
// The aggregation channel previously completely bypassed the token bucket - allow was deducted by takeTokens but no one used it.
// rate_per_min has no effect on digest mode. Now limit also participates in constraints.
func TestClaimDigestBatchHonorsCallerLimit(t *testing.T) {
	d := notifyTestDB(t)
	ctx := context.Background()
	ch := newTestChannel(t, d, notify.KindDingTalk, NotifyModeDigest, `{}`)
	for i := 0; i < 10; i++ {
		addTestEvent(t, d, notify.EventFindingCreated, int64(7000+i), notify.Snapshot{Severity: "high"})
	}
	if _, _, err := d.FanOutPendingEvents(ctx, 100); err != nil {
		t.Fatal(err)
	}
	// Take limit=3: you can only get 3 items, and keep the rest in the library.
	got, err := d.ClaimDigestBatch(ctx, ch.ID, 3, time.Minute)
	if err != nil {
		t.Fatalf("Failed to collect: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("You should only receive 3 items according to the caller's current limit, and get %d", len(got))
	}
	// limit=0 means that the quota for this round has been exhausted: no one should be claimed, and no error should be reported.
	if got, err := d.ClaimDigestBatch(ctx, ch.ID, 0, time.Minute); err != nil || len(got) != 0 {
		t.Fatalf("When the quota is 0, 0 items should be received and no error will be reported, and %d items will be obtained. err=%v", len(got), err)
	}
}

// TestFinishFindingRetestEmitsStatusChange covers an integrity gap pointed out by the audit:
// When the retest conclusion is "fixed", the status has indeed changed, but the UPDATE was written directly to the library.
// Bypassing the version with notification - so the on_status_change channel is configured to transfer this status
// I couldn't receive push notifications at all, and the status on the interface changed quietly. Operation and maintenance had to open the platform to find out.
//
// This use case locks "all paths that change state must register state change events".
func TestFinishFindingRetestEmitsStatusChange(t *testing.T) {
	d := notifyTestDB(t)
	ctx := context.Background()

	tk, err := d.CreateTask("复测推送测试", "目标", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(tk.ID)
	es := d.Exploration(tk.ExplorationID)
	f, err := es.RecordFinding(ctx, RecordFindingInput{
		TaskID: tk.ID, Worker: "test", VulnClass: "SQL注入", Name: "复测目标",
		Severity: "high", Summary: "Summary",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Exec(`DELETE FROM notification_events WHERE finding_id=$1`, f.FindingID) })

	// Create a retest record and push it directly to the completion state.
	rt, _, _, err := d.CreateFindingRetest(ctx, f.FindingID, "复核")
	if err != nil {
		t.Fatal(err)
	}
	if rt.ConversationID == nil {
		t.Fatal("Retests should be associated with a session")
	}
	// The retest must be running before conclusions can be drawn (consistent with the real process).
	if ok, err := d.StartFindingRetest(ctx, rt.ID); err != nil || !ok {
		t.Fatalf("Failed to start retest: ok=%v err=%v", ok, err)
	}
	if err := d.RecordFindingRetestResult(ctx, *rt.ConversationID, "fixed", "Fixed", "证据"); err != nil {
		t.Fatal(err)
	}
	if err := d.FinishFindingRetest(rt.ID, "completed", ""); err != nil {
		t.Fatalf("Failed to end retest: %v", err)
	}

	var status string
	if err := d.QueryRow(`SELECT status FROM findings WHERE id=$1`, f.FindingID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != FindingFixed {
		t.Fatalf("After retesting, it was determined that the status should be fixed after repair, and %s was obtained.", status)
	}

	// Key assertion: There must be a state change event, and the from/to is correct.
	var snapshot []byte
	err = d.QueryRow(`SELECT snapshot FROM notification_events WHERE finding_id=$1 AND kind=$2 ORDER BY id DESC LIMIT 1`,
		f.FindingID, notify.EventFindingStatusChanged).Scan(&snapshot)
	if err != nil {
		t.Fatalf("After retesting, it has been determined that the status change push event should be registered (otherwise the channel configured with on_status_change will not receive it): %v", err)
	}
	var snap notify.Snapshot
	if err := json.Unmarshal(snapshot, &snap); err != nil {
		t.Fatal(err)
	}
	if snap.FromStatus != "pending" || snap.ToStatus != FindingFixed {
		t.Fatalf("The status flow of the snapshot is incorrect: %s → %s", snap.FromStatus, snap.ToStatus)
	}
	// The snapshot must contain fields required for rendering, otherwise it will be an empty shell when pushed.
	if snap.Name != "复测目标" || snap.Severity != "high" {
		t.Fatalf("Snapshot is missing rendering fields: %+v", snap)
	}
}
