package syncservice

import (
	"fmt"
	"telegram-message-sync-bot/internal/Entity"
	"telegram-message-sync-bot/pkg/SocialMediaUtils"
)

var sendBlueSkyTextDetailed = SocialMediaUtils.SendBlueSkyDetailed
var sendBlueSkyImagesDetailed = SocialMediaUtils.SendBlueSkyWithImagesDetailed
var sendMastodonTextDetailed = SocialMediaUtils.SendMastodonDetailed
var sendMastodonImagesDetailed = SocialMediaUtils.SendMastodonWithImagesDetailed
var sendTwitterTextDetailed = SocialMediaUtils.SendTwitterDetailed
var sendTwitterImagesDetailed = SocialMediaUtils.SendTwitterWithImagesDetailed

type blueSkySender struct{}

func (blueSkySender) Name() string {
	return "BlueSky"
}

func (blueSkySender) Send(config Entity.Config, payload Payload) DispatchResult {
	prepared := PreparePlatformText("BlueSky", payload.Text)
	imagePaths := payload.ImagePaths()
	if len(imagePaths) > 0 {
		imageResult := sendBlueSkyImagesDetailed(config, prepared.Text, imagePaths)
		if imageResult.Success {
			return dispatchResultFromPublish("BlueSky", imageResult, true, true, prepared.Truncated)
		}
		textResult := sendBlueSkyTextDetailed(config, prepared.Text)
		textResult.ErrorMessage = mergeImageFallbackError(textResult, imageResult)
		return dispatchResultFromPublish("BlueSky", textResult, true, false, prepared.Truncated)
	}
	return dispatchResultFromPublish("BlueSky", sendBlueSkyTextDetailed(config, prepared.Text), false, false, prepared.Truncated)
}

type mastodonSender struct{}

func (mastodonSender) Name() string {
	return "Mastodon"
}

func (mastodonSender) Send(config Entity.Config, payload Payload) DispatchResult {
	prepared := PreparePlatformText("Mastodon", payload.Text)
	imagePaths := payload.ImagePaths()
	if len(imagePaths) > 0 {
		imageResult := sendMastodonImagesDetailed(config, prepared.Text, imagePaths)
		if imageResult.Success {
			return dispatchResultFromPublish("Mastodon", imageResult, true, true, prepared.Truncated)
		}
		textResult := sendMastodonTextDetailed(config, prepared.Text)
		textResult.ErrorMessage = mergeImageFallbackError(textResult, imageResult)
		return dispatchResultFromPublish("Mastodon", textResult, true, false, prepared.Truncated)
	}
	return dispatchResultFromPublish("Mastodon", sendMastodonTextDetailed(config, prepared.Text), false, false, prepared.Truncated)
}

type twitterSender struct{}

func (twitterSender) Name() string {
	return "Twitter"
}

func (twitterSender) Send(config Entity.Config, payload Payload) DispatchResult {
	prepared := PreparePlatformText("Twitter", payload.Text)
	imagePaths := payload.ImagePaths()
	if len(imagePaths) > 0 {
		imageResult := sendTwitterImagesDetailed(config, prepared.Text, imagePaths)
		if imageResult.Success {
			return dispatchResultFromPublish("Twitter", imageResult, true, true, prepared.Truncated)
		}
		textResult := sendTwitterTextDetailed(config, prepared.Text)
		textResult.ErrorMessage = mergeImageFallbackError(textResult, imageResult)
		return dispatchResultFromPublish("Twitter", textResult, true, false, prepared.Truncated)
	}
	return dispatchResultFromPublish("Twitter", sendTwitterTextDetailed(config, prepared.Text), false, false, prepared.Truncated)
}

func DefaultSenders() []Sender {
	return []Sender{
		blueSkySender{},
		mastodonSender{},
		twitterSender{},
	}
}

func dispatchResultFromPublish(platform string, result SocialMediaUtils.PublishResult, imageRequested bool, usedImage bool, truncated bool) DispatchResult {
	return DispatchResult{
		Platform:       platform,
		Success:        result.Success,
		ImageRequested: imageRequested,
		UsedImage:      usedImage,
		Truncated:      truncated,
		RemoteID:       result.RemoteID,
		RemoteURL:      result.RemoteURL,
		ErrorMessage:   result.ErrorMessage,
	}
}

func mergeImageFallbackError(textResult SocialMediaUtils.PublishResult, imageResult SocialMediaUtils.PublishResult) string {
	if imageResult.ErrorMessage == "" {
		return textResult.ErrorMessage
	}
	if !textResult.Success {
		if textResult.ErrorMessage != "" {
			return textResult.ErrorMessage
		}
		return imageResult.ErrorMessage
	}
	return fmt.Sprintf("图片上传失败，已降级为纯文本: %s", imageResult.ErrorMessage)
}
