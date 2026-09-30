package pipelineservice

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"telegram-message-sync-bot/internal/Database"
	"telegram-message-sync-bot/internal/Entity"
	"telegram-message-sync-bot/internal/service/albumservice"
	"telegram-message-sync-bot/internal/service/archiveservice"
	"telegram-message-sync-bot/internal/service/notifyservice"
	"telegram-message-sync-bot/internal/service/syncservice"
)

type fakeArchiveStage struct {
	run func(ctx context.Context, b *bot.Bot, update *models.Update, config Entity.Config) archiveservice.PersistResult
}

func (f fakeArchiveStage) Run(ctx context.Context, b *bot.Bot, update *models.Update, config Entity.Config) archiveservice.PersistResult {
	return f.run(ctx, b, update, config)
}

type fakeSyncStage struct {
	run func(config Entity.Config, persistResult archiveservice.PersistResult) (bool, string, []syncservice.DispatchResult)
}

func (f fakeSyncStage) Run(config Entity.Config, persistResult archiveservice.PersistResult) (bool, string, []syncservice.DispatchResult) {
	return f.run(config, persistResult)
}

type fakeNotifyStage struct {
	run func(config Entity.Config, update *models.Update, persistResult archiveservice.PersistResult, syncEnabled bool, syncReason string, dispatchResults []syncservice.DispatchResult) []notifyservice.OutboundMessage
}

func (f fakeNotifyStage) Run(config Entity.Config, update *models.Update, persistResult archiveservice.PersistResult, syncEnabled bool, syncReason string, dispatchResults []syncservice.DispatchResult) []notifyservice.OutboundMessage {
	return f.run(config, update, persistResult, syncEnabled, syncReason, dispatchResults)
}

func TestProcessUpdate_StageOrderAndOutput(t *testing.T) {
	order := make([]string, 0)

	p := Pipeline{
		ArchiveStage: fakeArchiveStage{run: func(_ context.Context, _ *bot.Bot, _ *models.Update, _ Entity.Config) archiveservice.PersistResult {
			order = append(order, "archive")
			return archiveservice.PersistResult{OK: true, Message: "file.md", SourceLink: "link", MsgText: "content", SourceID: "imbGZo"}
		}},
		SyncStage: fakeSyncStage{run: func(_ Entity.Config, _ archiveservice.PersistResult) (bool, string, []syncservice.DispatchResult) {
			order = append(order, "sync")
			return true, "", []syncservice.DispatchResult{{Platform: "BlueSky", Success: true}}
		}},
		NotifyStage: fakeNotifyStage{run: func(_ Entity.Config, _ *models.Update, _ archiveservice.PersistResult, _ bool, _ string, _ []syncservice.DispatchResult) []notifyservice.OutboundMessage {
			order = append(order, "notify")
			return []notifyservice.OutboundMessage{{ChatID: 1, Text: "archive"}, {ChatID: 1, Text: "sync-ok"}}
		}},
	}

	update := &models.Update{Message: &models.Message{Chat: models.Chat{ID: 1}}}
	result := p.ProcessUpdate(context.Background(), nil, update, Entity.Config{})

	expectedOrder := []string{"archive", "sync", "notify"}
	if !reflect.DeepEqual(order, expectedOrder) {
		t.Fatalf("unexpected stage order: %+v", order)
	}

	if len(result.OutboundMessages) != 2 {
		t.Fatalf("unexpected outbound size: %d", len(result.OutboundMessages))
	}
	if !result.SyncEnabled {
		t.Fatalf("expected sync enabled")
	}
}

func TestProcessUpdate_WhenSyncDisabled_StillNotify(t *testing.T) {
	notifyCalled := false

	p := Pipeline{
		ArchiveStage: fakeArchiveStage{run: func(_ context.Context, _ *bot.Bot, _ *models.Update, _ Entity.Config) archiveservice.PersistResult {
			return archiveservice.PersistResult{OK: true, Message: "file.md", SourceLink: "link", MsgText: "content", SourceID: "other"}
		}},
		SyncStage: fakeSyncStage{run: func(_ Entity.Config, _ archiveservice.PersistResult) (bool, string, []syncservice.DispatchResult) {
			return false, "skip", nil
		}},
		NotifyStage: fakeNotifyStage{run: func(_ Entity.Config, _ *models.Update, _ archiveservice.PersistResult, syncEnabled bool, syncReason string, _ []syncservice.DispatchResult) []notifyservice.OutboundMessage {
			notifyCalled = true
			if syncEnabled {
				t.Fatalf("sync should be disabled")
			}
			if syncReason != "skip" {
				t.Fatalf("unexpected sync reason: %s", syncReason)
			}
			return []notifyservice.OutboundMessage{{ChatID: 1, Text: "archive"}, {ChatID: 1, Text: "skip"}}
		}},
	}

	update := &models.Update{Message: &models.Message{Chat: models.Chat{ID: 1}}}
	result := p.ProcessUpdate(context.Background(), nil, update, Entity.Config{})

	if !notifyCalled {
		t.Fatalf("notify stage should be called when sync is disabled")
	}
	if result.SyncEnabled {
		t.Fatalf("expected sync disabled")
	}
	if result.SyncReason != "skip" {
		t.Fatalf("unexpected sync reason: %s", result.SyncReason)
	}
}

func TestProcessUpdate_NilUpdate_ReturnEmpty(t *testing.T) {
	p := NewDefaultPipeline(nil)
	result := p.ProcessUpdate(context.Background(), nil, nil, Entity.Config{})
	if result.PersistResult.OK {
		t.Fatalf("expected empty result when update is nil")
	}
	if len(result.OutboundMessages) != 0 {
		t.Fatalf("expected no outbound messages for nil update")
	}
}

func TestProcessUpdate_AsyncExperimental_ConsistencyWithSerial(t *testing.T) {
	update := &models.Update{Message: &models.Message{Chat: models.Chat{ID: 1}}}

	buildPipeline := func() Pipeline {
		return Pipeline{
			ArchiveStage: fakeArchiveStage{run: func(_ context.Context, _ *bot.Bot, _ *models.Update, _ Entity.Config) archiveservice.PersistResult {
				return archiveservice.PersistResult{OK: true, Message: "file.md", SourceLink: "link", MsgText: "content", SourceID: "imbGZo"}
			}},
			SyncStage: fakeSyncStage{run: func(_ Entity.Config, _ archiveservice.PersistResult) (bool, string, []syncservice.DispatchResult) {
				return true, "", []syncservice.DispatchResult{{Platform: "BlueSky", Success: true}, {Platform: "Twitter", Success: false}}
			}},
			NotifyStage: fakeNotifyStage{run: func(_ Entity.Config, _ *models.Update, _ archiveservice.PersistResult, _ bool, _ string, results []syncservice.DispatchResult) []notifyservice.OutboundMessage {
				if len(results) != 2 {
					t.Fatalf("unexpected dispatch result size: %d", len(results))
				}
				return []notifyservice.OutboundMessage{{ChatID: 1, Text: "archive"}, {ChatID: 1, Text: "sync-ok"}, {ChatID: 1, Text: "sync-fail"}}
			}},
			Mode: ExecutionModeSerial,
		}
	}

	serialPipeline := buildPipeline()
	serialResult := serialPipeline.ProcessUpdate(context.Background(), nil, update, Entity.Config{})

	asyncPipeline := buildPipeline()
	asyncPipeline.SetExecutionMode(ExecutionModeAsyncExperimental)
	asyncResult := asyncPipeline.ProcessUpdate(context.Background(), nil, update, Entity.Config{})

	if !reflect.DeepEqual(serialResult, asyncResult) {
		t.Fatalf("async experimental result differs from serial\nserial=%+v\nasync=%+v", serialResult, asyncResult)
	}
}

func TestProcessUpdate_EmptyMode_FallbackToSerial(t *testing.T) {
	called := false
	p := Pipeline{
		ArchiveStage: fakeArchiveStage{run: func(_ context.Context, _ *bot.Bot, _ *models.Update, _ Entity.Config) archiveservice.PersistResult {
			called = true
			return archiveservice.PersistResult{OK: true, Message: "file.md", SourceLink: "link", MsgText: "content", SourceID: "imbGZo"}
		}},
		SyncStage: fakeSyncStage{run: func(_ Entity.Config, _ archiveservice.PersistResult) (bool, string, []syncservice.DispatchResult) {
			return false, "skip", nil
		}},
		NotifyStage: fakeNotifyStage{run: func(_ Entity.Config, _ *models.Update, _ archiveservice.PersistResult, _ bool, _ string, _ []syncservice.DispatchResult) []notifyservice.OutboundMessage {
			return nil
		}},
		Mode: "",
	}

	update := &models.Update{Message: &models.Message{Chat: models.Chat{ID: 1}}}
	_ = p.ProcessUpdate(context.Background(), nil, update, Entity.Config{})

	if !called {
		t.Fatalf("expected serial fallback to execute archive stage")
	}
}

func TestResolveExecutionMode_FromConfig(t *testing.T) {
	config := Entity.Config{}
	config.Pipeline.ExecutionMode = "async_experimental"
	if ResolveExecutionMode(config) != ExecutionModeAsyncExperimental {
		t.Fatalf("expected async_experimental mode")
	}

	config.Pipeline.ExecutionMode = "SERIAL"
	if ResolveExecutionMode(config) != ExecutionModeSerial {
		t.Fatalf("expected serial mode for case-insensitive input")
	}

	config.Pipeline.ExecutionMode = "unknown"
	if ResolveExecutionMode(config) != ExecutionModeSerial {
		t.Fatalf("expected serial fallback for unknown mode")
	}
}

func TestProcessUpdate_ModeFromConfig_Equivalent(t *testing.T) {
	update := &models.Update{Message: &models.Message{Chat: models.Chat{ID: 1}}}
	config := Entity.Config{}
	config.Pipeline.ExecutionMode = "async_experimental"

	buildPipeline := func() Pipeline {
		return Pipeline{
			ArchiveStage: fakeArchiveStage{run: func(_ context.Context, _ *bot.Bot, _ *models.Update, _ Entity.Config) archiveservice.PersistResult {
				return archiveservice.PersistResult{OK: true, Message: "file.md", SourceLink: "link", MsgText: "content", SourceID: "imbGZo"}
			}},
			SyncStage: fakeSyncStage{run: func(_ Entity.Config, _ archiveservice.PersistResult) (bool, string, []syncservice.DispatchResult) {
				return true, "", []syncservice.DispatchResult{{Platform: "BlueSky", Success: true}}
			}},
			NotifyStage: fakeNotifyStage{run: func(_ Entity.Config, _ *models.Update, _ archiveservice.PersistResult, _ bool, _ string, _ []syncservice.DispatchResult) []notifyservice.OutboundMessage {
				return []notifyservice.OutboundMessage{{ChatID: 1, Text: "archive"}, {ChatID: 1, Text: "sync-ok"}}
			}},
			Mode: ExecutionModeSerial,
		}
	}

	serialPipeline := buildPipeline()
	serialResult := serialPipeline.ProcessUpdate(context.Background(), nil, update, config)

	asyncPipeline := buildPipeline()
	asyncPipeline.SetExecutionMode(ResolveExecutionMode(config))
	asyncResult := asyncPipeline.ProcessUpdate(context.Background(), nil, update, config)

	if !reflect.DeepEqual(serialResult, asyncResult) {
		t.Fatalf("mode from config should keep equivalent result\nserial=%+v\nasync=%+v", serialResult, asyncResult)
	}
}

type captureSender struct {
	payload syncservice.Payload
}

func (c *captureSender) Name() string {
	return "capture"
}

func (c *captureSender) Send(_ Entity.Config, payload syncservice.Payload) syncservice.DispatchResult {
	c.payload = payload
	return syncservice.DispatchResult{Platform: "capture", Success: true, ImageRequested: payload.Image != nil, UsedImage: payload.Image != nil}
}

func TestDefaultSyncStage_BuildPayloadWithImagePath(t *testing.T) {
	sender := &captureSender{}
	originalFactory := defaultSendersFactory
	defaultSendersFactory = func() []syncservice.Sender {
		return []syncservice.Sender{sender}
	}
	defer func() {
		defaultSendersFactory = originalFactory
	}()

	config := Entity.Config{}
	config.SocialMediaSync.Enable = true
	config.SocialMediaSync.TargetChannel = []string{"imbGZo"}
	imagePath := filepath.Join(t.TempDir(), "test.jpg")
	if err := os.WriteFile(imagePath, []byte("img"), 0o644); err != nil {
		t.Fatalf("failed to create test image: %v", err)
	}

	stage := defaultSyncStage{}
	enabled, reason, results := stage.Run(config, archiveservice.PersistResult{
		SourceID:  "imbGZo",
		MsgText:   "content",
		ImagePath: imagePath,
	})

	if !enabled {
		t.Fatalf("expected sync enabled, got reason: %s", reason)
	}
	if len(results) != 1 || !results[0].Success {
		t.Fatalf("unexpected dispatch results: %+v", results)
	}
	if sender.payload.Text != "content" {
		t.Fatalf("unexpected payload text: %+v", sender.payload)
	}
	if sender.payload.Image == nil || sender.payload.Image.FilePath != imagePath {
		t.Fatalf("unexpected payload image: %+v", sender.payload)
	}
}

func TestDefaultSyncStage_ResolvesRelativeImagePathFromChannelAssets(t *testing.T) {
	sender := &captureSender{}
	originalFactory := defaultSendersFactory
	defaultSendersFactory = func() []syncservice.Sender {
		return []syncservice.Sender{sender}
	}
	defer func() {
		defaultSendersFactory = originalFactory
	}()

	root := t.TempDir()
	channelDir := filepath.Join(root, "channel")
	absImagePath := filepath.Join(channelDir, "assets", "imbGZo", "test.jpg")
	if err := os.MkdirAll(filepath.Dir(absImagePath), 0o755); err != nil {
		t.Fatalf("failed to create image dir: %v", err)
	}
	if err := os.WriteFile(absImagePath, []byte("img"), 0o644); err != nil {
		t.Fatalf("failed to create test image: %v", err)
	}

	config := Entity.Config{}
	config.Output.ChannelDir = channelDir
	config.SocialMediaSync.Enable = true
	config.SocialMediaSync.TargetChannel = []string{"imbGZo"}

	stage := defaultSyncStage{}
	enabled, reason, results := stage.Run(config, archiveservice.PersistResult{
		SourceID:  "imbGZo",
		MsgText:   "content",
		ImagePath: filepath.Join("assets", "imbGZo", "test.jpg"),
	})

	if !enabled {
		t.Fatalf("expected sync enabled, got reason: %s", reason)
	}
	if len(results) != 1 || !results[0].Success {
		t.Fatalf("unexpected dispatch results: %+v", results)
	}
	if sender.payload.Image == nil || sender.payload.Image.FilePath != absImagePath {
		t.Fatalf("unexpected payload image: %+v", sender.payload)
	}
}

func TestDefaultSyncStage_PersistDispatchResults(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open sqlite memory db: %v", err)
	}
	if err := db.AutoMigrate(&Entity.Message{}, &Entity.Attachment{}, &Entity.SyncRecord{}); err != nil {
		t.Fatalf("failed to migrate tables: %v", err)
	}
	Database.DB = db

	message := &Entity.Message{
		MessageID:   3001,
		Username:    "imbGZo",
		Content:     "content",
		MessageUrl:  "https://t.me/imbGZo/3001",
		MessageDate: time.Now(),
		CreatedTime: time.Now(),
	}
	archivedMessageID, err := Database.SaveMessage(message)
	if err != nil {
		t.Fatalf("failed to save message: %v", err)
	}

	sender := &captureSender{}
	originalFactory := defaultSendersFactory
	defaultSendersFactory = func() []syncservice.Sender {
		return []syncservice.Sender{sender}
	}
	defer func() {
		defaultSendersFactory = originalFactory
	}()

	config := Entity.Config{}
	config.SocialMediaSync.Enable = true
	config.SocialMediaSync.TargetChannel = []string{"imbGZo"}

	stage := defaultSyncStage{}
	_, _, results := stage.Run(config, archiveservice.PersistResult{
		SourceID:          "imbGZo",
		MsgText:           "content",
		ArchivedMessageID: archivedMessageID,
	})

	if len(results) != 1 || !results[0].Success {
		t.Fatalf("unexpected dispatch results: %+v", results)
	}

	records, err := Database.ListSyncRecordsByMessage(archivedMessageID)
	if err != nil {
		t.Fatalf("list sync records should succeed, got err: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("expected one sync record, got %d", len(records))
	}
	if records[0].Platform != "capture" || records[0].Status != Entity.SyncStatusSucceeded {
		t.Fatalf("unexpected persisted sync record: %+v", records[0])
	}
}

type fakeAlbumRegistrar struct {
	members []albumservice.Member
}

func (f *fakeAlbumRegistrar) Register(member albumservice.Member) {
	f.members = append(f.members, member)
}

func TestDefaultSyncStage_AlbumMemberDefersToAlbumRegistrar(t *testing.T) {
	registrar := &fakeAlbumRegistrar{}
	sender := &captureSender{}
	originalFactory := defaultSendersFactory
	defaultSendersFactory = func() []syncservice.Sender {
		return []syncservice.Sender{sender}
	}
	defer func() {
		defaultSendersFactory = originalFactory
	}()

	config := Entity.Config{}
	config.SocialMediaSync.Enable = true
	config.SocialMediaSync.TargetChannel = []string{"imbGZo"}

	stage := defaultSyncStage{albums: registrar}
	enabled, reason, results := stage.Run(config, archiveservice.PersistResult{
		SourceID:          "imbGZo",
		MsgText:           "album caption",
		MediaGroupID:      "gid-1",
		ChatID:            -1001,
		ArchivedMessageID: 77,
	})

	if !enabled {
		t.Fatalf("expected sync enabled for album member, reason: %s", reason)
	}
	if len(results) != 0 {
		t.Fatalf("expected no immediate dispatch results, got: %+v", results)
	}
	if sender.payload.Text != "" {
		t.Fatalf("expected immediate dispatch to be skipped, got: %+v", sender.payload)
	}
	if len(registrar.members) != 1 {
		t.Fatalf("expected album member registration, got: %+v", registrar.members)
	}
	member := registrar.members[0]
	if member.SourceID != "imbGZo" || member.MediaGroupID != "gid-1" || member.ChatID != -1001 || member.ArchivedMessageID != 77 {
		t.Fatalf("unexpected album member: %+v", member)
	}
}

func TestDefaultSyncStage_AlbumMemberSkipsWhenSyncDisabled(t *testing.T) {
	registrar := &fakeAlbumRegistrar{}
	config := Entity.Config{}
	config.SocialMediaSync.Enable = false

	stage := defaultSyncStage{albums: registrar}
	enabled, _, _ := stage.Run(config, archiveservice.PersistResult{
		SourceID:     "imbGZo",
		MediaGroupID: "gid-1",
	})

	if enabled {
		t.Fatalf("expected sync disabled when social sync is off")
	}
	if len(registrar.members) != 0 {
		t.Fatalf("expected no album registration when sync disabled, got: %+v", registrar.members)
	}
}
