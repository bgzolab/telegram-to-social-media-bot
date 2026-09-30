package SocialMediaUtils

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"telegram-message-sync-bot/internal/Entity"

	"github.com/michimani/gotwi"
	uploadTypes "github.com/michimani/gotwi/media/upload/types"
	"github.com/michimani/gotwi/resources"
	manageTweetTypes "github.com/michimani/gotwi/tweet/managetweet/types"
)

var testPNGBytes = []byte("\x89PNG\r\n\x1a\nrest")

func TestSendTwitterWithImage_UploadAndAttachMedia(t *testing.T) {
	originalNewClient := newTwitterClient
	originalInitialize := twitterUploadInitialize
	originalAppend := twitterUploadAppend
	originalFinalize := twitterUploadFinalize
	originalCreateTweet := twitterCreateTweet
	defer func() {
		newTwitterClient = originalNewClient
		twitterUploadInitialize = originalInitialize
		twitterUploadAppend = originalAppend
		twitterUploadFinalize = originalFinalize
		twitterCreateTweet = originalCreateTweet
	}()

	root := t.TempDir()
	imagePath := filepath.Join(root, "single.png")
	if err := os.WriteFile(imagePath, testPNGBytes, 0o644); err != nil {
		t.Fatalf("failed to create test image: %v", err)
	}

	newTwitterClient = func(_ *gotwi.NewClientInput) (gotwi.IClient, error) {
		return gotwi.NewMockGotwiClientWithFunc(gotwi.MockFuncInput{}), nil
	}
	twitterUploadInitialize = func(client gotwi.IClient, input *uploadTypes.InitializeInput) (*uploadTypes.InitializeOutput, error) {
		if input.MediaCategory != uploadTypes.MediaCategoryTweetImage {
			t.Fatalf("unexpected media category: %+v", input)
		}
		if input.MediaType != uploadTypes.MediaTypePNG {
			t.Fatalf("unexpected media type: %+v", input)
		}
		return &uploadTypes.InitializeOutput{Data: resources.UploadedMedia{MediaID: "media-1"}}, nil
	}
	twitterUploadAppend = func(client gotwi.IClient, input *uploadTypes.AppendInput) (*uploadTypes.AppendOutput, error) {
		if input.MediaID != "media-1" {
			t.Fatalf("unexpected append input: %+v", input)
		}
		return &uploadTypes.AppendOutput{}, nil
	}
	twitterUploadFinalize = func(client gotwi.IClient, input *uploadTypes.FinalizeInput) (*uploadTypes.FinalizeOutput, error) {
		if input.MediaID != "media-1" {
			t.Fatalf("unexpected finalize input: %+v", input)
		}
		return &uploadTypes.FinalizeOutput{}, nil
	}
	twitterCreateTweet = func(client gotwi.IClient, input *manageTweetTypes.CreateInput) (*manageTweetTypes.CreateOutput, error) {
		if input.Media == nil || len(input.Media.MediaIDs) != 1 || input.Media.MediaIDs[0] != "media-1" {
			t.Fatalf("expected tweet create input to contain media id, got: %+v", input)
		}
		return &manageTweetTypes.CreateOutput{Data: struct {
			ID   *string `json:"id"`
			Text *string `json:"text"`
		}{ID: gotwi.String("1"), Text: gotwi.String("hello")}}, nil
	}

	config := Entity.Config{}
	config.SocialMediaSync.Twitter.Enable = true

	ok := SendTwitterWithImage(config, "hello", imagePath)
	if !ok {
		t.Fatalf("expected SendTwitterWithImage success")
	}
}

func TestSendTwitterWithImage_ConfiguresLongerHTTPTimeout(t *testing.T) {
	originalNewClient := newTwitterClient
	defer func() {
		newTwitterClient = originalNewClient
	}()

	newTwitterClient = func(in *gotwi.NewClientInput) (gotwi.IClient, error) {
		if in.HTTPClient == nil {
			t.Fatalf("expected configured http client")
		}
		if in.HTTPClient.Timeout != twitterHTTPTimeout {
			t.Fatalf("unexpected twitter timeout: %v", in.HTTPClient.Timeout)
		}
		return gotwi.NewMockGotwiClientWithFunc(gotwi.MockFuncInput{}), nil
	}

	config := Entity.Config{}
	config.SocialMediaSync.Twitter.Enable = true

	result := SendTwitterDetailed(config, "hello")
	if !result.Success {
		t.Fatalf("expected SendTwitterDetailed success")
	}
}

func TestSendTwitterWithImage_ReturnFalseWhenUploadFails(t *testing.T) {
	originalNewClient := newTwitterClient
	originalInitialize := twitterUploadInitialize
	defer func() {
		newTwitterClient = originalNewClient
		twitterUploadInitialize = originalInitialize
	}()

	root := t.TempDir()
	imagePath := filepath.Join(root, "single.png")
	if err := os.WriteFile(imagePath, testPNGBytes, 0o644); err != nil {
		t.Fatalf("failed to create test image: %v", err)
	}

	newTwitterClient = func(_ *gotwi.NewClientInput) (gotwi.IClient, error) {
		return gotwi.NewMockGotwiClientWithFunc(gotwi.MockFuncInput{}), nil
	}
	twitterUploadInitialize = func(client gotwi.IClient, input *uploadTypes.InitializeInput) (*uploadTypes.InitializeOutput, error) {
		return nil, fmt.Errorf("init failed")
	}

	config := Entity.Config{}
	config.SocialMediaSync.Twitter.Enable = true

	ok := SendTwitterWithImage(config, "hello", imagePath)
	if ok {
		t.Fatalf("expected SendTwitterWithImage failure when upload initialize fails")
	}
}

func TestResolveTwitterMediaType(t *testing.T) {
	mediaType, err := resolveTwitterMediaType([]byte("\x89PNG\r\n\x1a\nrest"))
	if err != nil {
		t.Fatalf("expected PNG media type, got error: %v", err)
	}
	if mediaType != uploadTypes.MediaTypePNG {
		t.Fatalf("unexpected media type: %s", mediaType)
	}
}

func stubTwitterUploadPipeline(t *testing.T, imagePaths []string) (*[]*manageTweetTypes.CreateInput, *int) {
	t.Helper()

	originalNewClient := newTwitterClient
	originalInitialize := twitterUploadInitialize
	originalAppend := twitterUploadAppend
	originalFinalize := twitterUploadFinalize
	originalCreateTweet := twitterCreateTweet
	t.Cleanup(func() {
		newTwitterClient = originalNewClient
		twitterUploadInitialize = originalInitialize
		twitterUploadAppend = originalAppend
		twitterUploadFinalize = originalFinalize
		twitterCreateTweet = originalCreateTweet
	})

	for _, imagePath := range imagePaths {
		if err := os.WriteFile(imagePath, testPNGBytes, 0o644); err != nil {
			t.Fatalf("failed to create test image: %v", err)
		}
	}

	uploadCount := 0
	newTwitterClient = func(_ *gotwi.NewClientInput) (gotwi.IClient, error) {
		return gotwi.NewMockGotwiClientWithFunc(gotwi.MockFuncInput{}), nil
	}
	twitterUploadInitialize = func(_ gotwi.IClient, _ *uploadTypes.InitializeInput) (*uploadTypes.InitializeOutput, error) {
		uploadCount++
		return &uploadTypes.InitializeOutput{Data: resources.UploadedMedia{MediaID: fmt.Sprintf("media-%d", uploadCount)}}, nil
	}
	twitterUploadAppend = func(_ gotwi.IClient, _ *uploadTypes.AppendInput) (*uploadTypes.AppendOutput, error) {
		return &uploadTypes.AppendOutput{}, nil
	}
	twitterUploadFinalize = func(_ gotwi.IClient, _ *uploadTypes.FinalizeInput) (*uploadTypes.FinalizeOutput, error) {
		return &uploadTypes.FinalizeOutput{}, nil
	}

	inputs := make([]*manageTweetTypes.CreateInput, 0, 2)
	twitterCreateTweet = func(_ gotwi.IClient, input *manageTweetTypes.CreateInput) (*manageTweetTypes.CreateOutput, error) {
		inputs = append(inputs, input)
		return &manageTweetTypes.CreateOutput{Data: struct {
			ID   *string `json:"id"`
			Text *string `json:"text"`
		}{ID: gotwi.String(fmt.Sprintf("tweet-%d", len(inputs))), Text: gotwi.String("ok")}}, nil
	}

	return &inputs, &uploadCount
}

func TestSendTwitterWithImages_AttachesAllImagesInOneTweet(t *testing.T) {
	paths := []string{"/tmp/a.png", "/tmp/b.png", "/tmp/c.png"}
	inputs, uploadCount := stubTwitterUploadPipeline(t, paths)

	config := Entity.Config{}
	config.SocialMediaSync.Twitter.Enable = true

	result := SendTwitterWithImagesDetailed(config, "album", paths)
	if !result.Success {
		t.Fatalf("expected multi-image publish success, got: %+v", result)
	}
	if *uploadCount != 3 {
		t.Fatalf("expected 3 uploads, got %d", *uploadCount)
	}
	if len(*inputs) != 1 {
		t.Fatalf("expected a single tweet, got %d", len(*inputs))
	}
	tweet := (*inputs)[0]
	if tweet.Media == nil || len(tweet.Media.MediaIDs) != 3 {
		t.Fatalf("expected 3 media ids, got: %+v", tweet.Media)
	}
	if tweet.Text == nil || *tweet.Text != "album" {
		t.Fatalf("unexpected tweet text: %+v", tweet.Text)
	}
	if tweet.Reply != nil {
		t.Fatalf("expected standalone tweet to have no reply: %+v", tweet.Reply)
	}
}

func TestSendTwitterWithImages_ThreadsOverLimitImages(t *testing.T) {
	paths := []string{"/tmp/1.png", "/tmp/2.png", "/tmp/3.png", "/tmp/4.png", "/tmp/5.png"}
	inputs, _ := stubTwitterUploadPipeline(t, paths)

	config := Entity.Config{}
	config.SocialMediaSync.Twitter.Enable = true

	result := SendTwitterWithImagesDetailed(config, "album", paths)
	if !result.Success {
		t.Fatalf("expected threaded publish success, got: %+v", result)
	}
	if result.RemoteID != "tweet-1" {
		t.Fatalf("expected first tweet remote id, got: %s", result.RemoteID)
	}
	if len(*inputs) != 2 {
		t.Fatalf("expected 2 threaded tweets, got %d", len(*inputs))
	}

	first := (*inputs)[0]
	if first.Media == nil || len(first.Media.MediaIDs) != 4 {
		t.Fatalf("expected 4 media ids on first tweet, got: %+v", first.Media)
	}

	second := (*inputs)[1]
	if second.Media == nil || len(second.Media.MediaIDs) != 1 {
		t.Fatalf("expected 1 media id on continuation tweet, got: %+v", second.Media)
	}
	if second.Reply == nil || second.Reply.InReplyToTweetID != "tweet-1" {
		t.Fatalf("expected continuation tweet to reply to first, got: %+v", second.Reply)
	}
	if second.Text != nil {
		t.Fatalf("expected continuation tweet without text, got: %+v", second.Text)
	}
}
