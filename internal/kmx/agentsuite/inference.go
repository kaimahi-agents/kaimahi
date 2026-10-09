package agentsuite

import "errors"

// InferenceRequirements describes the model API needed by an agent, not a
// particular model deployment. Token limits refer to total context and output.
type InferenceRequirements struct {
	API           string `json:"api"`
	ContextTokens int    `json:"contextTokens"`
	OutputTokens  int    `json:"outputTokens"`
	Streaming     bool   `json:"streaming"`
	ToolCalling   bool   `json:"toolCalling"`
}

func (r InferenceRequirements) Validate() error {
	if r.API != "openai-chat-completions-v1" {
		return errors.New("unsupported inference API; expected openai-chat-completions-v1")
	}
	if r.ContextTokens < 1 || r.OutputTokens < 1 || r.OutputTokens > r.ContextTokens || r.ContextTokens > 10000000 {
		return errors.New("inference token limits must be positive, bounded, and output must fit context")
	}
	return nil
}

// Match uses explicitly declared connection capabilities. It does not infer
// token limits from /models or claim that operator declarations are verified.
func (r InferenceRequirements) Match(available InferenceRequirements) error {
	if err := r.Validate(); err != nil {
		return err
	}
	if err := available.Validate(); err != nil {
		return err
	}
	if available.API != r.API || available.ContextTokens < r.ContextTokens || available.OutputTokens < r.OutputTokens || r.Streaming && !available.Streaming || r.ToolCalling && !available.ToolCalling {
		return errors.New("inference connection does not satisfy the suite capabilities")
	}
	return nil
}
