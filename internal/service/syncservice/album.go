package syncservice

import (
	"sort"
	"strings"

	"telegram-message-sync-bot/internal/Entity"
)

// AlbumHead 返回相册用于生成社媒正文的成员消息。
// 规则：按源消息ID排序后取最早一条带正文的成员；全部成员正文为空时回退到最早一条。
// 这样做的原因是 Telegram 相册的 caption 通常挂在第一条，但实测存在挂在中后段的情况，不能写死第一条。
func AlbumHead(messages []Entity.Message) Entity.Message {
	if len(messages) == 0 {
		return Entity.Message{}
	}

	ordered := SortAlbumMessages(messages)
	for _, msg := range ordered {
		if strings.TrimSpace(msg.Content) != "" {
			return msg
		}
	}
	return ordered[0]
}

// SortAlbumMessages 按源消息ID升序返回相册成员副本，保证正文选取与图片顺序稳定。
func SortAlbumMessages(messages []Entity.Message) []Entity.Message {
	ordered := make([]Entity.Message, len(messages))
	copy(ordered, messages)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].MessageID != ordered[j].MessageID {
			return ordered[i].MessageID < ordered[j].MessageID
		}
		return ordered[i].ID < ordered[j].ID
	})
	return ordered
}

// AlbumText 拼接相册内所有非空正文（按源消息ID顺序、去重、换行分隔）。
// 这样做的原因是实测存在多条成员各带 caption 的相册，只取第一条会静默丢失内容。
func AlbumText(messages []Entity.Message) string {
	parts := make([]string, 0, len(messages))
	seen := make(map[string]struct{}, len(messages))

	for _, msg := range SortAlbumMessages(messages) {
		text := strings.TrimSpace(msg.Content)
		if text == "" {
			continue
		}
		if _, ok := seen[text]; ok {
			continue
		}
		seen[text] = struct{}{}
		parts = append(parts, text)
	}

	return strings.Join(parts, "\n")
}

// CollectAlbumImagePaths 按相册成员顺序收集可用的本地图片路径，用于多图投递。
// 这样做的原因是社媒平台上传需要本地文件，历史补录的行没有附件，只能跳过。
func CollectAlbumImagePaths(config Entity.Config, messages []Entity.Message) []string {
	paths := make([]string, 0, len(messages))
	seen := make(map[string]struct{}, len(messages))

	for _, msg := range SortAlbumMessages(messages) {
		for _, attachment := range msg.Attachments {
			if attachment.Type != Entity.ImageMessage {
				continue
			}

			resolved := ResolvePayloadImagePath(config, attachment.FilePath)
			if resolved == "" {
				continue
			}
			if _, ok := seen[resolved]; ok {
				continue
			}
			seen[resolved] = struct{}{}
			paths = append(paths, resolved)
		}
	}

	return paths
}
