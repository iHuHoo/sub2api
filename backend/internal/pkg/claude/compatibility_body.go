package claude

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
)

// The reviewed request-body grammar for native Claude Code requests (#153). It checks the
// protocol structure and the capability-bearing fields seen in the offline captures of
// 2.1.280/309/318; user text, tool names, descriptions, schemas, tool inputs and results
// are payload and are only type-checked. A field or block type outside this grammar
// (documents, top-level images, redacted_thinking, tool_choice, stop_sequences, top_p,
// top_k, server tools, cache_control on tools, ...) is unreviewed, not proven invalid.

var (
	messagesBodyKeys = []string{"context_management", "max_tokens", "messages", "metadata", "model",
		"output_config", "safeguards", "stream", "system", "temperature", "thinking", "tools"}
	countTokensBodyKeys = []string{"messages", "model", "tools"}
	imageMediaTypes     = []string{"image/png", "image/jpeg", "image/gif", "image/webp"}
)

type bodyFields = map[string]any

func bodyUnsupported(format string, a ...any) error {
	return &CompatibilityError{Class: CompatibilityUnsupported, Reason: "body " + fmt.Sprintf(format, a...)}
}

func bodyInvalid(format string, a ...any) error {
	return &CompatibilityError{Class: CompatibilityInvalidBody, Reason: "body " + fmt.Sprintf(format, a...)}
}

// bodySelector validates body for endpoint and returns its model and structural selector.
func bodySelector(endpoint string, body []byte) (string, CompatibilitySelector, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var b bodyFields
	if err := dec.Decode(&b); err != nil || b == nil {
		return "", CompatibilitySelector{}, bodyInvalid("is not a JSON object")
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return "", CompatibilitySelector{}, bodyInvalid("has trailing data")
	}
	allowed := messagesBodyKeys
	if endpoint == CompatibilityEndpointCountTokens {
		allowed = countTokensBodyKeys
	}
	if err := onlyKeys(b, "", allowed); err != nil {
		return "", CompatibilitySelector{}, err
	}
	model, ok := b["model"].(string)
	if !ok || !modelIDRe.MatchString(model) {
		return "", CompatibilitySelector{}, &CompatibilityError{Class: CompatibilityInvalidModel, Reason: "body model is not a model id"}
	}
	v := &bodyCheck{model: model}
	sel := CompatibilitySelector{Cache: "none", Thinking: "absent"}
	var err error
	if sel.Thinking, err = v.thinking(b); err != nil {
		return "", sel, err
	}
	if err := v.messages(b["messages"]); err != nil {
		return "", sel, err
	}
	if sel.Tools, sel.DeferredTools, err = v.tools(b); err != nil {
		return "", sel, err
	}
	if s, has := b["system"]; has {
		if err := v.system(s); err != nil {
			return "", sel, err
		}
	}
	if oc, has := b["output_config"]; has {
		if sel.Effort, sel.OutputFormat, err = v.outputConfig("output_config", oc, true); err != nil {
			return "", sel, err
		}
	}
	if cm, has := b["context_management"]; has {
		if err := contextManagement(cm); err != nil {
			return "", sel, err
		}
		sel.ContextManagement = true
	}
	if sg, has := b["safeguards"]; has {
		// shortcut: safeguards is the client's own classifier config; only its outer shape
		// is checked, upgrade if a reviewed contract for its contents is ever needed.
		list, ok := sg.([]any)
		if !ok || len(list) == 0 {
			return "", sel, bodyInvalid("safeguards must be a non-empty array")
		}
		for _, x := range list {
			o, ok := x.(bodyFields)
			if _, typed := o["type"].(string); !ok || !typed {
				return "", sel, bodyInvalid("safeguards entries must be objects with a type")
			}
		}
		sel.Safeguards = true
	}
	if md, has := b["metadata"]; has {
		o, ok := md.(bodyFields)
		if !ok {
			return "", sel, bodyInvalid("metadata must be an object")
		}
		if err := onlyKeys(o, "metadata.", []string{"user_id"}); err != nil {
			return "", sel, err
		}
		if _, ok := o["user_id"].(string); !ok {
			return "", sel, bodyInvalid("metadata.user_id must be a string")
		}
	}
	if mt, has := b["max_tokens"]; has && !positiveInt(mt) {
		return "", sel, bodyInvalid("max_tokens must be a positive integer")
	}
	if st, has := b["stream"]; has {
		if _, ok := st.(bool); !ok {
			return "", sel, bodyInvalid("stream must be a boolean")
		}
	}
	if tp, has := b["temperature"]; has {
		if _, ok := tp.(json.Number); !ok {
			return "", sel, bodyInvalid("temperature must be a number")
		}
	}
	sel.Cache = v.cacheForm()
	return model, sel, nil
}

type bodyCheck struct {
	model             string
	cache, cacheTTL1h bool
}

func (v *bodyCheck) cacheForm() string {
	switch {
	case v.cacheTTL1h:
		return "ttl1h"
	case v.cache:
		return "default"
	}
	return "none"
}

func (v *bodyCheck) cacheControl(where string, o bodyFields) error {
	cc, has := o["cache_control"]
	if !has {
		return nil
	}
	c, ok := cc.(bodyFields)
	if !ok {
		return bodyInvalid("%s.cache_control must be an object", where)
	}
	if err := onlyKeys(c, where+".cache_control.", []string{"type", "ttl"}); err != nil {
		return err
	}
	if c["type"] != "ephemeral" {
		return bodyUnsupported("%s.cache_control.type is not ephemeral", where)
	}
	v.cache = true
	if ttl, has := c["ttl"]; has {
		if ttl != "1h" {
			return bodyUnsupported("%s.cache_control.ttl is not 1h", where)
		}
		v.cacheTTL1h = true
	}
	return nil
}

func (v *bodyCheck) thinking(b bodyFields) (string, error) {
	raw, has := b["thinking"]
	if !has {
		return "absent", nil
	}
	th, ok := raw.(bodyFields)
	if !ok {
		return "", bodyInvalid("thinking must be an object")
	}
	typ, _ := th["type"].(string)
	keys := []string{"type", "display"}
	switch typ {
	case "adaptive":
	case "enabled":
		keys = append(keys, "budget_tokens")
		if !positiveInt(th["budget_tokens"]) {
			return "", bodyInvalid("thinking.budget_tokens must be a positive integer")
		}
	case "disabled":
		keys = []string{"type"}
	default:
		return "", bodyUnsupported("thinking.type %q is not reviewed", typ)
	}
	if err := onlyKeys(th, "thinking.", keys); err != nil {
		return "", err
	}
	d, has := th["display"]
	if !has {
		return typ, nil
	}
	ds, _ := d.(string)
	if ds != "updates" && ds != "omitted" {
		return "", bodyUnsupported("thinking.display is not updates or omitted")
	}
	return typ + "/" + ds, nil
}

func (v *bodyCheck) messages(raw any) error {
	list, ok := raw.([]any)
	if !ok || len(list) == 0 {
		return bodyInvalid("messages must be a non-empty array")
	}
	for i, x := range list {
		where := fmt.Sprintf("messages[%d]", i)
		m, ok := x.(bodyFields)
		if !ok {
			return bodyInvalid("%s must be an object", where)
		}
		role, _ := m["role"].(string)
		keys := []string{"role", "content"}
		var blocks []string
		switch role {
		case "user":
			blocks = []string{"text", "tool_result"}
		case "assistant":
			blocks = []string{"text", "thinking", "tool_use"}
		case "system":
			blocks = []string{"text"}
			keys = append(keys, "output_config")
		default:
			return bodyUnsupported("%s.role %q is not reviewed", where, role)
		}
		if err := onlyKeys(m, where+".", keys); err != nil {
			return err
		}
		if oc, has := m["output_config"]; has {
			if _, _, err := v.outputConfig(where+".output_config", oc, false); err != nil {
				return err
			}
		}
		if err := v.content(where, m["content"], blocks); err != nil {
			return err
		}
	}
	return nil
}

func (v *bodyCheck) content(where string, raw any, blocks []string) error {
	if _, ok := raw.(string); ok {
		return nil
	}
	list, ok := raw.([]any)
	if !ok || len(list) == 0 {
		return bodyInvalid("%s.content must be a string or a non-empty array", where)
	}
	for i, x := range list {
		bw := fmt.Sprintf("%s.content[%d]", where, i)
		blk, ok := x.(bodyFields)
		if !ok {
			return bodyInvalid("%s must be an object", bw)
		}
		typ, _ := blk["type"].(string)
		if !slices.Contains(blocks, typ) {
			return bodyUnsupported("%s.type %q is not reviewed here", bw, typ)
		}
		if err := v.block(bw, typ, blk); err != nil {
			return err
		}
	}
	return nil
}

func (v *bodyCheck) block(where, typ string, blk bodyFields) error {
	str := func(k string) error {
		if _, ok := blk[k].(string); !ok {
			return bodyInvalid("%s.%s must be a string", where, k)
		}
		return nil
	}
	switch typ {
	case "text":
		if err := onlyKeys(blk, where+".", []string{"type", "text", "cache_control"}); err != nil {
			return err
		}
		if err := str("text"); err != nil {
			return err
		}
		return v.cacheControl(where, blk)
	case "thinking":
		if err := onlyKeys(blk, where+".", []string{"type", "thinking", "signature"}); err != nil {
			return err
		}
		if err := str("thinking"); err != nil {
			return err
		}
		return str("signature")
	case "tool_use":
		if err := onlyKeys(blk, where+".", []string{"type", "id", "name", "input"}); err != nil {
			return err
		}
		if err := str("id"); err != nil {
			return err
		}
		if err := str("name"); err != nil {
			return err
		}
		if _, ok := blk["input"].(bodyFields); !ok {
			return bodyInvalid("%s.input must be an object", where)
		}
		return nil
	case "tool_result":
		if err := onlyKeys(blk, where+".", []string{"type", "tool_use_id", "content", "is_error", "cache_control"}); err != nil {
			return err
		}
		if err := str("tool_use_id"); err != nil {
			return err
		}
		if e, has := blk["is_error"]; has {
			if _, ok := e.(bool); !ok {
				return bodyInvalid("%s.is_error must be a boolean", where)
			}
		}
		if err := v.cacheControl(where, blk); err != nil {
			return err
		}
		if _, ok := blk["content"].(string); ok {
			return nil
		}
		return v.content(where, blk["content"], []string{"text", "image"})
	case "image":
		if err := onlyKeys(blk, where+".", []string{"type", "source"}); err != nil {
			return err
		}
		src, ok := blk["source"].(bodyFields)
		if !ok {
			return bodyInvalid("%s.source must be an object", where)
		}
		if err := onlyKeys(src, where+".source.", []string{"type", "media_type", "data"}); err != nil {
			return err
		}
		mt, _ := src["media_type"].(string)
		if src["type"] != "base64" || !slices.Contains(imageMediaTypes, mt) {
			return bodyUnsupported("%s.source is not a reviewed base64 image", where)
		}
		if _, ok := src["data"].(string); !ok {
			return bodyInvalid("%s.source.data must be a string", where)
		}
		return nil
	}
	return bodyUnsupported("%s.type %q is not reviewed", where, typ)
}

func (v *bodyCheck) tools(b bodyFields) (present, deferred bool, err error) {
	raw, has := b["tools"]
	if !has {
		return false, false, nil
	}
	list, ok := raw.([]any)
	if !ok {
		return false, false, bodyInvalid("tools must be an array")
	}
	for i, x := range list {
		where := fmt.Sprintf("tools[%d]", i)
		t, ok := x.(bodyFields)
		if !ok {
			return false, false, bodyInvalid("%s must be an object", where)
		}
		// A "type" key marks a server tool (web_search, code execution, ...): unreviewed.
		if err := onlyKeys(t, where+".", []string{"name", "description", "input_schema", "defer_loading"}); err != nil {
			return false, false, err
		}
		if n, _ := t["name"].(string); n == "" {
			return false, false, bodyInvalid("%s.name must be a non-empty string", where)
		}
		if d, has := t["description"]; has {
			if _, ok := d.(string); !ok {
				return false, false, bodyInvalid("%s.description must be a string", where)
			}
		}
		if _, ok := t["input_schema"].(bodyFields); !ok {
			return false, false, bodyInvalid("%s.input_schema must be an object", where)
		}
		if dl, has := t["defer_loading"]; has {
			d, ok := dl.(bool)
			if !ok {
				return false, false, bodyInvalid("%s.defer_loading must be a boolean", where)
			}
			deferred = deferred || d
		}
	}
	return len(list) > 0, deferred, nil
}

func (v *bodyCheck) system(raw any) error {
	if _, ok := raw.(string); ok {
		return nil
	}
	list, ok := raw.([]any)
	if !ok {
		return bodyInvalid("system must be a string or an array")
	}
	for i, x := range list {
		where := fmt.Sprintf("system[%d]", i)
		blk, ok := x.(bodyFields)
		if !ok || blk["type"] != "text" {
			return bodyUnsupported("%s is not a text block", where)
		}
		if err := v.block(where, "text", blk); err != nil {
			return err
		}
	}
	return nil
}

// outputConfig checks effort against the model's catalog levels; format only where allowed.
func (v *bodyCheck) outputConfig(where string, raw any, formatAllowed bool) (effort, format bool, err error) {
	oc, ok := raw.(bodyFields)
	if !ok || len(oc) == 0 {
		return false, false, bodyInvalid("%s must be a non-empty object", where)
	}
	keys := []string{"effort"}
	if formatAllowed {
		keys = append(keys, "format")
	}
	if err := onlyKeys(oc, where+".", keys); err != nil {
		return false, false, err
	}
	if e, has := oc["effort"]; has {
		s, _ := e.(string)
		if !slices.Contains(EffortLevelsForModel(v.model), s) {
			return false, false, bodyUnsupported("%s.effort is not a level of the model", where)
		}
		effort = true
	}
	if f, has := oc["format"]; has {
		fo, ok := f.(bodyFields)
		if !ok {
			return false, false, bodyInvalid("%s.format must be an object", where)
		}
		if err := onlyKeys(fo, where+".format.", []string{"type", "schema"}); err != nil {
			return false, false, err
		}
		if fo["type"] != "json_schema" {
			return false, false, bodyUnsupported("%s.format.type is not json_schema", where)
		}
		if _, ok := fo["schema"].(bodyFields); !ok {
			return false, false, bodyInvalid("%s.format.schema must be an object", where)
		}
		format = true
	}
	return effort, format, nil
}

// contextManagement accepts only the form the client was captured sending.
func contextManagement(raw any) error {
	cm, ok := raw.(bodyFields)
	if !ok {
		return bodyInvalid("context_management must be an object")
	}
	if err := onlyKeys(cm, "context_management.", []string{"edits"}); err != nil {
		return err
	}
	edits, ok := cm["edits"].([]any)
	if !ok || len(edits) == 0 {
		return bodyInvalid("context_management.edits must be a non-empty array")
	}
	for _, x := range edits {
		e, ok := x.(bodyFields)
		if !ok || len(e) != 2 || e["type"] != "clear_thinking_20251015" || e["keep"] != "all" {
			return bodyUnsupported("context_management.edits holds an unreviewed edit")
		}
	}
	return nil
}

func onlyKeys(o bodyFields, prefix string, allowed []string) error {
	for k := range o {
		if !slices.Contains(allowed, k) {
			return bodyUnsupported("field %s%s is not reviewed", prefix, k)
		}
	}
	return nil
}

func positiveInt(x any) bool {
	n, ok := x.(json.Number)
	if !ok {
		return false
	}
	i, err := n.Int64()
	return err == nil && i > 0
}
