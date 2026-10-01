package syncservice

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"telegram-message-sync-bot/internal/Database"
	"telegram-message-sync-bot/internal/Entity"
)

func TestAlbumHead_PrefersFirstMemberWithContent(t *testing.T) {
	messages := []Entity.Message{
		{ID: 3, MessageID: 30, Content: ""},
		{ID: 2, MessageID: 20, Content: "caption"},
		{ID: 1, MessageID: 10, Content: "earlier caption"},
	}

	head := AlbumHead(messages)
	if head.MessageID != 10 || head.Content != "earlier caption" {
		t.Fatalf("expected earliest captioned member, got: %+v", head)
	}
}

func TestAlbumHead_FallsBackToEarliestMember(t *testing.T) {
	messages := []Entity.Message{
		{ID: 2, MessageID: 20},
		{ID: 1, MessageID: 10},
	}

	head := AlbumHead(messages)
	if head.MessageID != 10 {
		t.Fatalf("expected earliest member when no caption exists, got: %+v", head)
	}

	if empty := AlbumHead(nil); empty.MessageID != 0 {
		t.Fatalf("expected zero head for empty album, got: %+v", empty)
	}
}

func TestAlbumText_ConcatenatesDistinctCaptionsInOrder(t *testing.T) {
	messages := []Entity.Message{
		{ID: 3, MessageID: 30, Content: "third"},
		{ID: 1, MessageID: 10, Content: "first"},
		{ID: 2, MessageID: 20, Content: "first"},
		{ID: 4, MessageID: 40, Content: "  "},
	}

	text := AlbumText(messages)
	if text != "first\nthird" {
		t.Fatalf("expected ordered distinct captions, got: %q", text)
	}

	if empty := AlbumText(nil); empty != "" {
		t.Fatalf("expected empty album text, got: %q", empty)
	}
}

func TestBuildPayloadWithImages_FiltersMissingFiles(t *testing.T) {
	root := t.TempDir()
	existing := filepath.Join(root, "a.jpg")
	if err := os.WriteFile(existing, []byte("img"), 0o644); err != nil {
		t.Fatalf("failed to create test image: %v", err)
	}

	payload := BuildPayloadWithImages("hello \\#tag", []string{existing, filepath.Join(root, "missing.jpg"), ""})
	if payload.Text != "hello #tag" {
		t.Fatalf("expected unescaped hashtag text, got: %s", payload.Text)
	}
	paths := payload.ImagePaths()
	if len(paths) != 1 || paths[0] != existing {
		t.Fatalf("expected only existing image, got: %+v", paths)
	}
}

func TestPayload_ImagePathsDeduplicates(t *testing.T) {
	payload := Payload{
		Image:  &ImagePayload{FilePath: "a.jpg"},
		Images: []ImagePayload{{FilePath: "a.jpg"}, {FilePath: "b.jpg"}, {FilePath: ""}},
	}

	paths := payload.ImagePaths()
	if len(paths) != 2 || paths[0] != "a.jpg" || paths[1] != "b.jpg" {
		t.Fatalf("unexpected deduplicated paths: %+v", paths)
	}
}

func TestCollectAlbumImagePaths_ResolvesChannelAssetsInOrder(t *testing.T) {
	root := t.TempDir()
	channelDir := filepath.Join(root, "channel")
	first := filepath.Join(channelDir, "assets", "imbGZo", "first.jpg")
	second := filepath.Join(channelDir, "assets", "imbGZo", "second.jpg")
	for _, path := range []string{first, second} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("failed to create asset dir: %v", err)
		}
		if err := os.WriteFile(path, []byte("img"), 0o644); err != nil {
			t.Fatalf("failed to create asset: %v", err)
		}
	}

	config := Entity.Config{}
	config.Output.ChannelDir = channelDir

	messages := []Entity.Message{
		{ID: 2, MessageID: 20, Attachments: []Entity.Attachment{{Type: Entity.ImageMessage, FilePath: "assets/imbGZo/second.jpg"}}},
		{ID: 1, MessageID: 10, Attachments: []Entity.Attachment{{Type: Entity.ImageMessage, FilePath: "assets/imbGZo/first.jpg"}}},
	}

	paths := CollectAlbumImagePaths(config, messages)
	if len(paths) != 2 || paths[0] != first || paths[1] != second {
		t.Fatalf("expected album image order by message id, got: %+v", paths)
	}
}

func TestManualResync_AlbumMemberResolvesWholeAlbum(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open sqlite memory db: %v", err)
	}
	if err := db.AutoMigrate(&Entity.Message{}, &Entity.Attachment{}, &Entity.SyncRecord{}); err != nil {
		t.Fatalf("failed to migrate tables: %v", err)
	}
	Database.DB = db

	root := t.TempDir()
	channelDir := filepath.Join(root, "channel")
	imageOne := filepath.Join(channelDir, "assets", "imbGZo", "one.jpg")
	imageTwo := filepath.Join(channelDir, "assets", "imbGZo", "two.jpg")
	for _, path := range []string{imageOne, imageTwo} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("failed to create asset dir: %v", err)
		}
		if err := os.WriteFile(path, []byte("img"), 0o644); err != nil {
			t.Fatalf("failed to create asset: %v", err)
		}
	}

	headID, err := Database.SaveMessage(&Entity.Message{
		MessageID:    5001,
		Username:     "imbGZo",
		Content:      "album caption",
		MediaGroupID: "gid-manual",
		MessageDate:  time.Now(),
		Attachments:  []Entity.Attachment{{Type: Entity.ImageMessage, FilePath: "assets/imbGZo/one.jpg"}},
		CreatedTime:  time.Now(),
	})
	if err != nil {
		t.Fatalf("failed to save head message: %v", err)
	}
	if _, err := Database.SaveMessage(&Entity.Message{
		MessageID:    5002,
		Username:     "imbGZo",
		MediaGroupID: "gid-manual",
		MessageDate:  time.Now(),
		Attachments:  []Entity.Attachment{{Type: Entity.ImageMessage, FilePath: "assets/imbGZo/two.jpg"}},
		CreatedTime:  time.Now(),
	}); err != nil {
		t.Fatalf("failed to save member message: %v", err)
	}

	sender := &manualFakeSender{name: "Twitter", result: DispatchResult{Success: true}}
	originalFactory := manualDispatchFactory
	manualDispatchFactory = func() []Sender {
		return []Sender{sender}
	}
	defer func() {
		manualDispatchFactory = originalFactory
	}()

	config := Entity.Config{}
	config.Output.ChannelDir = channelDir
	config.SocialMediaSync.TargetChannel = []string{"imbGZo"}

	// 点击相册中无正文的第二张触发重同步，也应按整组投递。
	member, err := Database.GetMessageBySource(5002, "imbGZo")
	if err != nil {
		t.Fatalf("failed to load member message: %v", err)
	}

	result, err := ManualResync(config, member.ID, "twitter")
	if err != nil {
		t.Fatalf("manual resync failed: %v", err)
	}
	if !result.Requested {
		t.Fatalf("expected manual resync to be requested: %+v", result)
	}
	if sender.payload.Text != "album caption" {
		t.Fatalf("expected album head text, got: %s", sender.payload.Text)
	}
	paths := sender.payload.ImagePaths()
	if len(paths) != 2 || paths[0] != imageOne || paths[1] != imageTwo {
		t.Fatalf("expected both album images, got: %+v", paths)
	}
	if headID == 0 {
		t.Fatalf("expected saved head message id")
	}
}

func TestAlbumScopeKey_GroupedByAlbumAndMessage(t *testing.T) {
	albumScope := AlbumScopeKey("imbGZo", "gid-1", 42)
	if albumScope != "album:imbGZo|gid-1" {
		t.Fatalf("unexpected album scope: %s", albumScope)
	}
	if AlbumScopeKey("imbGZo", "gid-1", 43) != albumScope {
		t.Fatalf("expected album members to share the same scope")
	}

	messageScope := AlbumScopeKey("imbGZo", "", 42)
	if messageScope != "message:42" {
		t.Fatalf("unexpected message scope: %s", messageScope)
	}
	if messageScope == AlbumScopeKey("imbGZo", "", 43) {
		t.Fatalf("expected distinct message scopes")
	}
}

func TestManualResync_BlocksWhenAlbumDeliverInFlight(t *testing.T) {
	setupManualSyncTestDB(t)

	scope := AlbumScopeKey("imbGZo", "gid-manual", 0)
	if !AcquireAlbumDispatch(scope) {
		t.Fatalf("expected to acquire album scope for test setup")
	}
	defer ReleaseAlbumDispatch(scope)

	config := Entity.Config{}
	config.SocialMediaSync.TargetChannel = []string{"imbGZo"}

	// 手动重同步应被相册投递互斥挡住，而不是并发重复发帖。
	archivedID, err := Database.SaveMessage(&Entity.Message{
		MessageID:    6001,
		Username:     "imbGZo",
		Content:      "album caption",
		MediaGroupID: "gid-manual",
		MessageDate:  time.Now(),
		CreatedTime:  time.Now(),
	})
	if err != nil {
		t.Fatalf("failed to save album message: %v", err)
	}

	result, err := ManualResync(config, archivedID, "twitter")
	if err != nil {
		t.Fatalf("manual resync should not fail: %v", err)
	}
	if result.Requested {
		t.Fatalf("expected manual resync to be blocked while album dispatch in flight")
	}
}
