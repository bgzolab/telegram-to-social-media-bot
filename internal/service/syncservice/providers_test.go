package syncservice

import (
	"strings"
	"testing"

	"telegram-message-sync-bot/internal/Entity"
	"telegram-message-sync-bot/pkg/SocialMediaUtils"
)

func TestMastodonSender_UsesImagePathWhenPresent(t *testing.T) {
	originalTextDetailed := sendMastodonTextDetailed
	originalImageDetailed := sendMastodonImagesDetailed
	defer func() {
		sendMastodonTextDetailed = originalTextDetailed
		sendMastodonImagesDetailed = originalImageDetailed
	}()

	textCalled := false
	imageCalled := false
	sendMastodonTextDetailed = func(_ Entity.Config, _ string) SocialMediaUtils.PublishResult {
		textCalled = true
		return SocialMediaUtils.PublishResult{Success: true}
	}
	sendMastodonImagesDetailed = func(_ Entity.Config, message string, imagePaths []string) SocialMediaUtils.PublishResult {
		imageCalled = true
		if message != "hello" {
			t.Fatalf("unexpected message: %s", message)
		}
		if len(imagePaths) != 1 || imagePaths[0] != "/tmp/test.jpg" {
			t.Fatalf("unexpected image paths: %+v", imagePaths)
		}
		return SocialMediaUtils.PublishResult{Success: true, RemoteID: "mastodon-1", RemoteURL: "https://mastodon.example/@user/1"}
	}

	sender := mastodonSender{}
	result := sender.Send(Entity.Config{}, Payload{Text: "hello", Image: &ImagePayload{FilePath: "/tmp/test.jpg"}})
	if !result.Success {
		t.Fatalf("expected image send success")
	}
	if !result.ImageRequested || !result.UsedImage {
		t.Fatalf("unexpected image result: %+v", result)
	}
	if result.RemoteURL == "" {
		t.Fatalf("expected remote url in result: %+v", result)
	}
	if textCalled {
		t.Fatalf("text sender should not be called when image exists")
	}
	if !imageCalled {
		t.Fatalf("image sender should be called when image exists")
	}
}

func TestBlueSkySender_UsesImagePathWhenPresent(t *testing.T) {
	originalTextDetailed := sendBlueSkyTextDetailed
	originalImageDetailed := sendBlueSkyImagesDetailed
	defer func() {
		sendBlueSkyTextDetailed = originalTextDetailed
		sendBlueSkyImagesDetailed = originalImageDetailed
	}()

	textCalled := false
	imageCalled := false
	sendBlueSkyTextDetailed = func(_ Entity.Config, _ string) SocialMediaUtils.PublishResult {
		textCalled = true
		return SocialMediaUtils.PublishResult{Success: true}
	}
	sendBlueSkyImagesDetailed = func(_ Entity.Config, message string, imagePaths []string) SocialMediaUtils.PublishResult {
		imageCalled = true
		if message != "hello" {
			t.Fatalf("unexpected message: %s", message)
		}
		if len(imagePaths) != 1 || imagePaths[0] != "/tmp/test.jpg" {
			t.Fatalf("unexpected image paths: %+v", imagePaths)
		}
		return SocialMediaUtils.PublishResult{Success: true, RemoteID: "at://did:plc:test/app.bsky.feed.post/123"}
	}

	sender := blueSkySender{}
	result := sender.Send(Entity.Config{}, Payload{Text: "hello", Image: &ImagePayload{FilePath: "/tmp/test.jpg"}})
	if !result.Success {
		t.Fatalf("expected image send success")
	}
	if !result.ImageRequested || !result.UsedImage {
		t.Fatalf("unexpected image result: %+v", result)
	}
	if result.RemoteID == "" {
		t.Fatalf("expected remote id in result: %+v", result)
	}
	if result.Truncated {
		t.Fatalf("did not expect truncation: %+v", result)
	}
	if textCalled {
		t.Fatalf("text sender should not be called when image exists")
	}
	if !imageCalled {
		t.Fatalf("image sender should be called when image exists")
	}
}

func TestBlueSkySender_FallsBackToTextWhenNoImage(t *testing.T) {
	originalTextDetailed := sendBlueSkyTextDetailed
	originalImageDetailed := sendBlueSkyImagesDetailed
	defer func() {
		sendBlueSkyTextDetailed = originalTextDetailed
		sendBlueSkyImagesDetailed = originalImageDetailed
	}()

	textCalled := false
	imageCalled := false
	sendBlueSkyTextDetailed = func(_ Entity.Config, message string) SocialMediaUtils.PublishResult {
		textCalled = true
		if message != "hello" {
			t.Fatalf("unexpected message: %s", message)
		}
		return SocialMediaUtils.PublishResult{Success: true, RemoteID: "at://did:plc:test/app.bsky.feed.post/456"}
	}
	sendBlueSkyImagesDetailed = func(_ Entity.Config, _ string, _ []string) SocialMediaUtils.PublishResult {
		imageCalled = true
		return SocialMediaUtils.PublishResult{Success: true}
	}

	sender := blueSkySender{}
	result := sender.Send(Entity.Config{}, Payload{Text: "hello"})
	if !result.Success {
		t.Fatalf("expected text send success")
	}
	if result.ImageRequested || result.UsedImage {
		t.Fatalf("unexpected text-only result: %+v", result)
	}
	if !textCalled {
		t.Fatalf("text sender should be called when image is absent")
	}
	if imageCalled {
		t.Fatalf("image sender should not be called when image is absent")
	}
}

func TestTwitterSender_UsesImagePathWhenPresent(t *testing.T) {
	originalTextDetailed := sendTwitterTextDetailed
	originalImageDetailed := sendTwitterImagesDetailed
	defer func() {
		sendTwitterTextDetailed = originalTextDetailed
		sendTwitterImagesDetailed = originalImageDetailed
	}()

	textCalled := false
	imageCalled := false
	sendTwitterTextDetailed = func(_ Entity.Config, _ string) SocialMediaUtils.PublishResult {
		textCalled = true
		return SocialMediaUtils.PublishResult{Success: true}
	}
	sendTwitterImagesDetailed = func(_ Entity.Config, message string, imagePaths []string) SocialMediaUtils.PublishResult {
		imageCalled = true
		if message != "hello" {
			t.Fatalf("unexpected message: %s", message)
		}
		if len(imagePaths) != 1 || imagePaths[0] != "/tmp/test.jpg" {
			t.Fatalf("unexpected image paths: %+v", imagePaths)
		}
		return SocialMediaUtils.PublishResult{Success: true, RemoteID: "123", RemoteURL: "https://twitter.com/i/web/status/123"}
	}

	sender := twitterSender{}
	result := sender.Send(Entity.Config{}, Payload{Text: "hello", Image: &ImagePayload{FilePath: "/tmp/test.jpg"}})
	if !result.Success {
		t.Fatalf("expected image send success")
	}
	if !result.ImageRequested || !result.UsedImage {
		t.Fatalf("unexpected image result: %+v", result)
	}
	if result.RemoteID == "" {
		t.Fatalf("expected remote id in result: %+v", result)
	}
	if textCalled {
		t.Fatalf("text sender should not be called when image exists")
	}
	if !imageCalled {
		t.Fatalf("image sender should be called when image exists")
	}
}

func TestTwitterSender_FallsBackToTextWhenNoImage(t *testing.T) {
	originalTextDetailed := sendTwitterTextDetailed
	originalImageDetailed := sendTwitterImagesDetailed
	defer func() {
		sendTwitterTextDetailed = originalTextDetailed
		sendTwitterImagesDetailed = originalImageDetailed
	}()

	textCalled := false
	imageCalled := false
	sendTwitterTextDetailed = func(_ Entity.Config, message string) SocialMediaUtils.PublishResult {
		textCalled = true
		if message != "hello" {
			t.Fatalf("unexpected message: %s", message)
		}
		return SocialMediaUtils.PublishResult{Success: true, RemoteID: "456", RemoteURL: "https://twitter.com/i/web/status/456"}
	}
	sendTwitterImagesDetailed = func(_ Entity.Config, _ string, _ []string) SocialMediaUtils.PublishResult {
		imageCalled = true
		return SocialMediaUtils.PublishResult{Success: true}
	}

	sender := twitterSender{}
	result := sender.Send(Entity.Config{}, Payload{Text: "hello"})
	if !result.Success {
		t.Fatalf("expected text send success")
	}
	if result.ImageRequested || result.UsedImage {
		t.Fatalf("unexpected text-only result: %+v", result)
	}
	if !textCalled {
		t.Fatalf("text sender should be called when image is absent")
	}
	if imageCalled {
		t.Fatalf("image sender should not be called when image is absent")
	}
}

func TestMastodonSender_FallsBackToTextWhenNoImage(t *testing.T) {
	originalTextDetailed := sendMastodonTextDetailed
	originalImageDetailed := sendMastodonImagesDetailed
	defer func() {
		sendMastodonTextDetailed = originalTextDetailed
		sendMastodonImagesDetailed = originalImageDetailed
	}()

	textCalled := false
	imageCalled := false
	sendMastodonTextDetailed = func(_ Entity.Config, message string) SocialMediaUtils.PublishResult {
		textCalled = true
		if message != "hello" {
			t.Fatalf("unexpected message: %s", message)
		}
		return SocialMediaUtils.PublishResult{Success: true, RemoteID: "mastodon-2", RemoteURL: "https://mastodon.example/@user/2"}
	}
	sendMastodonImagesDetailed = func(_ Entity.Config, _ string, _ []string) SocialMediaUtils.PublishResult {
		imageCalled = true
		return SocialMediaUtils.PublishResult{Success: true}
	}

	sender := mastodonSender{}
	result := sender.Send(Entity.Config{}, Payload{Text: "hello"})
	if !result.Success {
		t.Fatalf("expected text send success")
	}
	if result.ImageRequested || result.UsedImage {
		t.Fatalf("unexpected text-only result: %+v", result)
	}
	if !textCalled {
		t.Fatalf("text sender should be called when image is absent")
	}
	if imageCalled {
		t.Fatalf("image sender should not be called when image is absent")
	}
}

func TestBlueSkySender_FallsBackToTextWhenImageSendFails(t *testing.T) {
	originalTextDetailed := sendBlueSkyTextDetailed
	originalImageDetailed := sendBlueSkyImagesDetailed
	defer func() {
		sendBlueSkyTextDetailed = originalTextDetailed
		sendBlueSkyImagesDetailed = originalImageDetailed
	}()

	textCalled := false
	sendBlueSkyTextDetailed = func(_ Entity.Config, _ string) SocialMediaUtils.PublishResult {
		textCalled = true
		return SocialMediaUtils.PublishResult{Success: true, RemoteID: "at://did:plc:test/app.bsky.feed.post/fallback"}
	}
	sendBlueSkyImagesDetailed = func(_ Entity.Config, _ string, _ []string) SocialMediaUtils.PublishResult {
		return SocialMediaUtils.PublishResult{Success: false, ErrorMessage: "upload failed"}
	}

	result := blueSkySender{}.Send(Entity.Config{}, Payload{Text: "hello", Image: &ImagePayload{FilePath: "/tmp/test.jpg"}})
	if !result.Success || !result.ImageRequested || result.UsedImage {
		t.Fatalf("expected text fallback result, got: %+v", result)
	}
	if !textCalled {
		t.Fatalf("expected text fallback to be called")
	}
	if result.RemoteID == "" {
		t.Fatalf("expected fallback result to keep remote id: %+v", result)
	}
	if result.ErrorMessage != "图片上传失败，已降级为纯文本: upload failed" {
		t.Fatalf("expected fallback image error to be preserved: %+v", result)
	}
}

func TestMastodonSender_FallbackKeepsImageErrorWhenTextSucceeds(t *testing.T) {
	originalTextDetailed := sendMastodonTextDetailed
	originalImageDetailed := sendMastodonImagesDetailed
	defer func() {
		sendMastodonTextDetailed = originalTextDetailed
		sendMastodonImagesDetailed = originalImageDetailed
	}()

	sendMastodonTextDetailed = func(_ Entity.Config, _ string) SocialMediaUtils.PublishResult {
		return SocialMediaUtils.PublishResult{Success: true, RemoteID: "mastodon-fallback"}
	}
	sendMastodonImagesDetailed = func(_ Entity.Config, _ string, _ []string) SocialMediaUtils.PublishResult {
		return SocialMediaUtils.PublishResult{Success: false, ErrorMessage: "scope missing"}
	}

	result := mastodonSender{}.Send(Entity.Config{}, Payload{Text: "hello", Image: &ImagePayload{FilePath: "/tmp/test.jpg"}})
	if !result.Success || !result.ImageRequested || result.UsedImage {
		t.Fatalf("expected text fallback result, got: %+v", result)
	}
	if result.ErrorMessage != "图片上传失败，已降级为纯文本: scope missing" {
		t.Fatalf("expected fallback image error to be preserved: %+v", result)
	}
}

func TestTwitterSender_TruncatesBeforeSend(t *testing.T) {
	originalTextDetailed := sendTwitterTextDetailed
	defer func() {
		sendTwitterTextDetailed = originalTextDetailed
	}()

	longText := strings.Repeat("a", 320)
	sendTwitterTextDetailed = func(_ Entity.Config, message string) SocialMediaUtils.PublishResult {
		if twitterWeightedLength(message) != twitterTextLimit {
			t.Fatalf("unexpected sent message length: %d", twitterWeightedLength(message))
		}
		if !strings.HasSuffix(message, truncateSuffix) {
			t.Fatalf("expected truncated suffix, got: %s", message)
		}
		return SocialMediaUtils.PublishResult{Success: true}
	}

	result := twitterSender{}.Send(Entity.Config{}, Payload{Text: longText})
	if !result.Truncated {
		t.Fatalf("expected truncated result: %+v", result)
	}
}
