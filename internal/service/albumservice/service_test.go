package albumservice

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

type capturedSchedule struct {
	delay    time.Duration
	callback func()
	stopper  *fakeStopper
}

type albumTestHarness struct {
	service    *Service
	schedules  []capturedSchedule
	dispatched []syncservice.Payload
	persisted  []int64
	sender     *fakeMessageSender
}

func (h *albumTestHarness) run(index int) {
	h.schedules[index].callback()
}

func (h *albumTestHarness) runAll() {
	for index := range h.schedules {
		h.schedules[index].callback()
	}
}

func newAlbumTestHarness(config Entity.Config, sender *fakeMessageSender) *albumTestHarness {
	h := &albumTestHarness{sender: sender}

	service := New(sender, config)
	service.afterFunc = func(delay time.Duration, callback func()) stopper {
		stopper := &fakeStopper{}
		h.schedules = append(h.schedules, capturedSchedule{delay: delay, callback: callback, stopper: stopper})
		return stopper
	}
	service.dispatch = func(_ Entity.Config, payload syncservice.Payload) []syncservice.DispatchResult {
		h.dispatched = append(h.dispatched, payload)
		return []syncservice.DispatchResult{{Platform: "BlueSky", Success: true, ImageRequested: true, UsedImage: true}}
	}
	service.persistResults = func(archivedMessageID int64, _ []syncservice.DispatchResult) error {
		h.persisted = append(h.persisted, archivedMessageID)
		return nil
	}

	h.service = service
	return h
}

func setupAlbumTestDB(t *testing.T) {
	t.Helper()

	// 使用共享缓存的内存库：SQLite 的 ":memory:" 是每连接一个库，
	// 并发登记/投递会从连接池拿到看不到表结构的新连接。
	dsn := fmt.Sprintf("file:albumservice-%d?mode=memory&cache=shared", time.Now().UnixNano())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
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

func TestRegister_FlushesWholeAlbumAfterQuietWindow(t *testing.T) {
	setupAlbumTestDB(t)

	channelDir := t.TempDir()
	messages := seedAlbumMessages(t, channelDir, "gid-1", 100, time.Now().Add(-time.Minute))

	config := Entity.Config{}
	config.Output.ChannelDir = channelDir

	sender := &fakeMessageSender{}
	h := newAlbumTestHarness(config, sender)

	for _, message := range messages {
		h.service.Register(Member{SourceID: "imbGZo", MediaGroupID: "gid-1", ChatID: 42, ArchivedMessageID: message.ID})
	}

	if len(h.schedules) != 3 {
		t.Fatalf("expected 3 scheduled flushes, got %d", len(h.schedules))
	}
	if h.schedules[0].stopper.stopCalls != 1 || h.schedules[1].stopper.stopCalls != 1 {
		t.Fatalf("expected previous timers to be stopped: %+v", h.schedules)
	}

	// 只触发最后一次定时器，模拟静默窗口真正到期。
	h.run(2)

	if len(h.dispatched) != 1 {
		t.Fatalf("expected one album dispatch, got %d", len(h.dispatched))
	}
	payload := h.dispatched[0]
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

	if len(h.persisted) != 3 {
		t.Fatalf("expected sync records for all members, got: %+v", h.persisted)
	}

	if len(sender.chatIDs) != 1 || sender.chatIDs[0] != "42" {
		t.Fatalf("expected single notification to member chat, got: %+v", sender.chatIDs)
	}
	if !strings.Contains(sender.texts[0], "相册消息（3 张图）") || !strings.Contains(sender.texts[0], "https://t.me/imbGZo/100") {
		t.Fatalf("unexpected album notification: %s", sender.texts[0])
	}

	h.service.mu.Lock()
	pendingCount := len(h.service.pending)
	h.service.mu.Unlock()
	if pendingCount != 0 {
		t.Fatalf("expected pending group to be removed after flush")
	}
}

func TestRegister_ReschedulesWhileAlbumStillFresh(t *testing.T) {
	setupAlbumTestDB(t)

	channelDir := t.TempDir()
	// 最新成员刚刚归档：交付时应检测到仍在窗口内并顺延，而不是直接投递。
	seedAlbumMessages(t, channelDir, "gid-fresh", 150, time.Now())

	config := Entity.Config{}
	config.Output.ChannelDir = channelDir
	h := newAlbumTestHarness(config, &fakeMessageSender{})

	h.service.Register(Member{SourceID: "imbGZo", MediaGroupID: "gid-fresh", ChatID: 42})
	h.run(0)

	if len(h.dispatched) != 0 {
		t.Fatalf("expected delivery to be postponed, got: %+v", h.dispatched)
	}
	if len(h.schedules) != 2 {
		t.Fatalf("expected a postponed timer, got %d schedules", len(h.schedules))
	}
	if h.schedules[1].delay <= 0 || h.schedules[1].delay > DefaultDebounce {
		t.Fatalf("expected remaining quiet window delay, got: %s", h.schedules[1].delay)
	}

	// 时间推进到窗口之外后，重排的定时器应完成投递。
	h.service.now = func() time.Time { return time.Now().Add(DefaultDebounce + time.Second) }
	h.run(1)
	if len(h.dispatched) != 1 {
		t.Fatalf("expected postponed delivery to complete, got: %d", len(h.dispatched))
	}
}

func TestRegister_FlushesImmediatelyWhenAlbumReachesMaxSize(t *testing.T) {
	setupAlbumTestDB(t)

	config := Entity.Config{}
	h := newAlbumTestHarness(config, &fakeMessageSender{})

	for i := 0; i < MaxAlbumSize; i++ {
		h.service.Register(Member{SourceID: "imbGZo", MediaGroupID: "gid-max", ChatID: 42, ArchivedMessageID: int64(i + 1)})
	}

	if len(h.schedules) != MaxAlbumSize {
		t.Fatalf("expected one schedule per member, got %d", len(h.schedules))
	}
	if h.schedules[MaxAlbumSize-1].delay != 0 {
		t.Fatalf("expected immediate flush at max album size, got delay: %s", h.schedules[MaxAlbumSize-1].delay)
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
	h := newAlbumTestHarness(config, &fakeMessageSender{})

	h.service.Register(Member{SourceID: "imbGZo", MediaGroupID: "gid-2", ChatID: 42})

	if len(h.schedules) != 0 {
		t.Fatalf("expected no scheduled flush for synced album")
	}
	if len(h.dispatched) != 0 {
		t.Fatalf("expected no dispatch for synced album")
	}
}

func TestRegister_SchedulesWhenSyncRecordCheckFails(t *testing.T) {
	setupAlbumTestDB(t)

	config := Entity.Config{}
	h := newAlbumTestHarness(config, &fakeMessageSender{})
	h.service.hasRecords = func(string, string) (bool, error) {
		return false, fmt.Errorf("db unavailable")
	}

	h.service.Register(Member{SourceID: "imbGZo", MediaGroupID: "gid-check", ChatID: 42})

	if len(h.schedules) != 1 {
		t.Fatalf("expected registration to survive record check failure, got %d schedules", len(h.schedules))
	}
}

func TestDeliver_SkipsWhenAlbumGetsSyncedBeforeFlush(t *testing.T) {
	setupAlbumTestDB(t)

	channelDir := t.TempDir()
	messages := seedAlbumMessages(t, channelDir, "gid-race", 500, time.Now().Add(-time.Minute))

	config := Entity.Config{}
	config.Output.ChannelDir = channelDir
	h := newAlbumTestHarness(config, &fakeMessageSender{})

	h.service.Register(Member{SourceID: "imbGZo", MediaGroupID: "gid-race", ChatID: 42})
	if len(h.schedules) != 1 {
		t.Fatalf("expected one scheduled flush, got %d", len(h.schedules))
	}

	// 模拟静默窗口内自动投递已完成：投递前复查应跳过。
	if _, err := Database.SaveSyncRecord(&Entity.SyncRecord{
		ArchivedMessageID: messages[0].ID,
		Platform:          "Mastodon",
		Status:            Entity.SyncStatusSucceeded,
		Trigger:           Entity.SyncTriggerAutomatic,
		CreatedTime:       time.Now(),
	}); err != nil {
		t.Fatalf("failed to seed sync record: %v", err)
	}

	h.run(0)
	if len(h.dispatched) != 0 {
		t.Fatalf("expected flush to skip already delivered album, got: %+v", h.dispatched)
	}
}

func TestDeliver_ProceedsWhenOnlyManualRecordsExist(t *testing.T) {
	setupAlbumTestDB(t)

	channelDir := t.TempDir()
	messages := seedAlbumMessages(t, channelDir, "gid-manual-record", 550, time.Now().Add(-time.Minute))

	// 手动重同步只覆盖了单个平台：自动投递不应被它抑制，否则其他平台永久缺帖。
	if _, err := Database.SaveSyncRecord(&Entity.SyncRecord{
		ArchivedMessageID: messages[0].ID,
		Platform:          "Twitter",
		Status:            Entity.SyncStatusSucceeded,
		Trigger:           Entity.SyncTriggerManual,
		CreatedTime:       time.Now(),
	}); err != nil {
		t.Fatalf("failed to seed manual sync record: %v", err)
	}

	config := Entity.Config{}
	config.Output.ChannelDir = channelDir
	h := newAlbumTestHarness(config, &fakeMessageSender{})

	h.service.Register(Member{SourceID: "imbGZo", MediaGroupID: "gid-manual-record", ChatID: 42})
	if len(h.schedules) != 1 {
		t.Fatalf("expected manual records not to suppress registration, got %d schedules", len(h.schedules))
	}

	h.run(0)
	if len(h.dispatched) != 1 {
		t.Fatalf("expected album delivery despite manual records, got: %+v", h.dispatched)
	}
}

func TestDeliver_RetriesWhenAllPlatformsFail(t *testing.T) {
	setupAlbumTestDB(t)

	channelDir := t.TempDir()
	messages := seedAlbumMessages(t, channelDir, "gid-all-failed", 700, time.Now().Add(-time.Minute))

	config := Entity.Config{}
	config.Output.ChannelDir = channelDir
	h := newAlbumTestHarness(config, &fakeMessageSender{})
	h.service.dispatch = func(_ Entity.Config, _ syncservice.Payload) []syncservice.DispatchResult {
		return []syncservice.DispatchResult{{Platform: "BlueSky", Success: false, ErrorMessage: "boom"}}
	}

	h.service.Register(Member{SourceID: "imbGZo", MediaGroupID: "gid-all-failed", ChatID: 42})
	h.run(0)

	if len(h.persisted) != len(messages) {
		t.Fatalf("expected failure records to be persisted, got: %+v", h.persisted)
	}
	if len(h.schedules) != 2 {
		t.Fatalf("expected a retry schedule after all-platform failure, got %d", len(h.schedules))
	}
}

func TestDeliver_RetriesWhenAlbumLoadFails(t *testing.T) {
	setupAlbumTestDB(t)

	channelDir := t.TempDir()
	seedAlbumMessages(t, channelDir, "gid-retry", 600, time.Now().Add(-time.Minute))

	config := Entity.Config{}
	config.Output.ChannelDir = channelDir
	h := newAlbumTestHarness(config, &fakeMessageSender{})

	loadCalls := 0
	originalLoad := h.service.loadMessages
	h.service.loadMessages = func(sourceID string, mediaGroupID string) ([]Entity.Message, error) {
		loadCalls++
		if loadCalls == 1 {
			return nil, fmt.Errorf("temporary failure")
		}
		return originalLoad(sourceID, mediaGroupID)
	}

	h.service.Register(Member{SourceID: "imbGZo", MediaGroupID: "gid-retry", ChatID: 42})
	h.run(0)

	if len(h.dispatched) != 0 {
		t.Fatalf("expected first attempt to fail, got: %+v", h.dispatched)
	}
	if len(h.schedules) != 2 {
		t.Fatalf("expected a retry schedule, got %d", len(h.schedules))
	}

	h.run(1)
	if len(h.dispatched) != 1 {
		t.Fatalf("expected retry to deliver album, got %d", len(h.dispatched))
	}
}

func TestDeliver_RetriesWhenDispatchReturnsNoResults(t *testing.T) {
	setupAlbumTestDB(t)

	channelDir := t.TempDir()
	seedAlbumMessages(t, channelDir, "gid-no-results", 750, time.Now().Add(-time.Minute))

	config := Entity.Config{}
	config.Output.ChannelDir = channelDir
	h := newAlbumTestHarness(config, &fakeMessageSender{})
	h.service.dispatch = func(_ Entity.Config, _ syncservice.Payload) []syncservice.DispatchResult {
		return nil
	}

	h.service.Register(Member{SourceID: "imbGZo", MediaGroupID: "gid-no-results", ChatID: 42})
	h.run(0)

	if len(h.schedules) != 2 {
		t.Fatalf("expected retry schedule for empty dispatch results, got %d", len(h.schedules))
	}
}

func TestDeliver_GivesUpAfterMaxAttempts(t *testing.T) {
	setupAlbumTestDB(t)

	config := Entity.Config{}
	h := newAlbumTestHarness(config, &fakeMessageSender{})
	h.service.loadMessages = func(string, string) ([]Entity.Message, error) {
		return nil, fmt.Errorf("persistent failure")
	}

	h.service.Register(Member{SourceID: "imbGZo", MediaGroupID: "gid-giveup", ChatID: 42})

	// 依次触发首发 + 重试，达到上限后不再重排。
	for index := 0; index < maxDeliveryAttempts+1 && index < len(h.schedules); index++ {
		h.run(index)
	}
	if len(h.schedules) != maxDeliveryAttempts {
		t.Fatalf("expected %d attempts before giving up, got %d", maxDeliveryAttempts, len(h.schedules))
	}
	if len(h.dispatched) != 0 {
		t.Fatalf("expected no delivery, got: %+v", h.dispatched)
	}
}

func TestRecoverPending_SchedulesStaleTargetAlbumsImmediately(t *testing.T) {
	setupAlbumTestDB(t)

	channelDir := t.TempDir()
	seedAlbumMessages(t, channelDir, "gid-recover", 300, time.Now().Add(-10*time.Minute))

	otherSource := Entity.Message{
		MessageID:    900,
		Username:     "otherChannel",
		MediaGroupID: "gid-other",
		MessageUrl:   "https://t.me/otherChannel/900",
		MessageDate:  time.Now().Add(-10 * time.Minute),
		CreatedTime:  time.Now().Add(-10 * time.Minute),
	}
	if _, err := Database.SaveMessage(&otherSource); err != nil {
		t.Fatalf("failed to save other source album: %v", err)
	}

	config := Entity.Config{}
	config.Output.ChannelDir = channelDir
	config.SocialMediaSync.Enable = true
	config.SocialMediaSync.TargetChannel = []string{"imbGZo"}

	h := newAlbumTestHarness(config, &fakeMessageSender{})
	h.service.RecoverPending()

	if len(h.schedules) != 1 {
		t.Fatalf("expected only stale target album to be recovered, got %d", len(h.schedules))
	}
	if h.schedules[0].delay != 0 {
		t.Fatalf("expected immediate recovery, got delay: %s", h.schedules[0].delay)
	}

	h.run(0)
	if len(h.dispatched) != 1 {
		t.Fatalf("expected recovered album to dispatch once, got %d", len(h.dispatched))
	}
	if len(h.dispatched[0].ImagePaths()) != 3 {
		t.Fatalf("expected recovered album images, got: %+v", h.dispatched[0].ImagePaths())
	}
}

func TestRecoverPending_KeepsInFlightAlbumsWithRemainingDelay(t *testing.T) {
	setupAlbumTestDB(t)

	channelDir := t.TempDir()
	seedAlbumMessages(t, channelDir, "gid-inflight", 400, time.Now())

	config := Entity.Config{}
	config.Output.ChannelDir = channelDir
	config.SocialMediaSync.Enable = true
	config.SocialMediaSync.TargetChannel = []string{"imbGZo"}

	h := newAlbumTestHarness(config, &fakeMessageSender{})
	h.service.RecoverPending()

	if len(h.schedules) != 1 {
		t.Fatalf("expected in-flight album to be recovered, got %d", len(h.schedules))
	}
	if h.schedules[0].delay <= 0 || h.schedules[0].delay > DefaultDebounce {
		t.Fatalf("expected remaining quiet window delay, got: %s", h.schedules[0].delay)
	}
}

func TestNew_ResolvesAlbumDebounceFromConfig(t *testing.T) {
	config := Entity.Config{}
	if got := New(nil, config).debounce; got != DefaultDebounce {
		t.Fatalf("expected default debounce, got: %s", got)
	}

	config.SocialMediaSync.AlbumDebounceSeconds = 3
	if got := New(nil, config).debounce; got != 3*time.Second {
		t.Fatalf("expected configured debounce, got: %s", got)
	}

	config.SocialMediaSync.AlbumDebounceSeconds = -1
	if got := New(nil, config).debounce; got != DefaultDebounce {
		t.Fatalf("expected invalid debounce to fall back to default, got: %s", got)
	}
}

func TestRegister_ConcurrentRegistrationAndFlushDoesNotRace(t *testing.T) {
	setupAlbumTestDB(t)

	channelDir := t.TempDir()
	seedAlbumMessages(t, channelDir, "gid-concurrent", 800, time.Now().Add(-time.Minute))

	config := Entity.Config{}
	config.Output.ChannelDir = channelDir
	config.SocialMediaSync.AlbumDebounceSeconds = 1

	service := New(nil, config)
	dispatched := make(chan struct{}, 1)
	service.dispatch = func(_ Entity.Config, _ syncservice.Payload) []syncservice.DispatchResult {
		select {
		case dispatched <- struct{}{}:
		default:
		}
		return []syncservice.DispatchResult{{Platform: "BlueSky", Success: true}}
	}
	service.persistResults = func(int64, []syncservice.DispatchResult) error { return nil }

	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			service.Register(Member{SourceID: "imbGZo", MediaGroupID: "gid-concurrent", ChatID: 42, ArchivedMessageID: int64(i + 1)})
		}(i)
	}
	wg.Wait()

	select {
	case <-dispatched:
	case <-time.After(5 * time.Second):
		t.Fatalf("expected album dispatch after quiet window")
	}
}
