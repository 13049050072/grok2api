package httpserver

// SwaggerMessage 表示 Chat Completions 请求中的一条消息。
type SwaggerMessage struct {
	Role    string `json:"role" example:"user"`
	Content any    `json:"content"`
}

// SwaggerResponsesRequest 表示最小 Responses 请求。
type SwaggerResponsesRequest struct {
	Model              string `json:"model" example:"grok-chat-auto"`
	Input              any    `json:"input"`
	Stream             bool   `json:"stream" example:"false"`
	Store              bool   `json:"store" example:"false"`
	PreviousResponseID string `json:"previous_response_id,omitempty"`
	PromptCacheKey     string `json:"prompt_cache_key,omitempty"`
}

// SwaggerChatRequest 表示最小 Chat Completions 请求。
type SwaggerChatRequest struct {
	Model    string           `json:"model" example:"grok-chat-fast"`
	Messages []SwaggerMessage `json:"messages"`
	Stream   bool             `json:"stream" example:"false"`
}

// SwaggerMessagesRequest 表示最小 Anthropic Messages 请求。
type SwaggerMessagesRequest struct {
	Model     string           `json:"model" example:"grok-chat-expert"`
	MaxTokens int              `json:"max_tokens" example:"1024"`
	Messages  []SwaggerMessage `json:"messages"`
	Stream    bool             `json:"stream" example:"false"`
}

// SwaggerImageGenerationRequest 表示图片生成请求。
type SwaggerImageGenerationRequest struct {
	Model          string `json:"model" example:"grok-imagine-image-quality"`
	Prompt         string `json:"prompt" example:"A cinematic city at night"`
	N              int    `json:"n" example:"1"`
	AspectRatio    string `json:"aspect_ratio,omitempty" example:"16:9"`
	Resolution     string `json:"resolution,omitempty" example:"2k"`
	ResponseFormat string `json:"response_format,omitempty" example:"url"`
	Stream         bool   `json:"stream,omitempty" example:"false"`
	PartialImages  int    `json:"partial_images,omitempty" example:"0"`
}

// SwaggerImageReference 表示图片编辑里的单个图片输入：对象 {"url":...} 与
// {"file_id":...} 两种写法，也允许直接把字符串当 url 传（等价于 {"url":...}）。
type SwaggerImageReference struct {
	// HTTP(S) 图片地址，或 data URL（如 data:image/png;base64,...）。
	URL string `json:"url,omitempty" example:"https://example.com/source.png"`
	// 临时输入资产 ID（input_ 前缀，由上传任务产生），与 url 二选一。
	FileID string `json:"file_id,omitempty" example:"input_5q2t9b3xk1v8mN0pQrStUvWxYzAbCd"`
}

// SwaggerImageEditRequest 表示图片编辑请求。
// image 与 images 至少提供一个，合计 1-8 张；字段本身可以是单对象、单字符串或数组。
type SwaggerImageEditRequest struct {
	Model  string `json:"model" example:"grok-imagine-image-edit"`
	Prompt string `json:"prompt" example:"Change the background to black"`
	// 单张参考图：对象写法见 SwaggerImageReference，也可直接给字符串。
	Image *SwaggerImageReference `json:"image,omitempty"`
	// 多张参考图，元素写法与 image 相同。
	Images         []SwaggerImageReference `json:"images,omitempty"`
	N              int                     `json:"n" example:"1"`
	Size           string                  `json:"size,omitempty" example:"1024x1024"`
	AspectRatio    string                  `json:"aspect_ratio,omitempty" example:"1:1"`
	Resolution     string                  `json:"resolution,omitempty" example:"1k"`
	// 生成质量，仅 Console grok-imagine-image-2.0 路由接受 low/medium；Web 编辑路由不接受该字段。
	Quality        string                  `json:"quality,omitempty" example:"medium"`
	ResponseFormat string                  `json:"response_format,omitempty" example:"url"`
	Stream         bool                    `json:"stream,omitempty" example:"false"`
	PartialImages  int                     `json:"partial_images,omitempty" example:"0"`
}

// SwaggerVideoGenerationRequest 表示视频生成请求。
// image 与 reference_images/reference_audios 互斥；参考图模式 resolution 最高 720p。
type SwaggerVideoGenerationRequest struct {
	Model            string                    `json:"model" example:"grok-imagine-video"`
	Prompt           string                    `json:"prompt" example:"A cinematic tracking shot in the rain"`
	Duration         int                       `json:"duration" example:"8"`
	AspectRatio      string                    `json:"aspect_ratio,omitempty" example:"16:9"`
	Resolution       string                    `json:"resolution,omitempty" example:"720p"`
	Image            *SwaggerVideoMediaInput   `json:"image,omitempty"`
	ReferenceImages  []SwaggerVideoMediaInput  `json:"reference_images,omitempty"`
	ReferenceAudios  []SwaggerVideoAudioInput  `json:"reference_audios,omitempty"`
	Video            *SwaggerVideoMediaInput   `json:"video,omitempty"`
}

// SwaggerVideoMediaInput 表示视频相关的图片/视频输入。
type SwaggerVideoMediaInput struct {
	URL    string `json:"url,omitempty" example:"https://example.com/input.png"`
	FileID string `json:"file_id,omitempty" example:"file_123"`
}

// SwaggerVideoAudioInput 表示参考音频（内置 voice_id）。
type SwaggerVideoAudioInput struct {
	VoiceID string `json:"voice_id" example:"eve"`
}

// swaggerHealth godoc
// @Summary 存活检查
// @Tags System
// @Produce json
// @Success 200 {object} map[string]bool
// @Router /healthz [get]
func swaggerHealth() {}

// swaggerReady godoc
// @Summary 就绪检查
// @Tags System
// @Produce json
// @Success 200 {object} map[string]bool
// @Failure 503 {object} map[string]bool
// @Router /readyz [get]
func swaggerReady() {}

// swaggerModels godoc
// @Summary 获取可用模型
// @Tags Models
// @Security BearerAuth
// @Produce json
// @Success 200 {object} map[string]any
// @Failure 401 {object} map[string]any
// @Router /v1/models [get]
func swaggerModels() {}

// swaggerResponses godoc
// @Summary 创建 Response
// @Description 支持 JSON 与 SSE；stream=true 时返回 text/event-stream。
// @Tags Responses
// @Security BearerAuth
// @Accept json
// @Produce json
// @Param request body SwaggerResponsesRequest true "请求"
// @Success 200 {object} map[string]any
// @Failure 400 {object} map[string]any
// @Failure 401 {object} map[string]any
// @Router /v1/responses [post]
func swaggerResponses() {}

// swaggerCompactResponse godoc
// @Summary 压缩 Response 上下文
// @Tags Responses
// @Security BearerAuth
// @Accept json
// @Produce json
// @Param request body SwaggerResponsesRequest true "请求"
// @Success 200 {object} map[string]any
// @Router /v1/responses/compact [post]
func swaggerCompactResponse() {}

// swaggerGetResponse godoc
// @Summary 查询 Response
// @Tags Responses
// @Security BearerAuth
// @Produce json
// @Param response_id path string true "Response ID"
// @Success 200 {object} map[string]any
// @Failure 404 {object} map[string]any
// @Router /v1/responses/{response_id} [get]
func swaggerGetResponse() {}

// swaggerDeleteResponse godoc
// @Summary 删除 Response
// @Tags Responses
// @Security BearerAuth
// @Produce json
// @Param response_id path string true "Response ID"
// @Success 200 {object} map[string]any
// @Failure 404 {object} map[string]any
// @Router /v1/responses/{response_id} [delete]
func swaggerDeleteResponse() {}

// swaggerChat godoc
// @Summary 创建 Chat Completion
// @Description 支持 JSON 与 SSE、图片输入和函数工具。
// @Tags Chat
// @Security BearerAuth
// @Accept json
// @Produce json
// @Param request body SwaggerChatRequest true "请求"
// @Success 200 {object} map[string]any
// @Failure 400 {object} map[string]any
// @Router /v1/chat/completions [post]
func swaggerChat() {}

// swaggerMessages godoc
// @Summary 创建 Anthropic Message
// @Tags Messages
// @Security BearerAuth
// @Accept json
// @Produce json
// @Param anthropic-version header string true "Anthropic API version" default(2023-06-01)
// @Param request body SwaggerMessagesRequest true "请求"
// @Success 200 {object} map[string]any
// @Failure 400 {object} map[string]any
// @Router /v1/messages [post]
func swaggerMessages() {}

// swaggerGenerateImage godoc
// @Summary 生成图片
// @Tags Images
// @Security BearerAuth
// @Accept json
// @Produce json
// @Param request body SwaggerImageGenerationRequest true "请求"
// @Success 200 {object} map[string]any
// @Failure 400 {object} map[string]any
// @Router /v1/images/generations [post]
func swaggerGenerateImage() {}

// swaggerEditImage godoc
// @Summary 编辑图片
// @Description 支持 application/json 与 multipart/form-data 两种传输形态。
// @Description JSON 形态：image 与 images 至少提供一个，合计 1-8 张；每个元素支持对象 {"url":"https://..."}（也接受 data URL）、对象 {"file_id":"input_..."}（临时输入资产）以及直接传字符串三种写法。
// @Description multipart 形态：图片放文件字段 image、images、image[]、images[]，只接受 PNG、JPEG、WebP、GIF，单文件上限 20 MiB；同一字段也可传 URL 或 file_id 文本。
// @Description 全部参考图 Base64 后合计不超过 32 MiB（与请求体上限一致）。
// @Tags Images
// @Security BearerAuth
// @Accept json,multipart/form-data
// @Produce json
// @Param request body SwaggerImageEditRequest true "请求"
// @Success 200 {object} map[string]any
// @Failure 400 {object} map[string]any
// @Router /v1/images/edits [post]
func swaggerEditImage() {}

// swaggerGetImage godoc
// @Summary 获取归档图片
// @Tags Images
// @Produce image/png
// @Param asset_id path string true "Asset ID"
// @Success 200 {file} binary
// @Failure 404
// @Router /v1/media/images/{asset_id} [get]
func swaggerGetImage() {}

// swaggerGenerateVideo godoc
// @Summary 创建异步视频任务
// @Tags Videos
// @Security BearerAuth
// @Accept json
// @Produce json
// @Param request body SwaggerVideoGenerationRequest true "请求"
// @Success 200 {object} map[string]string
// @Failure 400 {object} map[string]any
// @Router /v1/videos/generations [post]
func swaggerGenerateVideo() {}

// swaggerEditVideo godoc
// @Summary 创建异步视频编辑任务
// @Tags Videos
// @Security BearerAuth
// @Accept json
// @Produce json
// @Param request body SwaggerVideoGenerationRequest true "请求"
// @Success 200 {object} map[string]string
// @Failure 400 {object} map[string]any
// @Router /v1/videos/edits [post]
func swaggerEditVideo() {}

// swaggerExtendVideo godoc
// @Summary 创建异步视频延长任务
// @Tags Videos
// @Security BearerAuth
// @Accept json
// @Produce json
// @Param request body SwaggerVideoGenerationRequest true "请求"
// @Success 200 {object} map[string]string
// @Failure 400 {object} map[string]any
// @Router /v1/videos/extensions [post]
func swaggerExtendVideo() {}

// swaggerGetVideo godoc
// @Summary 查询异步视频任务
// @Tags Videos
// @Security BearerAuth
// @Produce json
// @Param request_id path string true "Request ID"
// @Success 200 {object} map[string]any
// @Failure 404 {object} map[string]any
// @Router /v1/videos/{request_id} [get]
func swaggerGetVideo() {}
