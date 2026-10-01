package SocialMediaUtils

// maxImagesPerPost 为各平台单帖图片上限：BlueSky、Mastodon、Twitter 均为 4。
// 超过上限的相册图片由各平台适配器按线程（回复）续发。
const maxImagesPerPost = 4

// chunkImagePaths 按单帖上限切分图片路径并保持原顺序；无有效输入时返回 nil。
func chunkImagePaths(imagePaths []string, size int) [][]string {
	if size <= 0 || len(imagePaths) == 0 {
		return nil
	}

	chunks := make([][]string, 0, (len(imagePaths)+size-1)/size)
	for start := 0; start < len(imagePaths); start += size {
		end := start + size
		if end > len(imagePaths) {
			end = len(imagePaths)
		}
		chunks = append(chunks, imagePaths[start:end])
	}
	return chunks
}
