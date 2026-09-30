package notifyservice

import (
	"testing"

	"telegram-message-sync-bot/internal/Entity"
	"telegram-message-sync-bot/internal/service/syncservice"
)

func TestResolveTargetChatIDs_WithConfiguredTargets(t *testing.T) {
	config := Entity.Config{}
	config.TargetUserList = []int64{1001, 1002}

	chatIDs := ResolveTargetChatIDs(config, 999)
	if len(chatIDs) != 2 || chatIDs[0] != 1001 || chatIDs[1] != 1002 {
		t.Fatalf("unexpected chatIDs: %+v", chatIDs)
	}
}

func TestResolveTargetChatIDs_WithFallback(t *testing.T) {
	config := Entity.Config{}

	chatIDs := ResolveTargetChatIDs(config, 999)
	if len(chatIDs) != 1 || chatIDs[0] != 999 {
		t.Fatalf("unexpected fallback chatIDs: %+v", chatIDs)
	}
}

func TestBuildArchiveResponse(t *testing.T) {
	successText := BuildArchiveResponse(true, "link", "file.md")
	if successText != "link\n消息已备案至: file.md!" {
		t.Fatalf("unexpected success text: %s", successText)
	}

	failText := BuildArchiveResponse(false, "link", "err")
	if failText != "link\n消息备份出现异常: err!" {
		t.Fatalf("unexpected fail text: %s", failText)
	}
}

func TestBuildSyncNotifications(t *testing.T) {
	whenDisabled := BuildSyncNotifications(false, "skip reason", nil)
	if len(whenDisabled) != 1 || whenDisabled[0] != "skip reason" {
		t.Fatalf("unexpected disabled notifications: %+v", whenDisabled)
	}

	results := []syncservice.DispatchResult{
		{Platform: "BlueSky", Success: true, ImageRequested: true, UsedImage: true, Truncated: true},
		{Platform: "Mastodon", Success: true, ImageRequested: true, UsedImage: false, ErrorMessage: "图片上传失败，已降级为纯文本: scope missing"},
		{Platform: "Twitter", Success: false, ImageRequested: true, ErrorMessage: "too long"},
	}
	whenEnabled := BuildSyncNotifications(true, "", results)
	if len(whenEnabled) != 3 {
		t.Fatalf("unexpected enabled notification size: %d", len(whenEnabled))
	}
	if whenEnabled[0] != "消息已同步至 BlueSky，并附带图片，文本已截断!" {
		t.Fatalf("unexpected first notification: %s", whenEnabled[0])
	}
	if whenEnabled[1] != "消息已同步至 Mastodon，但图片已跳过: 图片上传失败，已降级为纯文本: scope missing" {
		t.Fatalf("unexpected second notification: %s", whenEnabled[1])
	}
	if whenEnabled[2] != "同步 Twitter 失败: too long" {
		t.Fatalf("unexpected third notification: %s", whenEnabled[2])
	}
}

func TestBuildOutboundMessages(t *testing.T) {
	chatIDs := []int64{1, 2}
	archive := "archive"
	syncTexts := []string{"s1", "s2"}

	msgs := BuildOutboundMessages(chatIDs, archive, syncTexts)
	if len(msgs) != 6 {
		t.Fatalf("unexpected outbound size: %d", len(msgs))
	}

	if msgs[0].ChatID != 1 || msgs[0].Text != "archive" {
		t.Fatalf("unexpected first msg: %+v", msgs[0])
	}
	if msgs[1].ChatID != 1 || msgs[1].Text != "s1" {
		t.Fatalf("unexpected second msg: %+v", msgs[1])
	}
	if msgs[2].ChatID != 1 || msgs[2].Text != "s2" {
		t.Fatalf("unexpected third msg: %+v", msgs[2])
	}
	if msgs[3].ChatID != 2 || msgs[3].Text != "archive" {
		t.Fatalf("unexpected fourth msg: %+v", msgs[3])
	}
}

func TestBuildAlbumSyncNotifications_AddsSourceAndImageCount(t *testing.T) {
	results := []syncservice.DispatchResult{
		{Platform: "BlueSky", Success: true, ImageRequested: true, UsedImage: true},
		{Platform: "Twitter", Success: false, ErrorMessage: "too long"},
	}

	notifications := BuildAlbumSyncNotifications("https://t.me/imbGZo/100", 3, results)
	if len(notifications) != 2 {
		t.Fatalf("unexpected notifications: %+v", notifications)
	}
	expectedFirst := "https://t.me/imbGZo/100\n相册消息（3 张图）\n消息已同步至 BlueSky，并附带图片!"
	if notifications[0] != expectedFirst {
		t.Fatalf("unexpected first notification: %s", notifications[0])
	}
	if notifications[1] != "https://t.me/imbGZo/100\n相册消息（3 张图）\n同步 Twitter 失败: too long" {
		t.Fatalf("unexpected second notification: %s", notifications[1])
	}
}

func TestBuildAlbumSyncNotifications_WithoutSourceOrImages(t *testing.T) {
	results := []syncservice.DispatchResult{{Platform: "Mastodon", Success: true}}

	notifications := BuildAlbumSyncNotifications("", 0, results)
	if len(notifications) != 1 {
		t.Fatalf("unexpected notifications: %+v", notifications)
	}
	if notifications[0] != "相册消息\n消息已同步至 Mastodon!" {
		t.Fatalf("unexpected notification: %s", notifications[0])
	}

	if empty := BuildAlbumSyncNotifications("link", 1, nil); len(empty) != 0 {
		t.Fatalf("expected no notifications without results, got: %+v", empty)
	}
}
