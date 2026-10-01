package SocialMediaUtils

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strings"
	"telegram-message-sync-bot/internal/Entity"
	"time"
	"unicode"

	"github.com/reiver/go-atproto/com/atproto/repo"
	"github.com/reiver/go-atproto/com/atproto/server"
	"github.com/rivo/uniseg"
)

const blueSkyUploadBlobURL = "https://bsky.social/xrpc/com.atproto.repo.uploadBlob"
const blueSkyImageMaxBytes = 1000000
const blueSkyTagMaxGraphemes = 64
const blueSkyTagMaxBytes = 640

var blueSkyCreateSession = server.CreateSession
var blueSkyCreateRecord = repo.CreateRecord
var blueSkyHTTPClient = http.DefaultClient

var blueSkyURLRegexp = regexp.MustCompile(`https?://[^\s]+`)

// blueSkyTagRegexp 对齐官方客户端 @atproto/api 的 TAG_REGEX：
// hashtag 前缀必须是行首或空白，body 排除空白与零宽字符。
// 空白类与 JS 的 \s 等价（Go 的 \s 不含 \v 与 BOM，需显式补上）。
var blueSkyTagRegexp = regexp.MustCompile(`(?:^|[\s\p{Z}\x{000B}\x{FEFF}])([#\x{FF03}])([^\s\p{Z}\x{000B}\x{FEFF}\x{00AD}\x{2060}\x{200A}-\x{200D}\x{20E2}]+)`)

func initBlueSky(config Entity.Config) (username string, password string) {
	BlueSky := config.SocialMediaSync.BlueSky
	return BlueSky.Identifier, BlueSky.Password
}

func SendBlueSky(config Entity.Config, Message string) bool {
	return SendBlueSkyDetailed(config, Message).Success
}

func SendBlueSkyWithImage(config Entity.Config, Message string, imagePath string) bool {
	return SendBlueSkyWithImageDetailed(config, Message, imagePath).Success
}

func SendBlueSkyWithImages(config Entity.Config, Message string, imagePaths []string) bool {
	return SendBlueSkyWithImagesDetailed(config, Message, imagePaths).Success
}

func SendBlueSkyDetailed(config Entity.Config, message string) PublishResult {
	return sendBlueSkyImagesPostDetailed(config, message, nil)
}

func SendBlueSkyWithImageDetailed(config Entity.Config, message string, imagePath string) PublishResult {
	if imagePath == "" {
		return sendBlueSkyImagesPostDetailed(config, message, nil)
	}
	return sendBlueSkyImagesPostDetailed(config, message, []string{imagePath})
}

// SendBlueSkyWithImagesDetailed 发布“文本 + 多图”帖子；超过单帖上限的图片按回复线程续发。
// 这样做的原因是相册聚合后的成员图片数可能超过平台单帖上限，不能静默丢弃。
func SendBlueSkyWithImagesDetailed(config Entity.Config, message string, imagePaths []string) PublishResult {
	return sendBlueSkyImagesPostDetailed(config, message, imagePaths)
}

func sendBlueSkyImagesPostDetailed(config Entity.Config, message string, imagePaths []string) PublishResult {
	if config.SocialMediaSync.BlueSky.Enable == false {
		return PublishResult{ErrorMessage: "BlueSky is not enabled in the configuration."}
	}

	/**
	 * 开启了二步验证怎么办？
	 */
	var identifier, password = initBlueSky(config)

	var dst server.CreateSessionResponse
	err := blueSkyCreateSession(&dst, identifier, password)
	if nil != err {
		return PublishResult{ErrorMessage: err.Error()}
	}
	bearerToken := dst.AccessJWT
	var repoName string = dst.DID
	if repoName == "" {
		repoName = identifier
	}
	var collection string = "app.bsky.feed.post"

	chunks := chunkImagePaths(imagePaths, maxImagesPerPost)
	if len(chunks) == 0 {
		chunks = [][]string{nil}
	}

	result := PublishResult{}
	skippedImages := 0
	var rootURI, rootCID, parentURI, parentCID string
	for index, chunk := range chunks {
		postText := ""
		if index == 0 {
			postText = message
		}

		post, skipped, err := buildBlueSkyPostWithImages(postText, chunk, bearerToken, buildBlueSkyReplyRef(rootURI, rootCID, parentURI, parentCID))
		skippedImages += skipped
		if err != nil {
			if index == 0 {
				return PublishResult{ErrorMessage: err.Error()}
			}
			result.ErrorMessage = fmt.Sprintf("后续图片线程发布失败: %v", err)
			break
		}

		var created repo.CreateRecordResponse
		recordErr := blueSkyCreateRecord(&created, bearerToken, repoName, collection, post)
		if nil != recordErr {
			if index == 0 {
				return PublishResult{ErrorMessage: recordErr.Error()}
			}
			result.ErrorMessage = fmt.Sprintf("后续图片线程发布失败: %v", recordErr)
			break
		}

		if index == 0 {
			remoteID := created.URI
			if remoteID == "" {
				remoteID = created.CID
			}
			result.Success = true
			result.RemoteID = remoteID
		}
		if rootURI == "" {
			rootURI, rootCID = created.URI, created.CID
		}
		parentURI, parentCID = created.URI, created.CID
	}

	if result.Success && skippedImages > 0 && result.ErrorMessage == "" {
		result.ErrorMessage = fmt.Sprintf("%d 张图片上传失败，已跳过", skippedImages)
	}

	return result
}

// buildBlueSkyReplyRef 构造线程回复引用；根帖与父帖信息缺失时返回 nil，按独立帖子发布。
func buildBlueSkyReplyRef(rootURI, rootCID, parentURI, parentCID string) map[string]any {
	if rootURI == "" || rootCID == "" || parentURI == "" || parentCID == "" {
		return nil
	}

	return map[string]any{
		"root": map[string]any{
			"uri": rootURI,
			"cid": rootCID,
		},
		"parent": map[string]any{
			"uri": parentURI,
			"cid": parentCID,
		},
	}
}

// buildBlueSkyPostWithImages 构造帖子记录：文本 facets + 多图 embed + 可选线程回复引用。
// 返回被跳过的图片数量：单张图片上传失败时跳过该图片，只有全部图片都失败才返回错误，
// 交由上层决定降级为纯文本；部分失败会由调用方写入投递结果的错误信息。
func buildBlueSkyPostWithImages(message string, imagePaths []string, bearerToken string, reply map[string]any) (map[string]any, int, error) {
	when := time.Now().Format("2006-01-02T15:04:05.999Z")
	post := map[string]any{
		"$type":     "app.bsky.feed.post",
		"text":      message,
		"createdAt": when,
	}
	facets := buildBlueSkyLinkFacets(message)
	facets = append(facets, buildBlueSkyTagFacets(message)...)
	if len(facets) > 0 {
		post["facets"] = facets
	}
	if reply != nil {
		post["reply"] = reply
	}

	if len(imagePaths) == 0 {
		return post, 0, nil
	}

	images := make([]map[string]any, 0, len(imagePaths))
	var uploadErr error
	for _, imagePath := range imagePaths {
		blob, err := uploadBlueSkyBlob(imagePath, bearerToken)
		if err != nil {
			uploadErr = err
			continue
		}

		images = append(images, map[string]any{
			"alt":   "",
			"image": blob,
		})
	}
	if len(images) == 0 {
		if uploadErr == nil {
			uploadErr = os.ErrInvalid
		}
		return nil, len(imagePaths), uploadErr
	}

	post["embed"] = map[string]any{
		"$type":  "app.bsky.embed.images",
		"images": images,
	}

	return post, len(imagePaths) - len(images), nil
}

func uploadBlueSkyBlob(imagePath string, bearerToken string) (map[string]any, error) {
	data, err := os.ReadFile(imagePath)
	if err != nil {
		return nil, err
	}
	if len(data) > blueSkyImageMaxBytes {
		return nil, os.ErrInvalid
	}

	req, err := http.NewRequest(http.MethodPost, blueSkyUploadBlobURL, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+bearerToken)
	req.Header.Set("Content-Type", http.DetectContentType(data))

	resp, err := blueSkyHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("bluesky blob upload failed: %s", resp.Status)
	}

	var payload struct {
		Blob map[string]any `json:"blob"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}
	if payload.Blob == nil {
		return nil, fmt.Errorf("bluesky blob upload returned empty blob")
	}

	return payload.Blob, nil
}

func buildBlueSkyLinkFacets(message string) []map[string]any {
	matches := blueSkyURLRegexp.FindAllStringIndex(message, -1)
	if len(matches) == 0 {
		return nil
	}

	facets := make([]map[string]any, 0, len(matches))
	for _, match := range matches {
		start := match[0]
		end := trimBlueSkyLinkEnd(message, match[1])
		if end <= start {
			continue
		}
		uri := message[start:end]
		facets = append(facets, map[string]any{
			"index": map[string]any{
				"byteStart": start,
				"byteEnd":   end,
			},
			"features": []map[string]any{
				{
					"$type": "app.bsky.richtext.facet#link",
					"uri":   uri,
				},
			},
		})
	}

	if len(facets) == 0 {
		return nil
	}

	return facets
}

func trimBlueSkyLinkEnd(message string, end int) int {
	for end > 0 {
		r, size := lastRuneBefore(message[:end])
		if size == 0 {
			return end
		}
		if !strings.ContainsRune(").,!?:;)]}'\"", r) {
			return end
		}
		end -= size
	}
	return end
}

func lastRuneBefore(text string) (rune, int) {
	for i := len(text); i > 0; {
		r := rune(text[i-1])
		if r < 0x80 {
			return r, 1
		}
		break
	}

	runes := []rune(text)
	if len(runes) == 0 {
		return 0, 0
	}
	last := runes[len(runes)-1]
	return last, len(string(last))
}

// buildBlueSkyTagFacets 将 hashtag 标注为 app.bsky.richtext.facet#tag。
// Bluesky 客户端不会自行识别正文里的 hashtag，缺少 facet 时只能按纯文本渲染。
// 规则对齐官方客户端 @atproto/api 的 detectFacets：tag 值不含 '#'，
// 但 facet 的字节范围包含 '#'，索引为 UTF-8 字节偏移。
func buildBlueSkyTagFacets(message string) []map[string]any {
	matches := blueSkyTagRegexp.FindAllStringSubmatchIndex(message, -1)
	if len(matches) == 0 {
		return nil
	}

	facets := make([]map[string]any, 0, len(matches))
	for _, match := range matches {
		// match: [fullStart, fullEnd, hashStart, hashEnd, bodyStart, bodyEnd]
		hashStart := match[2]
		bodyStart := match[4]
		body := message[bodyStart:match[5]]

		// 官方规则：'#' 后紧跟变体选择符（如 keycap emoji "#️⃣"）时不算 hashtag。
		if strings.HasPrefix(body, "\uFE0F") {
			continue
		}

		// 尾部标点不计入 tag，也不计入 facet 范围。
		tag := strings.TrimRightFunc(body, unicode.IsPunct)
		if !isValidBlueSkyTag(tag) {
			continue
		}

		facets = append(facets, map[string]any{
			"index": map[string]any{
				"byteStart": hashStart,
				"byteEnd":   bodyStart + len(tag),
			},
			"features": []map[string]any{
				{
					"$type": "app.bsky.richtext.facet#tag",
					"tag":   tag,
				},
			},
		})
	}

	if len(facets) == 0 {
		return nil
	}

	return facets
}

// isValidBlueSkyTag 对齐官方客户端校验：至少包含一个非数字、非标点字符，
// 且满足 tag 词法约束（64 graphemes / 640 bytes），避免生成被 PDS 拒绝的 facet。
// 空白与零宽字符由 blueSkyTagRegexp 的 body 字符类排除，不在此重复维护。
func isValidBlueSkyTag(tag string) bool {
	if tag == "" || len(tag) > blueSkyTagMaxBytes {
		return false
	}
	if uniseg.GraphemeClusterCount(tag) > blueSkyTagMaxGraphemes {
		return false
	}

	for _, r := range tag {
		if (r >= '0' && r <= '9') || unicode.IsPunct(r) {
			continue
		}
		return true
	}

	return false
}
