package app

// Options are process settings that still live on flags rather than Config.
type Options struct {
	InferenceAddr      string
	ImageInferenceAddr string
	WorkDir            string
	MaxConcurrent      int
	UIDir              string
}
