package Database

import (
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"telegram-message-sync-bot/internal/Entity"
)

func setupTestDB(t *testing.T) {
	t.Helper()

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open in-memory sqlite: %v", err)
	}

	err = db.AutoMigrate(&Entity.Message{}, &Entity.Attachment{}, &Entity.SyncRecord{})
	if err != nil {
		t.Fatalf("failed to migrate schema: %v", err)
	}

	DB = db
}

func TestSaveMessage_DuplicateBySourceUniqueIndex(t *testing.T) {
	setupTestDB(t)

	msg1 := &Entity.Message{
		MessageID:   1001,
		Username:    "imbGZo",
		Content:     "first",
		MessageUrl:  "https://t.me/imbGZo/1001",
		MessageDate: time.Now(),
		CreatedTime: time.Now(),
	}

	_, err := SaveMessage(msg1)
	if err != nil {
		t.Fatalf("first save should succeed, got err: %v", err)
	}

	msg2 := &Entity.Message{
		MessageID:   1001,
		Username:    "imbGZo",
		Content:     "duplicate",
		MessageUrl:  "https://t.me/imbGZo/1001",
		MessageDate: time.Now(),
		CreatedTime: time.Now(),
	}

	_, err = SaveMessage(msg2)
	if err == nil {
		t.Fatalf("duplicate save should fail by unique constraint")
	}

	if !IsDuplicateMessageError(err) {
		t.Fatalf("expected duplicate error recognizer to return true, got err: %v", err)
	}
}

func TestSaveMessage_DifferentSourceCanCoexist(t *testing.T) {
	setupTestDB(t)

	msg1 := &Entity.Message{
		MessageID:   1001,
		Username:    "imbGZo",
		Content:     "first",
		MessageUrl:  "https://t.me/imbGZo/1001",
		MessageDate: time.Now(),
		CreatedTime: time.Now(),
	}

	msg2 := &Entity.Message{
		MessageID:   1001,
		Username:    "anotherChannel",
		Content:     "second",
		MessageUrl:  "https://t.me/anotherChannel/1001",
		MessageDate: time.Now(),
		CreatedTime: time.Now(),
	}

	if _, err := SaveMessage(msg1); err != nil {
		t.Fatalf("first save should succeed, got err: %v", err)
	}

	if _, err := SaveMessage(msg2); err != nil {
		t.Fatalf("second save with different source should succeed, got err: %v", err)
	}
}

func TestSaveSyncRecord_KeepsAttemptHistory(t *testing.T) {
	setupTestDB(t)

	msg := &Entity.Message{
		MessageID:   2001,
		Username:    "imbGZo",
		Content:     "hello",
		MessageUrl:  "https://t.me/imbGZo/2001",
		MessageDate: time.Now(),
		CreatedTime: time.Now(),
	}
	archivedMessageID, err := SaveMessage(msg)
	if err != nil {
		t.Fatalf("save message should succeed, got err: %v", err)
	}

	first := &Entity.SyncRecord{
		ArchivedMessageID: archivedMessageID,
		Platform:          "BlueSky",
		Status:            Entity.SyncStatusFailed,
		ErrorMessage:      "temporary failure",
		Trigger:           Entity.SyncTriggerAutomatic,
		CreatedTime:       time.Now(),
	}
	if _, err := SaveSyncRecord(first); err != nil {
		t.Fatalf("first sync record should save, got err: %v", err)
	}

	second := &Entity.SyncRecord{
		ArchivedMessageID: archivedMessageID,
		Platform:          "BlueSky",
		Status:            Entity.SyncStatusSucceeded,
		RemoteID:          "at://did:plc:test/app.bsky.feed.post/abc",
		Trigger:           Entity.SyncTriggerAutomatic,
		CreatedTime:       time.Now(),
	}
	if _, err := SaveSyncRecord(second); err != nil {
		t.Fatalf("second sync record should save, got err: %v", err)
	}

	records, err := ListSyncRecordsByMessage(archivedMessageID)
	if err != nil {
		t.Fatalf("list sync records should succeed, got err: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("expected 2 sync records, got %d", len(records))
	}
	if records[0].AttemptNo != 1 || records[1].AttemptNo != 2 {
		t.Fatalf("unexpected attempt sequence: %+v", records)
	}

	latest, err := GetLatestSyncRecord(archivedMessageID, "BlueSky")
	if err != nil {
		t.Fatalf("get latest sync record should succeed, got err: %v", err)
	}
	if latest.Status != Entity.SyncStatusSucceeded {
		t.Fatalf("expected latest status succeeded, got: %+v", latest)
	}
	if latest.RemoteID == "" {
		t.Fatalf("expected latest record to keep remote id, got: %+v", latest)
	}
}

func newAlbumTestMessage(messageID int64, mediaGroupID string) *Entity.Message {
	return &Entity.Message{
		MessageID:    messageID,
		Username:     "imbGZo",
		Content:      "album-member",
		MessageUrl:   "https://t.me/imbGZo/" + time.Unix(messageID, 0).Format("150405"),
		MessageDate:  time.Now(),
		MediaGroupID: mediaGroupID,
		CreatedTime:  time.Now(),
	}
}

func TestListMessagesByMediaGroup_ReturnsMembersInSourceOrder(t *testing.T) {
	setupTestDB(t)

	for _, messageID := range []int64{30, 10, 20} {
		if _, err := SaveMessage(newAlbumTestMessage(messageID, "gid-1")); err != nil {
			t.Fatalf("save message %d should succeed, got err: %v", messageID, err)
		}
	}
	if _, err := SaveMessage(newAlbumTestMessage(40, "")); err != nil {
		t.Fatalf("save non-album message should succeed, got err: %v", err)
	}

	messages, err := ListMessagesByMediaGroup("imbGZo", "gid-1")
	if err != nil {
		t.Fatalf("list album messages should succeed, got err: %v", err)
	}
	if len(messages) != 3 {
		t.Fatalf("expected 3 album members, got %d", len(messages))
	}
	if messages[0].MessageID != 10 || messages[1].MessageID != 20 || messages[2].MessageID != 30 {
		t.Fatalf("expected source message order, got: %+v", messages)
	}

	empty, err := ListMessagesByMediaGroup("imbGZo", "")
	if err != nil {
		t.Fatalf("empty media group should succeed, got err: %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("expected no members for empty media group, got %d", len(empty))
	}
}

func TestHasSuccessfulSyncRecordForMediaGroup(t *testing.T) {
	setupTestDB(t)

	messageID, err := SaveMessage(newAlbumTestMessage(101, "gid-2"))
	if err != nil {
		t.Fatalf("save album message should succeed, got err: %v", err)
	}

	synced, err := HasSuccessfulSyncRecordForMediaGroup("imbGZo", "gid-2")
	if err != nil {
		t.Fatalf("query sync records should succeed, got err: %v", err)
	}
	if synced {
		t.Fatalf("expected no successful sync records before dispatch")
	}

	// 失败记录与手动重同步记录都不算已投递，自动路径仍应继续重试。
	for _, record := range []Entity.SyncRecord{
		{ArchivedMessageID: messageID, Platform: "BlueSky", Status: Entity.SyncStatusFailed, Trigger: Entity.SyncTriggerAutomatic},
		{ArchivedMessageID: messageID, Platform: "Mastodon", Status: Entity.SyncStatusSucceeded, Trigger: Entity.SyncTriggerManual},
	} {
		record.CreatedTime = time.Now()
		if _, err := SaveSyncRecord(&record); err != nil {
			t.Fatalf("save sync record should succeed, got err: %v", err)
		}
	}

	synced, err = HasSuccessfulSyncRecordForMediaGroup("imbGZo", "gid-2")
	if err != nil {
		t.Fatalf("query sync records should succeed, got err: %v", err)
	}
	if synced {
		t.Fatalf("expected failed/manual records not to count as delivered")
	}

	if _, err := SaveSyncRecord(&Entity.SyncRecord{
		ArchivedMessageID: messageID,
		Platform:          "BlueSky",
		Status:            Entity.SyncStatusSucceeded,
		Trigger:           Entity.SyncTriggerAutomatic,
		CreatedTime:       time.Now(),
	}); err != nil {
		t.Fatalf("save sync record should succeed, got err: %v", err)
	}

	synced, err = HasSuccessfulSyncRecordForMediaGroup("imbGZo", "gid-2")
	if err != nil {
		t.Fatalf("query sync records should succeed, got err: %v", err)
	}
	if !synced {
		t.Fatalf("expected album to be recognized as delivered")
	}

	other, err := HasSuccessfulSyncRecordForMediaGroup("imbGZo", "gid-other")
	if err != nil {
		t.Fatalf("query other group should succeed, got err: %v", err)
	}
	if other {
		t.Fatalf("expected unrelated group to stay unsynced")
	}
}

func TestListPendingMediaGroups(t *testing.T) {
	setupTestDB(t)

	recent := time.Now()
	syncedID, err := SaveMessage(newAlbumTestMessage(201, "gid-synced"))
	if err != nil {
		t.Fatalf("save synced album message should succeed, got err: %v", err)
	}
	if _, err := SaveMessage(newAlbumTestMessage(202, "gid-synced")); err != nil {
		t.Fatalf("save second synced member should succeed, got err: %v", err)
	}
	if _, err := SaveMessage(newAlbumTestMessage(203, "gid-pending")); err != nil {
		t.Fatalf("save pending album message should succeed, got err: %v", err)
	}
	if _, err := SaveMessage(newAlbumTestMessage(204, "")); err != nil {
		t.Fatalf("save non-album message should succeed, got err: %v", err)
	}

	stale := newAlbumTestMessage(205, "gid-stale")
	stale.CreatedTime = recent.Add(-48 * time.Hour)
	if _, err := SaveMessage(stale); err != nil {
		t.Fatalf("save stale album message should succeed, got err: %v", err)
	}

	if _, err := SaveSyncRecord(&Entity.SyncRecord{
		ArchivedMessageID: syncedID,
		Platform:          "BlueSky",
		Status:            Entity.SyncStatusSucceeded,
		Trigger:           Entity.SyncTriggerAutomatic,
		CreatedTime:       time.Now(),
	}); err != nil {
		t.Fatalf("save sync record should succeed, got err: %v", err)
	}

	// 失败记录不视为已投递：该分组仍应出现在待投递列表中。
	failedID, err := SaveMessage(newAlbumTestMessage(206, "gid-failed"))
	if err != nil {
		t.Fatalf("save failed album message should succeed, got err: %v", err)
	}
	if _, err := SaveSyncRecord(&Entity.SyncRecord{
		ArchivedMessageID: failedID,
		Platform:          "BlueSky",
		Status:            Entity.SyncStatusFailed,
		Trigger:           Entity.SyncTriggerAutomatic,
		CreatedTime:       time.Now(),
	}); err != nil {
		t.Fatalf("save failed sync record should succeed, got err: %v", err)
	}

	pending, err := ListPendingMediaGroups(recent.Add(-24 * time.Hour))
	if err != nil {
		t.Fatalf("list pending media groups should succeed, got err: %v", err)
	}
	if len(pending) != 2 {
		t.Fatalf("expected 2 pending media groups, got: %+v", pending)
	}
	byGroup := map[string]string{}
	for _, group := range pending {
		byGroup[group.MediaGroupID] = group.Username
	}
	if byGroup["gid-pending"] != "imbGZo" || byGroup["gid-failed"] != "imbGZo" {
		t.Fatalf("unexpected pending groups: %+v", pending)
	}
}

func TestUpdateMessageMediaGroup_OnlyFillsEmpty(t *testing.T) {
	setupTestDB(t)

	if _, err := SaveMessage(newAlbumTestMessage(301, "")); err != nil {
		t.Fatalf("save message should succeed, got err: %v", err)
	}

	affected, err := UpdateMessageMediaGroup("imbGZo", 301, "gid-3")
	if err != nil {
		t.Fatalf("backfill media group should succeed, got err: %v", err)
	}
	if affected != 1 {
		t.Fatalf("expected 1 updated row, got %d", affected)
	}

	affected, err = UpdateMessageMediaGroup("imbGZo", 301, "gid-other")
	if err != nil {
		t.Fatalf("second backfill should succeed, got err: %v", err)
	}
	if affected != 0 {
		t.Fatalf("expected no row update when media group already set, got %d", affected)
	}

	messages, err := ListMessagesByMediaGroup("imbGZo", "gid-3")
	if err != nil {
		t.Fatalf("list album messages should succeed, got err: %v", err)
	}
	if len(messages) != 1 {
		t.Fatalf("expected original media group to remain, got %d", len(messages))
	}
}
