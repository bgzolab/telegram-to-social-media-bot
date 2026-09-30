package SocialMediaUtils

import (
	"context"
	"fmt"
	"log"
	"strings"
	"telegram-message-sync-bot/internal/Entity"

	"github.com/mattn/go-mastodon"
)

type mastodonClient interface {
	UploadMedia(ctx context.Context, file string) (*mastodon.Attachment, error)
	PostStatus(ctx context.Context, toot *mastodon.Toot) (*mastodon.Status, error)
}

var newMastodonClient = func(config *mastodon.Config) mastodonClient {
	return mastodon.NewClient(config)
}

func initMastodon(config Entity.Config) mastodon.Config {
	Mastodon := config.SocialMediaSync.Mastodon
	return mastodon.Config{
		Server:       Mastodon.Instance,
		ClientID:     Mastodon.ClientId,
		ClientSecret: Mastodon.ClientSecret,
		AccessToken:  Mastodon.AccessToken,
	}
}

func SendMastodon(globalConfig Entity.Config, Message string) bool {
	return SendMastodonDetailed(globalConfig, Message).Success
}

func SendMastodonDetailed(globalConfig Entity.Config, Message string) PublishResult {
	if globalConfig.SocialMediaSync.Mastodon.Enable == false {
		log.Println("Mastodon is not enabled in the configuration.")
		return PublishResult{ErrorMessage: "Mastodon is not enabled in the configuration."}
	}

	config := initMastodon(globalConfig)
	return postMastodonWithImages(newMastodonClient(&config), Message, nil)
}

func SendMastodonWithImage(globalConfig Entity.Config, Message string, imagePath string) bool {
	return SendMastodonWithImageDetailed(globalConfig, Message, imagePath).Success
}

func SendMastodonWithImageDetailed(globalConfig Entity.Config, Message string, imagePath string) PublishResult {
	if globalConfig.SocialMediaSync.Mastodon.Enable == false {
		log.Println("Mastodon is not enabled in the configuration.")
		return PublishResult{ErrorMessage: "Mastodon is not enabled in the configuration."}
	}

	config := initMastodon(globalConfig)
	if imagePath == "" {
		return postMastodonWithImages(newMastodonClient(&config), Message, nil)
	}
	return postMastodonWithImages(newMastodonClient(&config), Message, []string{imagePath})
}

func SendMastodonWithImages(globalConfig Entity.Config, Message string, imagePaths []string) bool {
	return SendMastodonWithImagesDetailed(globalConfig, Message, imagePaths).Success
}

// SendMastodonWithImagesDetailed 发布“文本 + 多图”嘟文；超过单帖上限的图片按回复线程续发。
func SendMastodonWithImagesDetailed(globalConfig Entity.Config, Message string, imagePaths []string) PublishResult {
	if globalConfig.SocialMediaSync.Mastodon.Enable == false {
		log.Println("Mastodon is not enabled in the configuration.")
		return PublishResult{ErrorMessage: "Mastodon is not enabled in the configuration."}
	}

	config := initMastodon(globalConfig)
	return postMastodonWithImages(newMastodonClient(&config), Message, imagePaths)
}

func postMastodonWithImages(client mastodonClient, message string, imagePaths []string) PublishResult {
	if client == nil {
		return PublishResult{ErrorMessage: "mastodon client is nil"}
	}

	visibility := "public"

	chunks := chunkImagePaths(imagePaths, maxImagesPerPost)
	if len(chunks) == 0 {
		chunks = [][]string{nil}
	}

	result := PublishResult{}
	skippedImages := 0
	var lastStatusID mastodon.ID
	for index, chunk := range chunks {
		toot := &mastodon.Toot{Visibility: visibility}
		if index == 0 {
			toot.Status = message
		}
		if lastStatusID != "" {
			toot.InReplyToID = lastStatusID
		}

		uploadErrMessage := ""
		for _, imagePath := range chunk {
			attachment, err := client.UploadMedia(context.Background(), imagePath)
			if err != nil {
				log.Println(err)
				uploadErrMessage = describeMastodonMediaUploadError(err)
				skippedImages++
				continue
			}
			toot.MediaIDs = append(toot.MediaIDs, attachment.ID)
		}
		// 首帖图片全部上传失败时保留原有降级语义：交给上层改为纯文本发送。
		if index == 0 && len(chunk) > 0 && len(toot.MediaIDs) == 0 {
			return PublishResult{ErrorMessage: uploadErrMessage}
		}
		if index > 0 && len(toot.MediaIDs) == 0 {
			result.ErrorMessage = fmt.Sprintf("后续图片线程发布失败: %s", uploadErrMessage)
			break
		}

		post, err := client.PostStatus(context.Background(), toot)
		if err != nil {
			log.Println(err)
			if index == 0 {
				return PublishResult{ErrorMessage: err.Error()}
			}
			result.ErrorMessage = fmt.Sprintf("后续图片线程发布失败: %v", err)
			break
		}

		if index == 0 {
			result.Success = true
			result.RemoteID = string(post.ID)
			result.RemoteURL = post.URL
		}
		lastStatusID = post.ID
	}

	if result.Success && skippedImages > 0 && result.ErrorMessage == "" {
		result.ErrorMessage = fmt.Sprintf("%d 张图片上传失败，已跳过", skippedImages)
	}

	return result
}

func describeMastodonMediaUploadError(err error) string {
	if err == nil {
		return ""
	}

	message := err.Error()
	lowerMessage := strings.ToLower(message)
	if strings.Contains(lowerMessage, "outside the authorized scopes") ||
		(strings.Contains(lowerMessage, "403") && strings.Contains(lowerMessage, "forbidden")) {
		return "Mastodon 图片上传被拒绝，当前 AccessToken 缺少媒体上传权限 scope（通常需要 write:media）"
	}

	return message
}
