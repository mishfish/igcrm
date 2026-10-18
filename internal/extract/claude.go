package extract

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// Claude — клієнт до Claude API (Anthropic) як альтернатива OpenAI-сумісному LLM.
type Claude struct {
	Model  string
	client anthropic.Client
}

func NewClaude(apiKey, model string) *Claude {
	return &Claude{Model: model, client: anthropic.NewClient(option.WithAPIKey(apiKey))}
}

// Complete повертає JSON-об'єкт з відповіді моделі.
func (c *Claude) Complete(ctx context.Context, system, user string) (json.RawMessage, error) {
	resp, err := c.client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     anthropic.Model(c.Model),
		MaxTokens: 1024,
		System:    []anthropic.TextBlockParam{{Text: system}},
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(user)),
		},
	})
	if err != nil {
		return nil, fmt.Errorf("claude: %w", err)
	}
	for _, block := range resp.Content {
		if tb, ok := block.AsAny().(anthropic.TextBlock); ok {
			return extractJSONObject(tb.Text)
		}
	}
	return nil, fmt.Errorf("claude: no text block in response")
}
