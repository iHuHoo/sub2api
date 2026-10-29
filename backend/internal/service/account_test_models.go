package service

import (
	"context"
	"sort"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/antigravity"
	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/Wei-Shaw/sub2api/internal/pkg/geminicli"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/Wei-Shaw/sub2api/internal/pkg/typesafe"
	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
)

type AccountTestModel struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
}

// AvailableAccountTestModels is shared by the admin and supplier model pickers.
func AvailableAccountTestModels(ctx context.Context, testService *AccountTestService, account *Account) any {
	if account.IsOpenAI() {
		if testService != nil {
			if models, err := testService.FetchOpenAIAccountModels(ctx, account); err == nil {
				return models
			}
		}
		if account.IsOpenAIPassthroughEnabled() {
			return openai.DefaultModels
		}
		mapping := account.GetModelMapping()
		if len(mapping) == 0 {
			return openai.DefaultModels
		}
		models := make([]openai.Model, 0, len(mapping))
		for requestedModel := range mapping {
			found := false
			for _, model := range openai.DefaultModels {
				if model.ID == requestedModel {
					models = append(models, model)
					found = true
					break
				}
			}
			if !found {
				models = append(models, openai.Model{ID: requestedModel, Object: "model", Type: "model", DisplayName: requestedModel})
			}
		}
		return models
	}

	if account.IsGemini() {
		if account.IsOAuth() {
			if account.IsGeminiGoogleOne() {
				return geminicli.GoogleOneModels
			}
			return geminicli.DefaultModels
		}
		mapping := account.GetModelMapping()
		if len(mapping) == 0 {
			return geminicli.DefaultModels
		}
		models := make([]geminicli.Model, 0, len(mapping))
		for requestedModel := range mapping {
			found := false
			for _, model := range geminicli.DefaultModels {
				if model.ID == requestedModel {
					models = append(models, model)
					found = true
					break
				}
			}
			if !found {
				models = append(models, geminicli.Model{ID: requestedModel, Type: "model", DisplayName: requestedModel})
			}
		}
		return models
	}

	if account.Platform == PlatformAntigravity {
		return antigravityAccountTestModels(account.Credentials["model_mapping"])
	}
	if account.Platform == PlatformGrok {
		defaults := xai.DefaultModels()
		hasMapping := false
		switch mapping := account.Credentials["model_mapping"].(type) {
		case map[string]any:
			hasMapping = len(mapping) > 0
		case map[string]string:
			hasMapping = len(mapping) > 0
		}
		if !hasMapping {
			return defaults
		}
		mapping := account.GetModelMapping()
		if len(mapping) == 0 {
			return defaults
		}
		defaultByID := make(map[string]xai.Model, len(defaults))
		for _, model := range defaults {
			defaultByID[model.ID] = model
		}
		ids := make([]string, 0, len(mapping))
		for id := range mapping {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		models := make([]xai.Model, 0, len(ids))
		for _, id := range ids {
			if model, ok := defaultByID[id]; ok {
				models = append(models, model)
			} else {
				models = append(models, xai.Model{ID: id, Object: "model", OwnedBy: "xai", DisplayName: id})
			}
		}
		return models
	}
	if account.IsTypeSafe() {
		return []claude.Model{{ID: typesafe.JevLatestModel, Type: "model", DisplayName: typesafe.JevLatestModel}}
	}
	if account.IsOAuth() {
		return claude.DefaultModels
	}
	mapping := account.GetModelMapping()
	if len(mapping) == 0 {
		return claude.DefaultModels
	}
	models := make([]claude.Model, 0, len(mapping))
	for requestedModel := range mapping {
		found := false
		for _, model := range claude.DefaultModels {
			if model.ID == requestedModel {
				models = append(models, model)
				found = true
				break
			}
		}
		if !found {
			models = append(models, claude.Model{ID: requestedModel, Type: "model", DisplayName: requestedModel})
		}
	}
	return models
}

func antigravityAccountTestModels(rawMapping any) []antigravity.ClaudeModel {
	var ids []string
	switch mapping := rawMapping.(type) {
	case map[string]any:
		for id := range mapping {
			if strings.TrimSpace(id) != "" && !strings.Contains(id, "*") {
				ids = append(ids, id)
			}
		}
	case map[string]string:
		for id := range mapping {
			if strings.TrimSpace(id) != "" && !strings.Contains(id, "*") {
				ids = append(ids, id)
			}
		}
	}
	if len(ids) == 0 {
		return antigravity.DefaultModels()
	}
	sort.Strings(ids)
	defaults := make(map[string]antigravity.ClaudeModel)
	for _, model := range antigravity.DefaultModels() {
		defaults[model.ID] = model
	}
	models := make([]antigravity.ClaudeModel, 0, len(ids))
	for _, id := range ids {
		if model, ok := defaults[id]; ok {
			models = append(models, model)
		} else {
			models = append(models, antigravity.ClaudeModel{ID: id, Type: "model", DisplayName: id})
		}
	}
	return models
}

func accountTestModelView(models any) []AccountTestModel {
	var out []AccountTestModel
	appendModel := func(id, displayName string) {
		out = append(out, AccountTestModel{ID: id, DisplayName: displayName})
	}
	switch typed := models.(type) {
	case []openai.Model:
		for _, model := range typed {
			appendModel(model.ID, model.DisplayName)
		}
	case []geminicli.Model:
		for _, model := range typed {
			appendModel(model.ID, model.DisplayName)
		}
	case []antigravity.ClaudeModel:
		for _, model := range typed {
			appendModel(model.ID, model.DisplayName)
		}
	case []xai.Model:
		for _, model := range typed {
			appendModel(model.ID, model.DisplayName)
		}
	case []claude.Model:
		for _, model := range typed {
			appendModel(model.ID, model.DisplayName)
		}
	}
	return out
}
