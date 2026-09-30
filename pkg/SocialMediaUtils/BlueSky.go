package SocialMediaUtils

import (
	"bytes"
	"encoding/json"
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

func SendBlueSkyDetailed(config Entity.Config, message string) PublishResult {
	return sendBlueSkyPostDetailed(config, message, "")
}

func SendBlueSkyWithImageDetailed(config Entity.Config, message string, imagePath string) PublishResult {
	return sendBlueSkyPostDetailed(config, message, imagePath)
}

func sendBlueSkyPostDetailed(config Entity.Config, message string, imagePath string) PublishResult {
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

	post, err := buildBlueSkyPost(message, imagePath, bearerToken)
	if err != nil {
		return PublishResult{ErrorMessage: err.Error()}
	}
	var repoName string = dst.DID
	if repoName == "" {
		repoName = identifier
	}
	var collection string = "app.bsky.feed.post"

	var created repo.CreateRecordResponse
	recordErr := blueSkyCreateRecord(&created, bearerToken, repoName, collection, post)

	if nil != recordErr {
		return PublishResult{ErrorMessage: recordErr.Error()}
	}
	remoteID := created.URI
	if remoteID == "" {
		remoteID = created.CID
	}
	return PublishResult{Success: true, RemoteID: remoteID}
}

func buildBlueSkyPost(message string, imagePath string, bearerToken string) (map[string]any, error) {
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

	if imagePath == "" {
		return post, nil
	}

	blob, err := uploadBlueSkyBlob(imagePath, bearerToken)
	if err != nil {
		return nil, err
	}

	post["embed"] = map[string]any{
		"$type": "app.bsky.embed.images",
		"images": []map[string]any{
			{
				"alt":   "",
				"image": blob,
			},
		},
	}

	return post, nil
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
		return nil, os.ErrInvalid
	}

	var payload struct {
		Blob map[string]any `json:"blob"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}
	if payload.Blob == nil {
		return nil, os.ErrInvalid
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
