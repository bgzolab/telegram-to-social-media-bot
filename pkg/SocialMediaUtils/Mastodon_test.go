package SocialMediaUtils

import (
	"context"
	"fmt"
	"testing"

	"telegram-message-sync-bot/internal/Entity"

	"github.com/mattn/go-mastodon"
)

type fakeMastodonClient struct {
	uploadedPath    string
	uploadedPaths   []string
	postedToot      *mastodon.Toot
	postedToots     []*mastodon.Toot
	uploadErr       error
	uploadErrAtCall int
	postErr         error
	uploadCount     int
	postCount       int
}

func (f *fakeMastodonClient) UploadMedia(_ context.Context, file string) (*mastodon.Attachment, error) {
	f.uploadedPath = file
	f.uploadedPaths = append(f.uploadedPaths, file)
	f.uploadCount++
	if f.uploadErr != nil {
		return nil, f.uploadErr
	}
	if f.uploadErrAtCall > 0 && f.uploadCount == f.uploadErrAtCall {
		return nil, fmt.Errorf("upload failed")
	}
	return &mastodon.Attachment{ID: mastodon.ID(fmt.Sprintf("attachment-%d", f.uploadCount))}, nil
}

func (f *fakeMastodonClient) PostStatus(_ context.Context, toot *mastodon.Toot) (*mastodon.Status, error) {
	f.postedToot = toot
	f.postedToots = append(f.postedToots, toot)
	if f.postErr != nil {
		return nil, f.postErr
	}
	f.postCount++
	return &mastodon.Status{ID: mastodon.ID(fmt.Sprintf("status-%d", f.postCount))}, nil
}

func TestSendMastodonWithImage_UploadAndAttachMedia(t *testing.T) {
	client := &fakeMastodonClient{}
	originalFactory := newMastodonClient
	newMastodonClient = func(_ *mastodon.Config) mastodonClient {
		return client
	}
	defer func() {
		newMastodonClient = originalFactory
	}()

	config := Entity.Config{}
	config.SocialMediaSync.Mastodon.Enable = true

	ok := SendMastodonWithImage(config, "hello", "/tmp/test.jpg")
	if !ok {
		t.Fatalf("expected SendMastodonWithImage success")
	}
	if client.uploadedPath != "/tmp/test.jpg" {
		t.Fatalf("unexpected uploaded path: %s", client.uploadedPath)
	}
	if client.postedToot == nil {
		t.Fatalf("expected toot to be posted")
	}
	if client.postedToot.Status != "hello" {
		t.Fatalf("unexpected toot status: %+v", client.postedToot)
	}
	if len(client.postedToot.MediaIDs) != 1 || client.postedToot.MediaIDs[0] != mastodon.ID("attachment-1") {
		t.Fatalf("unexpected media ids: %+v", client.postedToot.MediaIDs)
	}
}

func TestSendMastodonWithImage_ReturnFalseWhenUploadFails(t *testing.T) {
	client := &fakeMastodonClient{uploadErr: fmt.Errorf("upload failed")}
	originalFactory := newMastodonClient
	newMastodonClient = func(_ *mastodon.Config) mastodonClient {
		return client
	}
	defer func() {
		newMastodonClient = originalFactory
	}()

	config := Entity.Config{}
	config.SocialMediaSync.Mastodon.Enable = true

	ok := SendMastodonWithImage(config, "hello", "/tmp/test.jpg")
	if ok {
		t.Fatalf("expected SendMastodonWithImage failure when upload fails")
	}
	if client.postedToot != nil {
		t.Fatalf("expected post not to be attempted when upload fails")
	}
}

func TestSendMastodonWithImage_ExplainsMissingMediaScope(t *testing.T) {
	client := &fakeMastodonClient{uploadErr: fmt.Errorf("403 Forbidden: This action is outside the authorized scopes")}
	originalFactory := newMastodonClient
	newMastodonClient = func(_ *mastodon.Config) mastodonClient {
		return client
	}
	defer func() {
		newMastodonClient = originalFactory
	}()

	config := Entity.Config{}
	config.SocialMediaSync.Mastodon.Enable = true

	result := SendMastodonWithImageDetailed(config, "hello", "/tmp/test.jpg")
	if result.Success {
		t.Fatalf("expected SendMastodonWithImageDetailed failure when upload fails")
	}
	if result.ErrorMessage != "Mastodon 图片上传被拒绝，当前 AccessToken 缺少媒体上传权限 scope（通常需要 write:media）" {
		t.Fatalf("unexpected error message: %s", result.ErrorMessage)
	}
}

func TestSendMastodonWithImages_AttachesAllImagesInOneToot(t *testing.T) {
	client := &fakeMastodonClient{}
	originalFactory := newMastodonClient
	newMastodonClient = func(_ *mastodon.Config) mastodonClient {
		return client
	}
	defer func() {
		newMastodonClient = originalFactory
	}()

	config := Entity.Config{}
	config.SocialMediaSync.Mastodon.Enable = true

	imagePaths := []string{"/tmp/a.jpg", "/tmp/b.jpg", "/tmp/c.jpg"}
	result := SendMastodonWithImagesDetailed(config, "album", imagePaths)
	if !result.Success {
		t.Fatalf("expected multi-image publish success, got: %+v", result)
	}
	if len(client.postedToots) != 1 {
		t.Fatalf("expected a single toot, got %d", len(client.postedToots))
	}
	if len(client.postedToots[0].MediaIDs) != 3 {
		t.Fatalf("expected 3 media ids, got: %+v", client.postedToots[0].MediaIDs)
	}
	if client.postedToots[0].Status != "album" {
		t.Fatalf("unexpected toot status: %+v", client.postedToots[0])
	}
	if len(client.uploadedPaths) != 3 {
		t.Fatalf("expected 3 uploads, got: %+v", client.uploadedPaths)
	}
}

func TestSendMastodonWithImages_ThreadsOverLimitImages(t *testing.T) {
	client := &fakeMastodonClient{}
	originalFactory := newMastodonClient
	newMastodonClient = func(_ *mastodon.Config) mastodonClient {
		return client
	}
	defer func() {
		newMastodonClient = originalFactory
	}()

	config := Entity.Config{}
	config.SocialMediaSync.Mastodon.Enable = true

	imagePaths := []string{"/tmp/1.jpg", "/tmp/2.jpg", "/tmp/3.jpg", "/tmp/4.jpg", "/tmp/5.jpg"}
	result := SendMastodonWithImagesDetailed(config, "album", imagePaths)
	if !result.Success {
		t.Fatalf("expected threaded publish success, got: %+v", result)
	}
	if result.RemoteID != "status-1" {
		t.Fatalf("expected first status remote id, got: %s", result.RemoteID)
	}
	if len(client.postedToots) != 2 {
		t.Fatalf("expected 2 threaded toots, got %d", len(client.postedToots))
	}

	first := client.postedToots[0]
	if len(first.MediaIDs) != 4 || first.InReplyToID != mastodon.ID("") {
		t.Fatalf("unexpected first toot: %+v", first)
	}

	second := client.postedToots[1]
	if len(second.MediaIDs) != 1 {
		t.Fatalf("expected 1 media id on continuation toot, got: %+v", second.MediaIDs)
	}
	if second.InReplyToID != mastodon.ID("status-1") {
		t.Fatalf("expected continuation to reply to first status, got: %+v", second.InReplyToID)
	}
	if second.Status != "" {
		t.Fatalf("expected continuation toot without text, got: %s", second.Status)
	}
}

func TestSendMastodonWithImages_ReportsSkippedUploads(t *testing.T) {
	client := &fakeMastodonClient{uploadErrAtCall: 2}
	originalFactory := newMastodonClient
	newMastodonClient = func(_ *mastodon.Config) mastodonClient {
		return client
	}
	defer func() {
		newMastodonClient = originalFactory
	}()

	config := Entity.Config{}
	config.SocialMediaSync.Mastodon.Enable = true

	result := SendMastodonWithImagesDetailed(config, "album", []string{"/tmp/a.jpg", "/tmp/b.jpg", "/tmp/c.jpg"})
	if !result.Success {
		t.Fatalf("expected publish success despite one skipped image, got: %+v", result)
	}
	if result.ErrorMessage != "1 张图片上传失败，已跳过" {
		t.Fatalf("expected skipped image notice, got: %s", result.ErrorMessage)
	}
	if len(client.postedToots) != 1 || len(client.postedToots[0].MediaIDs) != 2 {
		t.Fatalf("expected toot with two media ids, got: %+v", client.postedToots)
	}
}
