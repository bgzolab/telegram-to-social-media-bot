package SocialMediaUtils

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"telegram-message-sync-bot/internal/Entity"
	"time"

	"github.com/michimani/gotwi/media/upload"
	uploadTypes "github.com/michimani/gotwi/media/upload/types"
	"github.com/michimani/gotwi/tweet/managetweet"
	manageTweetTypes "github.com/michimani/gotwi/tweet/managetweet/types"

	"github.com/michimani/gotwi"
)

const twitterHTTPTimeout = 90 * time.Second

var newTwitterClient = func(config *gotwi.NewClientInput) (gotwi.IClient, error) {
	return gotwi.NewClient(config)
}

var twitterCreateTweet = func(client gotwi.IClient, input *manageTweetTypes.CreateInput) (*manageTweetTypes.CreateOutput, error) {
	return managetweet.Create(context.Background(), client, input)
}

var twitterUploadInitialize = func(client gotwi.IClient, input *uploadTypes.InitializeInput) (*uploadTypes.InitializeOutput, error) {
	return upload.Initialize(context.Background(), client, input)
}

var twitterUploadAppend = func(client gotwi.IClient, input *uploadTypes.AppendInput) (*uploadTypes.AppendOutput, error) {
	return upload.Append(context.Background(), client, input)
}

var twitterUploadFinalize = func(client gotwi.IClient, input *uploadTypes.FinalizeInput) (*uploadTypes.FinalizeOutput, error) {
	return upload.Finalize(context.Background(), client, input)
}

func initTwitter(config Entity.Config) gotwi.NewClientInput {

	Twitter := config.SocialMediaSync.Twitter
	return gotwi.NewClientInput{
		AuthenticationMethod: gotwi.AuthenMethodOAuth1UserContext,
		OAuthToken:           Twitter.OauthToken,
		OAuthTokenSecret:     Twitter.OauthTokenSecret,
		HTTPClient:           &http.Client{Timeout: twitterHTTPTimeout},
	}
}

func SendTwitter(globalConfig Entity.Config, Message string) bool {
	return SendTwitterDetailed(globalConfig, Message).Success
}

func SendTwitterWithImage(globalConfig Entity.Config, Message string, imagePath string) bool {
	return SendTwitterWithImageDetailed(globalConfig, Message, imagePath).Success
}

func SendTwitterWithImages(globalConfig Entity.Config, Message string, imagePaths []string) bool {
	return SendTwitterWithImagesDetailed(globalConfig, Message, imagePaths).Success
}

func SendTwitterDetailed(globalConfig Entity.Config, message string) PublishResult {
	return sendTwitterImagesPostDetailed(globalConfig, message, nil)
}

func SendTwitterWithImageDetailed(globalConfig Entity.Config, message string, imagePath string) PublishResult {
	if imagePath == "" {
		return sendTwitterImagesPostDetailed(globalConfig, message, nil)
	}
	return sendTwitterImagesPostDetailed(globalConfig, message, []string{imagePath})
}

// SendTwitterWithImagesDetailed 发布“文本 + 多图”推文；超过单帖上限的图片按回复线程续发。
func SendTwitterWithImagesDetailed(globalConfig Entity.Config, message string, imagePaths []string) PublishResult {
	return sendTwitterImagesPostDetailed(globalConfig, message, imagePaths)
}

func sendTwitterImagesPostDetailed(globalConfig Entity.Config, message string, imagePaths []string) PublishResult {
	// 提前返回结果失败
	if globalConfig.SocialMediaSync.Twitter.Enable == false {
		log.Println("Twitter is not enabled in the configuration.")
		return PublishResult{ErrorMessage: "Twitter is not enabled in the configuration."}
	}

	config := initTwitter(globalConfig)
	/**
	* more config for
	* GOTWI_API_KEY
	* GOTWI_API_KEY_SECRET
	 */

	c, err := newTwitterClient(&config)
	if err != nil {
		fmt.Println(err)
		return PublishResult{ErrorMessage: err.Error()}
	}

	chunks := chunkImagePaths(imagePaths, maxImagesPerPost)
	if len(chunks) == 0 {
		chunks = [][]string{nil}
	}

	result := PublishResult{}
	var lastTweetID string
	for index, chunk := range chunks {
		p := &manageTweetTypes.CreateInput{}
		if index == 0 {
			p.Text = gotwi.String(message)
		}
		if lastTweetID != "" {
			p.Reply = &manageTweetTypes.CreateInputReply{InReplyToTweetID: lastTweetID}
		}

		uploadErr := error(nil)
		for _, imagePath := range chunk {
			mediaID, err := uploadTwitterImage(c, imagePath)
			if err != nil {
				fmt.Println(err)
				uploadErr = err
				continue
			}
			if p.Media == nil {
				p.Media = &manageTweetTypes.CreateInputMedia{}
			}
			p.Media.MediaIDs = append(p.Media.MediaIDs, mediaID)
		}
		// 首帖图片全部上传失败时保留原有降级语义：交给上层改为纯文本发送。
		if index == 0 && len(chunk) > 0 && p.Media == nil {
			return PublishResult{ErrorMessage: describeTwitterUploadError(uploadErr)}
		}
		if index > 0 && p.Media == nil {
			result.ErrorMessage = fmt.Sprintf("后续图片线程发布失败: %s", describeTwitterUploadError(uploadErr))
			break
		}

		res, err := twitterCreateTweet(c, p)
		if err != nil {
			fmt.Println(err.Error())
			if index == 0 {
				return PublishResult{ErrorMessage: err.Error()}
			}
			result.ErrorMessage = fmt.Sprintf("后续图片线程发布失败: %v", err)
			break
		}

		remoteID := gotwi.StringValue(res.Data.ID)
		if index == 0 {
			result.Success = true
			result.RemoteID = remoteID
			result.RemoteURL = fmt.Sprintf("https://twitter.com/i/web/status/%s", remoteID)
		}
		lastTweetID = remoteID
	}

	return result
}

func describeTwitterUploadError(err error) string {
	if err == nil {
		return "图片上传失败"
	}
	return err.Error()
}

func uploadTwitterImage(client gotwi.IClient, imagePath string) (string, error) {
	fileBytes, err := os.ReadFile(imagePath)
	if err != nil {
		return "", err
	}

	mediaType, err := resolveTwitterMediaType(fileBytes)
	if err != nil {
		return "", err
	}

	initRes, err := twitterUploadInitialize(client, &uploadTypes.InitializeInput{
		MediaType:     mediaType,
		TotalBytes:    len(fileBytes),
		Shared:        false,
		MediaCategory: uploadTypes.MediaCategoryTweetImage,
	})
	if err != nil {
		return "", err
	}

	mediaID := initRes.Data.MediaID
	if _, err := twitterUploadAppend(client, &uploadTypes.AppendInput{
		MediaID:      mediaID,
		Media:        bytes.NewReader(fileBytes),
		SegmentIndex: 0,
	}); err != nil {
		return "", err
	}

	if _, err := twitterUploadFinalize(client, &uploadTypes.FinalizeInput{MediaID: mediaID}); err != nil {
		return "", err
	}

	return mediaID, nil
}

func resolveTwitterMediaType(fileBytes []byte) (uploadTypes.MediaType, error) {
	switch http.DetectContentType(fileBytes) {
	case string(uploadTypes.MediaTypeJPEG):
		return uploadTypes.MediaTypeJPEG, nil
	case string(uploadTypes.MediaTypePNG):
		return uploadTypes.MediaTypePNG, nil
	case string(uploadTypes.MediaTypeGIF):
		return uploadTypes.MediaTypeGIF, nil
	case string(uploadTypes.MediaTypeWebP):
		return uploadTypes.MediaTypeWebP, nil
	default:
		return "", os.ErrInvalid
	}
}
