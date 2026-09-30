package albumservice

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"telegram-message-sync-bot/internal/Database"
	"telegram-message-sync-bot/internal/Entity"
	"telegram-message-sync-bot/internal/service/syncservice"
)

type fakeStopper struct {
	stopCalls int
}

func (f *fakeStopper) Stop() bool {
	f.stopCalls++
	return true
}

type fakeMessageSender struct {
	chatIDs []string
	texts   []string
}

func (f *fakeMessageSender) SendMessage(_ context.Context, params *bot.SendMessageParams) (*models.Message, error) {
	f.chatIDs = append(f.chatIDs, fmt.Sprintf("%v", params.ChatID))
	f.texts = append(f.texts, params.Text)
	return &models.Message{}, nil
}

func setupAlbumTestDB(t *testing.T) {
	t.Helper()

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open sqlite memory db: %v", err)
	}
	if err := db.AutoMigrate(&Entity.Message{}, &Entity.Attachment{}, &Entity.SyncRecord{}); err != nil {
		t.Fatalf("failed to migrate tables: %v", err)
	}
	Database.DB = db
}

func writeAlbumTestImage(t *testing.T, dir string, name string) string {
	t.Helper()

	path := filepath.Join(dir, "assets", "imbGZo", name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("failed to create asset dir: %v", err)
	}
	if err := os.WriteFile(path, []byte("img"), 0o644); err != nil {
		t.Fatalf("failed to create asset: %v", err)
	}
	return filepath.Join("assets", "imbGZo", name)
}

func seedAlbumMessages(t *testing.T, channelDir string, mediaGroupID string, baseMessageID int64, created time.Time) []Entity.Message {
	t.Helper()

	relativePaths := []string{
		writeAlbumTestImage(t, channelDir, "one.jpg"),
		writeAlbumTestImage(t, channelDir, "two.jpg"),
		writeAlbumTestImage(t, channelDir, "three.jpg"),
	}

	messages := make([]Entity.Message, 0, len(relativePaths))
	for index, relativePath := range relativePaths {
		message := Entity.Message{
			MessageID:    baseMessageID + int64(index),
			Username:     "imbGZo",
			MediaGroupID: mediaGroupID,
			MessageUrl:   "https://t.me/imbGZo/100",
			MessageDate:  created,
			CreatedTime:  created,
			Attachments:  []Entity.Attachment{{Type: Entity.ImageMessage, FilePath: relativePath}},
		}
		if index == 0 {
			message.Content = "album caption"
		}
		if _, err := Database.SaveMessage(&message); err != nil {
			t.Fatalf("failed to save album member: %v", err)
		}
		messages = append(messages, message)
	}
	return messages
}

func newCapturingService(t *testing.T, config Entity.Config, sender MessageSender) (*Service, *[]func(), *[]*fakeStopper, *[]syncservice.Payload, *[]int64) {
	t.Helper()

	service := New(sender, config)

	callbacks := make([]func(), 0, 4)
	stoppers := make([]*fakeStopper, 0, 4)
	service.afterFunc = func(_ time.Duration, callback func()) stopper {
		callbacks = append(callbacks, callback)
		stopper := &fakeStopper{}
		stoppers = append(stoppers, stopper)
		return stopper
	}

	dispatched := make([]syncservice.Payload, 0, 1)
	service.dispatch = func(_ Entity.Config, payload syncservice.Payload) []syncservice.DispatchResult {
		dispatched = append(dispatched, payload)
		return []syncservice.DispatchResult{{Platform: "BlueSky", Success: true, ImageRequested: true, UsedImage: true}}
	}

	persisted := make([]int64, 0, 4)
	service.persistResults = func(archivedMessageID int64, _ []syncservice.DispatchResult) error {
		persisted = append(persisted, archivedMessageID)
		return nil
	}

	return service, &callbacks, &stoppers, &dispatched, &persisted
}

func TestRegister_FlushesWholeAlbumAfterQuietWindow(t *testing.T) {
	setupAlbumTestDB(t)

	channelDir := t.TempDir()
	seedAlbumMessages(t, channelDir, "gid-1", 100, time.Now().Add(-time.Minute))

	config := Entity.Config{}
	config.Output.ChannelDir = channelDir

	sender := &fakeMessageSender{}
	service, callbacks, stoppers, dispatched, persisted := newCapturingService(t, config, sender)

	for i := 0; i < 3; i++ {
		service.Register(Member{SourceID: "imbGZo", MediaGroupID: "gid-1", ChatID: 42})
	}

	if len(*callbacks) != 3 {
		t.Fatalf("expected 3 scheduled flushes, got %d", len(*callbacks))
	}
	if (*stoppers)[0].stopCalls != 1 || (*stoppers)[1].stopCalls != 1 {
		t.Fatalf("expected previous timers to be stopped: %+v", *stoppers)
	}

	// 只触发最后一次定时器，模拟静默窗口真正到期。
	(*callbacks)[2]()

	if len(*dispatched) != 1 {
		t.Fatalf("expected one album dispatch, got %d", len(*dispatched))
	}
	payload := (*dispatched)[0]
	if payload.Text != "album caption" {
		t.Fatalf("expected album head text, got: %s", payload.Text)
	}
	paths := payload.ImagePaths()
	if len(paths) != 3 {
		t.Fatalf("expected 3 album images, got: %+v", paths)
	}
	for _, path := range paths {
		if !strings.HasPrefix(path, channelDir) {
			t.Fatalf("expected absolute channel asset path, got: %s", path)
		}
	}

	if len(*persisted) != 3 {
		t.Fatalf("expected sync records for all members, got: %+v", *persisted)
	}

	if len(sender.chatIDs) != 1 || sender.chatIDs[0] != "42" {
		t.Fatalf("expected single notification to member chat, got: %+v", sender.chatIDs)
	}
	if !strings.Contains(sender.texts[0], "相册消息（3 张图）") || !strings.Contains(sender.texts[0], "https://t.me/imbGZo/100") {
		t.Fatalf("unexpected album notification: %s", sender.texts[0])
	}

	service.mu.Lock()
	pendingCount := len(service.pending)
	service.mu.Unlock()
	if pendingCount != 0 {
		t.Fatalf("expected pending group to be removed after flush")
	}
}

func TestDeliver_SkipsWhenAlbumGetsSyncedBeforeFlush(t *testing.T) {
	setupAlbumTestDB(t)

	channelDir := t.TempDir()
	messages := seedAlbumMessages(t, channelDir, "gid-race", 500, time.Now().Add(-time.Minute))

	config := Entity.Config{}
	config.Output.ChannelDir = channelDir
	service, callbacks, _, dispatched, _ := newCapturingService(t, config, &fakeMessageSender{})

	service.Register(Member{SourceID: "imbGZo", MediaGroupID: "gid-race", ChatID: 42})
	if len(*callbacks) != 1 {
		t.Fatalf("expected one scheduled flush, got %d", len(*callbacks))
	}

	// 模拟静默窗口内发生了手动重同步：投递前应重新检查并跳过。
	if _, err := Database.SaveSyncRecord(&Entity.SyncRecord{
		ArchivedMessageID: messages[0].ID,
		Platform:          "Mastodon",
		Status:            Entity.SyncStatusSucceeded,
		Trigger:           Entity.SyncTriggerManual,
		CreatedTime:       time.Now(),
	}); err != nil {
		t.Fatalf("failed to seed sync record: %v", err)
	}

	(*callbacks)[0]()
	if len(*dispatched) != 0 {
		t.Fatalf("expected flush to skip already synced album, got: %+v", *dispatched)
	}
}

func TestRegister_SkipsGroupsWithExistingSyncRecords(t *testing.T) {
	setupAlbumTestDB(t)

	channelDir := t.TempDir()
	messages := seedAlbumMessages(t, channelDir, "gid-2", 200, time.Now().Add(-time.Minute))

	if _, err := Database.SaveSyncRecord(&Entity.SyncRecord{
		ArchivedMessageID: messages[0].ID,
		Platform:          "BlueSky",
		Status:            Entity.SyncStatusSucceeded,
		Trigger:           Entity.SyncTriggerAutomatic,
		CreatedTime:       time.Now(),
	}); err != nil {
		t.Fatalf("failed to seed sync record: %v", err)
	}

	config := Entity.Config{}
	config.Output.ChannelDir = channelDir
	service, callbacks, _, dispatched, _ := newCapturingService(t, config, &fakeMessageSender{})

	service.Register(Member{SourceID: "imbGZo", MediaGroupID: "gid-2", ChatID: 42})

	if len(*callbacks) != 0 {
		t.Fatalf("expected no scheduled flush for synced album")
	}
	if len(*dispatched) != 0 {
		t.Fatalf("expected no dispatch for synced album")
	}
}

func TestRecoverPending_SchedulesOnlyRecentTargetAlbums(t *testing.T) {
	setupAlbumTestDB(t)

	channelDir := t.TempDir()
	seedAlbumMessages(t, channelDir, "gid-recover", 300, time.Now().Add(-10*time.Minute))
	seedAlbumMessages(t, channelDir, "gid-inflight", 400, time.Now())

	staleOther := Entity.Message{
		MessageID:    900,
		Username:     "otherChannel",
		MediaGroupID: "gid-other",
		MessageUrl:   "https://t.me/otherChannel/900",
		MessageDate:  time.Now().Add(-10 * time.Minute),
		CreatedTime:  time.Now().Add(-10 * time.Minute),
	}
	if _, err := Database.SaveMessage(&staleOther); err != nil {
		t.Fatalf("failed to save other source album: %v", err)
	}

	config := Entity.Config{}
	config.Output.ChannelDir = channelDir
	config.SocialMediaSync.Enable = true
	config.SocialMediaSync.TargetChannel = []string{"imbGZo"}

	service, callbacks, _, dispatched, _ := newCapturingService(t, config, &fakeMessageSender{})

	service.RecoverPending()

	if len(*callbacks) != 1 {
		t.Fatalf("expected only stale target album to be recovered, got %d", len(*callbacks))
	}

	(*callbacks)[0]()
	if len(*dispatched) != 1 {
		t.Fatalf("expected recovered album to dispatch once, got %d", len(*dispatched))
	}
	if len((*dispatched)[0].ImagePaths()) != 3 {
		t.Fatalf("expected recovered album images, got: %+v", (*dispatched)[0].ImagePaths())
	}
}
