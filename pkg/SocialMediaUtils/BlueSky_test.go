package SocialMediaUtils

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"telegram-message-sync-bot/internal/Entity"

	"github.com/reiver/go-atproto/com/atproto/repo"
	"github.com/reiver/go-atproto/com/atproto/server"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestBuildBlueSkyPost_WithImageEmbed(t *testing.T) {
	root := t.TempDir()
	imagePath := filepath.Join(root, "single.png")
	if err := os.WriteFile(imagePath, []byte("png-data"), 0o644); err != nil {
		t.Fatalf("failed to create test image: %v", err)
	}

	originalClient := blueSkyHTTPClient
	blueSkyHTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() != blueSkyUploadBlobURL {
			t.Fatalf("unexpected url: %s", req.URL.String())
		}
		if req.Header.Get("Authorization") != "Bearer token" {
			t.Fatalf("unexpected auth header: %s", req.Header.Get("Authorization"))
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"blob":{"$type":"blob","mimeType":"image/png","size":8}}`)),
			Header:     make(http.Header),
		}, nil
	})}
	defer func() {
		blueSkyHTTPClient = originalClient
	}()

	post, err := buildBlueSkyPost("hello", imagePath, "token")
	if err != nil {
		t.Fatalf("expected buildBlueSkyPost success, got: %v", err)
	}
	if post["text"] != "hello" {
		t.Fatalf("unexpected post text: %+v", post)
	}
	embed, ok := post["embed"].(map[string]any)
	if !ok {
		t.Fatalf("expected embed map, got: %#v", post["embed"])
	}
	if embed["$type"] != "app.bsky.embed.images" {
		t.Fatalf("unexpected embed type: %+v", embed)
	}
}

func TestBuildBlueSkyPost_ReturnErrorWhenUploadFails(t *testing.T) {
	root := t.TempDir()
	imagePath := filepath.Join(root, "single.png")
	if err := os.WriteFile(imagePath, []byte("png-data"), 0o644); err != nil {
		t.Fatalf("failed to create test image: %v", err)
	}

	originalClient := blueSkyHTTPClient
	blueSkyHTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return nil, fmt.Errorf("upload failed")
	})}
	defer func() {
		blueSkyHTTPClient = originalClient
	}()

	_, err := buildBlueSkyPost("hello", imagePath, "token")
	if err == nil {
		t.Fatalf("expected buildBlueSkyPost failure when upload fails")
	}
}

func TestBuildBlueSkyPost_AddsLinkFacets(t *testing.T) {
	message := "read this https://example.com/path?x=1 and this https://bsky.app/profile/test"

	post, err := buildBlueSkyPost(message, "", "token")
	if err != nil {
		t.Fatalf("expected buildBlueSkyPost success, got: %v", err)
	}

	facets, ok := post["facets"].([]map[string]any)
	if !ok {
		t.Fatalf("expected facets slice, got: %#v", post["facets"])
	}
	if len(facets) != 2 {
		t.Fatalf("expected 2 facets, got: %d", len(facets))
	}

	firstIndex, ok := facets[0]["index"].(map[string]any)
	if !ok {
		t.Fatalf("expected first facet index, got: %#v", facets[0]["index"])
	}
	firstFeatures, ok := facets[0]["features"].([]map[string]any)
	if !ok || len(firstFeatures) != 1 {
		t.Fatalf("expected first facet features, got: %#v", facets[0]["features"])
	}
	if firstFeatures[0]["uri"] != "https://example.com/path?x=1" {
		t.Fatalf("unexpected first facet uri: %#v", firstFeatures[0]["uri"])
	}
	if firstIndex["byteStart"] != strings.Index(message, "https://example.com/path?x=1") {
		t.Fatalf("unexpected first facet start: %#v", firstIndex)
	}
}

func TestBuildBlueSkyPost_TrimsTrailingPunctuationFromLinkFacet(t *testing.T) {
	message := "see https://example.com/test."

	post, err := buildBlueSkyPost(message, "", "token")
	if err != nil {
		t.Fatalf("expected buildBlueSkyPost success, got: %v", err)
	}

	facets := post["facets"].([]map[string]any)
	features := facets[0]["features"].([]map[string]any)
	if features[0]["uri"] != "https://example.com/test" {
		t.Fatalf("unexpected facet uri: %#v", features[0]["uri"])
	}
}

func TestBuildBlueSkyPost_AddsTagFacets(t *testing.T) {
	message := "hello #update #pm"

	post, err := buildBlueSkyPost(message, "", "token")
	if err != nil {
		t.Fatalf("expected buildBlueSkyPost success, got: %v", err)
	}

	facets, ok := post["facets"].([]map[string]any)
	if !ok || len(facets) != 2 {
		t.Fatalf("expected 2 facets, got: %#v", post["facets"])
	}

	firstFeatures, ok := facets[0]["features"].([]map[string]any)
	if !ok || len(firstFeatures) != 1 {
		t.Fatalf("expected first facet features, got: %#v", facets[0]["features"])
	}
	if firstFeatures[0]["$type"] != "app.bsky.richtext.facet#tag" {
		t.Fatalf("unexpected first facet type: %#v", firstFeatures[0]["$type"])
	}
	if firstFeatures[0]["tag"] != "update" {
		t.Fatalf("unexpected first facet tag: %#v", firstFeatures[0]["tag"])
	}

	firstIndex, ok := facets[0]["index"].(map[string]any)
	if !ok {
		t.Fatalf("expected first facet index, got: %#v", facets[0]["index"])
	}
	if firstIndex["byteStart"] != strings.Index(message, "#update") {
		t.Fatalf("unexpected first facet byteStart: %#v", firstIndex)
	}
	if firstIndex["byteEnd"] != strings.Index(message, "#update")+len("#update") {
		t.Fatalf("unexpected first facet byteEnd: %#v", firstIndex)
	}

	secondFeatures := facets[1]["features"].([]map[string]any)
	if secondFeatures[0]["tag"] != "pm" {
		t.Fatalf("unexpected second facet tag: %#v", secondFeatures[0]["tag"])
	}
}

func TestBuildBlueSkyPost_TagFacetUsesUTF8ByteOffsets(t *testing.T) {
	message := "中文 #标签"

	post, err := buildBlueSkyPost(message, "", "token")
	if err != nil {
		t.Fatalf("expected buildBlueSkyPost success, got: %v", err)
	}

	facets := post["facets"].([]map[string]any)
	if len(facets) != 1 {
		t.Fatalf("expected 1 facet, got: %d", len(facets))
	}

	index := facets[0]["index"].(map[string]any)
	if index["byteStart"] != len("中文 ") {
		t.Fatalf("expected byteStart %d, got: %#v", len("中文 "), index)
	}
	if index["byteEnd"] != len(message) {
		t.Fatalf("expected byteEnd %d, got: %#v", len(message), index)
	}

	features := facets[0]["features"].([]map[string]any)
	if features[0]["tag"] != "标签" {
		t.Fatalf("unexpected facet tag: %#v", features[0]["tag"])
	}
}

func TestBuildBlueSkyPost_SkipsInvalidTagFacets(t *testing.T) {
	message := "#123 #1_2 #update2026"

	post, err := buildBlueSkyPost(message, "", "token")
	if err != nil {
		t.Fatalf("expected buildBlueSkyPost success, got: %v", err)
	}

	facets := post["facets"].([]map[string]any)
	if len(facets) != 1 {
		t.Fatalf("expected only the valid hashtag to get a facet, got: %#v", post["facets"])
	}

	features := facets[0]["features"].([]map[string]any)
	if features[0]["tag"] != "update2026" {
		t.Fatalf("unexpected facet tag: %#v", features[0]["tag"])
	}
}

func TestBuildBlueSkyPost_TrimsTrailingPunctuationFromTagFacet(t *testing.T) {
	message := "hello #tag."

	post, err := buildBlueSkyPost(message, "", "token")
	if err != nil {
		t.Fatalf("expected buildBlueSkyPost success, got: %v", err)
	}

	facets := post["facets"].([]map[string]any)
	features := facets[0]["features"].([]map[string]any)
	if features[0]["tag"] != "tag" {
		t.Fatalf("unexpected facet tag: %#v", features[0]["tag"])
	}

	index := facets[0]["index"].(map[string]any)
	if index["byteEnd"] != strings.Index(message, "#tag")+len("#tag") {
		t.Fatalf("expected trailing dot outside the facet range, got: %#v", index)
	}
}

func TestBuildBlueSkyPost_CombinesLinkAndTagFacets(t *testing.T) {
	message := "see https://example.com/x #tag"

	post, err := buildBlueSkyPost(message, "", "token")
	if err != nil {
		t.Fatalf("expected buildBlueSkyPost success, got: %v", err)
	}

	facets := post["facets"].([]map[string]any)
	if len(facets) != 2 {
		t.Fatalf("expected 2 facets, got: %d", len(facets))
	}

	linkFeatures := facets[0]["features"].([]map[string]any)
	if linkFeatures[0]["$type"] != "app.bsky.richtext.facet#link" {
		t.Fatalf("unexpected link facet type: %#v", linkFeatures[0]["$type"])
	}

	tagFeatures := facets[1]["features"].([]map[string]any)
	if tagFeatures[0]["$type"] != "app.bsky.richtext.facet#tag" || tagFeatures[0]["tag"] != "tag" {
		t.Fatalf("unexpected tag facet: %#v", tagFeatures[0])
	}
}

func TestBuildBlueSkyPost_DoesNotTagURLFragments(t *testing.T) {
	message := "see https://example.com/page#section"

	post, err := buildBlueSkyPost(message, "", "token")
	if err != nil {
		t.Fatalf("expected buildBlueSkyPost success, got: %v", err)
	}

	facets := post["facets"].([]map[string]any)
	if len(facets) != 1 {
		t.Fatalf("expected only the link facet, got: %#v", post["facets"])
	}

	features := facets[0]["features"].([]map[string]any)
	if features[0]["$type"] != "app.bsky.richtext.facet#link" {
		t.Fatalf("unexpected facet type: %#v", features[0]["$type"])
	}
}

func TestBuildBlueSkyPost_SkipsTooLongTagFacet(t *testing.T) {
	validTag := strings.Repeat("a", 64)
	message := "#" + validTag + " #" + strings.Repeat("b", 65)

	post, err := buildBlueSkyPost(message, "", "token")
	if err != nil {
		t.Fatalf("expected buildBlueSkyPost success, got: %v", err)
	}

	facets := post["facets"].([]map[string]any)
	if len(facets) != 1 {
		t.Fatalf("expected only the 64-grapheme tag to get a facet, got: %d", len(facets))
	}

	features := facets[0]["features"].([]map[string]any)
	if features[0]["tag"] != validTag {
		t.Fatalf("unexpected facet tag: %#v", features[0]["tag"])
	}
}

func TestBuildBlueSkyPost_StopsTagAtUnicodeWhitespace(t *testing.T) {
	message := "#标签\u3000后续"

	post, err := buildBlueSkyPost(message, "", "token")
	if err != nil {
		t.Fatalf("expected buildBlueSkyPost success, got: %v", err)
	}

	facets := post["facets"].([]map[string]any)
	if len(facets) != 1 {
		t.Fatalf("expected 1 facet, got: %d", len(facets))
	}

	features := facets[0]["features"].([]map[string]any)
	if features[0]["tag"] != "标签" {
		t.Fatalf("unexpected facet tag: %#v", features[0]["tag"])
	}

	index := facets[0]["index"].(map[string]any)
	if index["byteEnd"] != len("#标签") {
		t.Fatalf("expected the full-width space outside the facet range, got: %#v", index)
	}
}

func TestBuildBlueSkyPost_StopsTagAtVerticalTab(t *testing.T) {
	message := "#tag\x0Btail"

	post, err := buildBlueSkyPost(message, "", "token")
	if err != nil {
		t.Fatalf("expected buildBlueSkyPost success, got: %v", err)
	}

	facets := post["facets"].([]map[string]any)
	if len(facets) != 1 {
		t.Fatalf("expected 1 facet, got: %d", len(facets))
	}

	features := facets[0]["features"].([]map[string]any)
	if features[0]["tag"] != "tag" {
		t.Fatalf("unexpected facet tag: %#v", features[0]["tag"])
	}

	index := facets[0]["index"].(map[string]any)
	if index["byteEnd"] != len("#tag") {
		t.Fatalf("expected the vertical tab outside the facet range, got: %#v", index)
	}
}

func TestBuildBlueSkyPost_TagFacetWithFullWidthHash(t *testing.T) {
	message := "＃中文"

	post, err := buildBlueSkyPost(message, "", "token")
	if err != nil {
		t.Fatalf("expected buildBlueSkyPost success, got: %v", err)
	}

	facets := post["facets"].([]map[string]any)
	if len(facets) != 1 {
		t.Fatalf("expected 1 facet, got: %d", len(facets))
	}

	features := facets[0]["features"].([]map[string]any)
	if features[0]["tag"] != "中文" {
		t.Fatalf("unexpected facet tag: %#v", features[0]["tag"])
	}

	index := facets[0]["index"].(map[string]any)
	if index["byteStart"] != 0 {
		t.Fatalf("expected byteStart 0, got: %#v", index)
	}
	if index["byteEnd"] != len("＃中文") {
		t.Fatalf("expected byteEnd %d, got: %#v", len("＃中文"), index)
	}
}

func TestBuildBlueSkyPost_SkipsKeycapEmoji(t *testing.T) {
	message := "#️⃣ keycap"

	post, err := buildBlueSkyPost(message, "", "token")
	if err != nil {
		t.Fatalf("expected buildBlueSkyPost success, got: %v", err)
	}

	if facets, ok := post["facets"]; ok {
		t.Fatalf("expected no facets for keycap emoji, got: %#v", facets)
	}
}

func writeTestImages(t *testing.T, count int) []string {
	t.Helper()

	root := t.TempDir()
	paths := make([]string, 0, count)
	for i := 0; i < count; i++ {
		path := filepath.Join(root, fmt.Sprintf("image-%d.png", i))
		if err := os.WriteFile(path, []byte("png-data"), 0o644); err != nil {
			t.Fatalf("failed to create test image: %v", err)
		}
		paths = append(paths, path)
	}
	return paths
}

func stubBlueSkyBlobUpload(t *testing.T) {
	t.Helper()

	originalClient := blueSkyHTTPClient
	blueSkyHTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"blob":{"$type":"blob","mimeType":"image/png","size":8}}`)),
			Header:     make(http.Header),
		}, nil
	})}
	t.Cleanup(func() {
		blueSkyHTTPClient = originalClient
	})
}

func TestBuildBlueSkyPostWithImages_EmbedsAllImages(t *testing.T) {
	stubBlueSkyBlobUpload(t)
	imagePaths := writeTestImages(t, 3)

	post, skipped, err := buildBlueSkyPostWithImages("albums", imagePaths, "token", nil)
	if err != nil {
		t.Fatalf("expected build success, got: %v", err)
	}
	if skipped != 0 {
		t.Fatalf("expected no skipped images, got %d", skipped)
	}

	embed := post["embed"].(map[string]any)
	images := embed["images"].([]map[string]any)
	if len(images) != 3 {
		t.Fatalf("expected 3 embedded images, got %d", len(images))
	}
	if _, exists := post["reply"]; exists {
		t.Fatalf("expected no reply ref for standalone post")
	}
}

func TestBuildBlueSkyPostWithImages_AddsReplyRef(t *testing.T) {
	imagePaths := writeTestImages(t, 1)

	reply := map[string]any{
		"root":   map[string]any{"uri": "at://root", "cid": "cid-root"},
		"parent": map[string]any{"uri": "at://parent", "cid": "cid-parent"},
	}
	post, skipped, err := buildBlueSkyPostWithImages("", nil, "token", reply)
	if err != nil {
		t.Fatalf("expected build success, got: %v", err)
	}
	if skipped != 0 {
		t.Fatalf("expected no skipped images, got %d", skipped)
	}

	storedReply := post["reply"].(map[string]any)
	root := storedReply["root"].(map[string]any)
	if root["uri"] != "at://root" {
		t.Fatalf("unexpected root ref: %+v", storedReply)
	}
	if imagePaths == nil {
		t.Fatalf("expected test images to exist")
	}
}

func TestSendBlueSkyWithImagesDetailed_ThreadsOverLimitImages(t *testing.T) {
	stubBlueSkyBlobUpload(t)

	originalSession := blueSkyCreateSession
	originalRecord := blueSkyCreateRecord
	defer func() {
		blueSkyCreateSession = originalSession
		blueSkyCreateRecord = originalRecord
	}()

	blueSkyCreateSession = func(dst any, identifier string, password string) error {
		session := dst.(*server.CreateSessionResponse)
		session.AccessJWT = "token"
		session.DID = "did:plc:test"
		return nil
	}

	records := make([]map[string]any, 0, 2)
	blueSkyCreateRecord = func(dst any, bearerToken string, repoName string, collection string, record any) error {
		records = append(records, record.(map[string]any))
		response := dst.(*repo.CreateRecordResponse)
		response.URI = fmt.Sprintf("at://post-%d", len(records))
		response.CID = fmt.Sprintf("cid-%d", len(records))
		return nil
	}

	config := Entity.Config{}
	config.SocialMediaSync.BlueSky.Enable = true

	result := SendBlueSkyWithImagesDetailed(config, "album text", writeTestImages(t, 5))
	if !result.Success {
		t.Fatalf("expected album publish success, got: %+v", result)
	}
	if result.RemoteID != "at://post-1" {
		t.Fatalf("expected first post remote id, got: %s", result.RemoteID)
	}
	if len(records) != 2 {
		t.Fatalf("expected 2 threaded posts, got %d", len(records))
	}

	first := records[0]
	if first["text"] != "album text" {
		t.Fatalf("unexpected first post text: %+v", first["text"])
	}
	if len(first["embed"].(map[string]any)["images"].([]map[string]any)) != 4 {
		t.Fatalf("expected 4 images on first post, got: %+v", first["embed"])
	}
	if _, exists := first["reply"]; exists {
		t.Fatalf("expected first post to have no reply ref")
	}

	second := records[1]
	if second["text"] != "" {
		t.Fatalf("expected thread continuation without text, got: %+v", second["text"])
	}
	if len(second["embed"].(map[string]any)["images"].([]map[string]any)) != 1 {
		t.Fatalf("expected 1 image on continuation post, got: %+v", second["embed"])
	}
	reply := second["reply"].(map[string]any)
	if reply["root"].(map[string]any)["uri"] != "at://post-1" || reply["parent"].(map[string]any)["uri"] != "at://post-1" {
		t.Fatalf("unexpected continuation reply refs: %+v", reply)
	}
}

func TestBuildBlueSkyPostWithImages_CountsSkippedUploads(t *testing.T) {
	originalClient := blueSkyHTTPClient
	requests := 0
	blueSkyHTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests++
		if requests == 2 {
			return nil, fmt.Errorf("upload failed")
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"blob":{"$type":"blob","mimeType":"image/png","size":8}}`)),
			Header:     make(http.Header),
		}, nil
	})}
	defer func() {
		blueSkyHTTPClient = originalClient
	}()

	post, skipped, err := buildBlueSkyPostWithImages("albums", writeTestImages(t, 3), "token", nil)
	if err != nil {
		t.Fatalf("expected build success with partial uploads, got: %v", err)
	}
	if skipped != 1 {
		t.Fatalf("expected 1 skipped image, got %d", skipped)
	}
	images := post["embed"].(map[string]any)["images"].([]map[string]any)
	if len(images) != 2 {
		t.Fatalf("expected 2 embedded images, got %d", len(images))
	}
}

func TestSendBlueSkyWithImagesDetailed_ReportsSkippedUploads(t *testing.T) {
	originalClient := blueSkyHTTPClient
	requests := 0
	blueSkyHTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests++
		if requests == 2 {
			return nil, fmt.Errorf("upload failed")
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"blob":{"$type":"blob","mimeType":"image/png","size":8}}`)),
			Header:     make(http.Header),
		}, nil
	})}
	defer func() {
		blueSkyHTTPClient = originalClient
	}()

	originalSession := blueSkyCreateSession
	originalRecord := blueSkyCreateRecord
	defer func() {
		blueSkyCreateSession = originalSession
		blueSkyCreateRecord = originalRecord
	}()

	blueSkyCreateSession = func(dst any, identifier string, password string) error {
		session := dst.(*server.CreateSessionResponse)
		session.AccessJWT = "token"
		session.DID = "did:plc:test"
		return nil
	}
	blueSkyCreateRecord = func(dst any, bearerToken string, repoName string, collection string, record any) error {
		response := dst.(*repo.CreateRecordResponse)
		response.URI = "at://post-1"
		response.CID = "cid-1"
		return nil
	}

	config := Entity.Config{}
	config.SocialMediaSync.BlueSky.Enable = true

	result := SendBlueSkyWithImagesDetailed(config, "album text", writeTestImages(t, 3))
	if !result.Success {
		t.Fatalf("expected publish success despite one skipped image, got: %+v", result)
	}
	if result.ErrorMessage != "1 张图片上传失败，已跳过" {
		t.Fatalf("expected skipped image notice, got: %s", result.ErrorMessage)
	}
}
