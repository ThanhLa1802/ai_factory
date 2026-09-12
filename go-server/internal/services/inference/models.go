package inference

import "time"

// sessionRow maps the `sessions` table.
type sessionRow struct {
	ID           string    `gorm:"column:id;primaryKey"`
	TenantID     string    `gorm:"column:tenant_id;type:uuid"`
	UserID       *string   `gorm:"column:user_id;type:uuid"`
	Model        string    `gorm:"column:model"`
	SystemPrompt string    `gorm:"column:system_prompt"`
	MaxTokens    int       `gorm:"column:max_tokens"`
	Title        string    `gorm:"column:title"`
	CreatedAt    time.Time `gorm:"column:created_at"`
	UpdatedAt    time.Time `gorm:"column:updated_at"`
}

func (sessionRow) TableName() string { return "sessions" }

// messageRow maps the `messages` table (tool_calls is JSONB).
type messageRow struct {
	ID         int64     `gorm:"column:id;primaryKey"`
	SessionID  string    `gorm:"column:session_id"`
	Seq        int       `gorm:"column:seq"`
	Role       string    `gorm:"column:role"`
	Content    string    `gorm:"column:content"`
	ToolCalls  []ToolCall `gorm:"column:tool_calls;serializer:json"`
	ToolCallID string    `gorm:"column:tool_call_id"`
	ToolResult string    `gorm:"column:tool_result"`
	IsError    bool      `gorm:"column:is_error"`
	CreatedAt  time.Time `gorm:"column:created_at"`
}

func (messageRow) TableName() string { return "messages" }
